package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigWebFirstRunGuide(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	sources, err := configWebSources(path)
	if err != nil {
		t.Fatal(err)
	}
	handler := configWebHandler(webTestHost, sources)
	get := func(source string) *httptest.ResponseRecorder {
		page := httptest.NewRecorder()
		handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "http://"+webTestHost+"/?source="+source, nil))
		return page
	}

	const guide = `id="first-run"`
	const preflight = `href="/runtime/install?source=file"`
	firstRun := get("file")
	assertWebPage(t, firstRun, http.StatusOK, guide+" open", "首次配置", "保存后打开「接收预检」")
	for _, target := range []string{"feishu-bind", "primary-feishu", "primary-access", "primary-t3", "primary-routes", "config-actions"} {
		assertWebPage(t, firstRun, http.StatusOK, `href="#`+target+`"`, `id="`+target+`"`)
	}
	if strings.Contains(firstRun.Body.String(), preflight) {
		t.Fatal("first-run page linked to preflight before saving")
	}

	form := url.Values{"config": {string(sources[0].JSON)}, "revision": {"missing"}, "action": {"check"}}
	review := submitWebConfig(handler, form)
	assertWebPage(t, review, http.StatusOK, guide+">", "与来源的差异")
	form.Set("config", "{")
	rawOnly := submitWebConfig(handler, form)
	assertWebPage(t, rawOnly, http.StatusUnprocessableEntity,
		`data-workbench-default="advanced"`, `id="raw-config" class="panel advanced-row" data-workbench-page="advanced" open`)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("opening or reviewing the guide touched the configuration: %v", err)
	}

	template := get("generic")
	assertWebPage(t, template, http.StatusOK)
	if err := os.WriteFile(path, sources[0].JSON, 0600); err != nil {
		t.Fatal(err)
	}
	existing := get("file")
	assertWebPage(t, existing, http.StatusOK, preflight)
	for name, page := range map[string]*httptest.ResponseRecorder{"raw draft": rawOnly, "template": template, "existing file": existing} {
		if strings.Contains(page.Body.String(), guide) {
			t.Errorf("first-run guide appeared for %s", name)
		}
	}
}
