package main

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
)

func TestConfigTUIMessageReceiptLookup(t *testing.T) {
	const path = "synthetic-config.json"
	const threadURL = "https://t3.example.invalid/threads/environment-1/thread-1"
	source := webSource{Path: path, Revision: "saved", JSON: []byte(`{"appId":"cli_primary","additionalAccounts":[{"appId":"cli_extra"}]}`)}
	m := newConfigTUI(source)
	m.Update(tea.WindowSizeMsg{Width: 90, Height: 14})
	m.setDraft([]byte(`{"appId":"cli_primary","additionalAccounts":[{"appId":"cli_extra","tenant":"edited","stateDir":"draft-state"}]}`))
	before := m.draft
	tuiKeys(m, tea.KeyTab, tea.KeyDown)
	row := m.row
	calls := 0
	m.messageReceipt.lookup = func(gotPath, appID, messageID string) (bridge.MessageReceipt, error) {
		calls++
		if gotPath != path || appID != "cli_extra" || messageID != "om_Exact-01" {
			t.Fatalf("unexpected lookup parameters: %q %q %q", gotPath, appID, messageID)
		}
		return bridge.MessageReceipt{
			AppID: appID, MessageID: messageID, InputID: "om_input", TaskID: "task-1", RouteID: "route-1",
			EnvironmentID: "environment-1", ProjectID: "project-1",
			Phase: "done", RequestID: "request-1", ThreadID: "thread-1", ThreadURL: threadURL, RunID: "run-1",
			ReplyID: "om_reply", DeliveryState: bridge.ReplyDeliveryUnconfirmed, NeedsReconcile: true,
		}, nil
	}
	if !strings.Contains(m.View(), "v 查询消息回执") {
		t.Fatal("receipt shortcut is missing")
	}
	tuiType(m, "v")
	tuiKeys(m, tea.KeyEnter)
	if m.screen != tuiMessageReceiptInput || !strings.Contains(m.status, "消息 ID") {
		t.Fatalf("empty message ID was accepted: %s", m.View())
	}
	submit := func() tea.Cmd {
		t.Helper()
		tuiType(m, "om_Exact-01")
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if m.screen != tuiMessageReceiptLoading || cmd == nil {
			t.Fatalf("lookup was not scheduled: %s", m.View())
		}
		return cmd
	}
	first := submit()
	tuiKeys(m, tea.KeyEsc)
	tuiType(m, "v")
	second := submit()
	if calls != 0 {
		t.Fatal("lookup ran inside the keyboard update")
	}
	m.Update(first())
	if m.screen != tuiMessageReceiptLoading {
		t.Fatal("a previous result replaced the current lookup")
	}
	m.Update(second())
	if m.screen != tuiMessageReceiptResult || calls != 2 || !strings.Contains(m.View(), "入站消息 ID · om_Exact-01") ||
		!strings.Contains(m.View(), "T3 环境 ID · environment-1") || !strings.Contains(m.View(), "T3 项目 ID · project-1") ||
		strings.Contains(m.View(), "需要对账") {
		t.Fatalf("unexpected first result page: %s", m.View())
	}
	tuiKeys(m, tea.KeyPgDown)
	for _, want := range []string{"请求 ID · request-1", "线程 ID · thread-1", "运行 ID · run-1",
		"回复 ID · om_reply", "终态回复投递 · 结果待核对"} {
		if !strings.Contains(m.View(), want) {
			t.Fatalf("scrolled result omitted %q: %s", want, m.View())
		}
	}
	tuiKeys(m, tea.KeyPgDown)
	view := m.View()
	if !strings.Contains(view, "历史回执 · 否") || !strings.Contains(view, "需要对账 · 是") {
		t.Fatalf("scrolled result omitted receipt flags: %s", view)
	}
	if !slices.ContainsFunc(strings.Split(view, "\n"), func(line string) bool {
		return strings.TrimRight(line, " ") == threadURL
	}) {
		t.Fatalf("scrolled result omitted the standalone thread URL: %s", view)
	}
	tuiKeys(m, tea.KeyEsc)
	if m.screen != tuiBrowse || m.draft != before || m.account != 1 || m.row != row {
		t.Fatal("lookup changed the selected account, field or draft")
	}
}

func TestConfigTUIMessageReceiptDeliveryState(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state bridge.ReplyDeliveryState
		want  string
	}{
		{"pending", bridge.ReplyDeliveryPending, "等待终态回复"},
		{"confirmed", bridge.ReplyDeliveryConfirmed, "已获飞书确认"},
		{"unconfirmed", bridge.ReplyDeliveryUnconfirmed, "结果待核对"},
		{"not applicable", bridge.ReplyDeliveryNotApplicable, "无需终态回复"},
		{"unknown", bridge.ReplyDeliveryUnknown, "未知"},
		{"empty", "", "未知"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := messageReceiptText(bridge.MessageReceipt{DeliveryState: tc.state})
			if !slices.Contains(strings.Split(text, "\n"), "终态回复投递 · "+tc.want) {
				t.Fatalf("unexpected delivery state: %s", text)
			}
		})
	}
	text := messageReceiptText(bridge.MessageReceipt{Historical: true, ReplyID: "om_legacy_reply"})
	lines := strings.Split(text, "\n")
	for _, want := range []string{"终态回复投递 · 未知", "回复 ID · om_legacy_reply", "历史回执 · 是", "需要对账 · 否"} {
		if !slices.Contains(lines, want) {
			t.Fatalf("legacy receipt omitted %q: %s", want, text)
		}
	}
}

func TestConfigTUIMessageReceiptGuardsAndErrors(t *testing.T) {
	if text := messageReceiptText(bridge.MessageReceipt{ThreadID: "thread-old"}); !strings.Contains(text, "线程 ID · thread-old") ||
		!strings.Contains(text, "回复 ID · 尚无记录") || strings.Contains(text, "T3 线程链接") {
		t.Fatalf("unexpected receipt with missing metadata: %s", text)
	}
	source := webSource{JSON: []byte(`{"appId":"cli_sample"}`)}
	for _, s := range []webSource{source, {Path: "new.json", Revision: "missing", JSON: source.JSON}} {
		m := newConfigTUI(s)
		tuiType(m, "v")
		if m.screen != tuiBrowse || !strings.Contains(m.status, "保存") {
			t.Fatalf("receipt lookup opened without a saved file: %s", m.View())
		}
	}
	source.Path, source.Revision = "synthetic-config.json", "saved"
	m := newConfigTUI(source)
	m.setDraft([]byte(`{"appId":""}`))
	tuiType(m, "v")
	if m.screen != tuiBrowse || !strings.Contains(m.status, "App ID") {
		t.Fatalf("missing App ID was accepted: %s", m.View())
	}
	m.setDraft(source.JSON)
	const privateError = "synthetic-private-path-and-content"
	for _, tc := range []struct {
		err  error
		want string
	}{
		{bridge.ErrMessageReceiptNotFound, "未找到该入站消息"},
		{errors.New(privateError), "无法查询消息回执"},
	} {
		m.messageReceipt.lookup = func(string, string, string) (bridge.MessageReceipt, error) {
			return bridge.MessageReceipt{}, tc.err
		}
		tuiType(m, "v")
		tuiType(m, "om_sample")
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd == nil {
			t.Fatalf("lookup was not scheduled: %s", m.View())
		}
		m.Update(cmd())
		if view := m.View(); !strings.Contains(view, tc.want) || strings.Contains(view, privateError) {
			t.Fatalf("unexpected lookup error view: %s", view)
		}
		tuiKeys(m, tea.KeyEsc)
	}
}
