package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
)

func tuiTestSource(t *testing.T) webSource {
	t.Helper()
	sources, err := configWebSources("")
	if err != nil {
		t.Fatal(err)
	}
	return sources[0]
}

func tuiKeys(m *configTUI, keys ...tea.KeyType) {
	for _, key := range keys {
		m.Update(tea.KeyMsg{Type: key})
	}
}

func tuiType(m *configTUI, value string) {
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)})
}

func tuiSelect(t *testing.T, m *configTUI, label string) {
	t.Helper()
	tuiKeys(m, tea.KeyHome)
	for i := 0; i < len(m.rows); i++ {
		if m.rows[m.row].label == label {
			return
		}
		tuiKeys(m, tea.KeyDown)
	}
	t.Fatalf("missing field %q in %s", label, m.View())
}

func tuiSetField(t *testing.T, m *configTUI, label, value string) {
	t.Helper()
	tuiSelect(t, m, label)
	tuiKeys(m, tea.KeyEnter, tea.KeyF2)
	tuiType(m, value)
	tuiKeys(m, tea.KeyCtrlS)
	if m.screen != tuiBrowse {
		t.Fatalf("edit %q: %s", label, m.status)
	}
}

func TestConfigTUICatalogSelection(t *testing.T) {
	m := newConfigTUI(tuiTestSource(t))
	tuiSelect(t, m, `路由 · "work"`)
	tuiKeys(m, tea.KeyEnter)
	tuiSetField(t, m, "执行 · options", `{"Previous":"value"}`)
	provider := bridge.T3Provider{
		ID: "provider-sample", Driver: "codex", Name: "Provider Example",
		Models: []bridge.T3Model{{ID: "model-sample", Label: "Model Example"}},
	}
	result := bridge.T3Catalog{
		EnvironmentID: "target", LocalEnvironmentID: "local",
		Projects:       []bridge.Project{{ID: "skip"}, {ID: "project-sample", Title: "Example", Root: "/example"}},
		T3Capabilities: bridge.T3Capabilities{Providers: []bridge.T3Provider{provider}},
	}
	result.SuggestProject("", "/example")
	tuiKeys(m, tea.KeyF5, tea.KeyEnter, tea.KeyEsc, tea.KeyF5, tea.KeyEnter)
	m.Update(configTUICatalogResult{request: m.catalog.request - 1, result: result})
	if m.screen != tuiCatalogLoading {
		t.Fatal("an earlier T3 query changed the current screen")
	}
	m.catalog.cancel()
	m.Update(configTUICatalogResult{request: m.catalog.request, result: result})
	if m.catalog.row != 1 {
		t.Fatal("unique project suggestion was not selected in the picker")
	}
	tuiKeys(m, tea.KeyEnter)
	if !strings.Contains(m.View(), "Model Example") {
		t.Fatalf("catalog model is missing from the picker: %s", m.View())
	}
	tuiKeys(m, tea.KeyEnter)
	config, err := bridge.ParseConfig([]byte(m.draft))
	target := config.Routes["work"]
	if err != nil || m.screen != tuiBrowse || config.MCP.EnvironmentID != "local" ||
		target.EnvironmentID != "target" || target.ProjectID != "project-sample" || target.ProjectName != "Example" ||
		target.ProjectPath != "/example" || target.ProviderID != "provider-sample" || target.Model != "model-sample" ||
		len(target.Options) != 0 || target.RuntimeMode != "approval-required" {
		t.Fatalf("incorrect selected target: %+v, %v", target, err)
	}
}

func TestConfigTUIRouteDraftAndSave(t *testing.T) {
	original := tuiTestSource(t).JSON
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := configFileSource(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := newConfigTUI(source)
	tuiType(m, "r")
	tuiType(m, "Work.Team")
	tuiKeys(m, tea.KeyEnter)
	if m.screen != tuiBrowse || m.route != "Work.Team" {
		t.Fatalf("route was not added: %s", m.status)
	}
	tuiType(m, "p")
	tuiType(m, "s")
	if m.screen != tuiReview || m.reviewed != nil {
		t.Fatal("incomplete route could be saved")
	}
	if raw, err := os.ReadFile(path); err != nil || !bytes.Equal(raw, original) {
		t.Fatal("failed review wrote the config", err)
	}
	tuiKeys(m, tea.KeyEsc)
	for _, field := range [][2]string{
		{"目标 · environmentId", "environment-sample"}, {"目标 · projectId", "project-sample"},
		{"目标 · driver", "codex"}, {"目标 · providerId", "provider-sample"}, {"目标 · model", "model-sample"},
		{"执行 · interactionMode", "plan"}, {"ACL · users", `["ou_sample"]`}, {"路由 · aliases", `["Ops"]`},
	} {
		tuiSetField(t, m, field[0], field[1])
	}
	tuiSelect(t, m, "执行 · options")
	tuiKeys(m, tea.KeyEnter, tea.KeyF2)
	tuiType(m, `[]`)
	tuiKeys(m, tea.KeyCtrlS)
	if m.screen != tuiEdit || m.status == "" {
		t.Fatal("wrong option type should leave the field editable")
	}
	tuiKeys(m, tea.KeyF2)
	tuiType(m, `{"Effort":"high","Effort":"low"}`)
	tuiKeys(m, tea.KeyCtrlS)
	if m.screen != tuiEdit || m.status == "" {
		t.Fatal("duplicate option key should leave the field editable")
	}
	tuiKeys(m, tea.KeyF2)
	tuiType(m, `{"Effort":"high","effort":"low","Count":9007199254740993}`)
	tuiKeys(m, tea.KeyCtrlS, tea.KeyEsc)
	tuiSelect(t, m, `路由 · "work"`)
	tuiType(m, "d")
	tuiType(m, "p")
	if m.reviewed != nil || !strings.Contains(m.review.View(), "defaultRoute") {
		t.Fatal("review did not report stale default route")
	}
	tuiKeys(m, tea.KeyEsc)
	tuiSetField(t, m, "路由 · defaultRoute", "Work.Team")
	tuiSetField(t, m, "会议 · meetingEnabled", "true")
	tuiSetField(t, m, "会议 · meetingRoute", "Work.Team")
	tuiSetField(t, m, "会议 · meetingRoomReplies", "true")
	tuiSetField(t, m, "会议 · meetingAutoJoinUsers", `["ou_sample"]`)
	tuiSetField(t, m, "会议 · meetingSummaryTarget", "room")
	tuiType(m, "p")
	tuiType(m, "s")
	if m.reviewed != nil || !strings.Contains(m.review.View(), "meetingSummaryTarget must be empty, origin or owner") {
		t.Fatal("review did not reject an invalid meeting summary target")
	}
	if raw, err := os.ReadFile(path); err != nil || !bytes.Equal(raw, original) {
		t.Fatal("invalid meeting summary target changed the config", err)
	}
	tuiKeys(m, tea.KeyEsc)
	tuiSetField(t, m, "会议 · meetingSummaryTarget", "origin")
	tuiType(m, "p")
	if !bytes.Contains(m.reviewed, []byte(`"Count": 9007199254740993`)) {
		t.Fatalf("review lost the exact option value: %s", m.review.View())
	}
	tuiType(m, "s")
	c, err := bridge.LoadConfig(path)
	r := c.Routes["Work.Team"]
	if err != nil || !c.MeetingEnabled || !c.MeetingRoomReplies || c.MeetingRoute != "Work.Team" ||
		c.MeetingSummaryTarget != "origin" || !slices.Equal(c.MeetingAutoJoinUsers, []string{"ou_sample"}) ||
		len(c.Routes) != 1 || c.DefaultRoute != "Work.Team" || r.ProjectID != "project-sample" ||
		r.RuntimeMode != "approval-required" || r.InteractionMode != "plan" || !slices.Equal(r.Aliases, []string{"Ops"}) ||
		r.Options["Effort"] != "high" || r.Options["effort"] != "low" {
		t.Fatalf("unexpected saved route: %+v, %v", r, err)
	}
	if backup, err := os.ReadFile(path + ".backup.json"); err != nil || !bytes.Equal(backup, original) {
		t.Fatal("save did not preserve the previous file", err)
	}
	tuiType(m, "J")
	tuiKeys(m, tea.KeyF2)
	tuiType(m, `{"appId":"first","appId":"second"}`)
	tuiKeys(m, tea.KeyCtrlS)
	if m.screen != tuiEdit || m.draft != string(m.source.JSON) {
		t.Fatal("full JSON editor accepted duplicate keys")
	}
}

func TestConfigTUIAdditionalAccountDrafts(t *testing.T) {
	m := newConfigTUI(tuiTestSource(t))
	tuiKeys(m, tea.KeyEnter)
	tuiSetField(t, m, "执行 · runtimeMode", "auto")
	tuiKeys(m, tea.KeyEsc)
	fillAccount := func(app string) {
		tuiType(m, "a")
		for _, value := range []string{app, "ou_sample", "/sample/" + app + ".secret", "/sample/" + app} {
			tuiType(m, value)
			tuiKeys(m, tea.KeyTab)
		}
		tuiKeys(m, tea.KeyRight, tea.KeyTab)
	}
	fillAccount("cli_extra")
	tuiKeys(m, tea.KeyCtrlS)
	if m.screen != tuiBrowse || m.account != 1 {
		t.Fatalf("account was not selected: %s", m.status)
	}
	c, err := bridge.ParseConfig([]byte(m.draft))
	if err != nil || len(c.AdditionalAccounts) != 1 {
		t.Fatalf("account draft was not valid: %v", err)
	}
	mcp := c.MCP
	a := c.AdditionalAccounts[0]
	r := a.Routes["work"]
	if a.AppID != "cli_extra" || a.StateDir != "/sample/cli_extra" || a.Tenant != "lark" || a.ReceiverAuthorized ||
		a.MCP != mcp || !slices.Equal(a.Users, []string{"ou_sample"}) || !slices.Equal(r.Users, a.Users) ||
		r.RuntimeMode != "approval-required" || len(a.Admins) != 0 || len(a.Chats) != 0 {
		t.Fatalf("unexpected copied account: %+v", a)
	}
	tuiType(m, "r")
	tuiType(m, "extra-route")
	tuiKeys(m, tea.KeyEnter)
	tuiSetField(t, m, "目标 · environmentId", "extra-env")
	if !strings.Contains(string(m.routes["extra-route"]), "extra-env") {
		t.Fatal("route edit did not apply to the selected additional account")
	}
	tuiType(m, "d")
	tuiKeys(m, tea.KeyShiftTab)
	tuiType(m, "r")
	tuiType(m, "z-incomplete")
	tuiKeys(m, tea.KeyEnter, tea.KeyTab)
	fillAccount("cli_draft")
	tuiKeys(m, tea.KeyRight, tea.KeyCtrlS)
	if m.screen != tuiBrowse || m.account != 2 || m.routes["z-incomplete"] == nil {
		t.Fatalf("could not copy an incomplete route: %s", m.status)
	}
	tuiType(m, "p")
	if m.reviewed != nil {
		t.Fatal("review accepted incomplete accounts")
	}
	tuiKeys(m, tea.KeyEsc)
	tuiType(m, "D")
	if len(m.accounts) != 2 || m.account != 1 {
		t.Fatal("account was not removed from the draft")
	}
	tuiKeys(m, tea.KeyTab)
	tuiSelect(t, m, `路由 · "z-incomplete"`)
	tuiType(m, "d")
	tuiType(m, "p")
	if m.reviewed == nil {
		t.Fatalf("review rejected repaired draft: %s", m.review.View())
	}
}

func tuiProfileModel(t *testing.T) *configTUI {
	t.Helper()
	source := tuiTestSource(t)
	m := newConfigTUI(source)
	m.profiles.registry = profileRegistry{root: t.TempDir()}
	m.profiles.fallback = source
	return m
}

func TestConfigTUIProfileLifecycle(t *testing.T) {
	m := tuiProfileModel(t)
	r := m.profiles.registry
	if !strings.Contains(m.View(), "F4 命名配置") {
		t.Fatal("profile manager shortcut is missing")
	}
	tuiKeys(m, tea.KeyF4)
	tuiType(m, "n")
	tuiType(m, "work")
	tuiKeys(m, tea.KeyEnter)
	if m.screen != tuiProfiles || !slices.Equal(m.profiles.entries, []string{"work"}) {
		t.Fatalf("profile was not created: %s", m.View())
	}
	tuiType(m, "u")
	if m.source.Path != "" || m.profiles.active != "work" || !strings.Contains(m.View(), "work · 默认") {
		t.Fatalf("setting default changed the current source or failed: %s", m.View())
	}
	tuiKeys(m, tea.KeyEnter)
	path, err := r.existingFile("work")
	if err != nil || m.screen != tuiBrowse || m.source.Path != path {
		t.Fatalf("profile was not opened: %s (%v)", m.View(), err)
	}
	original := m.source.JSON
	tuiSetField(t, m, "账号 · receiverMachine", "work-edited")
	tuiType(m, "p")
	tuiType(m, "s")
	saved, err := bridge.LoadConfig(path)
	if err != nil || saved.ReceiverMachine != "work-edited" || saved.ReceiverAuthorized {
		t.Fatalf("profile was not saved in place: %+v, %v", saved, err)
	}
	running, err := lockProfileRuntime(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = running.Close() })
	tuiKeys(m, tea.KeyF4)
	tuiType(m, "a")
	tuiKeys(m, tea.KeyEnter)
	if m.screen != tuiConfirmProfileArchive || !strings.Contains(m.status, "already running") || m.source.Path != path {
		t.Fatalf("running profile was not protected: %s", m.View())
	}
	if err := running.Close(); err != nil {
		t.Fatal(err)
	}
	tuiKeys(m, tea.KeyEnter)
	if m.screen != tuiProfiles || m.source.Path != "" || m.profiles.active != "" || len(m.profiles.entries) != 0 {
		t.Fatalf("active profile did not move to archive: %s", m.View())
	}
	tuiKeys(m, tea.KeyTab)
	if !m.profiles.archived || len(m.profiles.entries) != 1 {
		t.Fatalf("archive is not selectable: %s", m.View())
	}
	archived := filepath.Join(r.root, "profile-archive", m.profiles.entries[0], "config.json")
	if backup, err := readConfigBackup(archived); err != nil || !bytes.Equal(backup, original) {
		t.Fatalf("archive did not preserve the configuration backup: %v", err)
	}
	tuiKeys(m, tea.KeyEnter, tea.KeyEnter)
	if m.screen != tuiBrowse || m.source.Path != path || !strings.Contains(m.draft, "work-edited") {
		t.Fatalf("restored profile was not opened: %s", m.View())
	}
	if restored, err := bridge.LoadConfig(path); err != nil || restored.StateDir != saved.StateDir {
		t.Fatalf("restored profile lost its state directory: %v", err)
	}
	if backup, err := readConfigBackup(path); err != nil || !bytes.Equal(backup, original) {
		t.Fatalf("restore did not preserve the configuration backup: %v", err)
	}
	export := filepath.Join(t.TempDir(), "work.candidate.json")
	tuiType(m, "p")
	tuiType(m, "e")
	tuiType(m, export)
	tuiKeys(m, tea.KeyEnter)
	if candidate, err := os.ReadFile(export); err != nil || !bytes.Equal(candidate, m.source.JSON) {
		t.Fatalf("profile preview did not export the candidate: %v", err)
	}
}

func TestConfigTUIProfileDraftProtection(t *testing.T) {
	m := tuiProfileModel(t)
	tuiKeys(m, tea.KeyF4)
	tuiType(m, "n")
	tuiType(m, "first")
	tuiKeys(m, tea.KeyEnter, tea.KeyEnter)
	tuiSetField(t, m, "账号 · appId", "cli_saved")
	tuiType(m, "p")
	tuiType(m, "s")
	first := m.source
	tuiSetField(t, m, "账号 · appId", "cli_unsaved")
	draft := m.draft

	tuiKeys(m, tea.KeyF4)
	tuiType(m, "a")
	tuiKeys(m, tea.KeyEnter)
	if m.screen != tuiConfirmProfileArchive || !strings.Contains(m.View(), "丢弃当前草稿") {
		t.Fatal("archiving the edited profile must explicitly confirm draft loss")
	}
	tuiKeys(m, tea.KeyEsc)
	tuiType(m, "n")
	tuiType(m, "second")
	tuiKeys(m, tea.KeyTab)
	tuiType(m, first.Path)
	tuiKeys(m, tea.KeyEnter)
	tuiType(m, "u")
	tuiKeys(m, tea.KeyEnter, tea.KeyEnter)
	if m.screen != tuiConfirmProfileOpen || m.draft != draft || m.source.Path != first.Path {
		t.Fatalf("profile manager did not retain the draft: %s", m.View())
	}
	tuiKeys(m, tea.KeyEsc, tea.KeyEsc)
	if m.screen != tuiBrowse || m.draft != draft {
		t.Fatal("canceling a switch lost the draft")
	}
	tuiKeys(m, tea.KeyF4, tea.KeyDown, tea.KeyEnter)
	tuiType(m, "d")
	cloned, err := bridge.ParseConfig(m.source.JSON)
	if err != nil || m.screen != tuiBrowse || cloned.AppID != "cli_saved" || cloned.ReceiverMachine != "second" {
		t.Fatalf("switch did not open a clone of the saved profile: %s (%v)", m.View(), err)
	}
	saved, err := bridge.LoadConfig(first.Path)
	if err != nil || cloned.StateDir == saved.StateDir || saved.AppID != "cli_saved" {
		t.Fatalf("discarding the draft changed the original or reused its state directory: %v", err)
	}
}
