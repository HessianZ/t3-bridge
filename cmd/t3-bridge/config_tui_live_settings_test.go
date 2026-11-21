package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
)

func TestConfigTUILiveSettingsConfirmAndPreserveDraft(t *testing.T) {
	m := newConfigTUI(tuiTestSource(t))
	dir := t.TempDir()
	tuiType(m, "a")
	for _, value := range []string{"cli_settings", "ou_sample", filepath.Join(dir, "extra.secret"), filepath.Join(dir, "extra")} {
		tuiType(m, value)
		tuiKeys(m, tea.KeyTab)
	}
	tuiKeys(m, tea.KeyRight, tea.KeyCtrlS)
	before := m.draft
	if m.account != 1 || !strings.Contains(m.View(), "F11 在线运行设置") {
		t.Fatalf("missing F11 on the additional account: %s", m.View())
	}
	m.profiles.registry = profileRegistry{root: t.TempDir()}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyF11})
	if cmd == nil || m.screen != tuiLiveSettingsLoading {
		t.Fatal("F11 did not schedule a lookup")
	}
	tuiKeys(m, tea.KeyEsc)
	account := receiverProcessAccount{Tenant: "lark", AppID: "cli_settings"}
	const id = "0123456789abcdef0123456789abcdef"
	processes := []receiverProcess{
		{ID: "unsupported", Accounts: []receiverProcessAccount{account}},
		{ID: "wrong-tenant", RunSettings: true, Accounts: []receiverProcessAccount{{Tenant: "feishu", AppID: account.AppID}}},
		{ID: id, RunSettings: true, Accounts: []receiverProcessAccount{{Tenant: "feishu", AppID: "cli_other"}, account}},
	}
	var requests []bridge.RunSettingsPatch
	view := bridge.RunSettingsView{MaxConcurrent: 4, Progress: true, ReplyMode: "card", ToolActivity: "default", ProcessMode: "off", IdleTimeoutMinutes: 12}
	cmd = m.openLiveSettings(func() ([]receiverProcess, error) { return processes, nil }, func(ctx context.Context, process, tenant, appID string, patch bridge.RunSettingsPatch) (bridge.RunSettingsView, error) {
		if _, timed := ctx.Deadline(); !timed || process != id || tenant != account.Tenant || appID != account.AppID {
			t.Fatalf("wrong target %s %s/%s", process, tenant, appID)
		}
		requests = append(requests, patch)
		if patch.Progress != nil {
			view.Progress = *patch.Progress
			view.IdleTimeoutMinutes = 15
		}
		if patch.ReplyMode != nil {
			view.ReplyMode = *patch.ReplyMode
		}
		if patch.ProcessMode != nil {
			view.ProcessMode = *patch.ProcessMode
		}
		return view, nil
	})
	m.Update(configTUILiveSettingsResult{request: m.liveSettings.request - 1})
	if m.screen != tuiLiveSettingsLoading || len(requests) != 0 {
		t.Fatal("query did not wait for its own command")
	}
	m.Update(cmd())
	if len(requests) != 1 || requests[0] != (bridge.RunSettingsPatch{}) || m.screen != tuiLiveSettingsPick ||
		!strings.Contains(m.View(), "并发预算 · 4") || !strings.Contains(m.View(), "影响后续派发") {
		t.Fatalf("unexpected initial settings: %s, %+v", m.View(), requests)
	}
	tuiKeys(m, tea.KeyEnter)
	m.input.SetValue("51")
	tuiKeys(m, tea.KeyEnter)
	if m.screen != tuiLiveSettingsEdit || m.status == "" {
		t.Fatal("invalid budget reached the confirmation screen")
	}
	m.input.SetValue("50")
	tuiKeys(m, tea.KeyEnter)
	if m.screen != tuiLiveSettingsConfirm || !strings.Contains(m.View(), "并发预算 · 4 → 50") {
		t.Fatal("the maximum budget did not reach the confirmation screen")
	}
	tuiKeys(m, tea.KeyEsc, tea.KeyEnter)
	m.input.SetValue("04")
	tuiKeys(m, tea.KeyEnter)
	if m.screen != tuiLiveSettingsPick || len(requests) != 1 {
		t.Fatal("the same numeric value produced an update")
	}
	tuiKeys(m, tea.KeyDown, tea.KeyEnter, tea.KeyDown, tea.KeyEnter)
	for _, value := range []string{id, "lark/cli_settings", "卡片进度 · 显示 → 隐藏", "包括在途轮次"} {
		if !strings.Contains(m.View(), value) {
			t.Fatalf("confirmation omitted %q: %s", value, m.View())
		}
	}
	tuiKeys(m, tea.KeyEnter, tea.KeyEsc)
	if m.screen != tuiLiveSettingsPick || len(requests) != 1 {
		t.Fatal("navigation or cancellation submitted a write")
	}
	tuiKeys(m, tea.KeyEnter, tea.KeyDown, tea.KeyEnter)
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil || len(requests) != 1 || m.screen != tuiLiveSettingsLoading {
		t.Fatal("confirmation did not schedule an asynchronous update")
	}
	m.Update(cmd())
	if len(requests) != 2 || requests[1].Progress == nil || *requests[1].Progress {
		t.Fatalf("explicit false did not reach the receiver: %+v", requests)
	}
	patch := requests[1]
	patch.Progress = nil
	if patch != (bridge.RunSettingsPatch{}) || !strings.Contains(m.View(), "默认空闲超时（分钟） · 15") {
		t.Fatalf("update included an unrelated field or missed the effective readback: %+v, %s", patch, m.View())
	}
	tuiKeys(m, tea.KeyDown, tea.KeyEnter, tea.KeyDown, tea.KeyDown, tea.KeyEnter)
	if !strings.Contains(m.View(), "回复模式 · 卡片 → 终态 Markdown") {
		t.Fatalf("Markdown setting absent from confirmation: %s", m.View())
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Fatal("Markdown confirmation did not schedule an update")
	}
	m.Update(cmd())
	markdown := "markdown"
	if len(requests) != 3 || requests[2].ReplyMode == nil || *requests[2].ReplyMode != markdown ||
		!strings.Contains(m.View(), "回复模式 · 终态 Markdown") {
		t.Fatalf("Markdown update did not reach the receiver: %+v, %s", requests, m.View())
	}
	tuiKeys(m, tea.KeyDown, tea.KeyDown, tea.KeyEnter)
	for _, value := range []string{"过程显示", "本卡正文", "独立正文预览", "本卡正文仅用于卡片回复。"} {
		if !strings.Contains(m.View(), value) {
			t.Fatalf("process display selector omitted %q: %s", value, m.View())
		}
	}
	tuiKeys(m, tea.KeyDown, tea.KeyDown, tea.KeyDown, tea.KeyEnter)
	if !strings.Contains(m.View(), "过程显示 · 关闭 → 本卡正文") {
		t.Fatalf("inline preview setting absent from confirmation: %s", m.View())
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Fatal("inline preview confirmation did not schedule an update")
	}
	m.Update(cmd())
	if len(requests) != 4 || requests[3].ProcessMode == nil || *requests[3].ProcessMode != "card" ||
		!strings.Contains(m.View(), "过程显示 · 本卡正文") {
		t.Fatalf("inline preview update did not reach the receiver: %+v, %s", requests, m.View())
	}
	m.height = 12
	tuiKeys(m, tea.KeyEnter, tea.KeyDown, tea.KeyDown, tea.KeyDown)
	if rendered := m.View(); strings.Count(rendered, "\n")+1 > m.height ||
		!strings.Contains(rendered, liveSettingMarker(true)+"独立正文＋详细工具") {
		t.Fatalf("selected process mode should remain visible in a short terminal: %s", rendered)
	}
	tuiKeys(m, tea.KeyEnter)
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Fatal("combined mode confirmation did not schedule an update")
	}
	m.Update(cmd())
	if len(requests) != 5 || requests[4].ProcessMode == nil || *requests[4].ProcessMode != "stream-detailed" {
		t.Fatalf("combined mode update did not reach the receiver: %+v", requests)
	}
	tuiKeys(m, tea.KeyEsc)
	if m.screen != tuiBrowse || m.account != 1 || m.draft != before {
		t.Fatal("online settings changed the startup draft or account")
	}
}

func TestConfigTUILiveSettingsRequiresUniqueMatchingProcess(t *testing.T) {
	account := receiverProcessAccount{Tenant: "feishu", AppID: "cli_settings"}
	for _, tc := range []struct {
		processes []receiverProcess
		message   string
	}{
		{message: "没有支持在线运行设置"},
		{processes: []receiverProcess{
			{ID: "first", RunSettings: true, Accounts: []receiverProcessAccount{account}},
			{ID: "second", RunSettings: true, Accounts: []receiverProcessAccount{account}},
		}, message: "config live-settings -process"},
	} {
		id, err := liveSettingsProcess(func() ([]receiverProcess, error) { return tc.processes, nil }, account)
		if id != "" || err == nil || !strings.Contains(err.Error(), tc.message) {
			t.Fatalf("expected %q, got %q and %v", tc.message, id, err)
		}
	}
}
