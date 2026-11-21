package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestConfigTUIReceiverStatusKeyboard(t *testing.T) {
	m := newConfigTUI(tuiTestSource(t))
	tuiSelect(t, m, `路由 · "work"`)
	tuiKeys(m, tea.KeyEnter)
	shortcut := tea.KeyMsg{Type: tea.KeyCtrlD}
	if _, cmd := m.Update(shortcut); cmd != nil || m.route != "work" || strings.Contains(m.View(), "Ctrl+D") {
		t.Fatal("route page exposed receiver status")
	}
	tuiKeys(m, tea.KeyEsc)

	dir := t.TempDir()
	tuiType(m, "a")
	for _, value := range []string{"cli_status", "ou_sample", filepath.Join(dir, "extra.secret"), filepath.Join(dir, "extra")} {
		tuiType(m, value)
		tuiKeys(m, tea.KeyTab)
	}
	tuiKeys(m, tea.KeyRight, tea.KeyCtrlS)
	account := receiverProcessAccount{Tenant: "lark", AppID: "cli_status"}
	if m.account != 1 || !strings.Contains(m.View(), "Ctrl+D 接收状态") {
		t.Fatalf("missing receiver status shortcut for the selected App: %s", m.View())
	}
	draft, row := m.draft, m.row
	at := time.Date(2026, 11, 20, 4, 5, 6, 0, time.UTC)
	const processID = "0123456789abcdef0123456789abcdef"
	view := receiverStatusView{Process: processID, PID: 1234, Tenant: account.Tenant, AppID: account.AppID, Sample: &receiverDiagnosticSample{
		At: at,
		Data: bridge.ReceiverDiagnostics{
			MCPVerified: true, FeishuReady: true, ReconnectWaiting: true, Inbox: 5, InFlight: 3, Reconcile: 2, DeliveryPending: 1,
			Capacity: &bridge.ReceiverRunCapacity{Limit: 6, Occupied: 4, Waiting: 2},
		},
	}}
	var roots []string
	var lookupErr error
	m.receiverStatus.lookup = func(profiles profileRegistry, process string, selected receiverProcessAccount) (receiverStatusView, error) {
		if selected != account || process != "" {
			t.Fatalf("lookup used the wrong target: %+v, process %q", selected, process)
		}
		roots = append(roots, profiles.root)
		return view, lookupErr
	}
	m.profiles.registry.root = dir
	_, stale := m.Update(shortcut)
	if stale == nil || m.screen != tuiReceiverStatusLoading || len(roots) != 0 {
		t.Fatal("Ctrl+D must schedule an asynchronous lookup")
	}
	tuiKeys(m, tea.KeyEsc)
	m.profiles.registry.root = filepath.Join(dir, "next")
	_, current := m.Update(shortcut)
	if current == nil {
		t.Fatal("Ctrl+D did not reopen receiver status")
	}
	m.Update(stale())
	if m.screen != tuiReceiverStatusLoading || len(roots) != 1 || roots[0] != dir {
		t.Fatal("a cancelled result was accepted or did not use its registry snapshot")
	}
	m.Update(current())
	if m.screen != tuiReceiverStatusResult || len(roots) != 2 || roots[1] != m.profiles.registry.root {
		t.Fatal("the current lookup did not use the current registry snapshot")
	}
	for _, want := range []string{"lark/cli_status", processID, "PID · 1234", at.Local().Format(time.RFC3339), "T3 最近建连 · 核验通过",
		"飞书 WebSocket · 已就绪", "入站 5 · 在途 3 · 待核对 2 · 送达缺口 1", "可用并发额度 · 2 / 6", "已占用 4 · 等待额度 2"} {
		if !strings.Contains(m.View(), want) {
			t.Fatalf("receiver status lacks %q: %s", want, m.View())
		}
	}

	m.Update(tea.WindowSizeMsg{Width: 40, Height: 11})
	if got := m.View(); !strings.Contains(got, "重连 · 暂停新派发，等待重连") || strings.Contains(got, "可用并发额度") {
		t.Fatalf("receiver status did not show the reconnect wait in the smaller viewport: %s", got)
	}
	for range 5 {
		tuiKeys(m, tea.KeyDown)
	}
	if !strings.Contains(m.View(), "可用并发额度 · 2 / 6") {
		t.Fatalf("receiver status did not scroll to available capacity: %s", m.View())
	}
	refresh := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}}
	view.Sample = nil
	_, cmd := m.Update(refresh)
	if cmd == nil || m.screen != tuiReceiverStatusLoading || len(roots) != 2 {
		t.Fatal("r must schedule another asynchronous lookup")
	}
	m.Update(cmd())
	if got := m.View(); len(roots) != 3 || !strings.Contains(got, "lark/cli_status") || !strings.Contains(got, "状态未知") || strings.Contains(got, "核验通过") || strings.Contains(got, "等待重连") {
		t.Fatalf("refresh did not reset the viewport and show missing data as unknown: %s", got)
	}

	lookupErr = errors.New("private lookup detail")
	_, cmd = m.Update(refresh)
	if cmd == nil {
		t.Fatal("r did not schedule the failing lookup")
	}
	m.Update(cmd())
	if got := m.View(); !strings.Contains(got, "无法读取接收状态") || strings.Contains(got, lookupErr.Error()) {
		t.Fatalf("unexpected lookup error display: %s", got)
	}
	tuiKeys(m, tea.KeyEsc)
	if m.screen != tuiBrowse || m.account != 1 || m.row != row || m.draft != draft {
		t.Fatal("Esc changed the selected App, row, or draft")
	}
}

func TestConfigTUIReceiverWorkerStatusNarrow(t *testing.T) {
	at := time.Date(2026, 11, 20, 4, 5, 6, 0, time.UTC)
	retryAt := at.Add(30 * time.Second)
	for _, tc := range []struct {
		status receiverWorkerStatus
		want   []string
		absent []string
	}{
		{
			status: receiverWorkerStatus{State: receiverWorkerRetrying, Attempts: 2, Failure: "initial-connect-failed", RetryAt: retryAt},
			want:   []string{"重试 · 已失败 2 次", "故障类别 · 初次建连失败", "下次尝试 · " + retryAt.Local().Format(time.RFC3339)},
			absent: []string{"initial-connect-failed", "已耗尽", "Ctrl+R"},
		},
		{
			status: receiverWorkerStatus{State: receiverWorkerExhausted, Attempts: 4},
			want:   []string{"重试 · 已失败 4 次", "重试 · 已耗尽", "Esc 返回账号页，Ctrl+R 立即重连"},
			absent: []string{"故障类别", "下次尝试"},
		},
	} {
		t.Run(tc.status.State, func(t *testing.T) {
			m := newConfigTUI(tuiTestSource(t))
			m.screen = tuiReceiverStatusLoading
			m.Update(tea.WindowSizeMsg{Width: 40, Height: 11})
			m.Update(configTUIReceiverStatusResult{view: receiverStatusView{
				Tenant: "lark", AppID: "cli_status", Process: "0123456789abcdef0123456789abcdef", PID: 1234,
				Worker: &receiverWorkerSample{At: at, receiverWorkerStatus: tc.status},
			}})
			top := m.View()
			for range 3 {
				tuiKeys(m, tea.KeyDown)
			}
			bottom := m.View()
			for _, want := range append(tc.want, "状态未知 · 暂无可用诊断样本") {
				if !strings.Contains(bottom, want) {
					t.Fatalf("scrolled receiver status lacks %q: %s", want, bottom)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(top+bottom, absent) {
					t.Fatalf("receiver status unexpectedly shows %q: %s", absent, bottom)
				}
			}
			for _, line := range strings.Split(top+"\n"+bottom, "\n") {
				if ansi.StringWidth(line) > 40 {
					t.Fatalf("receiver status line exceeds terminal width: %q", line)
				}
			}
		})
	}
}
