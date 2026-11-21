package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
)

func TestConfigTUIManualReplacementBinding(t *testing.T) {
	dir := t.TempDir()
	source := tuiTestSource(t)
	source.Path, source.Revision = filepath.Join(dir, "config.json"), "missing"
	m := newConfigTUI(source)
	raw := existingBindConfigFixture(t, dir)
	m.setDraft(raw)
	m.Update(tea.WindowSizeMsg{Width: 72, Height: 8})
	tuiKeys(m, tea.KeyTab, tea.KeyF7)
	if !strings.Contains(m.View(), "m 手动替换") {
		t.Fatal("binding review did not offer manual replacement")
	}
	tuiType(m, "x")
	tuiType(m, "m")
	if m.screen != tuiBindManual || m.binding.client != nil ||
		!strings.Contains(m.View(), "cli_otherfixture") || !strings.Contains(m.View(), "新应用 ID") {
		t.Fatalf("manual form did not show the selected App and first field: %s", m.View())
	}
	tuiKeys(m, tea.KeyCtrlS)
	if m.screen != tuiBindManual || m.status == "" || m.draft != string(raw) {
		t.Fatalf("incomplete replacement changed the draft: %s", m.View())
	}
	tuiKeys(m, tea.KeyEsc)
	if m.screen != tuiBindReview || m.binding.replaceAppID != "cli_otherfixture" || m.account != 1 || m.draft != string(raw) {
		t.Fatalf("cancellation lost the draft or the selected App: %s", m.View())
	}
	if view := m.View(); strings.Count(view, "\n") >= m.height ||
		!strings.Contains(view, "cli_otherfixture") || !strings.Contains(view, "m 手动替换") {
		t.Fatalf("short binding review did not fit the selected App and manual shortcut: %s", view)
	}

	const appID, user, secret = "cli_manualfixture", "ou_manualfixture", "synthetic-manual-secret"
	path := filepath.Join(dir, "manual.secret")
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tuiType(m, "m")
	tuiType(m, appID)
	tuiKeys(m, tea.KeyEnter)
	tuiType(m, user)
	tuiKeys(m, tea.KeyTab)
	tuiType(m, path)
	tuiKeys(m, tea.KeyDown, tea.KeyRight)
	if !strings.Contains(m.View(), "← lark →") {
		t.Fatalf("tenant selection was hidden in the short viewport: %s", m.View())
	}
	tuiKeys(m, tea.KeyCtrlS)
	if m.screen != tuiReview || m.reviewed == nil || m.account != 1 {
		t.Fatalf("manual replacement did not open the regular review: %s", m.View())
	}
	config, err := bridge.ParseConfig([]byte(m.draft))
	if err != nil {
		t.Fatal(err)
	}
	account := config.AdditionalAccounts[0]
	if account.AppID != appID || account.Tenant != "lark" || account.AppSecretFile != path ||
		len(account.Users) != 1 || account.Users[0] != user || config.AppID != "cli_mainfixture" {
		t.Fatal("manual replacement used the wrong account or form values")
	}
	if strings.Contains(m.draft, secret) {
		t.Fatal("draft contains the credential contents")
	}
	if _, err := os.Stat(source.Path); !os.IsNotExist(err) {
		t.Fatalf("review should leave the configuration unsaved: %v", err)
	}
}
