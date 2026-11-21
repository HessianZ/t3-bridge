package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestConfigWebToolIdentity(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LARK_CHANNEL_HOME", dir)
	const appID, routeID, mode = "cli_identity", "work", "user-default"
	const cliOutput = "synthetic subprocess secret"
	secretPath := filepath.Join(dir, "secret-reference")
	base, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := bridge.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	work := cfg.Routes[routeID]
	work.Users = []string{"ou_identity"}
	work.ToolContext = &bridge.ToolContext{
		CLIPath: filepath.Join(dir, "lark-cli"), EnvironmentID: work.EnvironmentID, Identity: mode,
		BotBinding: &bridge.ToolBotBinding{
			AppID: appID, Tenant: "feishu", BotOpenID: "ou_bot", Profile: "tools", ConfigDir: dir,
		},
		UserBinding: &bridge.ToolUserBinding{
			AppID: appID, Tenant: "feishu", UserOpenID: "ou_identity", Profile: "tools", ConfigDir: dir,
		},
	}
	ops := work
	ops.HostName, ops.Aliases = "", nil
	edits, err := json.Marshal([]legacyImportEdit{
		{[]string{"appId"}, appID}, {[]string{"appSecretFile"}, secretPath}, {[]string{"users"}, work.Users},
		{[]string{"routes", routeID}, work}, {[]string{"routes", "ops"}, ops},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := bridge.EditConfigJSON(base, edits)
	if err != nil {
		t.Fatal(err)
	}
	write := func(path string, data []byte, create bool) {
		t.Helper()
		if _, err := localfile.WritePrivateAtomic(path, data, create); err != nil {
			t.Fatal(err)
		}
	}
	path, plainPath := filepath.Join(dir, "config.json"), filepath.Join(dir, "plain.json")
	write(path, raw, true)
	write(plainPath, base, true)
	sources := []webSource{
		{ID: "file", Label: "身份配置", Path: path, Revision: "startup-only"},
		{ID: "plain", Label: "普通配置", Path: plainPath},
		{ID: "generic", Label: "模板", JSON: base},
		{ID: "missing", Label: "未保存", Path: filepath.Join(dir, "missing.json"), JSON: raw},
	}
	revision := configRevision(raw)
	const target = "/tool-identity"
	const origin = "http://" + webTestHost
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	var probeErr error
	handler := configToolIdentityHandler(func() ([]webSource, error) { return sources, nil },
		func(gotCtx context.Context, source webSource, app, route string) (workspaceToolIdentity, error) {
			calls++
			if gotCtx != ctx || source.ID != "file" || source.Path != path || source.Revision != revision || app != appID || route != routeID {
				t.Error("check did not receive the request context and the selected saved route")
			}
			return workspaceToolIdentity{AppID: app, RouteID: route, Identity: mode}, probeErr
		})
	serve := func(method, requestPath, requestOrigin, site, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, origin+requestPath, strings.NewReader(body)).WithContext(ctx)
		if method == http.MethodPost {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if requestOrigin != "" {
			r.Header.Set("Origin", requestOrigin)
		}
		if site != "" {
			r.Header.Set("Sec-Fetch-Site", site)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	assert := func(t *testing.T, w *httptest.ResponseRecorder, status int, markers ...string) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("status %d, want %d:\n%s", w.Code, status, w.Body.String())
		}
		for _, marker := range markers {
			if !strings.Contains(w.Body.String(), marker) {
				t.Errorf("page missing %q", marker)
			}
		}
		for _, forbidden := range []string{cliOutput, secretPath, `value="generic"`, `value="missing"`} {
			if strings.Contains(w.Body.String(), forbidden) {
				t.Errorf("page exposed %q", forbidden)
			}
		}
	}
	initial := serve(http.MethodGet, target+"?source=file", "", "none", "")
	assert(t, initial, http.StatusOK, `name="revision" value="`+revision+`"`, `<option value="0">`+appID+` · ops · `+mode,
		`<option value="1">`+appID+` · `+routeID+` · `+mode, "当前主机上的 lark-cli", "只读 API", "</html>")
	if initial.Header().Get("Referrer-Policy") != "same-origin" {
		t.Fatal("page omitted the referrer policy for native forms")
	}
	plain := serve(http.MethodGet, target+"?source=plain", "", "", "")
	assert(t, plain, http.StatusOK, "此配置没有绑定飞书工具身份的路由。")
	if strings.Contains(plain.Body.String(), `name="choice"`) {
		t.Fatal("an unguarded configuration offered a check")
	}
	assert(t, serve(http.MethodGet, target+"?source=generic", "", "", ""), http.StatusConflict)
	assert(t, serve(http.MethodGet, target+"?source=file", "https://other.test", "", ""), http.StatusForbidden)
	assert(t, serve(http.MethodGet, target+"?source=file", "", "same-site", ""), http.StatusForbidden)
	assert(t, serve(http.MethodGet, target+"?source=file&source=plain", "", "", ""), http.StatusBadRequest)
	assert(t, serve(http.MethodHead, target, "", "", ""), http.StatusMethodNotAllowed)

	form := func(source, rev, choice string) string {
		return url.Values{"source": {source}, "revision": {rev}, "choice": {choice}}.Encode()
	}
	valid := form("file", revision, "1")
	for _, tc := range []struct {
		name, query, origin, site, body string
		status                          int
	}{
		{name: "foreign origin", origin: "https://other.test", body: valid, status: http.StatusForbidden},
		{name: "missing origin", body: valid, status: http.StatusForbidden},
		{name: "foreign fetch site", origin: origin, site: "same-site", body: valid, status: http.StatusForbidden},
		{name: "query override", query: "?source=plain", origin: origin, body: valid, status: http.StatusBadRequest},
		{name: "missing revision", origin: origin, body: "source=file&choice=1", status: http.StatusBadRequest},
		{name: "duplicate choice", origin: origin, body: valid + "&choice=0", status: http.StatusBadRequest},
		{name: "extra path and command", origin: origin, body: valid + "&path=/tmp/a&command=synthetic", status: http.StatusBadRequest},
		{name: "malformed body", origin: origin, body: valid + "&broken=%zz", status: http.StatusBadRequest},
		{name: "oversized body", origin: origin, body: valid + strings.Repeat("a", 2048), status: http.StatusBadRequest},
		{name: "negative index", origin: origin, body: form("file", revision, "-1"), status: http.StatusBadRequest},
		{name: "outside choice list", origin: origin, body: form("file", revision, "2"), status: http.StatusBadRequest},
		{name: "template source", origin: origin, body: form("generic", revision, "1"), status: http.StatusConflict},
		{name: "unsaved source", origin: origin, body: form("missing", revision, "1"), status: http.StatusConflict},
		{name: "unregistered source", origin: origin, body: form(path, revision, "1"), status: http.StatusConflict},
		{name: "unguarded source", origin: origin, body: form("plain", configRevision(base), "0"), status: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert(t, serve(http.MethodPost, target+tc.query, tc.origin, tc.site, tc.body), tc.status)
			if calls != 0 {
				t.Fatal("a rejected POST invoked the identity check")
			}
		})
	}
	if calls != 0 {
		t.Fatal("GET or a rejected POST invoked the identity check")
	}
	submit := func() *httptest.ResponseRecorder { return serve(http.MethodPost, target, origin, "same-origin", valid) }
	assert(t, submit(), http.StatusOK, "核验通过", `<code>`+appID+`</code>`, `<code>`+routeID+`</code>`, `<code>`+mode+`</code>`)
	probeErr = errors.New(cliOutput)
	assert(t, submit(), http.StatusUnprocessableEntity, "核验未通过", `<code>`+appID+`</code>`, `<code>`+routeID+`</code>`, `<code>`+mode+`</code>`)
	probeErr = toolIdentityProbeFailure{reason: "专用飞书工具 CLI 未确认 default-as 为 auto"}
	assert(t, submit(), http.StatusUnprocessableEntity, "核验未通过", probeErr.Error())
	probeErr = errToolIdentityConfigChanged
	becameStale := submit()
	assert(t, becameStale, http.StatusConflict, errToolIdentityConfigChanged.Error())
	if strings.Contains(becameStale.Body.String(), "工具身份核验结果") || strings.Contains(becameStale.Body.String(), `name="choice"`) {
		t.Fatal("a revision conflict showed a result or let the user submit the stale revision again")
	}
	write(path, append(raw, '\n'), false)
	assert(t, submit(), http.StatusConflict, errToolIdentityConfigChanged.Error())
	if calls != 4 {
		t.Fatalf("check called %d times, want 4 valid submissions", calls)
	}

	workspace := configWebHandler(webTestHost, sources)
	open := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		workspace.ServeHTTP(w, httptest.NewRequest(http.MethodGet, origin+path, nil))
		return w
	}
	assertWebPage(t, open("/?source=file"), http.StatusOK, `href="/tool-identity?source=file"`)
	assertWebPage(t, open(target+"?source=file"), http.StatusOK, "核验飞书工具身份")
	post := httptest.NewRequest(http.MethodPost, origin+target, nil)
	post.Header.Set("Origin", origin)
	w := httptest.NewRecorder()
	workspace.ServeHTTP(w, post)
	assertWebPage(t, w, http.StatusBadRequest, "核验表单无效")
	for _, id := range []string{"generic", "missing"} {
		w := open("/?source=" + id)
		assertWebPage(t, w, http.StatusOK)
		if strings.Contains(w.Body.String(), `href="/tool-identity`) {
			t.Errorf("workbench exposed saved-config navigation for %s", id)
		}
	}
}
