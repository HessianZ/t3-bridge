package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

const credentialWebTestApp = "cli_web_credentials"

func TestConfigWebAppCredentialMaintenance(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "settings", "config.json")
	secretPath := filepath.Join(dir, "private", "app.secret")
	sources, err := configWebSources(configPath)
	if err != nil {
		t.Fatal(err)
	}
	handler := configWebHandler(webTestHost, sources)
	base := "http://" + webTestHost
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, base+path, nil))
		return w
	}
	assertWebPage(t, get("/credentials?source=file"), http.StatusOK, "请先在配置工作台保存本机 JSON 配置")
	writeCredentialWebConfig(t, configPath, secretPath, credentialWebTestApp)
	assertWebPage(t, get("/?source=file"), http.StatusOK, `href="/credentials?source=file"`)
	selected := get("/credentials?source=file&appId=" + credentialWebTestApp)
	assertWebPage(t, selected, http.StatusOK, credentialWebTestApp, secretPath, "feishu", "missing", `value="preview-set"`, "</html>")
	for _, unexpected := range []string{`value="generic"`, `value="preview-remove"`, `type="password"`, `<script`} {
		if strings.Contains(selected.Body.String(), unexpected) {
			t.Fatalf("selection offered unexpected markup %q", unexpected)
		}
	}
	form := url.Values{"source": {"file"}, "appId": {credentialWebTestApp}, "action": {"preview-set"}}
	form.Set("secret", "synthetic-premature")
	assertWebPage(t, submitCredentialWebForm(handler, form, base), http.StatusBadRequest)
	form.Del("secret")
	preview := submitCredentialWebForm(handler, form, base)
	assertWebPage(t, preview, http.StatusOK, `type="password" name="secret"`, `value="set"`, `value="cancel"`, secretPath, "</html>")
	if preview.Header().Get("Referrer-Policy") != "same-origin" {
		t.Fatal("native form submission requires a same-origin referrer policy")
	}
	review := credentialWebToken(t, preview)
	form.Set("action", "cancel")
	form.Set("review", review)
	canceled := submitCredentialWebForm(handler, form, base)
	assertWebPage(t, canceled, http.StatusSeeOther)
	if canceled.Header().Get("Location") != "/credentials?appId="+credentialWebTestApp+"&source=file" {
		t.Fatal("cancel did not return to the selected saved App")
	}
	const value = "synthetic+secret&value%"
	form.Set("action", "set")
	form.Set("secret", value)
	assertWebPage(t, submitCredentialWebForm(handler, form, base), http.StatusConflict, "刷新并重新预览")
	if _, err := os.Stat(secretPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preview or cancellation wrote a credential")
	}
	form.Del("secret")
	form.Del("review")
	form.Set("action", "preview-set")
	form.Set("review", credentialWebToken(t, submitCredentialWebForm(handler, form, base)))
	form.Set("action", "set")
	form.Set("secret", value)
	for _, origin := range []string{"", "http://other.invalid"} {
		assertWebPage(t, submitCredentialWebForm(handler, form, origin), http.StatusForbidden)
	}
	form.Set("secret", strings.Repeat("%", maxAppCredentialBytes))
	assertWebPage(t, submitCredentialWebForm(handler, form, base), http.StatusRequestEntityTooLarge)
	form.Set("secret", value)
	saved := submitCredentialWebForm(handler, form, base)
	assertWebPage(t, saved, http.StatusOK, "已保存本机 App Secret", "ready", "</html>")
	if raw, err := os.ReadFile(secretPath); err != nil || strings.TrimSpace(string(raw)) != value {
		t.Fatal("the reviewed credential was not saved to the selected file")
	}
	assertWebPage(t, submitCredentialWebForm(handler, form, base), http.StatusConflict, "刷新并重新预览")
	for _, response := range []*httptest.ResponseRecorder{saved, get("/credentials?source=file&appId=" + credentialWebTestApp + "&action=get")} {
		if strings.Contains(response.Body.String(), "synthetic+secret") || strings.Contains(response.Header().Get("Location"), value) {
			t.Fatal("credential appeared in a response")
		}
	}

	form.Del("secret")
	form.Del("review")
	form.Set("action", "get")
	assertWebPage(t, submitCredentialWebForm(handler, form, base), http.StatusBadRequest)
	form.Set("action", "preview-remove")
	deletion := submitCredentialWebForm(handler, form, base)
	assertWebPage(t, deletion, http.StatusOK, `type="checkbox" name="confirm" value="remove" required`, "确认删除上方路径", secretPath)
	if strings.Contains(deletion.Body.String(), `type="password"`) {
		t.Fatal("deleting offered a credential input")
	}
	form.Set("review", credentialWebToken(t, deletion))
	form.Set("action", "remove")
	assertWebPage(t, submitCredentialWebForm(handler, form, base), http.StatusBadRequest)
	if _, err := os.Stat(secretPath); err != nil {
		t.Fatal("unconfirmed deletion changed the credential", err)
	}
	form.Set("confirm", "remove")
	assertWebPage(t, submitCredentialWebForm(handler, form, base), http.StatusOK, "已删除本机 App Secret 文件", "missing")
	if _, err := os.Stat(secretPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("confirmed deletion did not remove the credential")
	}
}

func TestConfigWebAppCredentialReviewBinding(t *testing.T) {
	dir := t.TempDir()
	registry := profileRegistry{root: filepath.Join(dir, "registry")}
	configPath := filepath.Join(dir, "settings", "config.json")
	profilePath, err := registry.file("other")
	if err != nil {
		t.Fatal(err)
	}
	secretPath := filepath.Join(dir, "private", "app.secret")
	otherPath := filepath.Join(dir, "private", "other.secret")
	writeCredentialWebConfig(t, configPath, secretPath, credentialWebTestApp)
	writeCredentialWebConfig(t, profilePath, otherPath, credentialWebTestApp)
	sources, err := configWebSources(configPath)
	if err != nil {
		t.Fatal(err)
	}
	handler := configWorkspaceHandler(webTestHost, sources, registry)
	base := "http://" + webTestHost
	for _, test := range []struct {
		name       string
		change     func(*testing.T, url.Values)
		wantNotice string
		wantFile   string
	}{
		{"source", func(_ *testing.T, form url.Values) { form.Set("source", "profile-other") }, "预览已失效", ""},
		{"App", func(_ *testing.T, form url.Values) { form.Set("appId", "cli_other_app") }, "预览已失效", ""},
		{"saved path", func(t *testing.T, _ url.Values) {
			writeCredentialWebConfig(t, configPath, otherPath, credentialWebTestApp)
		}, "配置或凭据已变化", ""},
		{"credential revision", func(t *testing.T, _ url.Values) {
			if _, err := localfile.WritePrivateAtomic(secretPath, []byte("synthetic-changed\n"), false); err != nil {
				t.Fatal(err)
			}
		}, "配置或凭据已变化", "synthetic-changed\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			writeCredentialWebConfig(t, configPath, secretPath, credentialWebTestApp)
			form := url.Values{"source": {"file"}, "appId": {credentialWebTestApp}, "action": {"preview-set"}}
			preview := submitCredentialWebForm(handler, form, base)
			assertWebPage(t, preview, http.StatusOK, `value="profile-other"`, secretPath)
			form.Set("review", credentialWebToken(t, preview))
			form.Set("action", "set")
			form.Set("secret", "synthetic-submitted")
			test.change(t, form)
			conflict := submitCredentialWebForm(handler, form, base)
			assertWebPage(t, conflict, http.StatusConflict, test.wantNotice, "刷新凭据状态")
			if strings.Contains(conflict.Body.String(), "synthetic-submitted") {
				t.Fatal("credential appeared in a conflict response")
			}
			if _, err := os.Stat(otherPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("conflicting submission touched the other credential path")
			}
			raw, err := os.ReadFile(secretPath)
			if test.wantFile == "" && !errors.Is(err, os.ErrNotExist) || test.wantFile != "" && (err != nil || string(raw) != test.wantFile) {
				t.Fatal("conflicting submission changed the reviewed credential file")
			}
		})
	}
}

func writeCredentialWebConfig(t *testing.T, path, secretPath, appID string) {
	t.Helper()
	if err := localfile.MkdirAllPrivate(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if err := localfile.MkdirAllPrivate(filepath.Dir(secretPath)); err != nil {
		t.Fatal(err)
	}
	raw, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	edits, err := json.Marshal([]legacyImportEdit{
		{[]string{"appId"}, appID},
		{[]string{"appSecretFile"}, secretPath},
		{[]string{"stateDir"}, filepath.Join(filepath.Dir(path), "state")},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = bridge.EditConfigJSON(raw, edits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localfile.WritePrivateAtomic(path, raw, false); err != nil {
		t.Fatal(err)
	}
}

func submitCredentialWebForm(handler http.Handler, form url.Values, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "http://"+webTestHost+"/credentials", strings.NewReader(form.Encode()))
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

var credentialWebReviewField = regexp.MustCompile(`name="review" value="([A-Z2-7]+)"`)

func credentialWebToken(t *testing.T, page *httptest.ResponseRecorder) string {
	t.Helper()
	match := credentialWebReviewField.FindStringSubmatch(page.Body.String())
	if match == nil {
		t.Fatalf("credential preview did not contain a review token (status %d)", page.Code)
	}
	return match[1]
}
