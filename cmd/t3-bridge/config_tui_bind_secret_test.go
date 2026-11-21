package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
)

func TestConfigTUIManualReplacementTypedSecret(t *testing.T) {
	const appID, user, secret = "cli_typedfixture", "ou_typedfixture", "synthetic-stage178-app-secret"
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	profiles, err := localProfiles()
	if err != nil {
		t.Fatal(err)
	}
	secretPath := filepath.Join(profiles.root, "credentials", appID+".secret")
	path := filepath.Join(dir, "fixture", "config.json")
	original := existingBindConfigFixture(t, dir)
	writeReplaceAppFixtureFile(t, path, original)
	source, err := configFileSource(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := newConfigTUI(source)
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 64})
	tuiKeys(m, tea.KeyTab, tea.KeyF7)
	openForm := func() {
		tuiType(m, "m")
		tuiType(m, appID)
		tuiKeys(m, tea.KeyEnter)
		tuiType(m, user)
		tuiKeys(m, tea.KeyTab, tea.KeyF2)
		if m.screen != tuiBindManual || !strings.Contains(m.View(), "Ctrl+S 写入新的私有 App Secret 文件并审阅差异") {
			t.Fatal("manual replacement did not offer the explicit secret write action")
		}
	}
	paste := func(value string) {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value), Paste: true})
	}
	assertNoWrite := func() {
		t.Helper()
		if _, err := os.Lstat(secretPath); !errors.Is(err, os.ErrNotExist) || m.draft != string(source.JSON) {
			t.Fatal("the form wrote a credential or changed the draft before submission", err)
		}
	}

	openForm()
	paste(secret + "\nextra")
	if m.binding.manual.secretInput.Value() != "" || !strings.Contains(m.status, "凭据输入须为单行文本") {
		t.Fatal("the masked input accepted a multiline paste")
	}
	paste(secret)
	if view := m.View(); !strings.Contains(view, strings.Repeat("*", len(secret))) || strings.Contains(view, secret) {
		t.Fatal("the credential row did not mask the pasted secret")
	}
	tuiKeys(m, tea.KeyDown, tea.KeyRight)
	if view := m.View(); !strings.Contains(view, "已输入") || !strings.Contains(view, "← lark →") || strings.Contains(view, secret) ||
		m.binding.manual.fields.AppSecretFile != "" {
		t.Fatal("moving to the tenant row exposed or copied the secret")
	}
	tuiKeys(m, tea.KeyUp)
	if m.binding.manual.secretInput.Value() != secret || strings.Contains(m.View(), secret) {
		t.Fatal("returning to the credential row lost or revealed the input")
	}
	tuiKeys(m, tea.KeyEsc)
	if m.screen != tuiBindReview || m.binding.manual.secretInput.Value() != "" || strings.Contains(m.View(), secret) {
		t.Fatal("canceling retained the secret")
	}
	assertNoWrite()

	openForm()
	paste(secret)
	tuiKeys(m, tea.KeyDown, tea.KeyRight)
	if err := os.WriteFile(path, append(bytes.Clone(original), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	tuiKeys(m, tea.KeyCtrlS)
	if m.screen != tuiBindManual || !strings.Contains(m.status, "所选配置已变化") || strings.Contains(m.View(), secret) {
		t.Fatal("a changed source did not block the credential write")
	}
	assertNoWrite()
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	tuiKeys(m, tea.KeyCtrlS)
	if view := m.View(); m.screen != tuiReview || m.reviewed == nil || m.binding.manual.secretInput.Value() != "" ||
		!strings.Contains(view, "候选配置差异") || !strings.Contains(view, "@@ -") || !strings.Contains(view, appID) ||
		strings.Contains(view, secret) || strings.Contains(m.draft, secret) {
		t.Fatal("submission did not open the regular diff with the secret cleared")
	}
	candidate, err := bridge.ParseConfig(m.reviewed)
	if err != nil {
		t.Fatal(err)
	}
	base, err := bridge.ParseConfig(source.JSON)
	if err != nil {
		t.Fatal(err)
	}
	want, err := replaceAccountDraft(source.JSON, base, "cli_otherfixture", bridge.Config{
		AppID: appID, AppSecretFile: secretPath, Tenant: "lark",
		StateDir: candidate.AdditionalAccounts[0].StateDir, Users: []string{user},
	})
	if err != nil || !bytes.Equal(m.reviewed, want) || m.draft != string(want) {
		t.Fatal("the diff candidate did not match the requested account replacement", err)
	}
	if value, err := readAppCredential(secretPath); err != nil || string(value) != secret+"\n" {
		t.Fatal("submission did not store the exact secret in a private file", err)
	}
	if saved, err := os.ReadFile(path); err != nil || !bytes.Equal(saved, original) {
		t.Fatal("preview modified the saved configuration", err)
	}
}
