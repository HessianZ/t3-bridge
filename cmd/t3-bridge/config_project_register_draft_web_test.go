package main

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

const draftProjectURL = "/project-register/draft"

func TestConfigWebOpensProjectRegistrationFromCurrentDraft(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "config.json")
	sources, err := configWebSources(path)
	if err != nil {
		t.Fatal(err)
	}
	handler := configWebHandler(webTestHost, sources)
	form := url.Values{
		"action": {"register-draft-project"}, "revision": {"missing"}, "projectSource": {"file"},
		"config": {string(sources[0].JSON)}, "projectAppId": {"cli_pending"}, "projectRouteId": {"work"},
		"edits": {`[{"path":["appId"],"value":"cli_pending"},{"path":["routes","work","environmentId"],"value":"env-pending"},{"path":["routes","work","model"],"value":"pending-model"}]`},
	}
	const origin = "http://" + webTestHost
	const workbench = "/?source=file"
	page := serveDraftProject(handler, "/?source=generic", origin, nil)
	assertWebPage(t, page, http.StatusOK)
	if strings.Contains(page.Body.String(), `name="projectSource"`) {
		t.Fatal("template offered registration without a config path")
	}
	assertWebPage(t, serveDraftProject(handler, workbench, origin, nil), http.StatusOK,
		`data-draft-project`, `name="projectSource" value="file"`, `value="register-draft-project"`)
	assertWebPage(t, serveDraftProject(handler, workbench, "", form), http.StatusForbidden)
	assertWebPage(t, serveDraftProject(handler, workbench+"&source=file", origin, form), http.StatusBadRequest)
	form.Set("projectSource", "generic")
	assertWebPage(t, serveDraftProject(handler, workbench, origin, form), http.StatusBadRequest)
	form.Set("projectSource", "file")
	form.Set("revision", "stale")
	assertWebPage(t, serveDraftProject(handler, workbench, origin, form), http.StatusConflict)
	form.Set("revision", "missing")
	form.Set("projectRouteId", "Work")
	assertWebPage(t, serveDraftProject(handler, workbench, origin, form), http.StatusBadRequest)
	form.Set("projectRouteId", "work")
	page = serveDraftProject(handler, workbench, origin, form)
	assertWebPage(t, page, http.StatusOK, "cli_pending", "env-pending", `<code>work</code>`, `name="action" value="preview"`,
		`href="/workspace.css"`, `href="/?source=file#advanced" aria-current="page"`)
	draft := draftProjectInput(t, page, "config")
	assertWebPage(t, serveDraftProject(handler, workbench, origin, url.Values{
		"action": {"check"}, "config": {draft}, "revision": {"missing"},
	}), http.StatusOK, "与来源的差异", "pending-model", `name="revision" value="missing"`)
	if _, err := os.Lstat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("opening registration changed the config directory: %v", err)
	}
}

func TestConfigDraftProjectRegistrationConfirmation(t *testing.T) {
	const origin = "http://" + webTestHost
	const existingDirectory = `C:\work\A & B`
	for _, mode := range []string{"existing", "managed"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "new.json")
			managedDirectory := filepath.Join(t.TempDir(), "workspace")
			setupTokenFile := ""
			directory := existingDirectory
			if mode == "managed" {
				setupTokenFile = filepath.Join(t.TempDir(), "setup & register.json")
				directory = managedDirectory
			}
			source := webSource{ID: "file", Path: path, Revision: "missing"}
			draft, config := testProjectRegistrationDraft(t)
			candidate, err := bridge.EditConfigJSON(draft, []byte(`[{"path":["routes","work","projectId"],"value":"registered-project"}]`))
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			h := newConfigDraftProjectRegister(func() ([]webSource, error) { return []webSource{source}, nil },
				func(_ context.Context, configPath, revision string, submitted []byte, appID, routeID, registrationDirectory, token string) ([]byte, error) {
					calls++
					wantDirectory := directory
					if mode == "managed" {
						wantDirectory = ""
					}
					if configPath != path || revision != "missing" || string(submitted) != string(draft) ||
						appID != "cli_draft" || routeID != "work" || registrationDirectory != wantDirectory || token != setupTokenFile {
						t.Fatal("registration did not receive the previewed draft and selection")
					}
					return candidate, nil
				}, func(string, bridge.Config, string) (string, error) { return managedDirectory, nil })
			handler := http.HandlerFunc(h.serve)
			start := httptest.NewRecorder()
			h.start(start, httptest.NewRequest(http.MethodPost, "/", nil), source, source.Revision, draft, config, "cli_draft", "work")
			selectionTicket := draftProjectInput(t, start, "ticket")
			confirm := url.Values{"action": {"confirm"}, "ticket": {selectionTicket}, "confirm": {"register"}}
			assertWebPage(t, serveDraftProject(handler, draftProjectURL, origin, confirm), http.StatusConflict)
			start = httptest.NewRecorder()
			h.start(start, httptest.NewRequest(http.MethodPost, "/", nil), source, source.Revision, draft, config, "cli_draft", "work")
			preview := url.Values{"action": {"preview"}, "ticket": {draftProjectInput(t, start, "ticket")}, "mode": {mode}, "directory": {directory}}
			if mode == "managed" {
				preview.Set("directory", "")
				preview.Set("setupTokenFile", setupTokenFile)
			}
			page := serveDraftProject(handler, draftProjectURL, origin, preview)
			assertWebPage(t, page, http.StatusOK, "cli_draft", "env-draft", `<code>work</code>`, directory, `name="confirm"`)
			if page.Header().Get("Referrer-Policy") != "same-origin" || (setupTokenFile != "" && strings.Contains(page.Body.String(), html.EscapeString(setupTokenFile))) {
				t.Fatal("preview leaked the setup token path or could not submit a native same-origin confirmation")
			}
			confirm.Set("ticket", draftProjectInput(t, page, "ticket"))
			assertWebPage(t, serveDraftProject(handler, draftProjectURL, "https://other.test", confirm), http.StatusForbidden)
			assertWebPage(t, serveDraftProject(handler, draftProjectURL, "", confirm), http.StatusForbidden)
			assertWebPage(t, serveDraftProject(handler, draftProjectURL+"?source=file", origin, confirm), http.StatusBadRequest)
			confirm.Set("directory", directory+"-changed")
			assertWebPage(t, serveDraftProject(handler, draftProjectURL, origin, confirm), http.StatusBadRequest)
			confirm.Set("config", strings.Repeat("x", 16*1024))
			assertWebPage(t, serveDraftProject(handler, draftProjectURL, origin, confirm), http.StatusBadRequest)
			confirm.Del("config")
			confirm.Del("directory")
			if _, err := os.Lstat(managedDirectory); !os.IsNotExist(err) || calls != 0 {
				t.Fatalf("registration ran before valid confirmation, calls=%d, directory=%v", calls, err)
			}
			page = serveDraftProject(handler, draftProjectURL, origin, confirm)
			assertWebPage(t, page, http.StatusOK, `action="/?source=file"`, `name="action" value="check"`, `name="revision" value="missing"`, "registered-project", "pending-model")
			if returned := draftProjectInput(t, page, "config"); returned != string(candidate) || strings.Contains(page.Body.String(), `value="save"`) {
				t.Fatal("registration result did not return only the updated draft to ordinary review")
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("registration changed the source file: %v", err)
			}
			assertWebPage(t, serveDraftProject(handler, draftProjectURL, origin, confirm), http.StatusConflict)
			if calls != 1 {
				t.Fatalf("registration calls=%d, want 1", calls)
			}
		})
	}
}

func TestConfigDraftProjectRejectsStaleConfirmation(t *testing.T) {
	const origin = "http://" + webTestHost
	for _, change := range []string{"new file", "saved revision", "source ID", "managed path", "expired"} {
		t.Run(change, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.json")
			source := webSource{ID: "file", Path: path, Revision: "missing"}
			managedDirectory := filepath.Join(t.TempDir(), "workspace")
			draft, config := testProjectRegistrationDraft(t)
			if change == "saved revision" {
				if err := os.WriteFile(path, draft, 0600); err != nil {
					t.Fatal(err)
				}
				source.Revision = configRevision(draft)
			}
			h := newConfigDraftProjectRegister(func() ([]webSource, error) { return []webSource{source}, nil },
				func(context.Context, string, string, []byte, string, string, string, string) ([]byte, error) {
					t.Fatal("stale confirmation reached registration")
					return nil, nil
				}, func(string, bridge.Config, string) (string, error) { return managedDirectory, nil })
			handler := http.HandlerFunc(h.serve)
			start := httptest.NewRecorder()
			h.start(start, httptest.NewRequest(http.MethodPost, "/", nil), source, source.Revision, draft, config, "cli_draft", "work")
			preview := url.Values{"action": {"preview"}, "ticket": {draftProjectInput(t, start, "ticket")}, "mode": {"managed"}, "directory": {""}}
			page := serveDraftProject(handler, draftProjectURL, origin, preview)
			assertWebPage(t, page, http.StatusOK)
			confirm := url.Values{"action": {"confirm"}, "ticket": {draftProjectInput(t, page, "ticket")}, "confirm": {"register"}}
			switch change {
			case "new file", "saved revision":
				if err := os.WriteFile(path, append(append([]byte(nil), draft...), '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			case "source ID":
				source.ID = "other"
			case "managed path":
				managedDirectory += "-changed"
			case "expired":
				h.pending.expires = time.Now().Add(-time.Second)
			}
			assertWebPage(t, serveDraftProject(handler, draftProjectURL, origin, confirm), http.StatusConflict)
		})
	}
}

func testProjectRegistrationDraft(t *testing.T) ([]byte, bridge.Config) {
	t.Helper()
	raw, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	draft, err := bridge.EditConfigJSON(raw, []byte(`[{"path":["appId"],"value":"cli_draft"},{"path":["routes","work","environmentId"],"value":"env-draft"},{"path":["routes","work","model"],"value":"pending-model"}]`))
	if err != nil {
		t.Fatal(err)
	}
	config, err := bridge.ParseConfig(draft)
	if err != nil {
		t.Fatal(err)
	}
	return draft, config
}

func serveDraftProject(handler http.Handler, path, origin string, form url.Values) *httptest.ResponseRecorder {
	method := http.MethodPost
	if form == nil {
		method = http.MethodGet
	}
	r := httptest.NewRequest(method, "http://"+webTestHost+path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func draftProjectInput(t *testing.T, w *httptest.ResponseRecorder, name string) string {
	t.Helper()
	match := regexp.MustCompile(`name="` + regexp.QuoteMeta(name) + `" value="([^"]*)"`).FindStringSubmatch(w.Body.String())
	if match == nil {
		t.Fatalf("response status %d did not return %s", w.Code, name)
	}
	return html.UnescapeString(match[1])
}
