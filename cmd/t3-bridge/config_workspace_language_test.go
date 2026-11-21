package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceLanguage(t *testing.T) {
	sources, err := configWebSources("")
	if err != nil {
		t.Fatal(err)
	}
	profiles := profileRegistry{root: filepath.Join(t.TempDir(), "preferences")}
	handler := func() http.Handler {
		return profiles.withWorkspaceLanguage(configWorkspaceHandler("localhost", sources, profiles), false)
	}
	get := func(h http.Handler, path, accept string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil)
		req.Header.Set("Accept-Language", accept)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	wantPage := func(w *httptest.ResponseRecorder, language workspaceLanguage, label string) {
		t.Helper()
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `<html lang="`+string(language)+`">`) ||
			!strings.Contains(w.Body.String(), label) {
			t.Errorf("wanted %s and %q in page with status %d", language, label, w.Code)
		}
	}
	post := func(h http.Handler, value, origin string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "http://localhost/workspace-language",
			strings.NewReader(url.Values{"language": {value}}.Encode()))
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	h := handler()
	wantPage(get(h, "/", "zh-Hans-CN,en;q=0.8"), workspaceZH, "结构校验")
	wantPage(get(h, "/", "zh;q=0.4,en-US;q=1"), workspaceEN, "Validate &amp; review")
	if status := post(h, "zh-CN", "http://localhost"); status != http.StatusNoContent {
		t.Fatalf("save language status = %d", status)
	}
	h = handler()
	wantPage(get(h, "/", "en-US"), workspaceZH, "结构校验")
	wantPage(get(h, "/profiles", "en-US"), workspaceZH, "命名配置")
	if status := post(h, "en", "https://example.com"); status != http.StatusForbidden {
		t.Errorf("cross-origin language change status = %d", status)
	}
	if status := post(h, "invalid", "http://localhost"); status != http.StatusBadRequest {
		t.Errorf("invalid language status = %d", status)
	}
	if status := post(h, "auto", "http://localhost"); status != http.StatusNoContent {
		t.Fatalf("restore system status = %d", status)
	}
	wantPage(get(handler(), "/profiles", "en"), workspaceEN, "Profiles")
	if got := get(h, "/workspace-language?return=%2F%2Fevil.example", "en"); got.Code != http.StatusBadRequest {
		t.Error("cross-origin redirect accepted")
	}
	if got := get(h, "/workspace-language?return=%2F%3Fsource%3Dgeneric%23routes", "en"); got.Header().Get("Location") != "/?source=generic#routes" {
		t.Errorf("return URL = %q", got.Header().Get("Location"))
	}
}

func TestTerminalWorkspaceLanguage(t *testing.T) {
	sources, err := configWebSources("")
	if err != nil {
		t.Fatal(err)
	}
	m := newConfigTUI(sources[0])
	m.profiles.registry = profileRegistry{root: filepath.Join(t.TempDir(), "preferences")}
	m.languageChoice = workspaceAuto
	draft := m.draft
	for _, want := range []workspaceLanguage{workspaceEN, workspaceZH, workspaceAuto} {
		if _, handled := m.handleKey("f3"); !handled {
			t.Fatal("language shortcut was ignored")
		}
		if m.languageChoice != want || m.profiles.registry.workspaceLanguage() != want {
			t.Fatalf("F3 language = %q, want %q", m.languageChoice, want)
		}
	}
	if m.draft != draft || m.language != systemWorkspaceLanguage() {
		t.Fatal("returning to system language changed the configuration draft or did not adopt the system language")
	}
}
