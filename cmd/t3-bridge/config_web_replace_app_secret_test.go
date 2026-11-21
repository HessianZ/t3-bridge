package main

import (
	"bytes"
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

func TestConfigWebManualReplaceWithSecret(t *testing.T) {
	const (
		origin = "http://" + webTestHost
		newApp = "cli_manualreplacement"
		secret = "synthetic-replacement-password&+%"
		other  = "synthetic-conflicting-password&+%"
	)
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	profiles, err := localProfiles()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	secretPath := filepath.Join(profiles.root, "credentials", newApp+".secret")
	raw := existingBindConfigFixture(t, dir)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	handler := configWebHandler(webTestHost, []webSource{{ID: "file", Path: path}})
	target := origin + "/?source=file"
	get := func(target string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		return response
	}
	page := get(target)
	assertWebPage(t, page, http.StatusOK, `id="replacement-secret-controls" hidden`, `id="replacement-secret" type="password"`,
		`value="replace-app-secret" disabled>保存新私有密钥并生成换绑草稿`, `value="replace-app"`)
	if strings.Contains(page.Body.String(), `name="replacementSecret"`) {
		t.Fatal("the password field must be unnamed in rendered HTML")
	}
	assertWebPage(t, get(target+"&action=replace-app-secret"), http.StatusMethodNotAllowed)
	assertWebPage(t, get(target+"&replacementSecret="+url.QueryEscape(secret)), http.StatusBadRequest)

	revision := configRevision(raw)
	form := func() url.Values {
		return url.Values{
			"action": {"replace-app-secret"}, "config": {string(raw)}, "revision": {revision},
			"edits": {`[{"path":["additionalAccounts","1","appId"],"value":"cli_draft_oldapp"},` +
				`{"path":["routes","work","model"],"value":"draft-model"}]`},
			"replaceAppId": {"cli_draft_oldapp"}, "replacementAppId": {newApp}, "replacementTenant": {"feishu"},
			"replacementAppSecretFile": {""}, "replacementUser": {"ou_new_owner"}, "replacementSecret": {secret},
		}
	}
	post := func(t *testing.T, values url.Values, requestOrigin, query string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, target+query, strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if requestOrigin != "" {
			req.Header.Set("Origin", requestOrigin)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		assertWebPage(t, response, status)
		body := html.UnescapeString(response.Body.String())
		for _, value := range []string{secret, other, `name="replacementSecret"`} {
			if strings.Contains(body, value) || strings.Contains(response.Header().Get("Location"), url.QueryEscape(value)) {
				t.Fatal("response exposed the replacement password or a named password field")
			}
		}
		return response
	}
	assertOriginalConfig := func(t *testing.T) {
		t.Helper()
		if saved, err := os.ReadFile(path); err != nil || !bytes.Equal(saved, raw) {
			t.Fatalf("source configuration changed before reviewed save: %v", err)
		}
	}
	for _, tc := range []struct {
		name, origin, query string
		edit                func(url.Values)
		status              int
	}{
		{"missing origin", "", "", nil, http.StatusForbidden},
		{"cross origin", "https://foreign.test", "", nil, http.StatusForbidden},
		{"duplicate action", origin, "", func(f url.Values) { f.Add("action", "check") }, http.StatusForbidden},
		{"query action", origin, "&action=check", nil, http.StatusForbidden},
		{"query config", origin, "&config=other", nil, http.StatusBadRequest},
		{"query revision", origin, "&revision=other", nil, http.StatusBadRequest},
		{"query credential", origin, "&replacementSecret=" + url.QueryEscape(other), nil, http.StatusBadRequest},
		{"duplicate source", origin, "&source=file", nil, http.StatusBadRequest},
		{"body source", origin, "", func(f url.Values) { f.Set("source", "file") }, http.StatusBadRequest},
		{"duplicate config", origin, "", func(f url.Values) { f.Add("config", "{}") }, http.StatusBadRequest},
		{"duplicate edits", origin, "", func(f url.Values) { f.Add("edits", "[]") }, http.StatusBadRequest},
		{"missing revision", origin, "", func(f url.Values) { f.Del("revision") }, http.StatusBadRequest},
		{"duplicate revision", origin, "", func(f url.Values) { f.Add("revision", revision) }, http.StatusBadRequest},
		{"invalid revision", origin, "", func(f url.Values) { f.Set("revision", "missing") }, http.StatusConflict},
		{"duplicate secret", origin, "", func(f url.Values) { f.Add("replacementSecret", other) }, http.StatusBadRequest},
		{"existing path and secret", origin, "", func(f url.Values) { f.Set("replacementAppSecretFile", secretPath) }, http.StatusBadRequest},
		{"existing path action", origin, "", func(f url.Values) { f.Set("action", "replace-app") }, http.StatusBadRequest},
		{"unrelated check action", origin, "", func(f url.Values) { f.Set("action", "check") }, http.StatusBadRequest},
		{"nonexact selected App", origin, "", func(f url.Values) { f.Set("replaceAppId", "cli_draft_oldapp ") }, http.StatusUnprocessableEntity},
		{"invalid structural edit", origin, "", func(f url.Values) { f.Set("edits", `[{}]`) }, http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := form()
			if tc.edit != nil {
				tc.edit(values)
			}
			post(t, values, tc.origin, tc.query, tc.status)
			if _, err := os.Lstat(secretPath); !os.IsNotExist(err) {
				t.Fatalf("rejected replacement touched the private credential: %v", err)
			}
			assertOriginalConfig(t)
		})
	}

	preview := post(t, form(), origin, "", http.StatusOK)
	assertWebPage(t, preview, http.StatusOK, "与来源的差异", `name="revision" value="`+revision+`"`, newApp)
	draft := regexp.MustCompile(`(?s)id="config" name="config"[^>]*>\n(.*?)</textarea>`).FindStringSubmatch(preview.Body.String())
	if draft == nil {
		t.Fatal("replacement preview did not return a candidate")
	}
	candidate := html.UnescapeString(draft[1])
	reviewed := configRevision([]byte(candidate))
	assertWebPage(t, preview, http.StatusOK, `name="reviewed" value="`+reviewed+`"`)
	config, err := bridge.ParseConfig([]byte(candidate))
	if err != nil {
		t.Fatal(err)
	}
	if config.AppID != "cli_mainfixture" || config.Routes["work"].Model != "draft-model" ||
		len(config.AdditionalAccounts) != 2 || config.AdditionalAccounts[0].AppID != "cli_otherfixture" {
		t.Fatal("replacement did not preserve the other accounts and their draft edits")
	}
	replacement := config.AdditionalAccounts[1]
	if replacement.AppID != newApp || replacement.AppSecretFile != secretPath || replacement.ReceiverAuthorized ||
		replacement.Routes["work"].ProjectID != "project-larkfixture" || replacement.Tenant != "feishu" {
		t.Fatal("candidate does not contain the expected replacement and private file reference")
	}
	if _, err := os.Lstat(replacement.StateDir); !os.IsNotExist(err) {
		t.Fatalf("replacement preview created a state directory: %v", err)
	}
	post(t, form(), origin, "", http.StatusOK)
	conflict := form()
	conflict.Set("replacementSecret", other)
	post(t, conflict, origin, "", http.StatusUnprocessableEntity)
	if stored, err := readAppCredential(secretPath); err != nil || string(stored) != secret+"\n" {
		t.Fatalf("written credential was missing, unreadable or changed after replay or conflict: %v", err)
	}
	assertOriginalConfig(t)

	save := url.Values{"action": {"save"}, "config": {candidate}, "revision": {revision}}
	post(t, save, origin, "", http.StatusConflict)
	save.Set("reviewed", reviewed)
	save.Set("replacementSecret", other)
	post(t, save, origin, "", http.StatusBadRequest)
	assertOriginalConfig(t)
	save.Del("replacementSecret")
	post(t, save, origin, "", http.StatusSeeOther)
	if saved, err := os.ReadFile(path); err != nil || string(saved) != candidate {
		t.Fatalf("reviewed replacement was not saved as shown: %v", err)
	}
	stale := form()
	stale.Set("replacementAppId", "cli_stale_replay")
	post(t, stale, origin, "", http.StatusConflict)
	if _, err := os.Lstat(filepath.Join(profiles.root, "credentials", "cli_stale_replay.secret")); !os.IsNotExist(err) {
		t.Fatalf("stale replay touched a private credential: %v", err)
	}
}

func TestConfigWebManualReplaceWithSecretFromTemplate(t *testing.T) {
	const secret = "synthetic-template-replacement-password"
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	startupSources, err := configWebSources("")
	if err != nil {
		t.Fatal(err)
	}
	if source := startupSources[0]; source.Revision != configRevision(source.JSON) {
		t.Fatal("built-in template did not provide a source revision")
	}
	raw := existingBindConfigFixture(t, dir)
	revision := configRevision(raw)
	handler := configWebHandler(webTestHost, []webSource{
		{ID: "unversioned", JSON: raw}, {ID: "versioned", JSON: raw, Revision: revision},
	})
	const origin = "http://" + webTestHost
	unversioned := origin + "/?source=unversioned"
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, unversioned, nil))
	assertWebPage(t, page, http.StatusOK)
	if strings.Contains(page.Body.String(), `value="replace-app-secret"`) {
		t.Fatal("replacement with a new secret was offered without a source revision")
	}
	values := url.Values{
		"action": {"replace-app-secret"}, "config": {string(raw)}, "revision": {revision},
		"replaceAppId": {"cli_mainfixture"}, "replacementAppId": {"cli_template_replacement"}, "replacementTenant": {"feishu"},
		"replacementAppSecretFile": {""}, "replacementUser": {"ou_new_owner"}, "replacementSecret": {secret},
	}
	for _, tc := range []struct {
		target string
		status int
	}{
		{unversioned, http.StatusConflict},
		{origin + "/?source=versioned", http.StatusOK},
	} {
		req := httptest.NewRequest(http.MethodPost, tc.target, strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", origin)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		assertWebPage(t, response, tc.status)
		if strings.Contains(response.Body.String(), secret) {
			t.Fatal("template response exposed the replacement password")
		}
		if tc.status == http.StatusOK {
			assertWebPage(t, response, http.StatusOK, "与来源的差异", `name="revision" value="`+revision+`"`, "cli_template_replacement")
		}
	}
}
