package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestConfigTUIReceiverReadinessKeyboard(t *testing.T) {
	m := newConfigTUI(tuiTestSource(t))
	shortcut := tea.KeyMsg{Type: tea.KeyCtrlP}
	if _, cmd := m.Update(shortcut); cmd != nil || m.screen != tuiBrowse || !strings.Contains(m.status, "先保存配置") {
		t.Fatal("readiness should require a saved source")
	}
	path := filepath.Join(t.TempDir(), "saved.json")
	revision := configRevision(m.source.JSON)
	m.source.Path, m.source.Revision = path, revision
	m.profiles.registry.root = filepath.Dir(path)
	tuiSetField(t, m, "账号 · appId", "cli_draft")
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 16})
	if view := m.View(); !strings.Contains(view, "Ctrl+P 全部 App 就绪") || !strings.Contains(view, "Ctrl+D 接收状态") {
		t.Fatalf("receiver shortcuts missing from the narrow account view: %s", view)
	}
	tuiSelect(t, m, `路由 · "work"`)
	tuiKeys(m, tea.KeyEnter, tea.KeyDown)
	draft, account, route, row := m.draft, m.account, m.route, m.row
	calls := 0
	var lookupErr error
	m.readiness.lookup = func(profiles profileRegistry, gotPath, gotRevision string) (receiverReadinessReport, error) {
		calls++
		if profiles.root != filepath.Dir(path) || gotPath != path || gotRevision != revision {
			t.Fatalf("wrong source in readiness lookup: %q %q %q", profiles.root, gotPath, gotRevision)
		}
		return receiverReadinessReport{Revision: revision, Ready: true, Accounts: []receiverReadinessAccount{
			{receiverProcessAccount: receiverProcessAccount{Tenant: "feishu", AppID: "cli_saved"}, State: "ready"},
			{receiverProcessAccount: receiverProcessAccount{Tenant: "lark", AppID: "cli_disabled"}, State: "disabled"},
		}}, lookupErr
	}
	_, first := m.Update(shortcut)
	refresh := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}}
	_, latest := m.Update(refresh)
	if first == nil || latest == nil || calls != 0 || m.screen != tuiReceiverReadinessLoading {
		t.Fatal("opening and refreshing should both schedule asynchronous lookups")
	}
	m.Update(first())
	if m.screen != tuiReceiverReadinessLoading {
		t.Fatal("superseded result changed the screen")
	}
	m.Update(latest())
	view := m.View()
	for _, want := range []string{"已启用 App 均已就绪", "feishu/cli_saved", "lark/cli_disabled", "未启用接收", "r 刷新", "Esc 返回", "↑↓←→ 滚动"} {
		if !strings.Contains(view, want) {
			t.Fatalf("narrow report lacks %q: %s", want, view)
		}
	}
	for _, line := range strings.Split(view, "\n") {
		if ansi.StringWidth(line) > 40 {
			t.Fatalf("report line exceeds terminal width: %q", line)
		}
	}

	_, changed := m.Update(refresh)
	m.source.Revision = "other-revision"
	m.Update(changed())
	if view := m.View(); !strings.Contains(view, "请重新打开配置") || strings.Contains(view, "cli_saved") {
		t.Fatalf("result for a stale source was displayed: %s", view)
	}
	m.source.Revision = revision
	lookupErr = errWorkspaceServiceConfigChanged
	_, stale := m.Update(refresh)
	m.Update(stale())
	if view := m.View(); !strings.Contains(view, "请重新打开配置") || strings.Contains(view, "cli_saved") {
		t.Fatalf("disk revision mismatch was not displayed: %s", view)
	}
	_, pending := m.Update(refresh)
	tuiKeys(m, tea.KeyEsc)
	m.Update(pending())
	if m.screen != tuiBrowse || m.draft != draft || m.account != account || m.route != route || m.row != row {
		t.Fatal("returning during refresh changed the draft or selection")
	}
}

func TestReceiverReadinessTextShowsRevisionAndWorkerStates(t *testing.T) {
	now := time.Date(2026, 11, 20, 9, 10, 11, 0, time.UTC)
	var accounts []receiverReadinessAccount
	for _, state := range []string{"disabled", "offline", "ambiguous", "revision-unknown", "different-revision", "waiting-sample", "retrying", "exhausted", "reconnect-wait", "connecting", "ready"} {
		row := receiverReadinessAccount{receiverProcessAccount: receiverProcessAccount{Tenant: "feishu", AppID: "cli_" + state}, State: state}
		switch state {
		case "different-revision":
			row.Process, row.PID, row.Version, row.ProcessRevision = "some-process", 123, "v1", "other-revision"
		case "retrying":
			row.Worker = &receiverWorkerSample{At: now, receiverWorkerStatus: receiverWorkerStatus{
				State: receiverWorkerRetrying, Attempts: 2, Failure: "initial-connect-failed", RetryAt: now.Add(time.Minute),
			}}
		}
		accounts = append(accounts, row)
	}
	content := receiverReadinessText(receiverReadinessReport{Revision: "saved-revision", Accounts: accounts})
	for _, row := range accounts {
		if want := receiverStatusApp(row.Tenant, row.AppID) + "\n" + receiverReadinessStatusText(row.State); !strings.Contains(content, want) {
			t.Fatalf("report lacks the status for %s: %s", row.State, content)
		}
	}
	for _, want := range []string{"保存版本 · saved-revision", "进程配置版本 · other-revision", "程序版本 · \"v1\"", "PID · 123", "已失败 2 次", "初次建连失败", now.Add(time.Minute).Local().Format(time.RFC3339)} {
		if !strings.Contains(content, want) {
			t.Fatalf("report lacks %q: %s", want, content)
		}
	}
}
