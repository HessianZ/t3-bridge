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

func TestConfigTUIUseLocalT3(t *testing.T) {
	home := t.TempDir()
	t.Setenv("T3CODE_HOME", home)
	dir := t.TempDir()
	primary, err := bridge.ParseConfig(tuiTestSource(t).JSON)
	if err != nil {
		t.Fatal(err)
	}
	primary.StateDir = filepath.Join(dir, "primary-state")
	extra := primary
	extra.AppID, extra.StateDir = "cli_extra", filepath.Join(dir, "extra-state")
	primary.AdditionalAccounts = []bridge.Config{extra}
	original, err := json.Marshal(primary)
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
	m := newConfigTUI(source)
	tuiSetField(t, m, "账号 · receiverMachine", "primary-draft")
	tuiKeys(m, tea.KeyTab)
	tuiSetField(t, m, "账号 · receiverMachine", "extra-draft")
	if !strings.Contains(m.View(), "T 使用本机 T3") {
		t.Fatalf("account shortcut is missing: %s", m.View())
	}
	before := m.draft
	tuiType(m, "T")
	if m.screen != tuiBrowse || m.account != 1 || m.draft != before || !strings.Contains(m.View(), "无法读取本机 T3 运行文件") {
		t.Fatalf("local discovery error should keep the draft and show an error: %s", m.View())
	}

	const environmentID = "8fe98f01-817f-4d4f-9ea7-c0b9bb30d0e9"
	userdata := filepath.Join(home, "userdata")
	if err := os.Mkdir(userdata, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"server-runtime.json": `{"port":3774}`,
		"environment-id":      environmentID + "\n",
	} {
		if err := os.WriteFile(filepath.Join(userdata, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	tuiKeys(m, tea.KeyHome, tea.KeyEnter)
	tuiType(m, "T")
	if m.screen != tuiBrowse || m.route != "work" || m.draft != before || strings.Contains(m.View(), "T 使用本机 T3") {
		t.Fatalf("the account shortcut should be confined to the account overview: %s", m.View())
	}
	tuiKeys(m, tea.KeyEsc)
	tuiType(m, "T")
	if m.screen != tuiReview || m.reviewed == nil || m.account != 1 {
		t.Fatalf("expected a diff review for the selected account: %s", m.View())
	}
	if raw, err := os.ReadFile(path); err != nil || !bytes.Equal(raw, original) {
		t.Fatal("preview wrote the config", err)
	}
	tuiKeys(m, tea.KeyEsc)
	if m.screen != tuiBrowse || m.account != 1 || m.route != "" {
		t.Fatalf("Esc did not return to the selected account: %s", m.View())
	}
	draft, err := bridge.ParseConfig([]byte(m.draft))
	if err != nil {
		t.Fatal(err)
	}
	selected := draft.AdditionalAccounts[0]
	if draft.MCP != primary.MCP || draft.ReceiverMachine != "primary-draft" || selected.ReceiverMachine != "extra-draft" ||
		selected.MCP.EnvironmentID != environmentID || selected.MCP.RuntimeFile != filepath.Join(userdata, "server-runtime.json") {
		t.Fatalf("unexpected account drafts: %s", m.draft)
	}
	tuiType(m, "p")
	tuiType(m, "s")
	if raw, err := os.ReadFile(path); err != nil || !bytes.Equal(raw, []byte(m.draft)) || m.screen != tuiBrowse {
		t.Fatalf("explicit save did not persist the draft: %s, %v", m.status, err)
	}
}
