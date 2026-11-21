package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/localfile"
	tea "github.com/charmbracelet/bubbletea"
)

func credentialTUIFixture(t *testing.T) (*configTUI, string, []byte) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	target := filepath.Join(dir, "credentials", "app.secret")
	raw := writeCredentialConfig(t, path, target, filepath.Join(dir, "mcp.token"))
	source, err := configFileSource(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := newConfigTUI(source)
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 26})
	return m, target, raw
}

func tuiCredentialKey(m *configTUI, key rune) tea.Cmd {
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
	return cmd
}

func runCredentialTUICommand(t *testing.T, m *configTUI, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil || m.screen != tuiCredentialsLoading {
		t.Fatal("expected asynchronous credentials operation")
	}
	m.Update(cmd())
	if m.screen != tuiCredentialsPick {
		t.Fatal("credentials operation did not return to the list")
	}
}

func TestConfigTUICredentialsConfirmCancelAndMask(t *testing.T) {
	m, target, original := credentialTUIFixture(t)
	tuiSetField(t, m, "账号 · appId", "cli_unsaved")
	draft, row := m.draft, m.row
	if !strings.Contains(m.View(), "C 凭据") {
		t.Fatal("credentials shortcut is missing")
	}
	previous := tuiCredentialKey(m, 'C')
	if previous == nil || m.screen != tuiCredentialsLoading {
		t.Fatal("credentials were not loaded asynchronously")
	}
	tuiKeys(m, tea.KeyEsc)
	cmd := tuiCredentialKey(m, 'C')
	m.Update(previous())
	runCredentialTUICommand(t, m, cmd)
	for _, label := range []string{"cli_fixture", "feishu", "missing", target} {
		if !strings.Contains(m.View(), label) {
			t.Fatalf("missing saved credential metadata %q", label)
		}
	}
	if strings.Contains(m.View(), "cli_unsaved") {
		t.Fatal("credentials list used the draft")
	}

	const secret = "synthetic-credential-from-keyboard"
	tuiKeys(m, tea.KeyEnter)
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(secret), Paste: true})
	if m.credentials.input.Value() != secret || strings.Contains(m.View(), secret) {
		t.Fatal("secret was lost or displayed while typing")
	}
	tuiKeys(m, tea.KeyEsc, tea.KeyEnter)
	if m.credentials.input.Value() != "" {
		t.Fatal("canceling left a secret in the input")
	}
	tuiType(m, secret)
	tuiKeys(m, tea.KeyEnter)
	if m.screen != tuiCredentialSetConfirm || !strings.Contains(m.View(), target) || strings.Contains(m.View(), secret) {
		t.Fatal("set confirmation did not show only the target metadata")
	}
	tuiKeys(m, tea.KeyEnter)
	_, pasted := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}, Paste: true})
	if pasted != nil || m.screen != tuiCredentialSetConfirm {
		t.Fatal("pasted confirmation was accepted")
	}
	tuiKeys(m, tea.KeyEsc, tea.KeyEnter)
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) || m.credentials.input.Value() != "" {
		t.Fatal("canceling the review stored a credential")
	}

	tuiType(m, secret)
	tuiKeys(m, tea.KeyEnter)
	cmd = tuiCredentialKey(m, 'y')
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("confirmation wrote the credential before its command ran")
	}
	if m.credentials.input.Value() != "" || strings.Contains(m.View(), secret) {
		t.Fatal("the running operation retained a visible or editable secret")
	}
	runCredentialTUICommand(t, m, cmd)
	raw, err := os.ReadFile(target)
	if err != nil || string(raw) != secret+"\n" || !strings.Contains(m.View(), "ready") || strings.Contains(m.View(), secret) {
		t.Fatal("setting the credential did not refresh safe status", err)
	}

	tuiCredentialKey(m, 'd')
	if m.screen != tuiCredentialRemoveConfirm || !strings.Contains(m.View(), "确认删除") || !strings.Contains(m.View(), target) {
		t.Fatal("removal did not require a separate target confirmation")
	}
	tuiKeys(m, tea.KeyEnter, tea.KeyEsc)
	if value, err := os.ReadFile(target); err != nil || !bytes.Equal(raw, value) {
		t.Fatal("canceling removal changed the credential", err)
	}
	tuiCredentialKey(m, 'd')
	runCredentialTUICommand(t, m, tuiCredentialKey(m, 'y'))
	if !strings.Contains(m.View(), "missing") || !strings.Contains(m.View(), "App Secret 已删除") {
		t.Fatal("removal did not refresh missing status")
	}
	tuiKeys(m, tea.KeyEsc)
	if raw, err := os.ReadFile(m.source.Path); err != nil || !bytes.Equal(original, raw) {
		t.Fatal("credentials changed the saved configuration", err)
	}
	if m.screen != tuiBrowse || m.draft != draft || m.row != row {
		t.Fatal("credentials changed the draft or browse position")
	}
}

func TestConfigTUICredentialsFileImportFailureRefresh(t *testing.T) {
	m, target, _ := credentialTUIFixture(t)
	runCredentialTUICommand(t, m, tuiCredentialKey(m, 'C'))
	privateSource := filepath.Join(filepath.Dir(m.source.Path), "source.secret")
	tuiCredentialKey(m, 'p')
	tuiType(m, privateSource)
	tuiKeys(m, tea.KeyEnter)
	if m.screen != tuiCredentialSetConfirm || !strings.Contains(m.View(), "确认从私有文件导入") {
		t.Fatal("file import did not require confirmation")
	}
	item := m.credentials.items[0]
	if err := item.update("set", "synthetic-credential-from-another-update"); err != nil {
		t.Fatal(err)
	}
	runCredentialTUICommand(t, m, tuiCredentialKey(m, 'y'))
	if !strings.Contains(m.View(), "无法读取来源凭据文件") || !strings.Contains(m.View(), "ready") {
		t.Fatal("failed import did not report the error and read back fresh status")
	}

	const secret = "synthetic-credential-from-file"
	if _, err := localfile.WritePrivateAtomic(privateSource, []byte(secret+"\n"), true); err != nil {
		t.Fatal(err)
	}
	tuiCredentialKey(m, 'p')
	tuiType(m, privateSource)
	tuiKeys(m, tea.KeyEnter)
	runCredentialTUICommand(t, m, tuiCredentialKey(m, 'y'))
	value, err := os.ReadFile(target)
	if err != nil || string(value) != secret+"\n" || !strings.Contains(m.View(), "App Secret 已写入") ||
		strings.Contains(m.View(), secret) {
		t.Fatal("private file import did not update the credential safely", err)
	}
}
