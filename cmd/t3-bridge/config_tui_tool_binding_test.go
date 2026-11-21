package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
)

func TestConfigTUIToolBindingDraft(t *testing.T) {
	const route, env, app, user, bot = "Work.Tools", "local-extra", "cli_selected", "ou_selected", "ou_robot"
	dir := t.TempDir()
	cfg, err := bridge.ParseConfig(tuiTestSource(t).JSON)
	if err != nil {
		t.Fatal(err)
	}
	target := cfg.Routes["work"]
	cfg.Routes, cfg.DefaultRoute = map[string]bridge.Target{route: target}, route
	extra := cfg
	extra.AppID, extra.Tenant, extra.Users = app, "lark", []string{user}
	extra.StateDir = filepath.Join(dir, "state")
	extra.MCP.EnvironmentID, target.EnvironmentID, target.Users = env, env, extra.Users
	legacy := bridge.ToolContext{
		CLIPath: filepath.Join(dir, "lark-cli"), EnvironmentID: env, Identity: "bot", Version: "1.0", Verified: true,
	}
	target.ToolContext = &legacy
	extra.Routes = map[string]bridge.Target{route: target}
	cfg.AdditionalAccounts = []bridge.Config{extra}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = bridge.EditConfigJSON(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := configFileSource(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := newConfigTUI(source)
	tuiType(m, "B")
	if m.screen != tuiBrowse || !strings.Contains(m.status, "先打开") {
		t.Fatal("binding editor opened outside a route")
	}
	tuiKeys(m, tea.KeyTab)
	tuiSelect(t, m, `路由 · "Work.Tools"`)
	tuiKeys(m, tea.KeyEnter)
	tuiSelect(t, m, "路由 · toolContext")
	row := m.row
	tuiType(m, "B")
	tuiKeys(m, tea.KeyCtrlS)
	if view := m.View(); m.screen != tuiToolBinding || !strings.Contains(view, "原始 JSON") || m.draft != string(source.JSON) {
		t.Fatalf("opening or applying without a mode changed the legacy binding: %s", view)
	}
	tuiKeys(m, tea.KeyRight)
	if view := m.View(); !strings.Contains(view, "机器人 Open ID") || strings.Contains(view, "用户 Open ID") {
		t.Fatalf("bot mode showed incorrect fields: %s", view)
	}
	tuiKeys(m, tea.KeyCtrlS)
	if m.screen != tuiToolBinding || m.status == "" || m.draft != string(source.JSON) {
		t.Fatal("incomplete bot binding changed the draft")
	}
	tuiKeys(m, tea.KeyRight, tea.KeyRight)
	for _, value := range []string{"user-default", "机器人 Open ID", "用户 Open ID", "工具 App ID  " + app, "租户（feishu / lark）  " + extra.Tenant} {
		if !strings.Contains(m.View(), value) {
			t.Fatalf("binding form omitted %q: %s", value, m.View())
		}
	}
	tuiKeys(m, tea.KeyDown)
	if m.input.Value() != legacy.CLIPath {
		t.Fatal("legacy CLI path was not prefilled")
	}
	tuiKeys(m, tea.KeyTab, tea.KeyEnter, tea.KeyTab)
	tuiType(m, "personal")
	tuiKeys(m, tea.KeyTab)
	tuiType(m, dir)
	tuiKeys(m, tea.KeyTab)
	tuiType(m, bot)
	tuiKeys(m, tea.KeyTab)
	tuiType(m, user)
	tuiKeys(m, tea.KeyCtrlS)
	if m.screen != tuiBrowse || m.account != 1 || m.route != route || m.row != row {
		t.Fatalf("applying lost the selected account, route or row: %s", m.View())
	}
	updated, err := bridge.ParseConfig([]byte(m.draft))
	if err != nil {
		t.Fatal(err)
	}
	tool := updated.AdditionalAccounts[0].Routes[route].ToolContext
	wantBot := bridge.ToolBotBinding{AppID: app, Tenant: extra.Tenant, Profile: "personal", ConfigDir: dir, BotOpenID: bot}
	wantUser := bridge.ToolUserBinding{AppID: app, Tenant: extra.Tenant, Profile: "personal", ConfigDir: dir, UserOpenID: user}
	if updated.Routes[route].ToolContext != nil || tool == nil || tool.Identity != "user-default" ||
		tool.CLIPath != legacy.CLIPath || tool.EnvironmentID != env || tool.Version != legacy.Version || !tool.Verified ||
		tool.BotBinding == nil || *tool.BotBinding != wantBot || tool.UserBinding == nil || *tool.UserBinding != wantUser {
		t.Fatalf("incorrect binding in the selected account: %+v", tool)
	}
	tuiType(m, "p")
	if m.reviewed == nil || !strings.Contains(m.review.View(), "botBinding") {
		t.Fatal("structured binding was missing from the valid configuration preview")
	}
	tuiKeys(m, tea.KeyEsc)
	draft := m.draft
	tuiType(m, "B")
	tuiKeys(m, tea.KeyShiftTab, tea.KeyF2)
	tuiType(m, "ou_canceled")
	tuiKeys(m, tea.KeyEsc)
	if m.draft != draft || m.screen != tuiBrowse || m.account != 1 || m.route != route || m.row != row {
		t.Fatal("cancel changed the draft or selection")
	}
	tuiType(m, "B")
	tuiKeys(m, tea.KeyRight, tea.KeyCtrlS)
	if m.screen != tuiBrowse || strings.Contains(m.draft, `"toolContext"`) {
		t.Fatalf("off did not remove the binding from the draft: %s", m.View())
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("editing the draft changed the saved configuration: %v", err)
	}
}
