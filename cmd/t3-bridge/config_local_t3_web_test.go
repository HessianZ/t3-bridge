package main

import (
	"bytes"
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

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestConfigWebUseLocalT3Draft(t *testing.T) {
	home := t.TempDir()
	t.Setenv("T3CODE_HOME", home)
	userdata := filepath.Join(home, "userdata")
	if err := os.Mkdir(userdata, 0700); err != nil {
		t.Fatal(err)
	}
	runtimeFile := filepath.Join(userdata, "server-runtime.json")
	if err := os.WriteFile(runtimeFile, []byte(`{"port":3774}`), 0600); err != nil {
		t.Fatal(err)
	}
	const environmentID = "env-fixture"
	if err := os.WriteFile(filepath.Join(userdata, "environment-id"), []byte(environmentID+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	template, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(home, "unused-token.json")
	quotedTokenFile, _ := json.Marshal(tokenFile)
	base, err := bridge.EditConfigJSON(template, []byte(`[{"path":["mcp","tokenFile"],"value":`+string(quotedTokenFile)+`}]`))
	if err != nil {
		t.Fatal(err)
	}
	source, err := (manualAccountFields{
		AppID: "cli_local_t3_aux", Tenant: "feishu", User: "ou_aux", Route: "work",
		AppSecretFile: filepath.Join(home, "aux.secret"), StateDir: filepath.Join(home, "aux-state"),
	}).apply(base)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "config.json")
	if err := os.WriteFile(path, source, 0600); err != nil {
		t.Fatal(err)
	}
	sources, err := configWebSources(path)
	if err != nil {
		t.Fatal(err)
	}
	handler := configWebHandler(webTestHost, sources)
	const origin = "http://" + webTestHost
	const address = origin + "/?source=file"
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, address, nil))
	assertWebPage(t, page, http.StatusOK, `name="action" value="use-local-t3:0"`, `name="action" value="use-local-t3:1"`, "采用本机 T3")

	post := func(requestOrigin, query string, actions ...string) *httptest.ResponseRecorder {
		form := url.Values{
			"action":   actions,
			"config":   {strings.Replace(string(source), `"receiverMachine": "bridge"`, `"receiverMachine": "raw-draft"`, 1)},
			"edits":    {`[{"path":["additionalAccounts","0","routes","work","model"],"value":"form-draft"}]`},
			"revision": {sources[0].Revision},
		}
		req := httptest.NewRequest(http.MethodPost, address+query, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if requestOrigin != "" {
			req.Header.Set("Origin", requestOrigin)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	assertWebPage(t, post("", "", "use-local-t3:1"), http.StatusForbidden)
	assertWebPage(t, post("https://foreign.test", "", "use-local-t3:1"), http.StatusForbidden)
	assertWebPage(t, post(origin, "", "use-local-t3:1", "check"), http.StatusForbidden)
	assertWebPage(t, post(origin, "", "check", "use-local-t3:1"), http.StatusForbidden)
	assertWebPage(t, post(origin, "&action=use-local-t3:1"), http.StatusForbidden)
	assertWebPage(t, post(origin, "&action=check", "use-local-t3:1"), http.StatusForbidden)
	assertWebPage(t, post(origin, "", "use-local-t3:01"), http.StatusBadRequest)
	assertWebPage(t, post(origin, "", "use-local-t3:2"), http.StatusUnprocessableEntity, "所选账号不存在")

	draftField := regexp.MustCompile(`(?s)<textarea [^>]*name="config"[^>]*>\n(.*?)</textarea>`)
	for index, action := range []string{"use-local-t3:0", "use-local-t3:1"} {
		preview := post(origin, "", action)
		added := "+" + strings.Repeat(" ", 4*(index+1)) + `"environmentId": "env-fixture"`
		assertWebPage(t, preview, http.StatusOK, "与来源的差异", added, `name="action" value="save">`)
		draft := draftField.FindStringSubmatch(preview.Body.String())
		if draft == nil {
			t.Fatal("preview did not render a candidate draft")
		}
		candidate, err := bridge.ParseConfig([]byte(html.UnescapeString(draft[1])))
		if err != nil {
			t.Fatal(err)
		}
		accounts := candidate.Accounts()
		selected, other := accounts[index], accounts[1-index]
		if candidate.ReceiverMachine != "raw-draft" || accounts[1].Routes["work"].Model != "form-draft" ||
			selected.MCP.Endpoint != "" || selected.MCP.RuntimeFile != runtimeFile || selected.MCP.TokenFile != tokenFile ||
			selected.MCP.EnvironmentID != environmentID || selected.Routes["work"].EnvironmentID != environmentID ||
			other.MCP.RuntimeFile != "" || other.MCP.EnvironmentID != "REPLACE_ENVIRONMENT_ID" || other.Routes["work"].EnvironmentID != "REPLACE_ENVIRONMENT_ID" {
			t.Fatalf("%s did not preserve draft edits and update only the selected account", action)
		}
	}
	if saved, err := os.ReadFile(path); err != nil || !bytes.Equal(saved, source) {
		t.Fatalf("preview modified the saved source: %v", err)
	}
	if _, err := os.Stat(tokenFile); !os.IsNotExist(err) {
		t.Fatalf("preview touched credentials: %v", err)
	}
}
