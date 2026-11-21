package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
	tea "github.com/charmbracelet/bubbletea"
)

func TestConfigTUIWorkspaceImport(t *testing.T) {
	dir := t.TempDir()
	oldRoot := filepath.Join(dir, "old")
	t.Setenv("LARK_CHANNEL_HOME", oldRoot)
	template := tuiTestSource(t)
	configPath := filepath.Join(dir, "config.json")
	legacyPath := filepath.Join(oldRoot, "profiles", "sample", "workspaces.json")
	missing, err := configFileSource(configPath, template.JSON)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []webSource{template, missing} {
		m := newConfigTUI(source)
		tuiType(m, "W")
		if m.screen != tuiBrowse || !strings.Contains(m.status, "保存") {
			t.Fatalf("migration opened without a saved file: %s", m.View())
		}
	}

	primary, err := bridge.ParseConfig(template.JSON)
	if err != nil {
		t.Fatal(err)
	}
	primary.AppID, primary.StateDir = "cli_primary", filepath.Join(dir, "primary-state")
	account := primary
	account.AppID, account.StateDir = "cli_migrate", filepath.Join(dir, "migration-state")
	account.Users = []string{"ou_migrate"}
	projectPath := filepath.Join(dir, "project")
	route := primary.Routes["work"]
	route.ProjectID, route.ProjectPath, route.Users = "project-migrate", projectPath, account.Users
	account.Routes = map[string]bridge.Target{"work": route}
	primary.AdditionalAccounts = []bridge.Config{account}
	config, err := json.Marshal(primary)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := json.Marshal(map[string]any{"named": map[string]string{
		"sample\x1fowner\x1foc_direct\x1fAlpha": projectPath, "Unmapped": projectPath,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := localfile.MkdirAllPrivate(filepath.Dir(legacyPath)); err != nil {
		t.Fatal(err)
	}
	legacyConfig := []byte(`{"schemaVersion":2,"profiles":{"sample":{"schemaVersion":2,"accounts":{"app":{"id":"cli_migrate"}}}}}`)
	for path, data := range map[string][]byte{configPath: config, legacyPath: legacy, filepath.Join(oldRoot, "config.json"): legacyConfig} {
		if _, err := localfile.WritePrivateAtomic(path, data, true); err != nil {
			t.Fatal(err)
		}
	}
	resolvedPath, err := filepath.EvalSymlinks(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	source, err := configFileSource(configPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := newConfigTUI(source)
	tuiSetField(t, m, "账号 · receiverAuthorized", "true")
	tuiType(m, "W")
	if m.screen != tuiBrowse || !strings.Contains(m.status, "尚未保存") {
		t.Fatalf("migration opened with an unsaved draft: %s", m.View())
	}

	m = newConfigTUI(source)
	m.Update(tea.WindowSizeMsg{Width: max(100, len(resolvedPath)+1), Height: 24})
	if !strings.Contains(m.View(), "L 导入旧配置 · W 迁移工作区") {
		t.Fatalf("missing workspace migration shortcut: %s", m.View())
	}
	tuiKeys(m, tea.KeyTab)
	tuiType(m, "W")
	if m.screen != tuiWorkspaceImportPath || !strings.Contains(m.View(), account.AppID) || !strings.Contains(m.View(), "唯一 V2 profile") {
		t.Fatalf("migration did not select the additional account: %s", m.View())
	}
	tuiType(m, legacyPath)
	tuiKeys(m, tea.KeyTab)
	if !strings.Contains(m.View(), "可省略 projectId") {
		t.Fatalf("mapping view should explain when projectId is optional: %s", m.View())
	}
	const mapping = `[
  {"sourceKey":"sample\u001fowner\u001foc_direct\u001fAlpha","userId":"ou_migrate",
   "chatId":"oc_direct","chatType":"p2p","routeId":"work"}
]`
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(mapping), Paste: true})
	tuiKeys(m, tea.KeyCtrlS, tea.KeyEnter)
	if m.screen != tuiWorkspaceImportReview {
		t.Fatalf("expected a read-only preview: %s", m.View())
	}
	for _, part := range []string{legacyPath, "未映射 1 个", "新增 · 别名 Alpha", "项目 project-migrate", "会话 p2p · oc_direct", "用户 ou_migrate", "s 写入目标状态", "先停止目标接收端"} {
		if !strings.Contains(m.View(), part) {
			t.Fatalf("preview missing %q: %s", part, m.View())
		}
	}
	if _, err := os.Stat(account.StateDir); !os.IsNotExist(err) {
		t.Fatalf("preview created target state: %v", err)
	}
	tuiKeys(m, tea.KeyEsc, tea.KeyEsc)
	if m.screen != tuiWorkspaceImportPath || m.input.Value() != legacyPath || m.workspaceImport.review != "" {
		t.Fatalf("back did not return to the path and clear review: %s", m.View())
	}
	tuiKeys(m, tea.KeyF2, tea.KeyEnter)
	if got := m.editor.Value(); got != mapping {
		t.Fatalf("back changed the mapping: %q", got)
	}
	tuiKeys(m, tea.KeyCtrlS)
	if m.screen != tuiWorkspaceImportReview || m.workspaceImport.request.LegacyPath != "" || !strings.Contains(m.View(), resolvedPath) {
		t.Fatalf("automatic preview should show the resolved path and keep the input blank: %s", m.View())
	}
	store, err := bridge.OpenStore(account.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	tuiType(m, "s")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if m.screen != tuiWorkspaceImportReview || !strings.Contains(m.status, "already opened") {
		t.Fatalf("write should fail while the receiver holds the state lock: %s", m.View())
	}
	statePath := filepath.Join(account.StateDir, "state.json")
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("locked write changed state: %v", err)
	}
	tuiType(m, "s")
	if m.screen != tuiWorkspaceImportReview || !strings.Contains(m.View(), "已写入目标状态") || !strings.Contains(m.View(), resolvedPath) {
		t.Fatalf("completion did not show the resolved source: %s", m.View())
	}
	tuiKeys(m, tea.KeyEsc)
	if m.screen != tuiBrowse || m.account != 1 || m.draft != string(source.JSON) {
		t.Fatalf("write did not return to the unchanged account and draft: %s", m.View())
	}
	state, err := os.ReadFile(statePath)
	if err != nil || !bytes.Contains(state, []byte(`"cli_migrate"`)) || !bytes.Contains(state, []byte(`"Alpha"`)) {
		t.Fatalf("missing imported alias in target state: %s, %v", state, err)
	}
	if _, err := os.Stat(primary.StateDir); !os.IsNotExist(err) {
		t.Fatalf("migration touched primary account state: %v", err)
	}
	for path, want := range map[string][]byte{configPath: config, legacyPath: legacy} {
		if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("migration changed its input %s: %v", path, err)
		}
	}
}

func TestConfigTUIWorkspaceImportAliasKeyboard(t *testing.T) {
	dir := t.TempDir()
	configPath, legacyPath := filepath.Join(dir, "config.json"), filepath.Join(dir, "workspaces.json")
	account, err := bridge.ParseConfig(tuiTestSource(t).JSON)
	if err != nil {
		t.Fatal(err)
	}
	account.AppID, account.StateDir = "cli_alias", filepath.Join(dir, "state")
	account.Users, account.Chats = []string{"ou_allowed"}, []string{"oc_group"}
	projectPath := filepath.Join(dir, "project")
	route := account.Routes["work"]
	route.ProjectID, route.ProjectPath, route.Users = "project", projectPath, account.Users
	account.Routes["work"] = route
	config, err := json.Marshal(account)
	if err != nil {
		t.Fatal(err)
	}
	const topicAlias = "z-profile\x1fowner\x1foc_old:om_old\x1fTopicAlias"
	named := map[string]string{"Alpha": projectPath, topicAlias: projectPath, "Invalid": "relative"}
	for i := range 8 {
		named[fmt.Sprintf("Extra-%d", i)] = projectPath
	}
	legacy, err := json.Marshal(map[string]any{"named": named})
	if err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string][]byte{configPath: config, legacyPath: legacy} {
		if _, err := localfile.WritePrivateAtomic(path, data, true); err != nil {
			t.Fatal(err)
		}
	}
	source, err := configFileSource(configPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := newConfigTUI(source)
	m.Update(tea.WindowSizeMsg{Width: max(120, len(projectPath)+30), Height: 12})
	tuiType(m, "W")
	if !strings.Contains(m.View(), "F3 查看旧别名") {
		t.Fatalf("source view does not advertise F3: %s", m.View())
	}
	tuiType(m, legacyPath)
	tuiKeys(m, tea.KeyF3)
	if m.screen != tuiWorkspaceImportAliases || !strings.Contains(m.View(), "无效 1 个") || strings.Contains(m.View(), "TopicAlias") {
		t.Fatalf("expected first page of aliases: %s", m.View())
	}
	tuiKeys(m, tea.KeyEnter)
	tuiType(m, "ou_cancelled")
	tuiKeys(m, tea.KeyEsc, tea.KeyEsc)
	if m.screen != tuiWorkspaceImportPath || m.input.Value() != legacyPath {
		t.Fatalf("cancellation did not restore the source input: %s", m.View())
	}
	tuiKeys(m, tea.KeyEnter)
	const draft = `[{"sourceKey":"Alpha","userId":"ou_allowed","chatId":"oc_direct","chatType":"p2p","routeId":"work"}]`
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(draft), Paste: true})
	if !strings.Contains(m.View(), "F3 查看旧别名") {
		t.Fatalf("mapping view does not advertise F3: %s", m.View())
	}
	tuiKeys(m, tea.KeyF3, tea.KeyEsc)
	if m.screen != tuiWorkspaceImportMapping || m.editor.Value() != draft {
		t.Fatalf("cancellation changed the pasted draft: %s", m.View())
	}
	tuiKeys(m, tea.KeyF3)
	for range len(named) - 2 {
		tuiKeys(m, tea.KeyDown)
	}
	for _, want := range []string{"› [话题] TopicAlias", "z-profile / owner / oc_old:om_old / TopicAlias", projectPath, "10/10"} {
		if !strings.Contains(m.View(), want) {
			t.Fatalf("scrolled picker missing %q: %s", want, m.View())
		}
	}
	tuiKeys(m, tea.KeyEnter)
	if m.screen != tuiWorkspaceImportEntry || !strings.Contains(m.View(), "目标用户 Open ID") || !strings.Contains(m.View(), "可用路由 work") {
		t.Fatalf("expected explicit destination form: %s", m.View())
	}
	for _, value := range []string{"ou_denied", "oc_group", "group", "work"} {
		tuiType(m, value)
		tuiKeys(m, tea.KeyTab)
	}
	if !strings.Contains(m.View(), "目标话题 ID（必填）") {
		t.Fatalf("form did not mark the topic as required: %s", m.View())
	}
	tuiKeys(m, tea.KeyCtrlS)
	if m.screen != tuiWorkspaceImportEntry || !strings.Contains(m.status, "需要填写目标话题 ID") {
		t.Fatalf("form accepted a missing topic ID: %s", m.View())
	}
	tuiType(m, "om_topic")
	tuiKeys(m, tea.KeyTab)
	tuiType(m, "project")
	tuiKeys(m, tea.KeyCtrlS)
	var entries []workspaceImportDraftEntry
	if err := json.Unmarshal([]byte(m.editor.Value()), &entries); m.screen != tuiWorkspaceImportMapping || err != nil || len(entries) != 2 {
		t.Fatalf("expected the two draft rows in the JSON editor: %s, %v", m.editor.Value(), err)
	}
	if want := (workspaceImportDraftEntry{SourceKey: "Alpha", UserID: "ou_allowed", ChatID: "oc_direct", ChatType: "p2p", RouteID: "work"}); entries[0] != want {
		t.Fatalf("existing draft row changed: %+v", entries[0])
	}
	if want := (workspaceImportDraftEntry{SourceKey: topicAlias, UserID: "ou_denied", ChatID: "oc_group", ChatType: "group", TopicID: "om_topic", RouteID: "work", ProjectID: "project"}); entries[1] != want {
		t.Fatalf("added draft row = %+v, want %+v", entries[1], want)
	}
	tuiKeys(m, tea.KeyCtrlS)
	if m.screen != tuiWorkspaceImportMapping || !strings.Contains(m.status, "workspace mapping 2") || !strings.Contains(m.status, "must be authorized") {
		t.Fatalf("preview did not enforce destination ACL: %s", m.View())
	}
	if _, err := os.Stat(account.StateDir); !os.IsNotExist(err) {
		t.Fatalf("inspection and preview touched target state: %v", err)
	}
}
