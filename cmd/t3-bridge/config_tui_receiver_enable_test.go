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
)

func TestConfigTUIReceiverEnableAndSave(t *testing.T) {
	source := tuiReceiverEnableSource(t)
	candidate, err := bridge.EditConfigJSON(source.JSON, []byte(`[{"path":["additionalAccounts","0","receiverAuthorized"],"value":true}]`))
	if err != nil {
		t.Fatal(err)
	}
	m := newConfigTUI(source)
	calls := 0
	m.prepareReceiver = func(_ context.Context, path, revision, appID string) (receiverEnableDraft, error) {
		calls++
		if path != source.Path || revision != source.Revision || appID != "cli_additional" {
			t.Fatalf("wrong preparation scope: %q %q %q", path, revision, appID)
		}
		return receiverEnableDraft{Candidate: candidate, Report: bridge.ReceiverPreflight{
			Passed: true, AllAccounts: &bridge.ReceiverPreflightOutcome{Passed: true},
		}}, nil
	}
	tuiSelect(t, m, `路由 · "work"`)
	tuiKeys(m, tea.KeyEnter)
	if tuiReceiverEnableKey(m) != nil || strings.Contains(m.View(), "E 准备启用接收") {
		t.Fatal("receiver enable is available inside a route")
	}
	tuiKeys(m, tea.KeyEsc, tea.KeyTab)
	if !strings.Contains(m.View(), "E 准备启用接收") {
		t.Fatal("saved account overview is missing the receiver enable shortcut")
	}
	cmd := tuiReceiverEnableKey(m)
	if cmd == nil || calls != 0 || m.screen != tuiReceiverEnableLoading || !strings.Contains(m.View(), "cli_additional") {
		t.Fatalf("E did not schedule the selected app asynchronously: %s", m.View())
	}
	tuiKeys(m, tea.KeyEsc, tea.KeyTab)
	if tuiReceiverEnableKey(m) != nil || m.screen != tuiReceiverEnableLoading || m.account != 1 {
		t.Fatal("the pending check was left, repeated or switched accounts")
	}
	m.Update(cmd())
	if calls != 1 || m.screen != tuiReview || m.account != 1 || m.draft != string(candidate) ||
		!strings.Contains(m.View(), "候选配置差异") || !strings.Contains(m.View(), "receiverAuthorized") {
		t.Fatalf("preflight did not open the normal diff review: %s", m.View())
	}
	tuiKeys(m, tea.KeyEnter, tea.KeyCtrlS)
	if raw, err := os.ReadFile(source.Path); err != nil || !bytes.Equal(raw, source.JSON) {
		t.Fatalf("the source changed before explicit save: %v", err)
	}
	tuiType(m, "s")
	if raw, err := os.ReadFile(source.Path); err != nil || !bytes.Equal(raw, candidate) ||
		m.screen != tuiBrowse || m.account != 1 || m.source.Revision != configRevision(candidate) {
		t.Fatalf("s did not save the reviewed additional-account candidate: %v\n%s", err, m.View())
	}
}

func TestConfigTUIReceiverEnableFailureKeepsSelectionAndDraft(t *testing.T) {
	blocked := bridge.ReceiverPreflight{
		Passed: true, AllAccounts: &bridge.ReceiverPreflightOutcome{Passed: false},
		Accounts: []bridge.ReceiverPreflightAccount{{Tenant: "feishu", AppID: "cli_primary", Checks: []bridge.ReceiverPreflightCheck{{
			Check: "legacy-receiver-lock", Status: "failed", Detail: "旧桥仍在接收",
			LockHolder: &bridge.ReceiverLockHolder{Profile: "Work.Codex", PID: 1234},
		}}}},
	}
	for _, test := range []struct {
		name   string
		report bridge.ReceiverPreflight
		err    error
		want   string
	}{
		{name: "blocker", report: blocked, want: "所选应用未通过接收预检"},
		{name: "stale", err: errWorkspaceServiceConfigChanged, want: "请重新打开配置后再预检"},
		{name: "already enabled", err: errors.New("所选应用已启用接收"), want: "所选应用已启用接收"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := tuiReceiverEnableSource(t)
			m := newConfigTUI(source)
			m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
			tuiKeys(m, tea.KeyDown)
			row := m.row
			disk := source.JSON
			m.prepareReceiver = func(_ context.Context, path, revision, appID string) (receiverEnableDraft, error) {
				if path != source.Path || revision != source.Revision || appID != "cli_primary" {
					t.Fatalf("wrong preparation scope: %q %q %q", path, revision, appID)
				}
				if errors.Is(test.err, errWorkspaceServiceConfigChanged) {
					disk = append(bytes.Clone(source.JSON), '\n')
					if err := os.WriteFile(path, disk, 0600); err != nil {
						t.Fatal(err)
					}
				}
				return receiverEnableDraft{Report: test.report}, test.err
			}
			cmd := tuiReceiverEnableKey(m)
			if cmd == nil {
				t.Fatal("E did not schedule a preflight")
			}
			m.Update(cmd())
			if m.screen != tuiServicePreflightResult || !strings.Contains(m.View(), test.want) || m.reviewed != nil {
				t.Fatalf("failed preflight staged a candidate or omitted the result: %s", m.View())
			}
			if test.name == "blocker" {
				tuiKeys(m, tea.KeyPgDown)
				if view := m.View(); !strings.Contains(view, "本机旧桥接收锁 · 失败 · 旧桥仍在接收") || !strings.Contains(view, "Work.Codex · PID 1234") {
					t.Fatalf("scrolling did not show the existing check details: %s", view)
				}
			}
			tuiType(m, "s")
			tuiKeys(m, tea.KeyEsc)
			if m.screen != tuiBrowse || m.account != 0 || m.row != row || m.draft != string(source.JSON) ||
				m.source.Path != source.Path || m.source.Revision != source.Revision || !bytes.Equal(m.source.JSON, source.JSON) {
				t.Fatalf("failed preflight changed the workspace state: %s", m.View())
			}
			if raw, err := os.ReadFile(source.Path); err != nil || !bytes.Equal(raw, disk) {
				t.Fatalf("failed preflight overwrote the source: %v", err)
			}
		})
	}
}

func TestConfigTUIReceiverEnableRequiresSavedDraft(t *testing.T) {
	for _, source := range []webSource{tuiTestSource(t), tuiReceiverEnableSource(t)} {
		m := newConfigTUI(source)
		m.draft += "\n"
		draft := m.draft
		if cmd := tuiReceiverEnableKey(m); cmd != nil || m.screen != tuiBrowse || m.draft != draft || !strings.Contains(m.status, "保存") {
			t.Fatalf("E accepted or discarded an unsaved draft: %s", m.View())
		}
	}
}

func tuiReceiverEnableSource(t *testing.T) webSource {
	t.Helper()
	config, err := bridge.ParseConfig(tuiTestSource(t).JSON)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	config.AppID, config.StateDir = "cli_primary", filepath.Join(root, "primary")
	additional := config
	additional.AppID, additional.StateDir = "cli_additional", filepath.Join(root, "additional")
	config.AdditionalAccounts = []bridge.Config{additional}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	original, err := bridge.EditConfigJSON(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "receiver.json")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := configFileSource(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func tuiReceiverEnableKey(m *configTUI) tea.Cmd {
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'E'}})
	return cmd
}
