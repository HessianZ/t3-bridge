package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestWorkbenchProfileManagement(t *testing.T) {
	registry := profileRegistry{root: t.TempDir()}
	sources, err := configWebSources("")
	if err != nil {
		t.Fatal(err)
	}
	handler := configWorkspaceHandler(webTestHost, sources, registry)
	request := func(method, path string, form url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://"+webTestHost+path, strings.NewReader(form.Encode()))
		if method == http.MethodPost {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	profilePath := "/?source=profile-local-test"
	form := url.Values{"action": {"create"}, "name": {"local-test"}}
	response := request(http.MethodPost, "/profiles", form)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != profilePath {
		t.Fatalf("could not create profile in workbench: %s", response.Body)
	}
	assertWebPage(t, request(http.MethodGet, profilePath, nil), http.StatusOK, "命名配置 · local-test", "local-test")
	response = request(http.MethodGet, "/profiles/export?name=local-test", nil)
	settings, err := bridge.ParseConfig(response.Body.Bytes())
	if response.Code != http.StatusOK || err != nil || settings.ReceiverAuthorized || settings.ReceiverMachine != "local-test" {
		t.Fatalf("unexpected profile export: %d, %v", response.Code, err)
	}
	form.Set("action", "use")
	if response := request(http.MethodPost, "/profiles", form); response.Code != http.StatusSeeOther {
		t.Fatalf("could not select profile: %s", response.Body)
	}
	assertWebPage(t, request(http.MethodGet, "/profiles", nil), http.StatusOK, "默认配置 <strong>local-test</strong>")
	form.Set("action", "archive")
	if response := request(http.MethodPost, "/profiles", form); response.Code != http.StatusSeeOther {
		t.Fatalf("could not archive profile: %s", response.Body)
	}
	if response := request(http.MethodGet, profilePath, nil); response.Code != http.StatusNotFound {
		t.Fatalf("archived profile still editable: %d", response.Code)
	}
	archives, err := os.ReadDir(filepath.Join(registry.root, "profile-archive"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("expected one archived profile: %v", err)
	}
	form.Set("action", "restore")
	form.Set("name", archives[0].Name())
	if response := request(http.MethodPost, "/profiles", form); response.Code != http.StatusSeeOther {
		t.Fatalf("could not restore profile: %s", response.Body)
	}
	assertWebPage(t, request(http.MethodGet, profilePath, nil), http.StatusOK, "命名配置 · local-test")
}
