package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
)

func TestConfigTUILiveReconnectConfirmsSelectedApp(t *testing.T) {
	source := tuiTestSource(t)
	m := newConfigTUI(source)
	dir := t.TempDir()
	tuiType(m, "a")
	for _, value := range []string{"cli_reconnect", "ou_sample", filepath.Join(dir, "extra.secret"), filepath.Join(dir, "extra")} {
		tuiType(m, value)
		tuiKeys(m, tea.KeyTab)
	}
	tuiKeys(m, tea.KeyRight, tea.KeyCtrlS)
	draft := m.draft
	if m.account != 1 || !strings.Contains(m.View(), "Ctrl+R 重连接收端") {
		t.Fatalf("missing reconnect shortcut for the selected account: %s", m.View())
	}
	m.profiles.registry = profileRegistry{root: dir}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	if cmd == nil || m.screen != tuiLiveReconnectLoading {
		t.Fatal("Ctrl+R did not schedule target lookup")
	}
	tuiKeys(m, tea.KeyEsc)

	const id = "0123456789abcdef0123456789abcdef"
	account := receiverProcessAccount{Tenant: "lark", AppID: "cli_reconnect"}
	waitSupported := true
	list := func() ([]receiverProcess, error) {
		return []receiverProcess{
			{ID: "wrong-tenant", Reconnect: true, Accounts: []receiverProcessAccount{{Tenant: "feishu", AppID: account.AppID}}},
			{ID: id, Reconnect: true, ReconnectWait: waitSupported, Accounts: []receiverProcessAccount{{Tenant: "lark", AppID: "cli_other"}, account}},
		}, nil
	}
	var calls int
	var state string
	var opErr error
	var submitted bridge.ReconnectAction
	operate := func(ctx context.Context, process, tenant, appID string, action bridge.ReconnectAction) (bridge.LocalReconnectView, error) {
		if _, timed := ctx.Deadline(); !timed || process != id || tenant != account.Tenant || appID != account.AppID {
			t.Fatalf("wrong reconnect target %s %s/%s", process, tenant, appID)
		}
		calls++
		submitted = action
		return bridge.LocalReconnectView{State: state}, opErr
	}
	stale := m.openLiveReconnect(list, operate)
	tuiKeys(m, tea.KeyEsc)
	cmd = m.openLiveReconnect(list, operate)
	m.Update(stale())
	if m.screen != tuiLiveReconnectLoading {
		t.Fatal("cancelled lookup changed the current screen")
	}
	m.Update(cmd())
	normalSize := tea.WindowSizeMsg{Width: m.width, Height: m.height}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	if m.screen != tuiLiveReconnectConfirm || !strings.Contains(m.View(), "动作 · 立即重连") {
		t.Fatalf("confirmation omitted the default action: %s", m.View())
	}
	tuiKeys(m, tea.KeyUp)
	if !strings.Contains(m.View(), "动作 · 取消等待") {
		t.Fatalf("Up did not select cancel: %s", m.View())
	}
	tuiKeys(m, tea.KeyDown, tea.KeyDown)
	for _, want := range []string{"动作 · 等待 T3 收口后重连", "App · \"lark/cli_reconnect\"", "进程 · " + id, "↑↓ 选动作 · y 确认 · Esc 取消"} {
		if !strings.Contains(m.View(), want) {
			t.Fatalf("narrow confirmation lacks %q: %s", want, m.View())
		}
	}
	tuiKeys(m, tea.KeyEnter, tea.KeyEsc)
	if m.screen != tuiBrowse || calls != 0 {
		t.Fatal("Enter or Esc submitted a reconnect")
	}
	for i, tc := range []struct {
		state  string
		err    error
		key    tea.KeyType
		legacy bool
		narrow bool
		action bridge.ReconnectAction
		want   string
	}{
		{state: bridge.ReconnectStarted, want: "T3 MCP 已核验，飞书连接中。按 Ctrl+D 查看 WebSocket 状态。"},
		{state: bridge.ReconnectRecoveryRequested, narrow: true, want: "已受理恢复请求。Ctrl+D 核对连接"},
		{state: bridge.ReconnectWorkerOffline, key: tea.KeyDown, narrow: true, action: bridge.ReconnectAfterRuns, want: "App 离线，等待设置未变。Ctrl+R 立即重连"},
		{state: bridge.ReconnectWorkerOffline, key: tea.KeyUp, narrow: true, action: bridge.ReconnectCancelWait, want: "App 离线，等待设置未变。Ctrl+R 立即重连"},
		{state: bridge.ReconnectPending, want: "已有等待重连请求，继续等待 T3 收口。"},
		{state: bridge.ReconnectScheduled, key: tea.KeyDown, action: bridge.ReconnectAfterRuns, want: "已暂停新派发，等待 T3 收口后重连。"},
		{state: bridge.ReconnectCancelled, key: tea.KeyUp, action: bridge.ReconnectCancelWait, want: "已取消等待重连，恢复新派发。"},
		{state: bridge.ReconnectIdle, key: tea.KeyUp, action: bridge.ReconnectCancelWait, want: "当前没有等待重连请求。"},
		{state: bridge.ReconnectUnconfirmed, want: "按 Ctrl+D 核对接收状态，再决定是否重发"},
		{err: context.DeadlineExceeded, want: "按 Ctrl+D 核对接收状态，再决定是否重发"},
		{state: bridge.ReconnectStarted, legacy: true, key: tea.KeyDown, want: "T3 MCP 已核验，飞书连接中。按 Ctrl+D 查看 WebSocket 状态。"},
	} {
		state, opErr = tc.state, tc.err
		waitSupported = !tc.legacy
		if tc.narrow {
			m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
		} else {
			m.Update(normalSize)
		}
		m.Update(m.openLiveReconnect(list, operate)())
		if tc.key != 0 {
			tuiKeys(m, tc.key)
		}
		if tc.legacy && (!strings.Contains(m.View(), "动作 · 立即重连") || strings.Contains(m.View(), "选动作")) {
			t.Fatalf("legacy process exposed another action: %s", m.View())
		}
		yes := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}
		_, enter := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		_, cmd := m.Update(yes)
		_, duplicate := m.Update(yes)
		tuiKeys(m, tea.KeyEsc)
		if enter != nil || cmd == nil || duplicate != nil || calls != i || m.screen != tuiLiveReconnectApplying {
			t.Fatal("expected one asynchronous reconnect after y and no interruption by Esc")
		}
		m.Update(cmd())
		wantAction := tc.action
		if wantAction == "" {
			wantAction = bridge.ReconnectNow
		}
		if calls != i+1 || submitted != wantAction || m.screen != tuiBrowse || !strings.Contains(m.View(), tc.want) {
			t.Fatalf("unexpected reconnect result for action %q (want %q): %s", submitted, wantAction, m.View())
		}
		if m.account != 1 || m.draft != draft || m.source.ID != source.ID || string(m.source.JSON) != string(source.JSON) {
			t.Fatal("reconnect changed the source, draft, or selected account")
		}
	}

	m.Update(m.openLiveReconnect(func() ([]receiverProcess, error) {
		return []receiverProcess{
			{ID: id, Reconnect: true, ReconnectWait: true, Accounts: []receiverProcessAccount{account}},
			{ID: "older", Accounts: []receiverProcessAccount{account}},
		}, nil
	}, operate)())
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}); cmd != nil || m.screen != tuiBrowse || !strings.Contains(m.status, "匹配多个") {
		t.Fatalf("ambiguous process lookup reached confirmation or submitted: %s", m.View())
	}
}

func TestConfigTUILiveReconnectRejectsMissingAmbiguousAndOlderProcesses(t *testing.T) {
	account := receiverProcessAccount{Tenant: "feishu", AppID: "cli_reconnect"}
	older := receiverProcess{ID: "older", Accounts: []receiverProcessAccount{account}}
	current := receiverProcess{ID: "current", Accounts: []receiverProcessAccount{account}, Reconnect: true}
	for _, tc := range []struct {
		processes []receiverProcess
		want      string
	}{
		{want: "没有匹配"},
		{processes: []receiverProcess{older}, want: "版本不支持在线重连"},
		{processes: []receiverProcess{older, current}, want: "匹配多个"},
	} {
		process, err := liveReconnectProcess(func() ([]receiverProcess, error) { return tc.processes, nil }, account)
		if process.ID != "" || err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("expected %q, got %q and %v", tc.want, process.ID, err)
		}
	}
}
