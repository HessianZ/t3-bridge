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
	"slices"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestConfigWebManualReplaceDraftAccount(t *testing.T) {
	const (
		origin = "http://" + webTestHost
		oldApp = "cli_unsavedapp"
		newApp = "cli_replacement"
		user   = "ou_replacement_owner"
		secret = "synthetic-existing-app-secret\n"
	)
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	raw := existingBindConfigFixture(t, dir)
	path, secretPath := filepath.Join(dir, "config.json"), filepath.Join(dir, "existing.secret")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := localfile.WritePrivateAtomic(secretPath, []byte(secret), true); err != nil {
		t.Fatal(err)
	}
	handler := configWebHandler(webTestHost, []webSource{{ID: "file", Path: path}})
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, origin+"/?source=file", nil))
	assertWebPage(t, page, http.StatusOK, "手动替换账号", `name="replaceAppId"`, `value="cli_larkfixture">附加账号`, `value="replace-app"`)

	revision := configRevision(raw)
	form := url.Values{
		"action": {"replace-app"}, "revision": {revision},
		"config": {strings.Replace(string(raw), `"receiverMachine":"mainfixture"`, `"receiverMachine":"draft-machine"`, 1)},
		"edits": {`[{"path":["additionalAccounts","1","appId"],"value":"` + oldApp + `"},` +
			`{"path":["additionalAccounts","0","routes","work","model"],"value":"draft-model"}]`},
		"replaceAppId": {oldApp}, "replacementAppId": {newApp}, "replacementTenant": {"feishu"},
		"replacementAppSecretFile": {secretPath}, "replacementUser": {user},
	}
	post := func(requestOrigin, query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, origin+"/?source=file"+query, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if requestOrigin != "" {
			req.Header.Set("Origin", requestOrigin)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	assertWebPage(t, post("", ""), http.StatusForbidden)
	assertWebPage(t, post("https://foreign.test", ""), http.StatusForbidden)
	assertWebPage(t, post(origin, "&action=check"), http.StatusForbidden)
	assertWebPage(t, post(origin, "&replaceAppId=cli_mainfixture"), http.StatusBadRequest)
	preview := post(origin, "")
	assertWebPage(t, preview, http.StatusOK, "与来源的差异", `name="revision" value="`+revision+`"`, newApp)
	body := preview.Body.String()
	draft := regexp.MustCompile(`(?s)id="config" name="config"[^>]*>\n(.*?)</textarea>`).FindStringSubmatch(body)
	if draft == nil || strings.Contains(body, strings.TrimSpace(secret)) {
		t.Fatal("expected a candidate holding only the credential reference")
	}
	candidate := html.UnescapeString(draft[1])
	assertWebPage(t, preview, http.StatusOK, `name="reviewed" value="`+configRevision([]byte(candidate))+`"`)
	config, err := bridge.ParseConfig([]byte(candidate))
	if err != nil {
		t.Fatal(err)
	}
	if config.AppID != "cli_mainfixture" || config.ReceiverMachine != "draft-machine" ||
		len(config.AdditionalAccounts) != 2 || config.AdditionalAccounts[0].AppID != "cli_otherfixture" ||
		config.AdditionalAccounts[0].Routes["work"].Model != "draft-model" {
		t.Fatal("replacement did not preserve the other accounts and their draft edits")
	}
	replacement := config.AdditionalAccounts[1]
	if replacement.AppID != newApp || replacement.Tenant != "feishu" || replacement.AppSecretFile != secretPath ||
		!slices.Equal(replacement.Users, []string{user}) || replacement.ReceiverAuthorized ||
		!filepath.IsAbs(replacement.StateDir) || replacement.StateDir == filepath.Join(dir, "larkfixture-state") ||
		replacement.DefaultRoute != "work" || replacement.Routes["work"].ProjectID != "project-larkfixture" {
		t.Fatal("expected a replacement in the selected account's position with independent state and the old default route")
	}
	if _, err := os.Lstat(replacement.StateDir); !os.IsNotExist(err) {
		t.Fatalf("replacement preview created the state directory: %v", err)
	}
	assertWebPage(t, submitWebConfig(handler, url.Values{
		"action": {"save"}, "config": {candidate}, "revision": {revision},
	}), http.StatusConflict, "候选配置已修改")
	if saved, err := os.ReadFile(path); err != nil || !bytes.Equal(saved, raw) {
		t.Fatalf("replacement preview or unreviewed save changed the source configuration: %v", err)
	}
	if stored, err := os.ReadFile(secretPath); err != nil || string(stored) != secret {
		t.Fatalf("replacement preview changed the existing private credential: %v", err)
	}
	assertWebPage(t, submitWebConfig(handler, url.Values{
		"action": {"save"}, "config": {candidate}, "revision": {revision},
		"reviewed": {configRevision([]byte(candidate))},
	}), http.StatusSeeOther)
	if saved, err := os.ReadFile(path); err != nil || string(saved) != candidate {
		t.Fatalf("reviewed replacement was not saved as shown: %v", err)
	}
}
