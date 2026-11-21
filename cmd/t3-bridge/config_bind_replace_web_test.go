package main

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/larksuite/oapi-sdk-go/v3/scene/registration"
)

func TestConfigWebQRReplaceUnsavedAccount(t *testing.T) {
	const oldApp, newApp = "cli_unsavedapp", "cli_replacement"
	const secret = "synthetic-replacement-secret"
	dir := t.TempDir()
	raw := existingBindConfigFixture(t, dir)
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	binding := &qrRegistration{
		ctx: t.Context(), configDir: dir,
		register: func(context.Context, *registration.Options) (*registration.RegisterAppResult, error) {
			return &registration.RegisterAppResult{
				ClientID: newApp, ClientSecret: secret,
				UserInfo: &registration.UserInfo{TenantBrand: "feishu", OpenID: "ou_scanner"},
			}, nil
		},
	}
	handler := configWebHandler(webTestHost, []webSource{{ID: "file", Path: path}}, binding)
	start := httptest.NewRecorder()
	query := url.Values{"replaceAppId": {oldApp}}
	handler.ServeHTTP(start, httptest.NewRequest(http.MethodPost, "http://"+webTestHost+"/bind/start?"+query.Encode(), nil))
	var session struct{ ID string }
	if err := json.Unmarshal(start.Body.Bytes(), &session); start.Code != http.StatusOK || err != nil || session.ID == "" {
		t.Fatalf("start replacement scan: status %d, error %v", start.Code, err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		view, ok := binding.status(session.ID)
		if ok && view.State == "scanned" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("replacement scan did not complete: %q", view.State)
		}
		time.Sleep(time.Millisecond)
	}

	page := submitWebConfig(handler, url.Values{
		"config": {string(raw)}, "revision": {configRevision(raw)}, "action": {"bind"}, "bindingId": {session.ID},
		"edits": {`[{"path":["additionalAccounts","1","appId"],"value":"` + oldApp + `"},{"path":["routes","work","model"],"value":"draft-model"}]`},
	})
	assertWebPage(t, page, http.StatusOK, "替换已有账号", "与来源的差异", newApp)
	body := page.Body.String()
	reviewed := regexp.MustCompile(`name="reviewed" value="([a-f0-9]{64})"`).FindStringSubmatch(body)
	draft := regexp.MustCompile(`(?s)id="config" name="config"[^>]*>\n(.*?)</textarea>`).FindStringSubmatch(body)
	if reviewed == nil || draft == nil || strings.Contains(body, secret) {
		t.Fatal("expected a reviewable draft holding only the credential reference")
	}
	candidate := html.UnescapeString(draft[1])
	config, err := bridge.ParseConfig([]byte(candidate))
	if err != nil {
		t.Fatal(err)
	}
	if config.AppID != "cli_mainfixture" || config.Routes["work"].Model != "draft-model" ||
		len(config.AdditionalAccounts) != 2 || config.AdditionalAccounts[0].AppID != "cli_otherfixture" ||
		config.AdditionalAccounts[1].AppID != newApp || config.AdditionalAccounts[1].Routes["work"].ProjectID != "project-larkfixture" {
		t.Fatal("expected replacement at the edited account's original position with unrelated draft edits preserved")
	}
	if saved, err := os.ReadFile(path); err != nil || !bytes.Equal(saved, raw) {
		t.Fatalf("binding changed the original configuration before review: %v", err)
	}
	assertWebPage(t, submitWebConfig(handler, url.Values{
		"action": {"save"}, "config": {candidate}, "revision": {configRevision(raw)}, "reviewed": {reviewed[1]},
	}), http.StatusSeeOther)
	if saved, err := os.ReadFile(path); err != nil || string(saved) != candidate {
		t.Fatalf("reviewed replacement was not saved as shown: %v", err)
	}
}
