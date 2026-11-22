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
	const brand = `<img src="/workspace-icon.png" alt="" width="20" height="20">T3 Bridge`
	home := get(handler, "/")
	assertWebPage(t, home, http.StatusOK, link, `<header class="top">`, brand, `<link rel="icon" type="image/png" href="/workspace-icon.png">`)
	assertWebPage(t, home, http.StatusOK, `class="language-icon"`, `class="configuration-icon"`)
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
		// The advanced-card sub-tools no longer show the global tabs; they are
		// covered by TestAdvancedToolPagesNavigation. Named-config pages still use
		// the full workspace navigation.
		{"advanced", []string{"/profiles", "/profiles/import", "/profiles/lifecycle?name=fixture"}},
	} {
		for _, path := range group.paths {
			t.Run(path, func(t *testing.T) {
				assertWebPage(t, get(handler, path), http.StatusOK, link, `<header class="top">`, brand, `#`+group.tab+`" aria-current="page"`, "</html>")
			})
		}
	}
	credentials := get(handler, "/credentials?source=file")
	credentialPolicy := credentials.Header().Get("Content-Security-Policy")
	if !strings.Contains(credentialPolicy, "style-src 'self'") || !strings.Contains(credentialPolicy, "img-src 'self'") ||
		!strings.Contains(credentialPolicy, "default-src 'none'") || strings.Contains(credentialPolicy, "script-src") {
		t.Fatalf("unexpected credential page resource policy: %q", credentialPolicy)
	}
	logs := configRuntimeLogsHandler(func(string, bool) (string, error) { return "synthetic service log", nil })
	page := get(logs, "/runtime/logs?instance=fixture")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), link) ||
		!strings.Contains(page.Body.String(), `href="/#runtime" aria-current="page"`) {
		t.Fatal("service logs did not use the workspace theme")
	}
}

// TestAdvancedToolPagesNavigation verifies that the six advanced-card sub-tools
// keep only the "Advanced" context: a shared "← 返回高级" back link with a
// source-preserving target and breadcrumb, no global tabs and no local
// runtime/status navigation. Business forms and the desktop theme are unchanged.
func TestAdvancedToolPagesNavigation(t *testing.T) {
	get := func(handler http.Handler, path string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://"+webTestHost+path, nil))
		return response
	}

	// Handler with a saved private config: sub-tools are reached with ?source=file.
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
	runtimeStub := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	withSource := configWorkspaceHandlerWithRuntime(webTestHost, sources, profiles, runtimeStub)

	// Handler with no saved config at all: sub-tools are reached without a source.
	withoutSource := configWorkspaceHandlerWithRuntime(webTestHost, nil, profileRegistry{}, runtimeStub)

	toolPages := []struct {
		path, tool string
		// project-register requires a saved source by design (GET without one
		// returns 400), so it has no reachable no-source state.
		requireSource bool
	}{
		{"/credentials", "应用凭据", false},
		{"/tool-identity", "核验飞书工具身份", false},
		{"/meeting-preflight", "会议权限预检", false},
		{"/project-register", "登记 T3 项目", true},
		{"/workspace-import", "迁移旧命名工作区", false},
		{"/legacy-session-import", "转接旧会话", false},
	}
	for _, toolPage := range toolPages {
		t.Run(toolPage.path, func(t *testing.T) {
			assertAdvancedToolPage(t, get(withSource, toolPage.path+"?source=file"), `/?source=file#advanced`, toolPage.tool)
			if !toolPage.requireSource {
				assertAdvancedToolPage(t, get(withoutSource, toolPage.path), `/#advanced`, toolPage.tool)
			}
		})
	}
}

func assertAdvancedToolPage(t *testing.T, page *httptest.ResponseRecorder, wantHref, tool string) {
	t.Helper()
	// Preserve the shared-theme and business-content semantics: the page still
	// renders its own heading and uses the desktop workspace chrome.
	assertWebPage(t, page, http.StatusOK,
		`<header class="top">`, `<img src="/workspace-icon.png"`, `<link rel="stylesheet" href="/workspace.css">`, "</html>",
		"<h1>"+tool+"</h1>",
		`class="back-link" href="`+wantHref+`"`, "← 返回高级",
		`<ol class="breadcrumb">`, "配置工作台", `aria-current="page">`+tool)

	body := page.Body.String()
	// No global tabs (账号/项目与路由/运行/高级) and no local runtime/status
	// or generic "配置工作台" navigation entries.
	for _, marker := range []string{
		`class="tabs"`, `工作台页面`, `#accounts`, `#routes`, `#runtime"`, `#advanced" aria-current="page"`,
		`href="/runtime"`, `配置工作台</a>`, `运行状态</a>`,
	} {
		if strings.Contains(body, marker) {
			t.Errorf("advanced tool page unexpectedly contains %q", marker)
		}
	}
}

// TestAdvancedToolBackLinkBilingual checks the source-preserving back-link
// target for both source states and both UI languages through the shared
// workspace-header template used by all six sub-tools.
func TestAdvancedToolBackLinkBilingual(t *testing.T) {
	page := newWorkspacePage("advanced nav").Parse(`{{template "workspace-header" (advancedToolNav .Source .Tool)}}`)
	data := struct{ Source, Tool string }{Tool: "应用凭据"}
	for _, tc := range []struct {
		name, source, wantHref string
	}{
		{"with source", "file", `/?source=file#advanced`},
		{"no source", "", `/#advanced`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data.Source = tc.source
			for _, lang := range []struct {
				accept, back, tool string
			}{
				{"zh-CN", "← 返回高级", "应用凭据"},
				{"en", "← Back to Advanced", "Application of evidence"},
			} {
				req := httptest.NewRequest(http.MethodGet, "http://"+webTestHost+"/", nil)
				req.Header.Set("Accept-Language", lang.accept)
				rec := httptest.NewRecorder()
				if err := page.Execute(rec, req, data); err != nil {
					t.Fatal(err)
				}
				body := rec.Body.String()
				if !strings.Contains(body, `href="`+tc.wantHref+`"`) {
					t.Errorf("%s: back link missing href %q in %q", lang.accept, tc.wantHref, body)
				}
				if !strings.Contains(body, lang.back) {
					t.Errorf("%s: back link missing label %q", lang.accept, lang.back)
				}
				if !strings.Contains(body, lang.tool) {
					t.Errorf("%s: breadcrumb missing tool %q", lang.accept, lang.tool)
				}
			}
		})
	}
}
