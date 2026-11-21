package main

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/larksuite/oapi-sdk-go/v3/scene/registration"
)

func TestConfigBindFeishuRejectsInvalidDraft(t *testing.T) {
	output := filepath.Join(t.TempDir(), "candidate.json")
	var status strings.Builder
	input := strings.NewReader(`{"appId":"one","appId":"two"}`)
	err := runConfigBindFeishu([]string{"-draft-stdin", "-output", output}, input, &status)
	if err == nil || !strings.Contains(err.Error(), "invalid JSON config") || status.Len() != 0 {
		t.Fatalf("expected strict validation before binding, got %v", err)
	}
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		t.Fatalf("invalid draft created a candidate: %v", err)
	}
}

func TestTerminalFeishuBinding(t *testing.T) {
	source := tuiTestSource(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	const id = "cli_terminalfixture"
	const secret = "synthetic-terminal-app-secret"
	result := &registration.RegisterAppResult{ClientID: id, ClientSecret: secret,
		UserInfo: &registration.UserInfo{OpenID: "ou_fixture", TenantBrand: "feishu"}}
	register := func(context.Context, *registration.Options) (*registration.RegisterAppResult, error) {
		return result, nil
	}
	q := &qrRegistration{configDir: t.TempDir(), ctx: ctx, register: register}
	candidate, err := bindFeishuTerminal(q, source.JSON, false, "", &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	check := func(raw []byte) {
		t.Helper()
		config, err := bridge.ParseConfig(raw)
		if err != nil || config.AppID != id || config.ReceiverAuthorized || strings.Contains(string(raw), secret) {
			t.Fatalf("unexpected terminal candidate: app %q, parse error %v", config.AppID, err)
		}
		if value, err := bridge.Secret(config.AppSecretFile); err != nil || value != secret {
			t.Fatalf("could not read private credential: %v", err)
		}
	}
	check(candidate)

	m := newConfigTUI(source)
	tuiKeys(m, tea.KeyF7)
	if m.screen != tuiBindReview {
		t.Fatalf("terminal binding workspace was not opened: %s", m.status)
	}
	m.binding.client = &qrRegistration{configDir: t.TempDir(), ctx: ctx, register: register}
	m.binding.id = m.binding.client.start(false)
	m.screen = tuiBindProgress
	poll := m.pollFeishuBinding()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("synthetic terminal binding did not complete")
		default:
		}
		update := poll().(configTUIBindingResult)
		if poll = m.receiveFeishuBinding(update); poll == nil {
			t.Fatalf("unexpected terminal binding state: %s", update.view.State)
		}
		if update.view.State == "scanned" {
			break
		}
	}
	if m.draft != string(source.JSON) {
		t.Fatal("scanning changed the draft before confirmation")
	}
	tuiKeys(m, tea.KeyEnter)
	if m.screen != tuiBrowse {
		t.Fatalf("terminal binding did not return to the draft: %s", m.status)
	}
	check([]byte(m.draft))
}

func TestConfigWebQRCodeBinding(t *testing.T) {
	sources, err := configWebSources("")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scan := make(chan struct{})
	options := make(chan *registration.Options, 1)
	register := func(ctx context.Context, opts *registration.Options) (*registration.RegisterAppResult, error) {
		opts.OnQRCode(&registration.QRCodeInfo{URL: "https://accounts.feishu.cn/qr?test=1", ExpireIn: 120})
		options <- opts
		select {
		case <-scan:
			return &registration.RegisterAppResult{ClientID: "cli_sample", ClientSecret: "synthetic-secret-do-not-display",
				UserInfo: &registration.UserInfo{OpenID: "ou_scanner", TenantBrand: "lark"}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	binding := &qrRegistration{configDir: t.TempDir(), ctx: ctx, register: register}
	handler := configWebHandler(webTestHost, sources, binding)
	startReq := httptest.NewRequest(http.MethodPost, "http://"+webTestHost+"/bind/start?comments=true", nil)
	startResp := httptest.NewRecorder()
	handler.ServeHTTP(startResp, startReq)
	var start map[string]string
	if err := json.Unmarshal(startResp.Body.Bytes(), &start); err != nil || start["id"] == "" {
		t.Fatalf("missing registration id: %v", err)
	}
	select {
	case opts := <-options:
		if opts.Addons == nil || !slices.Contains(opts.Addons.Events.Items.Tenant, "drive.notice.comment_add_v1") {
			t.Fatal("comment permission choice not sent to official registration")
		}
	case <-time.After(time.Second):
		t.Fatal("registration never started")
	}
	status := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "http://"+webTestHost+"/bind/status?id="+start["id"], nil)
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		return resp
	}
	assertWebPage(t, status(), http.StatusOK, "pending", "data:image/png;base64,")
	close(scan)
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(status().Body.String(), "scanned") {
		if time.Now().After(deadline) {
			t.Fatal("registration never completed")
		}
		time.Sleep(time.Millisecond)
	}
	assertWebPage(t, status(), http.StatusOK, "scanned", "cli_sample")
	form := url.Values{"config": {string(sources[0].JSON)}, "source": {"generic"}, "action": {"bind"}, "bindingId": {start["id"]}}
	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "http://"+webTestHost+"/?source=generic", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		return resp
	}
	page := request()
	assertWebPage(t, page, http.StatusOK, "cli_sample", "ou_scanner", "lark")
	matches := regexp.MustCompile(`name="reviewed" value="([a-f0-9]{64})"`).FindStringSubmatch(page.Body.String())
	if matches == nil || strings.Contains(page.Body.String()+status().Body.String(), "synthetic-secret-do-not-display") {
		t.Fatal("candidate not reviewable or app secret was exposed")
	}
	raw := regexp.MustCompile(`(?s)id="config" name="config"[^>]*>(.*?)</textarea>`).FindStringSubmatch(page.Body.String())
	if raw == nil {
		t.Fatal("missing edited configuration")
	}
	form.Set("config", html.UnescapeString(raw[1]))
	form.Set("reviewed", matches[1])
	form.Set("action", "download")
	c, err := bridge.ParseConfig(request().Body.Bytes())
	if err != nil || c.AppID != "cli_sample" || c.ReceiverAuthorized || c.Tenant != "lark" ||
		c.AppSecretFile != filepath.Join(binding.configDir, "credentials", "cli_sample.secret") ||
		!slices.Equal(c.Routes["work"].Users, []string{"ou_scanner"}) {
		t.Fatalf("wrong bound config: %s / %v", c.AppID, err)
	}
	stat, err := os.Stat(c.AppSecretFile)
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatal("secret not saved privately", err)
	}
	if secret, err := bridge.Secret(c.AppSecretFile); err != nil || secret != "synthetic-secret-do-not-display" {
		t.Fatal("the secret file did not round-trip", err)
	}
}

func TestBindAnotherFeishuIdentity(t *testing.T) {
	sources, err := configWebSources("")
	if err != nil {
		t.Fatal(err)
	}
	first := &registration.RegisterAppResult{ClientID: "cli_first", UserInfo: &registration.UserInfo{OpenID: "ou_first"}}
	raw, err := bindConfig(sources[0].JSON, first, "/private/first.secret", "/private/first")
	if err != nil {
		t.Fatal(err)
	}
	second := &registration.RegisterAppResult{ClientID: "cli_second", UserInfo: &registration.UserInfo{OpenID: "ou_second"}}
	raw, err = bindConfig(raw, second, "/private/second.secret", "/private/second")
	if err != nil {
		t.Fatal(err)
	}
	c, err := bridge.ParseConfig(raw)
	if err != nil || len(c.Accounts()) != 2 || c.AppID != "cli_first" {
		t.Fatalf("did not append second app: %v", err)
	}
	a := c.AdditionalAccounts[0]
	if a.AppID != "cli_second" || a.ReceiverAuthorized || a.StateDir != "/private/second" ||
		!slices.Equal(a.Users, []string{"ou_second"}) || !slices.Equal(a.Routes["work"].Users, []string{"ou_second"}) {
		t.Fatal("second identity reused authorization or state from the first")
	}
}
