package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestWorkbenchProfilePurge(t *testing.T) {
	registry, handler, source, secretPath := newProfileLifecycleFixture(t)
	if _, err := registry.create("other", source); err != nil {
		t.Fatal(err)
	}
	config, err := bridge.LoadConfig(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := localfile.MkdirAllPrivate(config.StateDir); err != nil {
		t.Fatal(err)
	}
	if err := registry.run([]string{"use", "work"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"name": {"work"}, "operation": {"purge"}, "action": {"preview"}}
	post := func(origin string) *httptest.ResponseRecorder {
		return requestProfileLifecycle(handler, "/profiles/lifecycle", form, origin)
	}
	assertWebPage(t, post(""), http.StatusForbidden)
	plan, err := registry.previewPurge("work")
	if err != nil {
		t.Fatal(err)
	}
	markers := []string{"预览永久清除", "将清除的路径", "将保留的路径", "清除后的默认配置 <strong>other</strong>", plan.ProfileDir,
		`type="checkbox" name="confirm" value="yes" required`, `value="cancel"`, "</html>"}
	markers = append(markers, plan.Paths...)
	markers = append(markers, plan.Retained...)
	preview := post(profileLifecycleTestOrigin)
	assertWebPage(t, preview, http.StatusOK, markers...)
	form.Set("review", credentialWebToken(t, preview))
	form.Set("action", "cancel")
	assertWebPage(t, post(profileLifecycleTestOrigin), http.StatusSeeOther)
	form.Set("action", "confirm")
	form.Set("confirm", "yes")
	assertWebPage(t, post(profileLifecycleTestOrigin), http.StatusConflict, "重新预览")
	if _, err := os.Stat(source); err != nil {
		t.Fatal("preview or cancellation changed the profile", err)
	}

	confirm := func() {
		t.Helper()
		form.Del("confirm")
		form.Del("review")
		form.Set("action", "preview")
		form.Set("review", credentialWebToken(t, post(profileLifecycleTestOrigin)))
		form.Set("action", "confirm")
		form.Set("confirm", "yes")
	}
	confirm()
	changeProfileLifecycleRevision(t, source)
	assertWebPage(t, post(profileLifecycleTestOrigin), http.StatusConflict, "重新预览")
	if _, err := os.Stat(source); err != nil {
		t.Fatal("stale confirmation changed the profile", err)
	}
	confirm()
	assertWebPage(t, post(profileLifecycleTestOrigin), http.StatusOK, "已永久清除命名配置")
	assertWebPage(t, post(profileLifecycleTestOrigin), http.StatusConflict, "重新预览")
	assertProfileLifecycleFileMissing(t, source)
	assertProfileLifecycleFileMissing(t, config.StateDir)
	if _, err := os.Stat(secretPath); err != nil {
		t.Fatal("purge removed an external credential", err)
	}
	if active, err := registry.active(); err != nil || active != "other" {
		t.Fatalf("unexpected default after clearing its profile: %q, %v", active, err)
	}
}

func TestWorkbenchProfilePrivateExport(t *testing.T) {
	registry, handler, source, _ := newProfileLifecycleFixture(t)
	if _, err := registry.create("other", source); err != nil {
		t.Fatal(err)
	}
	const path = "/profiles/lifecycle"
	assertWebPage(t, requestProfileLifecycle(handler, "/profiles", nil, ""), http.StatusOK,
		`href="/profiles/export?name=work"`, `href="/profiles/lifecycle?name=work"`, "预览永久清除")
	assertWebPage(t, requestProfileLifecycle(handler, path+"?name=work", nil, ""), http.StatusOK,
		`name="destination"`, "本机新 JSON 文件的绝对路径", "</html>")
	safe := requestProfileLifecycle(handler, "/profiles/export?name=work&includeSecrets=true", nil, "")
	assertWebPage(t, safe, http.StatusOK)
	if _, err := bridge.ParseConfig(safe.Body.Bytes()); err != nil || strings.Contains(safe.Body.String(), profileLifecycleTestSecret) {
		t.Fatalf("unexpected ordinary JSON export: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "private", "review & export.json")
	post := func(t *testing.T, form url.Values, origin string) *httptest.ResponseRecorder {
		t.Helper()
		response := requestProfileLifecycle(handler, path, form, origin)
		if strings.Contains(response.Body.String(), profileLifecycleTestSecret) ||
			response.Header().Get("Content-Disposition") != "" || response.Header().Get("Content-Type") == "application/json" {
			t.Fatal("private export returned credential data or a download response")
		}
		return response
	}
	preview := func(t *testing.T) url.Values {
		t.Helper()
		form := url.Values{"name": {"work"}, "operation": {"export"}, "action": {"preview"}, "destination": {dest}}
		response := post(t, form, profileLifecycleTestOrigin)
		assertWebPage(t, response, http.StatusOK, source, dest, "1 项 App Secret", `value="cancel"`,
			`type="checkbox" name="confirm" value="yes" required`, "</html>")
		if response.Header().Get("Referrer-Policy") != "same-origin" {
			t.Fatal("the confirmation form needs a same-origin referrer policy")
		}
		form.Set("review", credentialWebToken(t, response))
		form.Set("action", "confirm")
		form.Set("confirm", "yes")
		return form
	}
	assertWebPage(t, post(t, url.Values{"name": {"work"}, "operation": {"export"}, "action": {"preview"},
		"destination": {"relative.json"}}, profileLifecycleTestOrigin), http.StatusConflict, "绝对路径")
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, url.Values)
		status int
	}{
		{"cancel", func(_ *testing.T, form url.Values) {
			form.Set("action", "cancel")
			form.Del("confirm")
		}, http.StatusSeeOther},
		{"missing confirmation", func(_ *testing.T, form url.Values) { form.Del("confirm") }, http.StatusBadRequest},
		{"other profile", func(_ *testing.T, form url.Values) { form.Set("name", "other") }, http.StatusConflict},
		{"other operation", func(_ *testing.T, form url.Values) {
			form.Set("operation", "purge")
			form.Del("destination")
		}, http.StatusConflict},
		{"other destination", func(_ *testing.T, form url.Values) {
			form.Set("destination", filepath.Join(registry.root, "changed.json"))
		}, http.StatusConflict},
		{"new source revision", func(t *testing.T, _ url.Values) {
			changeProfileLifecycleRevision(t, source)
		}, http.StatusConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			form := preview(t)
			originalToken := form.Get("review")
			test.mutate(t, form)
			assertWebPage(t, post(t, form, profileLifecycleTestOrigin), test.status)
			original := url.Values{"name": {"work"}, "operation": {"export"}, "action": {"confirm"},
				"destination": {dest}, "review": {originalToken}, "confirm": {"yes"}}
			assertWebPage(t, post(t, original, profileLifecycleTestOrigin), http.StatusConflict, "重新预览")
			assertProfileLifecycleFileMissing(t, dest)
			assertProfileLifecycleFileMissing(t, filepath.Join(registry.root, "changed.json"))
		})
	}

	form := preview(t)
	assertProfileLifecycleFileMissing(t, dest)
	if err := localfile.MkdirAllPrivate(filepath.Dir(dest)); err != nil {
		t.Fatal(err)
	}
	if _, err := localfile.WritePrivateAtomic(dest, []byte("synthetic existing file"), true); err != nil {
		t.Fatal(err)
	}
	assertWebPage(t, post(t, form, profileLifecycleTestOrigin), http.StatusConflict, "重新预览")
	if raw, err := os.ReadFile(dest); err != nil || string(raw) != "synthetic existing file" {
		t.Fatal("private export replaced an existing destination")
	}
	if err := os.Remove(dest); err != nil {
		t.Fatal(err)
	}
	assertWebPage(t, post(t, form, profileLifecycleTestOrigin), http.StatusConflict, "重新预览")
	assertProfileLifecycleFileMissing(t, dest)

	form = preview(t)
	for _, origin := range []string{"", "http://different.invalid"} {
		assertWebPage(t, post(t, form, origin), http.StatusForbidden)
	}
	assertProfileLifecycleFileMissing(t, dest)
	assertWebPage(t, post(t, form, profileLifecycleTestOrigin), http.StatusOK, "已写入本机私有导出文件", dest, "</html>")
	assertWebPage(t, post(t, form, profileLifecycleTestOrigin), http.StatusConflict, "重新预览")
	file, err := localfile.OpenPrivate(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var bundle profileBundle
	if err := json.NewDecoder(file).Decode(&bundle); err != nil || bundle.Profile != "work" ||
		len(bundle.AppSecrets) != 1 || bundle.AppSecrets[0].Secret != profileLifecycleTestSecret {
		t.Fatal("private file does not contain the selected profile and its synthetic credential")
	}
}

const profileLifecycleTestOrigin = "http://" + webTestHost
const profileLifecycleTestSecret = "synthetic-lifecycle-app-secret"

func newProfileLifecycleFixture(t *testing.T) (profileRegistry, http.Handler, string, string) {
	t.Helper()
	dir := t.TempDir()
	registry := profileRegistry{root: filepath.Join(dir, "registry")}
	draft := filepath.Join(dir, "source", "draft.json")
	secretPath := filepath.Join(dir, "credentials", "app.secret")
	writeCredentialWebConfig(t, draft, secretPath, "cli_lifecycle_test")
	if _, err := localfile.WritePrivateAtomic(secretPath, []byte(profileLifecycleTestSecret+"\n"), true); err != nil {
		t.Fatal(err)
	}
	source, err := registry.create("work", draft)
	if err != nil {
		t.Fatal(err)
	}
	return registry, configWorkspaceHandler(webTestHost, nil, registry), source, secretPath
}

func requestProfileLifecycle(handler http.Handler, path string, form url.Values, origin string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, profileLifecycleTestOrigin+path, nil)
	if form != nil {
		request = httptest.NewRequest(http.MethodPost, profileLifecycleTestOrigin+path, strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func changeProfileLifecycleRevision(t *testing.T, source string) {
	t.Helper()
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localfile.WritePrivateAtomic(source, append(raw, '\n'), false); err != nil {
		t.Fatal(err)
	}
}

func assertProfileLifecycleFileMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected %q to be absent: %v", path, err)
	}
}
