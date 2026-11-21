package main

import (
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspacePagesShareTheme(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bridge.json")
	data, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	sources, err := configWebSources(path)
	if err != nil {
		t.Fatal(err)
	}
	profiles := profileRegistry{root: t.TempDir()}
	runtimePage := configRuntimeHandler(profiles, func() ([]serviceInventoryEntry, error) {
		return nil, nil
	})
	handler := configWorkspaceHandlerWithRuntime(webTestHost, sources, profiles, runtimePage)
	get := func(handler http.Handler, path string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://"+webTestHost+path, nil))
		return response
	}
	css := get(handler, "/workspace.css")
	assertWebPage(t, css, http.StatusOK, ".tabs", ".tool-page h1", "--canvas", "prefers-color-scheme: dark")
	if css.Header().Get("Content-Type") != "text/css; charset=utf-8" {
		t.Fatalf("unexpected stylesheet type: %q", css.Header().Get("Content-Type"))
	}
	const link = `<link rel="stylesheet" href="/workspace.css">`
	const brand = `<img src="/workspace-icon.png" alt="" width="24" height="24">T3 Bridge`
	home := get(handler, "/")
	assertWebPage(t, home, http.StatusOK, link, `<header class="top">`, brand, `<link rel="icon" type="image/png" href="/workspace-icon.png">`)
	policy := home.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "style-src 'self'") || !strings.Contains(policy, "img-src 'self' data:") {
		t.Fatal("workspace policy blocks its own stylesheet or icons")
	}
	icon := get(handler, "/workspace-icon.png")
	if icon.Code != http.StatusOK || icon.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("workspace icon returned status %d and type %q", icon.Code, icon.Header().Get("Content-Type"))
	}
	if _, err := png.Decode(icon.Body); err != nil {
		t.Fatalf("workspace icon is not a valid PNG: %v", err)
	}
	favicon := get(handler, "/favicon.ico")
	if favicon.Code != http.StatusTemporaryRedirect || favicon.Header().Get("Location") != "/workspace-icon.png" {
		t.Fatalf("implicit favicon returned status %d and location %q", favicon.Code, favicon.Header().Get("Location"))
	}
	for _, group := range []struct {
		tab   string
		paths []string
	}{
		{"runtime", []string{"/runtime", "/runtime/install?source=file", "/runtime/receiver-readiness?source=file",
			"/runtime/receiver-enable?source=file", "/runtime/groups", "/runtime/access", "/runtime/settings",
			"/runtime/meetings", "/runtime/reconnect", "/telemetry-archive?source=file",
			"/drain-check?source=file", "/message-receipt?source=file"}},
		{"advanced", []string{"/credentials?source=file", "/tool-identity?source=file", "/meeting-preflight?source=file",
			"/project-register?source=file", "/workspace-import?source=file", "/legacy-session-import?source=file",
			"/profiles", "/profiles/import", "/profiles/lifecycle?name=fixture"}},
	} {
		for _, path := range group.paths {
			t.Run(path, func(t *testing.T) {
				assertWebPage(t, get(handler, path), http.StatusOK, link, `<header class="top">`, brand, `#`+group.tab+`" aria-current="page"`, "</html>")
			})
		}
	}
	logs := configRuntimeLogsHandler(func(string, bool) (string, error) { return "synthetic service log", nil })
	page := get(logs, "/runtime/logs?instance=fixture")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), link) ||
		!strings.Contains(page.Body.String(), `href="/#runtime" aria-current="page"`) {
		t.Fatal("service logs did not use the workspace theme")
	}
}
