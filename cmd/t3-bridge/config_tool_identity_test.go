package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
	tea "github.com/charmbracelet/bubbletea"
)

func TestConfigToolSessionRequiresLaunchKey(t *testing.T) {
	called := 0
	address, handler := configToolSession(webTestHost, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		if r.PostForm.Has("workspaceSession") || r.PostForm.Get("source") != "file" {
			t.Fatal("probe did not receive only its own fields")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	entry, err := url.Parse(address)
	if err != nil || entry.RawQuery != "" {
		t.Fatalf("invalid launch URL: %v", err)
	}
	fragment, err := url.ParseQuery(entry.Fragment)
	if err != nil || fragment.Get("workspaceSession") == "" {
		t.Fatal("launch URL missing its private fragment")
	}
	post := func(origin string, values []string) int {
		form := url.Values{"source": {"file"}, "workspaceSession": values}
		r := httptest.NewRequest(http.MethodPost, "http://"+webTestHost+"/tool-identity", bytes.NewBufferString(form.Encode()))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	key, origin := fragment.Get("workspaceSession"), "http://"+webTestHost
	for _, values := range [][]string{nil, {"wrong"}, {key, key}} {
		if got := post(origin, values); got != http.StatusForbidden {
			t.Fatalf("untrusted probe returned %d", got)
		}
	}
	if got := post("https://other.test", []string{key}); got != http.StatusForbidden || called != 0 {
		t.Fatal("foreign origin reached verification")
	}
	if got := post(origin, []string{key}); got != http.StatusNoContent || called != 1 {
		t.Fatal("launch key did not authorize the selected probe")
	}
}

func TestConfigWorkspacesPreserveUserDefaultBinding(t *testing.T) {
	dir := t.TempDir()
	base, err := bridge.EditConfigJSON(tuiTestSource(t).JSON, []byte(`[
		{"path":["appId"],"value":"cli_personal"},
		{"path":["users"],"value":["ou_personal"]},
		{"path":["routes","work","users"],"value":["ou_personal"]}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	binding := bridge.ToolContext{
		CLIPath: filepath.Join(dir, "lark-cli"), EnvironmentID: "REPLACE_ENVIRONMENT_ID", Identity: "user-default",
		BotBinding: &bridge.ToolBotBinding{
			AppID: "cli_personal", Tenant: "feishu", BotOpenID: "ou_bot", Profile: "personal", ConfigDir: dir,
		},
		UserBinding: &bridge.ToolUserBinding{
			AppID: "cli_personal", Tenant: "feishu", UserOpenID: "ou_personal", Profile: "personal", ConfigDir: dir,
		},
	}
	field := []string{"routes", "work", "toolContext"}
	edits, err := json.Marshal([]configTUIEdit{{Path: field, Value: binding}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, base, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := configFileSource(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	var cli bytes.Buffer
	if err := runConfig([]string{"patch", "-config", path}, bytes.NewReader(edits), &cli); err != nil {
		t.Fatal(err)
	}

	m := newConfigTUI(source)
	tuiSelect(t, m, `路由 · "work"`)
	tuiKeys(m, tea.KeyEnter)
	raw, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	tuiSetField(t, m, "路由 · toolContext", string(raw))
	tuiType(m, "p")
	if m.reviewed == nil {
		t.Fatalf("TUI could not review the binding: %s", m.status)
	}

	handler := configWebHandler(webTestHost, []webSource{source})
	form := url.Values{"config": {string(base)}, "edits": {string(edits)}, "action": {"check"}}
	page := submitWebConfig(handler, form)
	assertWebPage(t, page, http.StatusOK, `<option value="user-default" selected>`, `data-key="toolContext"`)
	review := regexp.MustCompile(`name="reviewed" value="([a-f0-9]{64})"`).FindStringSubmatch(page.Body.String())
	if review == nil {
		t.Fatal("workbench did not review the binding")
	}
	form.Set("action", "download")
	form.Set("reviewed", review[1])
	web := submitWebConfig(handler, form)
	assertWebPage(t, web, http.StatusOK, `"identity": "user-default"`)
	for surface, candidate := range map[string][]byte{"CLI": cli.Bytes(), "TUI": []byte(m.draft), "Web and desktop": web.Body.Bytes()} {
		parsed, err := bridge.ParseConfig(candidate)
		if err != nil {
			t.Fatalf("%s: %v", surface, err)
		}
		tool := parsed.Routes["work"].ToolContext
		if tool == nil || tool.Identity != binding.Identity || tool.BotBinding == nil || tool.UserBinding == nil ||
			*tool.BotBinding != *binding.BotBinding || *tool.UserBinding != *binding.UserBinding || tool.CLIPath != binding.CLIPath {
			t.Fatalf("%s lost the tool binding", surface)
		}
	}

	t.Run("saved identity", func(t *testing.T) {
		private := filepath.Join(dir, "saved.json")
		candidate := cli.Bytes()
		if _, err := localfile.WritePrivateAtomic(private, candidate, true); err != nil {
			t.Fatal(err)
		}
		revision, choices, err := savedToolIdentityChoices(private)
		want := workspaceToolIdentity{AppID: "cli_personal", RouteID: "work", Identity: "user-default"}
		if err != nil || revision != configRevision(candidate) || len(choices) != 1 || choices[0] != want {
			t.Fatalf("unexpected saved route choices: %v, %v", choices, err)
		}
		selected := webSource{Path: private, Revision: revision}
		calls := 0
		probe := func(_ context.Context, cfg bridge.Config, routeID string) error {
			t.Helper()
			calls++
			if cfg.AppID != want.AppID || routeID != want.RouteID || cfg.Routes[routeID].ToolContext.Identity != want.Identity {
				t.Fatal("probe did not receive the selected saved binding")
			}
			return nil
		}
		stale := selected
		stale.Revision = "stale"
		ctx := context.Background()
		if _, err := verifySavedToolIdentity(ctx, stale, want.AppID, want.RouteID, probe); !errors.Is(err, errToolIdentityConfigChanged) {
			t.Fatalf("accepted a stale revision: %v", err)
		}
		if _, err := verifySavedToolIdentity(ctx, selected, want.AppID, "other", probe); err == nil || calls != 0 {
			t.Fatalf("accepted another route or ran a rejected probe: %d, %v", calls, err)
		}
		if got, err := verifySavedToolIdentity(ctx, selected, want.AppID, want.RouteID, probe); err != nil || got != want || calls != 1 {
			t.Fatalf("unexpected verification result: %v, %d, %v", got, calls, err)
		}
		got, err := verifySavedToolIdentity(ctx, selected, want.AppID, want.RouteID, func(context.Context, bridge.Config, string) error {
			if _, err := localfile.WritePrivateAtomic(private, append(bytes.Clone(candidate), '\n'), false); err != nil {
				t.Fatal(err)
			}
			return nil
		})
		if !errors.Is(err, errToolIdentityConfigChanged) || got != (workspaceToolIdentity{}) {
			t.Fatalf("showed a successful result for a changed configuration: %v, %v", got, err)
		}
	})
}
