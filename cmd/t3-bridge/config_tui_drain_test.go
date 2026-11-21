package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestConfigTUIDrainChecksSavedApps(t *testing.T) {
	source := webSource{
		Path: "synthetic-config.json", Revision: "saved",
		JSON: []byte(`{"appId":"cli_primary","additionalAccounts":[{"appId":"cli_enabled","receiverAuthorized":true},{"appId":"cli_disabled","receiverAuthorized":false}]}`),
	}
	m := newConfigTUI(source)
	m.Update(tea.WindowSizeMsg{Width: 90, Height: 15})
	m.setDraft([]byte(`{"appId":"cli_primary","additionalAccounts":[{"appId":"cli_draft","stateDir":"draft-state"}]}`))
	before := m.draft
	tuiKeys(m, tea.KeyTab, tea.KeyDown)
	row := m.row
	calls := 0
	m.drain.check = func(path, revision string) (bridge.DrainReport, error) {
		calls++
		if path != source.Path || revision != source.Revision {
			t.Fatalf("unexpected check target: %q at %q", path, revision)
		}
		if calls == 1 {
			return bridge.DrainReport{Drained: true}, nil
		}
		return bridge.DrainReport{Accounts: []bridge.DrainAccount{
			{AppID: "cli_primary", Status: "drained"},
			{AppID: "cli_enabled", Status: "blocked", Blockers: []bridge.DrainBlocker{
				{Code: "active", Count: 3}, {Code: "delivery", Count: 2},
			}},
			{AppID: "cli_disabled", Status: "blocked", Blockers: []bridge.DrainBlocker{{Code: "inbox", Count: 1}}},
		}}, nil
	}
	if view := m.View(); !strings.Contains(view, "G 排空检查") {
		t.Fatalf("drain shortcut is missing: %s", view)
	}
	start := func() tea.Cmd {
		t.Helper()
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
		view := m.View()
		if m.screen != tuiDrainLoading || cmd == nil || !strings.Contains(view, "已保存配置的全部 App") ||
			!strings.Contains(view, "停止接收端后运行") || !strings.Contains(view, "当前本地状态快照") {
			t.Fatalf("drain check did not open with offline guidance: %s", view)
		}
		return cmd
	}
	first := start()
	tuiKeys(m, tea.KeyEsc)
	if m.screen != tuiBrowse || m.draft != before {
		t.Fatal("cancel did not return to the draft")
	}
	second := start()
	if calls != 0 {
		t.Fatal("drain check ran inside the keyboard update")
	}
	m.Update(first())
	if m.screen != tuiDrainLoading {
		t.Fatal("a canceled check replaced the current check")
	}
	m.Update(second())
	view := m.View()
	if m.screen != tuiDrainResult || calls != 2 || strings.Contains(view, "cli_disabled") || strings.Contains(view, "cli_draft") {
		t.Fatalf("unexpected first result page: %s", view)
	}
	for _, want := range []string{"未能确认全部 App 已排空", "含未启用接收的 App", "cli_primary · 已排空", "cli_enabled · 有未结事项", "在途任务 · 3", "待核对回复或过程消息 · 2"} {
		if !strings.Contains(view, want) {
			t.Fatalf("first result page omitted %q: %s", want, view)
		}
	}
	tuiKeys(m, tea.KeyPgDown)
	if view = m.View(); !strings.Contains(view, "cli_disabled · 有未结事项") || !strings.Contains(view, "入站队列 · 1") {
		t.Fatalf("scrolled result omitted the disabled App or its blocker: %s", view)
	}
	tuiKeys(m, tea.KeyEsc)
	if m.screen != tuiBrowse || m.draft != before || m.account != 1 || m.row != row {
		t.Fatal("drain check changed the selected account, field or draft")
	}
}

func TestConfigTUIDrainRequiresSavedConfig(t *testing.T) {
	m := newConfigTUI(webSource{JSON: []byte(`{"appId":"cli_sample"}`)})
	for _, source := range []webSource{m.source, {Path: "new.json"}, {Path: "new.json", Revision: "missing"}} {
		m.source.Path, m.source.Revision = source.Path, source.Revision
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
		if cmd != nil || m.screen != tuiBrowse || !strings.Contains(m.status, "请先保存配置文件") {
			t.Fatalf("drain check opened without a saved file: %s", m.View())
		}
	}
}

func TestConfigTUIDrainDeliverySamples(t *testing.T) {
	source := webSource{
		Path: "synthetic-config.json", Revision: "saved",
		JSON: []byte(`{"appId":"cli_primary","additionalAccounts":[{"appId":"cli_secondary"}]}`),
	}
	m := newConfigTUI(source)
	m.Update(tea.WindowSizeMsg{Width: 96, Height: 24})
	const replyID = "om_0123456789abcdef0011223344556677"
	const processID = "om_fedcba9876543210fedcba9876543210"
	m.drain.check = func(string, string) (bridge.DrainReport, error) {
		return bridge.DrainReport{Accounts: []bridge.DrainAccount{
			{
				AppID: "cli_primary", Status: "blocked",
				Blockers:        []bridge.DrainBlocker{{Code: "delivery", Count: 5}},
				DeliverySamples: []bridge.DrainDeliverySample{{MessageID: replyID, Kind: "reply"}},
			},
			{
				AppID: "cli_secondary", Status: "blocked",
				Blockers:        []bridge.DrainBlocker{{Code: "delivery", Count: 2}},
				DeliverySamples: []bridge.DrainDeliverySample{{MessageID: processID, Kind: "process"}},
			},
		}}, nil
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	if cmd == nil {
		t.Fatal("drain check was not scheduled")
	}
	m.Update(cmd())
	wide := m.View()
	primary, secondary, found := strings.Cut(wide, "cli_secondary · 有未结事项")
	if !found || !strings.Contains(primary, "cli_primary · 有未结事项") ||
		!strings.Contains(primary, "待核对回复或过程消息 · 5") || !strings.Contains(primary, "结果回复 · "+replyID) ||
		!strings.Contains(secondary, "过程卡 · "+processID) ||
		strings.Contains(primary, processID) || strings.Contains(secondary, replyID) {
		t.Fatalf("samples are missing or assigned to the wrong App: %s", wide)
	}
	if !strings.Contains(wide, "总数以待核对计数为准") || !strings.Contains(wide, "返回后选中此 App，按 v 查回执") {
		t.Fatalf("samples are missing count or receipt guidance: %s", wide)
	}

	const width = 32
	m.Update(tea.WindowSizeMsg{Width: width, Height: 14})
	narrowView := func() string {
		t.Helper()
		view := m.View()
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > width {
				t.Fatalf("result line exceeds terminal width: %q", line)
			}
		}
		return view
	}
	if view := narrowView(); !strings.Contains(view, replyID[:12]) || strings.Contains(view, replyID[len(replyID)-12:]) {
		t.Fatalf("narrow result did not show the start of the first sample: %s", view)
	}
	tuiKeys(m, tea.KeyRight, tea.KeyRight, tea.KeyRight)
	if view := narrowView(); !strings.Contains(view, replyID[len(replyID)-12:]) {
		t.Fatalf("horizontal scrolling did not show the end of the first sample: %s", view)
	}
	tuiKeys(m, tea.KeyPgDown, tea.KeyLeft, tea.KeyLeft, tea.KeyLeft)
	if view := narrowView(); !strings.Contains(view, "过程卡 · "+processID[:12]) {
		t.Fatalf("vertical scrolling did not show the second App's sample: %s", view)
	}
}

func TestConfigTUIDrainOutcome(t *testing.T) {
	m := newConfigTUI(webSource{Path: "synthetic-config.json", Revision: "saved", JSON: []byte(`{"appId":"cli_sample"}`)})
	const privateError = "synthetic-private-path-and-content"
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, "整体 · 全部 App 已排空"},
		{errWorkspaceServiceConfigChanged, "请重新打开配置后再检查"},
		{errors.New(privateError), "无法执行排空检查"},
	} {
		m.drain.check = func(string, string) (bridge.DrainReport, error) {
			return bridge.DrainReport{Drained: true, Accounts: []bridge.DrainAccount{{AppID: "cli_sample", Status: "drained"}}}, tc.err
		}
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
		if cmd == nil {
			t.Fatalf("drain check was not scheduled: %s", m.View())
		}
		m.Update(cmd())
		if view := m.View(); !strings.Contains(view, tc.want) || strings.Contains(view, privateError) ||
			(tc.err != nil && strings.Contains(view, "整体 · 全部 App 已排空")) {
			t.Fatalf("unexpected drain result: %s", view)
		}
		tuiKeys(m, tea.KeyEsc)
	}
}
