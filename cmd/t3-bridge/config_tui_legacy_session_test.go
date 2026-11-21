package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
)

func TestConfigTUILegacySessionRouteReview(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LARK_CHANNEL_HOME", dir)
	config, err := bridge.ParseConfig(tuiTestSource(t).JSON)
	if err != nil {
		t.Fatal(err)
	}
	account := config
	account.AppID, account.StateDir = "cli_selected", filepath.Join(dir, "state")
	target := account.Routes["work"]
	account.Routes = map[string]bridge.Target{"work": target}
	config.AdditionalAccounts = []bridge.Config{account}
	original, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := configFileSource(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	target.Model = "synthetic-thread-model"
	account.Routes["work"] = target
	candidate, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}

	m := newConfigTUI(source)
	tuiKeys(m, tea.KeyTab, tea.KeyEnter)
	tuiType(m, "S")
	if view := m.View(); m.screen != tuiLegacySessionForm || !strings.Contains(view, "Ctrl+R 准备路由配置") {
		t.Fatalf("missing prepare route action: %s", view)
	}
	legacyPath := filepath.Join(dir, "legacy", "config.json")
	for _, value := range []string{legacyPath, "legacy", "thread-legacy", "oc_legacy", "ou_legacy", "work", "group"} {
		tuiKeys(m, tea.KeyF2)
		tuiType(m, value)
		tuiKeys(m, tea.KeyTab)
	}
	tuiType(m, "omt_legacy")
	want := legacySessionRequest{LegacyConfig: legacyPath, LegacyProfile: "legacy", AppID: "cli_selected",
		ThreadID: "thread-legacy", ChatID: "oc_legacy", UserID: "ou_legacy", RouteID: "work", ChatType: "group", TopicID: "omt_legacy"}
	prepareKey := tea.KeyMsg{Type: tea.KeyCtrlR}
	_, cmd := m.Update(prepareKey)
	if cmd == nil || m.screen != tuiLegacySessionLoading || m.legacySession.request != want ||
		!strings.Contains(m.View(), "正在只读核验 T3 并准备路由配置") {
		t.Fatalf("Ctrl+R did not prepare the selected scope: %s", m.View())
	}
	// Deliver synthetic replies without executing T3 commands.
	reply := configTUILegacySessionPreview{id: m.legacySession.readID, candidate: candidate}
	tuiKeys(m, tea.KeyEsc)
	m.Update(reply)
	if m.screen != tuiLegacySessionForm || m.input.Value() != want.TopicID || m.draft != string(source.JSON) {
		t.Fatalf("Escape lost the scope or let a cancelled request update the draft: %s", m.View())
	}
	tuiKeys(m, tea.KeyCtrlT)
	m.Update(configTUILegacySessionPreview{id: m.legacySession.readID, verifyT3: true, checkLines: []string{"synthetic mismatch"}})
	tuiKeys(m, tea.KeyEsc)
	_, cmd = m.Update(prepareKey)
	m.Update(reply)
	if cmd == nil || m.screen != tuiLegacySessionLoading || m.draft != string(source.JSON) {
		t.Fatalf("a stale result displaced route preparation after the T3 check: %s", m.View())
	}
	reply.id = m.legacySession.readID
	m.Update(reply)
	if view := m.View(); m.screen != tuiReview || m.reviewed == nil || m.legacySession.review != "" ||
		m.source.Revision != source.Revision || m.account != 1 || m.route != "work" || m.legacySession.request != want ||
		!strings.Contains(view, "候选配置差异") || !strings.Contains(view, target.Model) || !strings.Contains(view, "会话须另行离线登记") {
		t.Fatalf("route preparation did not enter the normal config review: %s", view)
	}
	tuiKeys(m, tea.KeyEsc)
	if raw, err := os.ReadFile(path); err != nil || string(raw) != string(original) ||
		m.screen != tuiBrowse || m.draft != string(m.reviewed) {
		t.Fatalf("leaving review did not keep an unsaved draft: %v, %s", err, m.View())
	}
	tuiType(m, "p")
	tuiType(m, "s")
	if raw, err := os.ReadFile(path); err != nil || m.screen != tuiBrowse || m.source.Revision == source.Revision ||
		string(raw) != string(m.reviewed) || !strings.Contains(string(raw), target.Model) {
		t.Fatalf("normal save did not persist the route candidate: %v, %s", err, m.View())
	}
	if _, err := os.Stat(account.StateDir); !os.IsNotExist(err) {
		t.Fatalf("saving the route candidate wrote session state: %v", err)
	}
	tuiType(m, "S")
	if m.screen != tuiLegacySessionForm || m.legacySession.request != want || m.input.Value() != want.TopicID {
		t.Fatalf("reopening the saved config lost the selected transfer scope: %s", m.View())
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmd == nil || m.screen != tuiLegacySessionLoading {
		t.Fatalf("offline preview did not remain a separate form action: %s", m.View())
	}
}

func TestConfigTUILegacySession(t *testing.T) {
	template := tuiTestSource(t)
	dir := t.TempDir()
	suggestedRoot := filepath.Join(dir, "detected")
	t.Setenv("LARK_CHANNEL_HOME", suggestedRoot)
	suggestedPath := filepath.Join(suggestedRoot, "config.json")
	if err := os.Mkdir(suggestedRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(suggestedPath, []byte("synthetic non-JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.json")
	missing, err := configFileSource(configPath, template.JSON)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []webSource{template, missing} {
		m := newConfigTUI(source)
		tuiType(m, "S")
		if m.screen != tuiBrowse || !strings.Contains(m.status, "保存") {
			t.Fatalf("transferred from an unsaved source: %s", m.View())
		}
	}

	primary, err := bridge.ParseConfig(template.JSON)
	if err != nil {
		t.Fatal(err)
	}
	primary.AppID, primary.StateDir = "cli_primary", filepath.Join(dir, "primary")
	account := primary
	account.AppID, account.StateDir = "cli_session", filepath.Join(dir, "target")
	account.Users, account.Chats = []string{"ou_session"}, []string{"oc_session"}
	route := account.Routes["work"]
	route.ProjectID, route.EnvironmentID = "project-session", "environment-session"
	route.ProjectPath, route.Users = filepath.Join(dir, "project"), account.Users
	account.Routes = map[string]bridge.Target{"work": route}
	primary.AdditionalAccounts = []bridge.Config{account}
	config, err := json.Marshal(primary)
	if err != nil {
		t.Fatal(err)
	}
	const legacy = `{"schemaVersion":2,"profiles":{"legacy":{"schemaVersion":2,"agentKind":"t3","accounts":{"app":{"id":"cli_session"}}},"primary":{"schemaVersion":2,"agentKind":"t3","accounts":{"app":{"id":"cli_primary"}}}}}`
	legacyPath := filepath.Join(dir, "old", "config.json")
	catalogPath := filepath.Join(dir, "old", "profiles", "legacy", "sessions.json.catalog.json")
	if err := os.MkdirAll(filepath.Dir(catalogPath), 0700); err != nil {
		t.Fatal(err)
	}
	var entries []map[string]any
	for _, entry := range []struct {
		scopeID, threadID string
		updatedAt         int64
	}{
		{"oc_session", "thread-session", 1700000000000},
		{"oc_session", "thread-recent", 1700000001000},
		{"oc_other", "thread-other", 1700000002000},
		{"oc_session:omt_topic", "thread-topic", 1700000003000},
		{"oc_session:omt_other", "thread-other-topic", 1699999999000},
	} {
		policy := "synthetic policy " + entry.threadID
		entries = append(entries, map[string]any{
			"key":     strings.Join([]string{entry.scopeID, "t3", route.ProjectPath, policy}, "\x1f"),
			"scopeId": entry.scopeID, "cwdRealpath": route.ProjectPath, "policyFingerprint": policy,
			"agentId": "t3", "status": "active", "threadId": entry.threadID, "lastSummary": "synthetic transcript",
			"updatedAt": entry.updatedAt,
		})
	}
	catalog, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	fixtures := map[string][]byte{configPath: config, legacyPath: []byte(legacy), catalogPath: catalog}
	for path, contents := range fixtures {
		if err := os.WriteFile(path, contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	source, err := configFileSource(configPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := newConfigTUI(source)
	tuiSetField(t, m, "账号 · receiverAuthorized", "true")
	tuiType(m, "S")
	if m.screen != tuiBrowse || !strings.Contains(m.status, "尚未保存") {
		t.Fatalf("opened with an unsaved config draft: %s", m.View())
	}

	m = newConfigTUI(source)
	if !strings.Contains(m.View(), "S 转接旧会话") {
		t.Fatalf("missing session transfer shortcut: %s", m.View())
	}
	tuiKeys(m, tea.KeyTab)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 8})
	tuiType(m, "S")
	if view := m.View(); m.screen != tuiLegacySessionForm || m.input.Value() != suggestedPath || m.status != "" ||
		!strings.Contains(view, "App cli_session") || !strings.Contains(view, filepath.Join("detected", "config.json")) ||
		!strings.Contains(view, "Ctrl+S 离线预览") || !strings.Contains(view, "Ctrl+T 预览并核验 T3") ||
		!strings.Contains(view, "F2 清空") || !strings.Contains(view, "F3 盘点/查找") || len(strings.Split(view, "\n")) > m.height {
		t.Fatalf("missing selected App or editable suggestion in a short terminal: %s", view)
	}
	tuiKeys(m, tea.KeyF2)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyF3})
	if cmd != nil || m.screen != tuiLegacySessionForm || !strings.Contains(m.status, "旧配置绝对路径") {
		t.Fatalf("inventory accepted a missing source path: %s", m.View())
	}
	tuiType(m, legacyPath)
	tuiKeys(m, tea.KeyEsc)
	tuiType(m, "S")
	if m.screen != tuiLegacySessionForm || m.input.Value() != legacyPath {
		t.Fatalf("reopening the transfer changed the edited path: %s", m.View())
	}
	press := func(key tea.KeyMsg) tea.Cmd {
		t.Helper()
		_, cmd := m.Update(key)
		if cmd == nil {
			t.Fatalf("expected an async command for %s: %s", key, m.View())
		}
		return cmd
	}
	lookupKey := tea.KeyMsg{Type: tea.KeyF3}
	previewKey := tea.KeyMsg{Type: tea.KeyCtrlS}
	verifyKey := tea.KeyMsg{Type: tea.KeyCtrlT}
	confirmKey := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}}
	inventory := press(lookupKey)
	if m.screen != tuiLegacySessionInspecting || !strings.Contains(m.View(), "正在只读盘点") {
		t.Fatalf("expected pending inventory: %s", m.View())
	}
	m.Update(inventory())
	view := m.View()
	for _, part := range []string{"只读盘点", "App cli_session", "› 1 个线程 · oc_session / omt_topic", "Enter 填入群聊和话题", "旧目录无法验证聊天类型或成员"} {
		if m.screen != tuiLegacySessionPick || !strings.Contains(view, part) || len(strings.Split(view, "\n")) > m.height {
			t.Fatalf("inventory missing %q in a short terminal: %s", part, view)
		}
	}
	if len(m.legacySession.candidates) != 0 {
		t.Fatalf("inventory exposed thread candidates: %s", view)
	}
	for _, part := range []string{"thread-topic", "synthetic transcript", "synthetic policy", route.ProjectPath} {
		if strings.Contains(view, part) {
			t.Fatalf("inventory disclosed %q: %s", part, view)
		}
	}
	tuiKeys(m, tea.KeyDown, tea.KeyDown)
	if view = m.View(); !strings.Contains(view, "› 2 个线程 · oc_session") || !strings.Contains(view, "Enter 填入聊天 ID") || len(strings.Split(view, "\n")) > m.height {
		t.Fatalf("chat scope did not stay visible in a short terminal: %s", view)
	}
	tuiKeys(m, tea.KeyEnter)
	chatDraft := legacySessionRequest{LegacyConfig: legacyPath, AppID: "cli_session", ChatID: "oc_session"}
	if m.screen != tuiLegacySessionForm || m.input.Value() != legacyPath || m.legacySession.request != chatDraft ||
		!strings.Contains(m.View(), "请确认聊天类型") {
		t.Fatalf("chat selection inferred a chat type or lost the confirmation hint: %s", m.View())
	}
	_, cmd = m.Update(previewKey)
	if cmd != nil || m.screen != tuiLegacySessionForm || !strings.Contains(m.View(), "用户 Open ID") {
		t.Fatalf("chat selection allowed a preview with an incomplete mapping: %s", m.View())
	}

	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	for _, value := range []string{"legacy", "thread-manual", "oc_session", "ou_session", "work"} {
		tuiKeys(m, tea.KeyTab, tea.KeyF2)
		tuiType(m, value)
	}
	staleLookup := press(lookupKey)
	if m.screen != tuiLegacySessionInspecting {
		t.Fatalf("expected pending lookup: %s", m.View())
	}
	tuiKeys(m, tea.KeyEsc)
	lookup := press(lookupKey)
	m.Update(staleLookup())
	if m.screen != tuiLegacySessionInspecting {
		t.Fatalf("received a cancelled lookup: %s", m.View())
	}
	m.Update(lookup())
	view = m.View()
	for _, part := range []string{"› thread-recent", "thread-session", "项目目录 · ", "2023-11-14T22:13:21Z", "oc_session", "旧目录无法验证聊天类型或成员"} {
		if m.screen != tuiLegacySessionPick || !strings.Contains(view, part) {
			t.Fatalf("picker missing %q: %s", part, view)
		}
	}
	for _, part := range []string{"thread-other", "thread-topic", "synthetic transcript", "synthetic policy"} {
		if strings.Contains(view, part) {
			t.Fatalf("picker disclosed %q: %s", part, view)
		}
	}
	tuiKeys(m, tea.KeyDown, tea.KeyEsc)
	if m.screen != tuiLegacySessionForm || m.input.Value() != "work" || m.legacySession.request.ThreadID != "thread-manual" {
		t.Fatalf("closing the picker changed the form draft: %s", m.View())
	}
	m.Update(press(lookupKey)())
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 8})
	tuiKeys(m, tea.KeyDown)
	if view := m.View(); !strings.Contains(view, "› thread-session") || len(strings.Split(view, "\n")) > m.height {
		t.Fatalf("selection did not stay visible in a short terminal: %s", view)
	}
	tuiKeys(m, tea.KeyUp)
	if !strings.Contains(m.View(), "› thread-recent") {
		t.Fatalf("up did not restore the first selection: %s", m.View())
	}
	tuiKeys(m, tea.KeyDown, tea.KeyEnter)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	want := legacySessionRequest{LegacyConfig: legacyPath, LegacyProfile: "legacy", AppID: "cli_session",
		ThreadID: "thread-session", ChatID: "oc_session", UserID: "ou_session", RouteID: "work"}
	if m.screen != tuiLegacySessionForm || m.input.Value() != "work" || m.legacySession.request != want {
		t.Fatalf("selecting a thread did not preserve the form draft: %s", m.View())
	}
	if _, err := os.Stat(account.StateDir); !os.IsNotExist(err) {
		t.Fatalf("lookup or selection created state: %v", err)
	}
	for path, contents := range fixtures {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(contents) {
			t.Fatalf("lookup or selection changed %s: %v", path, err)
		}
	}
	stale := press(previewKey)
	if m.screen != tuiLegacySessionLoading {
		t.Fatalf("expected pending preview: %s", m.View())
	}
	tuiKeys(m, tea.KeyEsc, tea.KeyEsc)
	tuiType(m, "S")
	if m.screen != tuiLegacySessionForm || m.input.Value() != "work" {
		t.Fatalf("Esc did not keep the form draft: %s", m.View())
	}
	current := press(previewKey)
	m.Update(stale())
	if m.screen != tuiLegacySessionLoading {
		t.Fatalf("received a cancelled preview: %s", m.View())
	}
	offline := current().(configTUILegacySessionPreview)
	m.Update(offline)
	if m.screen != tuiLegacySessionReview || m.legacySession.review == "" {
		t.Fatalf("expected a review before writing: %s", m.View())
	}
	for _, part := range []string{"范围 · 私聊", "thread-session", "oc_session", "ou_session", "environment-session", "project-session", "首条输入", "停止旧接收端", "新 Store 已离线"} {
		if !strings.Contains(m.View(), part) {
			t.Fatalf("preview missing %q: %s", part, m.View())
		}
	}
	if strings.Contains(m.View(), "synthetic transcript") {
		t.Fatalf("preview disclosed legacy content: %s", m.View())
	}
	if _, err := os.Stat(account.StateDir); !os.IsNotExist(err) {
		t.Fatalf("preview created state: %v", err)
	}
	tuiKeys(m, tea.KeyEsc)
	press(verifyKey)
	if m.screen != tuiLegacySessionLoading || !strings.Contains(m.View(), "正在只读核验 T3") {
		t.Fatalf("expected pending T3 verification: %s", m.View())
	}
	// Inject T3 results without starting a remote command.
	verified := configTUILegacySessionPreview{id: m.legacySession.readID, result: offline.result,
		review: offline.review, verifyT3: true, checkLines: []string{"实际模型 · synthetic-model"}}
	tuiKeys(m, tea.KeyEsc)
	press(verifyKey)
	m.Update(verified)
	if m.screen != tuiLegacySessionLoading || m.legacySession.review != "" {
		t.Fatalf("received a cancelled T3 verification: %s", m.View())
	}
	blocked := configTUILegacySessionPreview{id: m.legacySession.readID, result: offline.result,
		verifyT3: true, checkLines: []string{"实际模型 · synthetic-model", "差异 · synthetic-mismatch"}}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m.Update(blocked)
	if view := m.View(); m.screen != tuiLegacySessionReview || m.legacySession.review != "" ||
		strings.Contains(view, "s 确认写入") || !strings.Contains(view, "Ctrl+T 重新核验或 Ctrl+S 离线预览") ||
		strings.Contains(view, "synthetic-mismatch") || len(strings.Split(view, "\n")) > m.height {
		t.Fatalf("expected a scrollable T3 review without confirmation: %s", view)
	}
	tuiKeys(m, tea.KeyPgDown, tea.KeyPgDown, tea.KeyPgDown)
	if view := m.View(); !strings.Contains(view, "实际模型 · synthetic-model") || !strings.Contains(view, "差异 · synthetic-mismatch") {
		t.Fatalf("could not scroll to the T3 verification details: %s", view)
	}
	if _, cmd := m.Update(confirmKey); cmd != nil || m.screen != tuiLegacySessionReview {
		t.Fatalf("T3 verification without a review allowed confirmation: %s", m.View())
	}
	if _, err := os.Stat(account.StateDir); !os.IsNotExist(err) {
		t.Fatalf("T3 verification without a review created state: %v", err)
	}
	tuiKeys(m, tea.KeyEsc)
	press(verifyKey)
	verified.id = m.legacySession.readID
	m.Update(verified)
	if view := m.View(); m.screen != tuiLegacySessionReview || m.legacySession.review != verified.review ||
		!strings.Contains(view, "T3 核验通过") || !strings.Contains(view, "s 确认写入") {
		t.Fatalf("verified review could not be confirmed: %s", view)
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	tuiKeys(m, tea.KeyEsc, tea.KeyShiftTab)
	if m.screen != tuiLegacySessionForm || m.legacySession.review != "" || m.input.Value() != "ou_session" {
		t.Fatalf("Esc did not return to the reviewed draft: %s", m.View())
	}
	m.Update(press(previewKey)())
	// Changing even catalog metadata invalidates the reviewed source.
	if err := os.WriteFile(catalogPath, append(catalog, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	m.Update(press(confirmKey)())
	if m.screen != tuiLegacySessionForm || m.legacySession.review != "" || !strings.Contains(m.status, "重新预览") {
		t.Fatalf("confirmation used a stale review: %s", m.View())
	}
	m.Update(press(previewKey)())
	confirm := press(confirmKey)
	if m.screen != tuiLegacySessionApplying {
		t.Fatalf("expected async confirmation: %s", m.View())
	}
	m.Update(confirm())
	if m.screen != tuiBrowse || m.draft != string(source.JSON) || m.account != 1 || !strings.Contains(m.status, "已写入转接元数据 · 私聊") {
		t.Fatalf("did not confirm the transfer for the selected App: %s", m.View())
	}
	state, err := os.ReadFile(filepath.Join(account.StateDir, "state.json"))
	if err != nil || !strings.Contains(string(state), "thread-session") || strings.Contains(string(state), "synthetic transcript") {
		t.Fatalf("unexpected transfer state: %s, %v", state, err)
	}
	if _, err := os.Stat(primary.StateDir); !os.IsNotExist(err) {
		t.Fatalf("transfer touched primary account state: %v", err)
	}

	tuiType(m, "S")
	tuiKeys(m, tea.KeyF2)
	tuiType(m, legacyPath)
	m.Update(press(lookupKey)())
	tuiKeys(m, tea.KeyEnter)
	topicDraft := legacySessionRequest{LegacyConfig: legacyPath, AppID: "cli_session", ChatID: "oc_session", ChatType: "group", TopicID: "omt_topic"}
	if m.screen != tuiLegacySessionForm || m.input.Value() != legacyPath || m.legacySession.request != topicDraft {
		t.Fatalf("selecting a catalog topic did not set its exact group scope: %s", m.View())
	}
	m.Update(press(lookupKey)())
	if candidates := m.legacySession.candidates; m.screen != tuiLegacySessionPick || len(candidates) != 1 ||
		candidates[0].ThreadID != "thread-topic" || !strings.Contains(m.View(), "话题 ID omt_topic") {
		t.Fatalf("F3 did not restrict candidates to the exact topic: %s", m.View())
	}
	tuiKeys(m, tea.KeyEnter)
	if _, cmd := m.Update(previewKey); cmd != nil || m.screen != tuiLegacySessionForm || !strings.Contains(m.status, "用户 Open ID") {
		t.Fatalf("topic selection allowed a preview with an incomplete mapping: %s", m.View())
	}
	tuiKeys(m, tea.KeyTab, tea.KeyTab, tea.KeyTab, tea.KeyTab)
	tuiType(m, "ou_session")
	tuiKeys(m, tea.KeyTab)
	tuiType(m, "work")
	tuiKeys(m, tea.KeyTab)
	if m.input.Value() != "group" || !strings.Contains(m.View(), "聊天类型") {
		t.Fatalf("the first appended field did not retain the selected chat type: %s", m.View())
	}
	tuiKeys(m, tea.KeyTab)
	if m.input.Value() != "omt_topic" || !strings.Contains(m.View(), "原话题 ID") {
		t.Fatalf("the second appended field did not retain the selected topic: %s", m.View())
	}
	tuiKeys(m, tea.KeyF2)
	m.Update(press(lookupKey)())
	if candidates := m.legacySession.candidates; m.screen != tuiLegacySessionPick || len(candidates) != 2 ||
		candidates[0].ThreadID != "thread-recent" || candidates[1].ThreadID != "thread-session" ||
		!strings.Contains(m.View(), "群聊 · 无话题") {
		t.Fatalf("F3 did not restrict group lookup to the plain chat scope: %s", m.View())
	}
	tuiKeys(m, tea.KeyEsc)
	tuiType(m, "omt_topic")
	tuiKeys(m, tea.KeyShiftTab, tea.KeyF2)
	tuiType(m, "p2p")
	m.Update(press(previewKey)())
	if m.screen != tuiLegacySessionForm || m.legacySession.review != "" || !strings.Contains(m.status, "explicit group chat type") {
		t.Fatalf("preview accepted a topic with private chat type: %s", m.View())
	}
	tuiKeys(m, tea.KeyF2)
	tuiType(m, "group")
	m.Update(press(previewKey)())
	if m.screen != tuiLegacySessionReview || m.legacySession.review == "" {
		t.Fatalf("expected an exact topic review before writing: %s", m.View())
	}
	for _, part := range []string{"范围 · 群聊 · 话题 ID omt_topic", "thread-topic", "ou_session", "所选用户在该范围的首条输入", "逐条交叉核对原 T3 会话的输入与飞书原始消息", "混有其他发送者时，运行时会拒绝续接"} {
		if !strings.Contains(m.View(), part) {
			t.Fatalf("topic review missing %q: %s", part, m.View())
		}
	}
	beforeConfirmation, err := os.ReadFile(filepath.Join(account.StateDir, "state.json"))
	if err != nil || string(beforeConfirmation) != string(state) {
		t.Fatalf("topic selection or preview changed the state: %v", err)
	}
	m.Update(press(confirmKey)())
	if m.screen != tuiBrowse || !strings.Contains(m.View(), "已写入转接元数据 · 群聊 · 话题 ID omt_topic · 待首条输入核验") {
		t.Fatalf("topic confirmation omitted the applied scope: %s", m.View())
	}
	state, err = os.ReadFile(filepath.Join(account.StateDir, "state.json"))
	if err != nil || !strings.Contains(string(state), "thread-topic") ||
		!strings.Contains(string(state), "thread-session") || strings.Contains(string(state), "synthetic transcript") {
		t.Fatalf("unexpected state after topic confirmation: %s, %v", state, err)
	}
}
