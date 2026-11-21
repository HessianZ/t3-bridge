package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestConfigTUILegacyImportDraft(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)
	t.Setenv("LARK_CHANNEL_HOME", dir)
	targetPath := filepath.Join(dir, "target.json")
	legacyPath := filepath.Join(dir, strings.Repeat("source-", 12)+"legacy.json")
	statePath, secretPath := filepath.Join(dir, "new-state"), filepath.Join(dir, "app.secret")
	original := tuiTestSource(t).JSON
	legacy := []byte(`{
		"schemaVersion": 2, "activeProfile": "sample", "profiles": {"sample": {
			"schemaVersion": 2, "accounts": {"app": {"id": "cli_imported"}},
			"access": {"allowedUsers": ["ou_imported"]}
		}}
	}`)
	for path, data := range map[string][]byte{targetPath: original, legacyPath: legacy} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	source, err := configFileSource(targetPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := newConfigTUI(source)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	if view := m.View(); !strings.Contains(view, "L 导入旧配置") || !strings.Contains(view, "q 退出") {
		t.Fatalf("missing browse shortcuts: %s", view)
	}
	tuiSetField(t, m, "账号 · receiverAuthorized", "true")
	tuiSelect(t, m, `路由 · "work"`)
	tuiKeys(m, tea.KeyEnter)
	tuiSetField(t, m, "目标 · projectId", "project-unsaved")
	tuiSetField(t, m, "ACL · users", `["ou_route"]`)
	tuiKeys(m, tea.KeyEsc)
	beforeImport := m.draft

	tuiType(m, "L")
	if m.screen != tuiImportLegacy || m.input.Value() != "" || m.status != "" {
		t.Fatalf("expected a blank path without a detected config: %s", m.View())
	}
	tuiType(m, legacyPath)
	view := m.View()
	if m.screen != tuiImportLegacy || !strings.Contains(view, "单路由模板可沿用旧用户名单") || !strings.Contains(view, "关闭所有账号的接收授权") {
		t.Fatalf("missing import form guidance: %s", view)
	}
	if lines := strings.Split(view, "\n"); len(lines) > m.height || slices.ContainsFunc(lines, func(line string) bool {
		return ansi.StringWidth(line) > m.width
	}) {
		t.Fatalf("import form exceeds window size: %q", view)
	}
	tuiKeys(m, tea.KeyEsc)
	if m.screen != tuiBrowse || m.draft != beforeImport {
		t.Fatal("cancel changed the draft")
	}

	suggestedPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(suggestedPath, []byte("synthetic non-JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 8})
	tuiType(m, "L")
	if view := m.View(); m.screen != tuiImportLegacy || m.input.Value() != suggestedPath || m.status != "" ||
		!strings.Contains(view, "config.json") || !strings.Contains(view, "F2 清空") || len(strings.Split(view, "\n")) > m.height {
		t.Fatalf("missing editable suggestion in a short terminal: %s", view)
	}
	tuiKeys(m, tea.KeyF2)
	tuiType(m, legacyPath)
	tuiKeys(m, tea.KeyTab, tea.KeyTab)
	tuiType(m, statePath)
	tuiKeys(m, tea.KeyTab, tea.KeyCtrlS)
	if m.screen != tuiImportLegacy || m.status == "" || m.draft != beforeImport {
		t.Fatalf("failed import did not keep the form and draft: %s", m.View())
	}
	tuiType(m, secretPath)
	tuiKeys(m, tea.KeyCtrlS)
	if m.screen != tuiReview || m.reviewed == nil || m.source.Path != targetPath || m.source.Revision != source.Revision || m.draft != string(m.reviewed) {
		t.Fatalf("successful import did not preview the draft in the current file: %s", m.View())
	}
	if strings.Contains(m.View(), "c 复制 App Secret") {
		t.Fatal("review offered credential copying without a provider")
	}
	candidate, err := bridge.ParseConfig(m.reviewed)
	if err != nil {
		t.Fatal(err)
	}
	route := candidate.Routes["work"]
	if candidate.AppID != "cli_imported" || !slices.Equal(candidate.Users, []string{"ou_imported"}) ||
		candidate.AppSecretFile != secretPath || candidate.StateDir != statePath || candidate.ReceiverAuthorized ||
		route.ProjectID != "project-unsaved" || !slices.Equal(route.Users, []string{"ou_route"}) {
		t.Fatal("import did not apply the legacy account and keep the edited route")
	}
	for path, want := range map[string][]byte{targetPath: original, legacyPath: legacy} {
		if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("import preview changed %s: %v", path, err)
		}
	}
	for _, path := range []string{statePath, secretPath} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("import preview touched %s: %v", path, err)
		}
	}
}

func TestConfigTUILegacyCopyConfirmation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)
	t.Setenv("LARK_CHANNEL_HOME", dir)
	targetPath, legacyPath := filepath.Join(dir, "target.json"), filepath.Join(dir, "legacy.json")
	statePath, secretPath := filepath.Join(dir, "state"), filepath.Join(dir, "app.secret")
	original := bytes.ReplaceAll(tuiTestSource(t).JSON, []byte("REPLACE_OPEN_ID"), []byte("ou_route"))
	legacy := []byte(`{
		"schemaVersion": 2, "activeProfile": "legacy.team", "profiles": {"legacy.team": {
			"schemaVersion": 2, "accounts": {"app": {"id": "cli_imported"}},
			"access": {"allowedUsers": ["ou_imported"]}
		}}
	}`)
	for path, raw := range map[string][]byte{targetPath: original, legacyPath: legacy} {
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	source, err := configFileSource(targetPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := newConfigTUI(source)
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 12})
	m.legacyProvider = filepath.Join(dir, "unused-legacy-bridge")
	attempts := 0
	var candidate string
	m.copyLegacy = func(plan legacyCredentialPlan, ctx context.Context, provider string, raw []byte) error {
		attempts++
		if plan.SourcePath != legacyPath || plan.Profile != "legacy.team" || plan.AppID != "cli_imported" ||
			plan.Destination != secretPath || provider != m.legacyProvider || string(raw) != candidate {
			t.Errorf("copy did not receive the reviewed plan: %+v", plan)
		}
		if attempts == 1 {
			return plan.copy(ctx, provider, raw)
		}
		return nil
	}
	press := func(key rune) tea.Cmd {
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
		return cmd
	}

	tuiType(m, "L")
	tuiType(m, legacyPath)
	tuiKeys(m, tea.KeyTab, tea.KeyTab)
	tuiType(m, statePath)
	tuiKeys(m, tea.KeyTab)
	tuiType(m, secretPath)
	tuiKeys(m, tea.KeyCtrlS)
	candidate = m.draft
	if m.screen != tuiReview || attempts != 0 || !strings.Contains(m.View(), "c 复制 App Secret") {
		t.Fatalf("preview did not offer explicit copy: %s", m.View())
	}
	if cmd := press('c'); cmd != nil || m.screen != tuiLegacyCopyConfirm || attempts != 0 {
		t.Fatal("opening confirmation started a copy")
	}
	for _, detail := range []string{"Profile \"legacy.team\"", "App cli_imported", secretPath, "y 确认", "Esc 取消"} {
		if !strings.Contains(m.View(), detail) {
			t.Fatalf("confirmation omitted %q: %s", detail, m.View())
		}
	}
	tuiKeys(m, tea.KeyEsc)
	if cmd := press('y'); cmd != nil || m.screen != tuiReview || attempts != 0 || m.draft != candidate {
		t.Fatal("canceled confirmation changed the draft or scheduled a copy")
	}

	press('c')
	m.draft += "\n"
	if cmd := press('y'); cmd != nil || m.screen != tuiReview || m.status != legacyTUICandidateChanged || attempts != 0 {
		t.Fatalf("changed draft accepted the previous confirmation: %s", m.View())
	}
	m.draft = candidate
	if err := os.WriteFile(legacyPath, append(legacy, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	press('c')
	cmd := press('y')
	if cmd == nil || m.screen != tuiLegacyCopyPending || attempts != 0 || press('y') != nil {
		t.Fatal("confirmation did not schedule exactly one asynchronous copy")
	}
	tuiType(m, "s")
	m.Update(cmd())
	if m.screen != tuiReview || attempts != 1 || !strings.Contains(m.status, "旧配置内容已变化") ||
		m.draft != candidate || !strings.Contains(m.View(), "c 复制 App Secret") {
		t.Fatalf("source drift did not return to review with a retry: %s", m.View())
	}

	press('L')
	if m.screen != tuiImportLegacy || m.input.Value() != secretPath || m.legacyImport.values[0] != legacyPath {
		t.Fatalf("reopening the import changed the edited form: %s", m.View())
	}
	tuiKeys(m, tea.KeyCtrlS)
	if m.screen != tuiReview || m.draft != candidate {
		t.Fatalf("could not preview the same import again: %s", m.View())
	}
	press('c')
	cmd = press('y')
	if cmd == nil {
		t.Fatal("copy retry did not start")
	}
	m.Update(cmd())
	if m.screen != tuiReview || attempts != 2 || m.status != "App Secret 已复制到私有文件" ||
		m.draft != candidate || string(m.reviewed) != candidate || m.source.Revision != source.Revision {
		t.Fatalf("copy success changed the reviewed configuration: %s", m.View())
	}
	if cfg, err := bridge.ParseConfig(m.reviewed); err != nil || cfg.AppSecretFile != secretPath || cfg.ReceiverAuthorized {
		t.Fatalf("copy success changed the candidate account: %v", err)
	}
	if raw, err := os.ReadFile(targetPath); err != nil || !bytes.Equal(raw, original) {
		t.Fatalf("copy saved the configuration: %v", err)
	}
}

func TestConfigTUILegacyInlineCopyWithoutProvider(t *testing.T) {
	const secret = "synthetic-tui-inline-secret"
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)
	t.Setenv("LARK_CHANNEL_HOME", dir)
	targetPath, legacyPath := filepath.Join(dir, "target.json"), filepath.Join(dir, "legacy.json")
	statePath, secretDir := filepath.Join(dir, "state"), filepath.Join(dir, "private")
	secretPath := filepath.Join(secretDir, "app.secret")
	original := bytes.ReplaceAll(tuiTestSource(t).JSON, []byte("REPLACE_OPEN_ID"), []byte("ou_route"))
	legacy := []byte(`{
		"schemaVersion": 1,
		"app": {"id": "cli_single", "tenant": "feishu", "secret": "` + secret + `"},
		"preferences": {"access": {"allowedUsers": ["ou_imported"]}}
	}`)
	for path, raw := range map[string][]byte{targetPath: original, legacyPath: legacy} {
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	source, err := configFileSource(targetPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := newConfigTUI(source)
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 12})
	assertPreviewOnly := func() {
		t.Helper()
		for _, path := range []string{secretDir, statePath} {
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("preview touched %s: %v", path, err)
			}
		}
		if raw, err := os.ReadFile(targetPath); err != nil || !bytes.Equal(raw, original) {
			t.Fatalf("preview wrote the configuration: %v", err)
		}
	}

	tuiType(m, "L")
	tuiType(m, legacyPath)
	tuiKeys(m, tea.KeyTab)
	if !strings.Contains(m.View(), "单账号配置留空") {
		t.Fatal("form omitted guidance for single-account imports")
	}
	tuiKeys(m, tea.KeyTab)
	tuiType(m, statePath)
	tuiKeys(m, tea.KeyTab)
	tuiType(m, secretPath)
	tuiKeys(m, tea.KeyCtrlS)
	view := m.View()
	candidate := m.draft
	if m.screen != tuiReview || !m.legacyImport.plan.Inline || m.legacyImport.plan.Profile != "" ||
		!strings.Contains(view, "c 复制 App Secret") {
		t.Fatal("single-account review did not offer copying")
	}
	if strings.Contains(view, secret) || strings.Contains(candidate, secret) || bytes.Contains(m.reviewed, []byte(secret)) {
		t.Fatal("review exposed the inline secret")
	}
	cfg, err := bridge.ParseConfig(m.reviewed)
	if err != nil || cfg.AppID != "cli_single" || cfg.AppSecretFile != secretPath || cfg.ReceiverAuthorized {
		t.Fatal("single-account candidate has an unexpected account")
	}
	assertPreviewOnly()

	tuiType(m, "c")
	view = m.View()
	if m.screen != tuiLegacyCopyConfirm || !strings.Contains(view, "单账号配置") ||
		!strings.Contains(view, "App cli_single") || !strings.Contains(view, secretPath) ||
		strings.Contains(view, secret) || strings.Contains(view, `Profile ""`) {
		t.Fatal("confirmation did not safely identify the single account and destination")
	}
	assertPreviewOnly()
	tuiKeys(m, tea.KeyEsc)
	if m.screen != tuiReview || m.draft != candidate {
		t.Fatal("cancel did not return to the reviewed candidate")
	}
	assertPreviewOnly()

	tuiType(m, "c")
	_, copyCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if copyCmd == nil || m.screen != tuiLegacyCopyPending {
		t.Fatal("confirm did not schedule a copy")
	}
	m.Update(copyCmd())
	if m.screen != tuiReview || m.status != "App Secret 已复制到私有文件" ||
		m.draft != candidate || string(m.reviewed) != candidate || strings.Contains(m.View(), secret) {
		t.Fatal("copy did not return to the reviewed candidate")
	}
	if value, err := bridge.Secret(secretPath); err != nil || value != secret {
		t.Fatal("copy did not write the expected private credential")
	}
	for path, want := range map[string][]byte{targetPath: original, legacyPath: legacy} {
		if raw, err := os.ReadFile(path); err != nil || !bytes.Equal(raw, want) {
			t.Fatalf("copy modified %s: %v", path, err)
		}
	}
	tuiType(m, "s")
	if raw, err := os.ReadFile(targetPath); err != nil || string(raw) != candidate || m.screen != tuiBrowse {
		t.Fatalf("explicit save did not write the reviewed candidate: %v", err)
	}
}
