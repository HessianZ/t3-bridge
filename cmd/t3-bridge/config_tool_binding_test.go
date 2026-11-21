package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestToolBindingDraftAndCLIProduceTheSameCandidate(t *testing.T) {
	dir := t.TempDir()
	base, err := bridge.ParseConfig(tuiTestSource(t).JSON)
	if err != nil {
		t.Fatal(err)
	}
	account := base
	account.AppID, account.Tenant = "cli_binding", "lark"
	account.StateDir, account.Users = filepath.Join(dir, "state"), []string{"ou_user"}
	target := base.Routes["work"]
	target.ProviderID, target.Users = "private", account.Users
	target.ToolContext = &bridge.ToolContext{
		CLIPath: filepath.Join(dir, "lark-cli"), EnvironmentID: target.EnvironmentID,
		Identity: "bot", Version: "1.0.96", Verified: true,
	}
	account.Routes = map[string]bridge.Target{"Work.Tools": target}
	account.DefaultRoute = "Work.Tools"
	base.AdditionalAccounts = []bridge.Config{account}
	raw, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "source.json")
	if _, err := localfile.WritePrivateAtomic(path, raw, true); err != nil {
		t.Fatal(err)
	}
	draft := toolBindingFields(target.ToolContext, account.AppID, account.Tenant)
	if draft.Mode != "" || draft.CLIPath != target.ToolContext.CLIPath || draft.AppID != account.AppID || draft.Tenant != account.Tenant {
		t.Fatalf("bad defaults for legacy configuration: %+v", draft)
	}
	draft.Mode, draft.Profile, draft.ConfigDir = "user-default", "Personal.Work", dir
	draft.BotOpenID, draft.UserOpenID = "ou_bot", account.Users[0]
	handler := configWebHandler(webTestHost, nil)
	send := func(origin string, d toolBindingDraft) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]any{"draft": d, "environmentId": target.EnvironmentID, "current": target.ToolContext})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "http://"+webTestHost+"/tool-binding/draft", bytes.NewReader(body))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	origin := "http://" + webTestHost
	if w := send("https://foreign.test", draft); w.Code != http.StatusForbidden {
		t.Fatalf("foreign request = %d", w.Code)
	}
	invalid := draft
	invalid.CLIPath = "relative/path"
	if w := send(origin, invalid); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid path = %d", w.Code)
	}
	var result struct{ ToolContext *bridge.ToolContext }
	w := send(origin, draft)
	if err := json.Unmarshal(w.Body.Bytes(), &result); w.Code != http.StatusOK || err != nil || result.ToolContext == nil {
		t.Fatalf("draft response = %d, %q", w.Code, w.Body.String())
	}
	if result.ToolContext.Version != "1.0.96" || !result.ToolContext.Verified || result.ToolContext.Identity != draft.Mode {
		t.Fatalf("draft lost advanced metadata: %+v", result.ToolContext)
	}
	var cli bytes.Buffer
	args := []string{"tool-binding", "-config", path, "-app-id", account.AppID, "-route", "Work.Tools", "-mode", draft.Mode}
	if err := runConfig(append(args, "-cli-path", draft.CLIPath, "-cli-profile", draft.Profile, "-cli-config-dir", dir,
		"-bot-open-id", draft.BotOpenID, "-user-open-id", draft.UserOpenID), nil, &cli); err != nil {
		t.Fatal(err)
	}
	parsed, err := bridge.ParseConfig(cli.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	got := parsed.AdditionalAccounts[0].Routes["Work.Tools"].ToolContext
	want := result.ToolContext
	if got == nil || got.BotBinding == nil || got.UserBinding == nil || *got.BotBinding != *want.BotBinding || *got.UserBinding != *want.UserBinding || got.CLIPath != want.CLIPath || parsed.Routes["work"].ToolContext != nil {
		t.Fatal("CLI and Web did not produce the same binding in the selected account")
	}
	draft.Mode = "off"
	if w := send(origin, draft); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"toolContext":null`) {
		t.Fatalf("removal draft = %d, %q", w.Code, w.Body.String())
	}
	cli.Reset()
	if err := runConfig(append(args[:len(args)-1], "off"), nil, &cli); err != nil || strings.Contains(cli.String(), `"toolContext"`) {
		t.Fatalf("CLI did not remove the binding: %v", err)
	}
	if current, err := os.ReadFile(path); err != nil || !bytes.Equal(current, raw) {
		t.Fatal("building drafts modified the source")
	}
}
