package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestConfigTUIProjectRegisterAndSave(t *testing.T) {
	const appID, routeID, environment = "cli_selected", "Branch.Main", "linked-target"
	const directory = `C:\Projects\Existing Demo`
	const setupTokenFile = "/private/project-setup.json"
	cfg, err := bridge.ParseConfig(tuiTestSource(t).JSON)
	if err != nil {
		t.Fatal(err)
	}
	extra := cfg
	extra.AppID, extra.StateDir, extra.DefaultRoute = appID, filepath.Join(t.TempDir(), "state"), routeID
	target := cfg.Routes["work"]
	target.EnvironmentID = environment
	extra.Routes = map[string]bridge.Target{routeID: target}
	cfg.AdditionalAccounts = []bridge.Config{extra}
	original, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := configFileSource(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	target.ProjectID, target.ProjectName, target.ProjectPath = "registered-id", "Existing Demo", directory
	extra.Routes[routeID] = target
	candidate, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	m := newConfigTUI(source)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	calls := 0
	m.projectRegister.register = func(_ context.Context, gotPath, revision string, draft []byte, app, route, dir, tokenFile string) ([]byte, error) {
		calls++
		if gotPath != path || revision != source.Revision || app != appID || route != routeID || dir != directory || tokenFile != setupTokenFile {
			t.Fatalf("wrong registration scope: %q %q %q %q %q %q", gotPath, revision, app, route, dir, tokenFile)
		}
		if !bytes.Equal(draft, source.JSON) {
			t.Fatal("registration did not receive the visible config")
		}
		return candidate, nil
	}
	tuiType(m, "K")
	if m.screen != tuiBrowse || !strings.Contains(m.status, "先打开") {
		t.Fatalf("registration opened outside a route: %s", m.View())
	}
	tuiKeys(m, tea.KeyTab)
	tuiSelect(t, m, `路由 · "Branch.Main"`)
	tuiKeys(m, tea.KeyEnter)
	if !strings.Contains(m.View(), "K 登记 T3 项目") {
		t.Fatal("project registration shortcut is missing")
	}
	tuiType(m, "K")
	if strings.Contains(m.View(), "专用目录") {
		t.Fatalf("linked route offered a managed directory: %s", m.View())
	}
	tuiKeys(m, tea.KeyEnter)
	if m.screen != tuiProjectRegisterInput {
		t.Fatal("empty directory advanced to confirmation")
	}
	tuiType(m, directory)
	tuiKeys(m, tea.KeyEnter)
	view := m.View()
	for _, value := range []string{appID, environment, routeID, directory, "账号 MCP 凭据", "在 T3 查找或登记项目", "y 确认"} {
		if !strings.Contains(view, value) {
			t.Fatalf("confirmation omitted %q: %s", value, view)
		}
	}
	lines := strings.Split(view, "\n")
	if len(lines) > 12 {
		t.Fatalf("confirmation exceeds terminal height: %s", view)
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > 80 {
			t.Fatalf("confirmation exceeds terminal width: %q", line)
		}
	}
	tuiKeys(m, tea.KeyEsc)
	if m.screen != tuiBrowse || m.route != routeID || calls != 0 || m.draft != string(source.JSON) {
		t.Fatal("cancelling before submission changed the route, draft or call count")
	}
	tuiType(m, "K")
	tuiType(m, directory)
	tuiKeys(m, tea.KeyF3)
	if !strings.Contains(m.View(), "私有 OAuth JSON 路径") || m.input.Value() != "" {
		t.Fatalf("F3 did not open an empty authorization path: %s", m.View())
	}
	tuiType(m, setupTokenFile)
	tuiKeys(m, tea.KeyEnter, tea.KeyEnter)
	if view := m.View(); m.screen != tuiProjectRegisterConfirm || !strings.Contains(view, directory) ||
		!strings.Contains(view, "本次登记授权 · "+setupTokenFile) || calls != 0 || m.draft != string(source.JSON) {
		t.Fatalf("confirmation lost the directory or authorization scope, or changed the draft: %s", view)
	}
	y := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}
	_, cmd := m.Update(y)
	if cmd == nil || calls != 0 || m.screen != tuiProjectRegisterPending {
		t.Fatal("confirmation did not schedule an asynchronous registration")
	}
	if m.projectRegister.setupTokenFile != "" || m.input.Value() != "" || strings.Contains(m.review.View(), setupTokenFile) {
		t.Fatal("submission retained the authorization path in the form")
	}
	tuiKeys(m, tea.KeyEsc, tea.KeyCtrlC)
	if _, next := m.Update(y); next != nil || m.screen != tuiProjectRegisterPending || !strings.Contains(m.status, "项目可能已登记") {
		t.Fatalf("pending registration was left or repeated: %s", m.View())
	}
	m.Update(cmd())
	if calls != 1 || m.screen != tuiReview || m.reviewed == nil || !strings.Contains(m.View(), "候选配置差异") {
		t.Fatalf("registration did not open the candidate review: %s", m.View())
	}
	if raw, err := os.ReadFile(path); err != nil || !bytes.Equal(raw, original) {
		t.Fatalf("registration changed the saved configuration before explicit save: %v", err)
	}
	tuiType(m, "s")
	if raw, err := os.ReadFile(path); err != nil || m.screen != tuiBrowse || !bytes.Equal(raw, m.reviewed) || bytes.Contains(raw, []byte(setupTokenFile)) {
		t.Fatalf("reviewed configuration was not saved: %v, %s", err, m.View())
	}
}

func TestConfigTUIProjectRegisterManagedWorkspaceFromFirstRunDraft(t *testing.T) {
	const appID, routeID, environment = "cli_managed", "work", "local-environment"
	const setupTokenFile = "/private/managed-setup.json"
	const model = "draft-model"
	dir := t.TempDir()
	registry := profileRegistry{root: filepath.Join(dir, "t3-bridge")}
	cfg, err := bridge.ParseConfig(tuiTestSource(t).JSON)
	if err != nil {
		t.Fatal(err)
	}
	extra := cfg
	extra.AppID, extra.StateDir = appID, filepath.Join(dir, "state")
	extra.MCP.Endpoint, extra.MCP.EnvironmentID = "http://127.0.0.1:3773/mcp", environment
	target := cfg.Routes[routeID]
	target.EnvironmentID = "previous-target"
	extra.Routes = map[string]bridge.Target{routeID: target}
	cfg.AdditionalAccounts = []bridge.Config{extra}
	original, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	source, err := configFileSource(path, original)
	if err != nil {
		t.Fatal(err)
	}
	target.EnvironmentID = environment
	extra.Routes[routeID] = target
	workspace, err := registry.managedProjectWorkspace(path, extra, routeID)
	if err != nil || !filepath.IsAbs(workspace) {
		t.Fatalf("expected a managed workspace for the local fixture: %q, %v", workspace, err)
	}
	target.ProjectID, target.ProjectName, target.ProjectPath = "managed-id", "Managed Project", workspace
	target.Model = model
	extra.Routes[routeID] = target
	candidate, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	m := newConfigTUI(source)
	m.projectRegister.workspace = registry.managedProjectWorkspace
	m.Update(tea.WindowSizeMsg{Width: max(80, ansi.StringWidth(workspace)+12), Height: 12})
	tuiKeys(m, tea.KeyTab)
	tuiSelect(t, m, `路由 · "work"`)
	tuiKeys(m, tea.KeyEnter)
	tuiSetField(t, m, "目标 · environmentId", environment)
	tuiSetField(t, m, "目标 · model", model)
	draft := m.draft
	calls := 0
	m.projectRegister.register = func(_ context.Context, gotPath, revision string, gotDraft []byte, app, route, directory, tokenFile string) ([]byte, error) {
		calls++
		if gotPath != path || revision != "missing" || app != appID || route != routeID || directory != "" || tokenFile != "" {
			t.Fatalf("wrong managed registration scope: %q %q %q %q %q %q", gotPath, revision, app, route, directory, tokenFile)
		}
		if string(gotDraft) != draft {
			t.Fatal("registration did not receive the visible draft")
		}
		return candidate, nil
	}
	tuiType(m, "K")
	if m.screen != tuiProjectRegisterInput || !strings.Contains(m.View(), "留空按 Enter 预览专用目录") {
		t.Fatalf("local route omitted the managed choice: %s", m.View())
	}
	tuiKeys(m, tea.KeyF3)
	tuiType(m, setupTokenFile)
	tuiKeys(m, tea.KeyEnter)
	tuiKeys(m, tea.KeyEnter, tea.KeyEnter)
	view := m.View()
	for _, value := range []string{appID, environment, routeID, workspace, setupTokenFile, "创建本机目录并登记 T3", "差异页按 s 保存", "y 确认"} {
		if !strings.Contains(view, value) {
			t.Fatalf("managed confirmation omitted %q: %s", value, view)
		}
	}
	if calls != 0 || m.screen != tuiProjectRegisterConfirm {
		t.Fatal("review submitted a managed registration")
	}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	view = m.View()
	lines := strings.Split(view, "\n")
	if len(lines) > 12 || !strings.Contains(view, "创建本机目录并登记 T3") || !strings.Contains(view, "差异页按 s 保存") {
		t.Fatalf("managed confirmation was clipped vertically or lost its action text: %s", view)
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > 40 {
			t.Fatalf("managed confirmation exceeds terminal width: %q", line)
		}
	}
	tuiKeys(m, tea.KeyEsc)
	if m.screen != tuiBrowse || m.route != routeID || calls != 0 || m.draft != draft {
		t.Fatal("cancelling managed registration changed the route, draft or call count")
	}
	if m.projectRegister.setupTokenFile != "" || strings.Contains(m.review.View(), setupTokenFile) {
		t.Fatal("cancellation retained the authorization path in the form")
	}
	tuiType(m, "K")
	tuiKeys(m, tea.KeyEnter)
	if !strings.Contains(m.View(), "账号 MCP 凭据") {
		t.Fatalf("reopening a cancelled registration did not restore default authorization: %s", m.View())
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil || calls != 0 || m.screen != tuiProjectRegisterPending {
		t.Fatal("explicit confirmation did not schedule managed registration")
	}
	m.Update(cmd())
	if calls != 1 || m.screen != tuiReview || m.reviewed == nil {
		t.Fatalf("managed registration did not open the candidate review: %s", m.View())
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed registration created the config before explicit save: %v", err)
	}
	if _, err := os.Lstat(workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("keyboard flow touched the managed directory: %v", err)
	}
	tuiType(m, "s")
	raw, err := os.ReadFile(path)
	if err != nil || m.screen != tuiBrowse || !bytes.Equal(raw, m.reviewed) {
		t.Fatalf("reviewed first-run configuration was not saved: %v, %s", err, m.View())
	}
	saved, err := bridge.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := saved.AdditionalAccounts[0].Routes[routeID]; got.Model != model || got.EnvironmentID != environment || got.ProjectPath != workspace {
		t.Fatalf("saved route lost draft edits or the registered directory: %+v", got)
	}
}

func TestConfigTUIProjectRegisterKeepsDraftOnFailure(t *testing.T) {
	source := tuiTestSource(t)
	m := newConfigTUI(source)
	tuiSelect(t, m, `路由 · "work"`)
	tuiKeys(m, tea.KeyEnter)
	tuiType(m, "K")
	if m.screen != tuiBrowse || !strings.Contains(m.status, "保存") {
		t.Fatal("registration opened for an unsaved template")
	}
	source.Path, source.Revision = filepath.Join(t.TempDir(), "config.json"), configRevision(source.JSON)
	m.source = source
	tuiSetField(t, m, "目标 · model", "")
	invalid := m.draft
	tuiType(m, "K")
	if m.screen != tuiBrowse || m.draft != invalid || !strings.Contains(m.status, "修正当前草稿") {
		t.Fatal("registration did not block and preserve an invalid draft")
	}
	tuiSetField(t, m, "目标 · model", "draft-model")
	draft := m.draft
	m.projectRegister.workspace = func(string, bridge.Config, string) (string, error) {
		return "", errors.New("managed workspace unavailable")
	}
	calls := 0
	m.projectRegister.register = func(_ context.Context, _, _ string, gotDraft []byte, _, _, _, _ string) ([]byte, error) {
		calls++
		if string(gotDraft) != draft {
			t.Fatal("manual registration did not receive the dirty draft")
		}
		return nil, errors.New("项目登记结果未知，请先在 T3 核对")
	}
	tuiType(m, "K")
	if m.screen != tuiProjectRegisterInput || m.draft != draft || strings.Contains(m.View(), "预览专用目录") {
		t.Fatalf("manual path fallback lost the dirty draft: %s", m.View())
	}
	tuiType(m, "/target/existing")
	tuiKeys(m, tea.KeyEnter)
	y := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}
	_, cmd := m.Update(y)
	if cmd == nil {
		t.Fatal("registration was not scheduled")
	}
	m.Update(cmd())
	if _, next := m.Update(y); next != nil || calls != 1 || m.screen != tuiBrowse || m.draft != draft ||
		!strings.Contains(m.status, "请先在 T3 核对") {
		t.Fatalf("registration error changed the draft or allowed a repeated confirmation: %s", m.View())
	}
}
