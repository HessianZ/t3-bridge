package main

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestConfigProjectRegisterWeb(t *testing.T) {
	const origin, target = "http://" + webTestHost, "/project-register"
	const directory = `C:\work\A & B`
	const secretPath = "/etc/t3-bridge/app.secret"
	const setupTokenPath = "/private/t3/setup & register.json"
	raw, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path, other := filepath.Join(dir, "saved.json"), filepath.Join(dir, "other.json")
	for file, contents := range map[string]string{path: string(raw), other: string(raw) + "\n"} {
		if err := os.WriteFile(file, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	sources := []webSource{
		{ID: "file", Label: "已保存", Path: path, Revision: "startup-version"},
		{ID: "other", Path: other},
		{ID: "template", JSON: raw},
		{ID: "missing", Path: filepath.Join(dir, "missing.json"), JSON: raw},
	}
	candidate, err := bridge.EditConfigJSON(raw, []byte(`[{"path":["routes","work","projectId"],"value":"registered-project"}]`))
	if err != nil {
		t.Fatal(err)
	}
	revision := configRevision(raw)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	var gotSetupTokenFile string
	var registrationErr error
	handler := configProjectRegisterHandler(func() ([]webSource, error) { return sources, nil },
		func(gotCtx context.Context, configPath, version, appID, routeID, pathOnTarget, setupTokenFile string) ([]byte, error) {
			calls++
			gotSetupTokenFile = setupTokenFile
			if gotCtx != ctx || configPath != path || version != revision || appID != "REPLACE_APP_ID" || routeID != "work" || pathOnTarget != directory {
				t.Fatal("registration did not receive the selected saved route and target directory")
			}
			return candidate, registrationErr
		}, managedProjectWorkspace)
	serve := func(handler http.Handler, method, requestPath, requestOrigin, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, origin+requestPath, strings.NewReader(body)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if requestOrigin != "" {
			r.Header.Set("Origin", requestOrigin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	_, workspace := configToolSession(webTestHost, configWebHandler(webTestHost, sources))
	assertWebPage(t, serve(workspace, http.MethodGet, "/?source=file", "", ""), http.StatusOK, `href="/project-register?source=file"`)
	for _, id := range []string{"template", "missing"} {
		page := serve(workspace, http.MethodGet, "/?source="+id, "", "")
		assertWebPage(t, page, http.StatusOK)
		if strings.Contains(page.Body.String(), `href="/project-register`) {
			t.Fatalf("unsaved source %q offered registration", id)
		}
	}
	selected := target + "?source=file&appId=REPLACE_APP_ID&routeId=work"
	assertWebPage(t, serve(workspace, http.MethodGet, target+"?source=file", "", ""), http.StatusOK, "REPLACE_APP_ID · work · REPLACE_ENVIRONMENT_ID")
	page := serve(workspace, http.MethodGet, selected, "", "")
	assertWebPage(t, page, http.StatusOK, `name="revision" value="`+revision+`"`, "目标机上的已有目录", "workstation", `name="confirm"`, "</html>")
	assertWebPage(t, page, http.StatusOK, `name="setupTokenFile"`, "单独授权的私有 OAuth JSON 文件路径（可选）", "仅项目字段会写入候选配置")
	if strings.Contains(page.Body.String(), secretPath) {
		t.Fatal("selection page exposed a config credential reference")
	}
	form := url.Values{"source": {"file"}, "revision": {revision}, "appId": {"REPLACE_APP_ID"}, "routeId": {"work"}, "directory": {directory}, "confirm": {"register"}}
	valid := form.Encode()
	setupTokenQuery := "setupTokenFile=" + url.QueryEscape(setupTokenPath)
	queryPage := serve(handler, http.MethodGet, selected+"&"+setupTokenQuery, "", "")
	assertWebPage(t, queryPage, http.StatusBadRequest)
	if strings.Contains(queryPage.Body.String(), html.EscapeString(setupTokenPath)) {
		t.Fatal("rejected GET echoed the setup token path")
	}
	for _, tc := range []struct {
		name, query, requestOrigin, body string
		want                             int
	}{
		{"foreign origin", "", "https://other.test", valid + "&" + setupTokenQuery, http.StatusForbidden},
		{"missing origin", "", "", valid, http.StatusForbidden},
		{"query override", "?source=other", origin, valid, http.StatusBadRequest},
		{"setup token in query", "?" + setupTokenQuery, origin, valid, http.StatusBadRequest},
		{"wrong saved source", "", origin, strings.Replace(valid, "source=file", "source=other", 1), http.StatusConflict},
		{"missing source file", "", origin, strings.Replace(valid, "source=file", "source=missing", 1), http.StatusConflict},
		{"template source", "", origin, strings.Replace(valid, "source=file", "source=template", 1), http.StatusConflict},
		{"stale revision", "", origin, strings.Replace(valid, revision, "stale", 1) + "&" + setupTokenQuery, http.StatusConflict},
		{"wrong App", "", origin, strings.Replace(valid, "REPLACE_APP_ID", "other", 1), http.StatusBadRequest},
		{"wrong route", "", origin, strings.Replace(valid, "routeId=work", "routeId=missing", 1), http.StatusBadRequest},
		{"duplicate field", "", origin, valid + "&source=file", http.StatusBadRequest},
		{"duplicate setup token", "", origin, valid + "&" + setupTokenQuery + "&setupTokenFile=", http.StatusBadRequest},
		{"extra field", "", origin, valid + "&environmentId=untrusted", http.StatusBadRequest},
		{"unconfirmed", "", origin, strings.Replace(valid, "confirm=register&", "", 1), http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertWebPage(t, serve(handler, http.MethodPost, target+tc.query, tc.requestOrigin, tc.body), tc.want)
			if calls != 0 {
				t.Fatal("invalid request reached registration")
			}
		})
	}
	result := serve(handler, http.MethodPost, target, origin, valid)
	assertWebPage(t, result, http.StatusOK, `action="/?source=file"`, `name="action" value="check"`, "registered-project", "</html>")
	match := regexp.MustCompile(`name="config" value="([^"]*)"`).FindStringSubmatch(result.Body.String())
	if calls != 1 || gotSetupTokenFile != "" || match == nil || strings.Contains(result.Body.String(), `value="save"`) {
		t.Fatal("successful registration did not offer the draft-only handoff")
	}
	review := url.Values{"config": {html.UnescapeString(match[1])}, "revision": {revision}, "action": {"check"}}
	assertWebPage(t, serve(workspace, http.MethodPost, "/?source=file", origin, review.Encode()), http.StatusOK, "与来源的差异", "registered-project", `name="revision" value="`+revision+`"`)
	for _, tc := range []struct {
		name, field, want string
	}{
		{"blank setup token", "setupTokenFile=", ""},
		{"whitespace setup token", "setupTokenFile=++", ""},
		{"separate setup token", setupTokenQuery, setupTokenPath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := calls
			result := serve(handler, http.MethodPost, target, origin, valid+"&"+tc.field)
			assertWebPage(t, result, http.StatusOK, "registered-project")
			if calls != before+1 || gotSetupTokenFile != tc.want {
				t.Fatal("registration did not receive the expected setup token option")
			}
			if strings.Contains(result.Body.String(), html.EscapeString(setupTokenPath)) || strings.Contains(result.Body.String(), "setupTokenFile") {
				t.Fatal("draft handoff exposed the setup token option")
			}
		})
	}
	registrationErr = errors.New("无法读取授权文件 " + setupTokenPath)
	result = serve(handler, http.MethodPost, target, origin, valid+"&"+setupTokenQuery)
	assertWebPage(t, result, http.StatusUnprocessableEntity, "无法读取授权文件")
	if strings.Contains(result.Body.String(), html.EscapeString(setupTokenPath)) {
		t.Fatal("registration error echoed the setup token path")
	}
	saved, err := os.ReadFile(path)
	if err != nil || string(saved) != string(raw) {
		t.Fatal("registration or review changed the saved file", err)
	}
}

func TestConfigProjectRegisterWebManagedWorkspace(t *testing.T) {
	const origin, target = "http://" + webTestHost, "/project-register"
	const setupTokenPath = "/private/t3/managed-setup.json"
	dir := t.TempDir()
	registry := profileRegistry{root: filepath.Join(dir, "t3-bridge")}
	raw, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	account, err := bridge.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	account.AppID = "cli_web_managed_project"
	account.MCP.Endpoint = "http://localhost:3773/mcp"
	account.MCP.EnvironmentID = "fixture-local"
	local := account.Routes["work"]
	local.EnvironmentID = account.MCP.EnvironmentID
	account.Routes["work"] = local
	linked := local
	linked.EnvironmentID, linked.HostName, linked.Aliases = "fixture-linked", "", nil
	account.Routes["linked"] = linked
	writeFixture := func(path string, config bridge.Config) []byte {
		t.Helper()
		data, err := json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		return data
	}
	path, remotePath := filepath.Join(dir, "local.json"), filepath.Join(dir, "remote.json")
	raw = writeFixture(path, account)
	managedDir, err := registry.managedProjectWorkspace(path, account, "work")
	if rel, relErr := filepath.Rel(dir, managedDir); err != nil || relErr != nil || !filepath.IsLocal(rel) || rel == "." {
		t.Fatalf("managed fixture path %q is outside the test directory: %v, %v", managedDir, err, relErr)
	}
	remote := account
	remote.MCP.Endpoint = "https://remote.example.test/mcp"
	remoteRaw := writeFixture(remotePath, remote)
	// T3 can return an equivalent spelling of the verified path.
	sep := string(filepath.Separator)
	actualDir := filepath.Dir(managedDir) + sep + "." + sep + filepath.Base(managedDir)
	local.ProjectID, local.ProjectPath = "registered-managed-project", actualDir
	account.Routes["work"] = local
	candidate, err := json.Marshal(account)
	if err != nil {
		t.Fatal(err)
	}
	sources := []webSource{{ID: "file", Path: path}, {ID: "remote", Path: remotePath}, {ID: "template", JSON: raw}}
	revision := configRevision(raw)
	calls := 0
	var gotSetupTokenFile string
	handler := configProjectRegisterHandler(func() ([]webSource, error) { return sources, nil },
		func(_ context.Context, configPath, version, appID, routeID, directory, setupTokenFile string) ([]byte, error) {
			calls++
			gotSetupTokenFile = setupTokenFile
			if configPath != path || version != revision || appID != account.AppID || routeID != "work" || directory != "" {
				t.Error("registration did not receive the selected saved route and managed mode")
			}
			return candidate, nil
		}, registry.managedProjectWorkspace)
	serve := func(method, query string, form url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, origin+target+query, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	selected := func(source, route string) *httptest.ResponseRecorder {
		query := url.Values{"source": {source}, "appId": {account.AppID}, "routeId": {route}}
		return serve(http.MethodGet, "?"+query.Encode(), nil)
	}
	page := selected("file", "work")
	assertWebPage(t, page, http.StatusOK, `value="register"`, `name="confirm" value="create-managed" required`,
		`name="directory" value="`+managedDir+`"`, `type="text" value="`+managedDir+`" readonly`,
		"在本机创建此目录并登记为 T3 项目", "审阅候选配置并单独保存")
	if strings.Count(page.Body.String(), `name="setupTokenFile"`) != 2 {
		t.Fatal("both registration forms should offer the setup token option")
	}
	for _, page := range []*httptest.ResponseRecorder{selected("file", "linked"), selected("remote", "work")} {
		assertWebPage(t, page, http.StatusOK, `value="register"`)
		if strings.Contains(page.Body.String(), `value="create-managed"`) {
			t.Fatal("remote target offered managed workspace creation")
		}
	}
	form := url.Values{"source": {"file"}, "revision": {revision}, "appId": {account.AppID}, "routeId": {"work"}, "directory": {managedDir}, "confirm": {"create-managed"}}
	for _, tc := range []struct {
		name   string
		fields url.Values
		status int
	}{
		{"unconfirmed", url.Values{"confirm": nil}, http.StatusBadRequest},
		{"unknown mode", url.Values{"confirm": {"create"}}, http.StatusBadRequest},
		{"directory omitted", url.Values{"directory": nil}, http.StatusBadRequest},
		{"directory tampered", url.Values{"directory": {managedDir + "-other"}}, http.StatusBadRequest},
		{"directory case changed", url.Values{"directory": {strings.ToUpper(managedDir)}}, http.StatusBadRequest},
		{"extra field", url.Values{"mode": {"managed"}}, http.StatusBadRequest},
		{"duplicate confirmation", url.Values{"confirm": {"create-managed", "register"}}, http.StatusBadRequest},
		{"duplicate setup token", url.Values{"setupTokenFile": {setupTokenPath, ""}}, http.StatusBadRequest},
		{"unknown route", url.Values{"routeId": {"missing"}}, http.StatusBadRequest},
		{"linked route", url.Values{"routeId": {"linked"}}, http.StatusBadRequest},
		{"remote endpoint", url.Values{"source": {"remote"}, "revision": {configRevision(remoteRaw)}}, http.StatusBadRequest},
		{"unsaved source", url.Values{"source": {"template"}}, http.StatusConflict},
		{"stale revision", url.Values{"revision": {"stale"}}, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := maps.Clone(form)
			maps.Copy(request, tc.fields)
			assertWebPage(t, serve(http.MethodPost, "", request), tc.status)
		})
	}
	if calls != 0 {
		t.Fatalf("unconfirmed requests reached registration %d times", calls)
	}
	if _, err := os.Lstat(filepath.Dir(managedDir)); !os.IsNotExist(err) {
		t.Fatalf("unconfirmed requests wrote workspace state: %v", err)
	}
	assertWebPage(t, serve(http.MethodPost, "", form), http.StatusOK, "<code>"+actualDir+"</code>",
		`name="action" value="check"`, "registered-managed-project")
	if calls != 1 || gotSetupTokenFile != "" {
		t.Fatal("legacy managed request did not reach registration with the saved authorization")
	}
	form.Set("setupTokenFile", setupTokenPath)
	result := serve(http.MethodPost, "", form)
	assertWebPage(t, result, http.StatusOK, "<code>"+actualDir+"</code>", "registered-managed-project")
	if calls != 2 || gotSetupTokenFile != setupTokenPath {
		t.Fatal("managed registration did not receive the separate setup token option")
	}
	if strings.Contains(result.Body.String(), setupTokenPath) || strings.Contains(result.Body.String(), "setupTokenFile") {
		t.Fatal("managed draft handoff exposed the setup token option")
	}
	if saved, err := os.ReadFile(path); err != nil || string(saved) != string(raw) {
		t.Fatal("registration changed the saved config", err)
	}
}
