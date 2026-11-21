package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestConfigTUITelemetryArchiveKeyboard(t *testing.T) {
	const day = "2026-02-20"
	const appID = "cli_additional"
	source := tuiReceiverEnableSource(t)
	config, err := bridge.ParseConfig(source.JSON)
	if err != nil {
		t.Fatal(err)
	}
	archive := receiverTelemetryArchiveDir(config.AdditionalAccounts[0].StateDir)
	if err := localfile.MkdirAllPrivate(archive); err != nil {
		t.Fatal(err)
	}
	var data strings.Builder
	for _, value := range []int{12345, 2, 3, 4, 5, 6, 7, 67890} {
		fmt.Fprintf(&data, `{"type":"receiver-metric","tenant":"feishu","appId":"%s","at":"%sT00:00:00Z","name":"pool_active","value":%d}`+"\n", appID, day, value)
	}
	if _, err := localfile.WritePrivateAtomic(filepath.Join(archive, receiverTelemetryArchiveName(day)), []byte(data.String()), true); err != nil {
		t.Fatal(err)
	}

	m := newConfigTUI(source)
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	press := func(key string) tea.Cmd {
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		return cmd
	}
	view := func(wants ...string) string {
		t.Helper()
		got := m.View()
		lines := strings.Split(got, "\n")
		if len(lines) > 12 {
			t.Fatalf("view exceeds 12 rows: %s", got)
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > 40 {
				t.Fatalf("line exceeds 40 columns: %q", line)
			}
		}
		for _, want := range wants {
			if !strings.Contains(got, want) {
				t.Fatalf("view is missing %q: %s", want, got)
			}
		}
		return got
	}

	tuiKeys(m, tea.KeyTab)
	tuiSelect(t, m, `路由 · "work"`)
	tuiKeys(m, tea.KeyEnter)
	if press("Y") != nil || m.route != "work" || strings.Contains(view(), "Y 每日遥测") {
		t.Fatal("route view exposed the daily archive")
	}
	tuiKeys(m, tea.KeyEsc)
	tuiSetField(t, m, "账号 · stateDir", filepath.Join(filepath.Dir(source.Path), "draft-state"))
	row, draft := m.row, m.draft
	view("Y 每日遥测")
	today := time.Now().UTC().Format(receiverTelemetryArchiveDate)
	press("Y")
	if got := m.input.Value(); got != today && got != time.Now().UTC().Format(receiverTelemetryArchiveDate) {
		t.Fatalf("default date is not UTC today: %q", got)
	}
	view("UTC 日期（YYYY-MM-DD）", "feishu/"+appID, "Enter 查询", "最多 100 行")
	tuiKeys(m, tea.KeyF2)
	tuiType(m, "2026-02-30")
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil || m.screen != tuiTelemetryArchiveDate {
		t.Fatal("an invalid date scheduled a query")
	}
	view("请输入有效的 UTC 日期")
	tuiKeys(m, tea.KeyF2)
	tuiType(m, day)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || m.screen != tuiTelemetryArchiveLoading {
		t.Fatal("Enter did not schedule an archive query")
	}
	m.Update(cmd())
	view("feishu/"+appID, day+" UTC", "最多 100 行", "[ 上一天", "] 下一天", "r 刷新", "e 改日期", "↑↓←→ 滚动", `{"type":"receiver-metric"`)
	for range 20 {
		tuiKeys(m, tea.KeyRight)
	}
	if strings.Contains(view(`"value":12345`), `"value":67890`) {
		t.Fatal("the last record appeared before vertical scrolling")
	}
	tuiKeys(m, tea.KeyPgDown)
	view(`"value":67890`)

	for _, step := range []struct{ key, date string }{{"[", "2026-02-19"}, {"r", "2026-02-19"}, {"]", day}} {
		cmd := press(step.key)
		if cmd == nil || m.screen != tuiTelemetryArchiveLoading {
			t.Fatalf("%q did not schedule a query", step.key)
		}
		m.Update(cmd())
		if step.date != day {
			view(step.date+" UTC", "暂无遥测记录")
		} else {
			view(day+" UTC", `{"type":"receiver-metric"`)
		}
	}
	press("e")
	if m.screen != tuiTelemetryArchiveDate || m.input.Value() != day {
		t.Fatal("date editor did not retain the queried UTC date")
	}
	tuiKeys(m, tea.KeyEsc)
	if m.screen != tuiBrowse || m.account != 1 || m.route != "" || m.row != row || m.draft != draft || m.rows[m.row].label != "账号 · stateDir" {
		t.Fatal("Escape changed the App, field cursor, or draft")
	}
}

func TestConfigTUITelemetryArchiveStaleAndLateResults(t *testing.T) {
	const day = "2026-02-20"
	source := tuiReceiverEnableSource(t)
	m := newConfigTUI(source)
	tuiKeys(m, tea.KeyTab)
	submit := func() tea.Cmd {
		t.Helper()
		tuiType(m, "Y")
		tuiKeys(m, tea.KeyF2)
		tuiType(m, day)
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd == nil || m.screen != tuiTelemetryArchiveLoading {
			t.Fatalf("lookup was not scheduled: %s", m.View())
		}
		return cmd
	}
	refresh := func() tea.Cmd {
		t.Helper()
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
		if cmd == nil || m.screen != tuiTelemetryArchiveLoading {
			t.Fatal("refresh was not scheduled")
		}
		return cmd
	}
	stale := submit()
	if err := os.WriteFile(source.Path, append(bytes.Clone(source.JSON), '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	m.Update(stale())
	if !strings.Contains(m.View(), "请重新打开配置后再查询") {
		t.Fatalf("saved revision drift was not reported: %s", m.View())
	}
	tuiKeys(m, tea.KeyEsc)
	var dates []time.Time
	var lookupErr error
	m.telemetry.lookup = func(path, appID, revision string, date time.Time) ([]byte, error) {
		if path != source.Path || appID != "cli_additional" || revision != source.Revision {
			t.Fatalf("lookup used the wrong saved selection: %q %q %q", path, appID, revision)
		}
		dates = append(dates, date)
		return []byte("\x1b[31mlatest-result\x07\n"), lookupErr
	}
	first := submit()
	tuiKeys(m, tea.KeyEsc)
	late := first()
	m.Update(late)
	if m.screen != tuiBrowse {
		t.Fatal("a late response reopened the archive after Escape")
	}
	second := submit()
	m.Update(late)
	if m.screen != tuiTelemetryArchiveLoading || strings.Contains(m.View(), "latest-result") {
		t.Fatal("a response from the previous visit replaced the new query")
	}
	latest := refresh()
	m.Update(second())
	if m.screen != tuiTelemetryArchiveLoading || strings.Contains(m.View(), "latest-result") {
		t.Fatal("a response superseded by refresh replaced the active query")
	}
	m.Update(latest())
	wantDay := time.Date(2026, 2, 20, 0, 0, 0, 0, time.UTC)
	if !slices.Equal(dates, []time.Time{wantDay, wantDay, wantDay}) {
		t.Fatalf("unexpected UTC query dates: %v", dates)
	}
	if got := m.View(); !strings.Contains(got, "�[31mlatest-result�") || strings.ContainsAny(got, "\x1b\x07") {
		t.Fatalf("archive did not sanitize terminal controls: %q", got)
	}
	lookupErr = errors.New("private lookup detail")
	m.Update(refresh()())
	if got := m.View(); !strings.Contains(got, "无法读取每日遥测") || strings.Contains(got, lookupErr.Error()) || strings.Contains(got, "latest-result") {
		t.Fatalf("unexpected error view: %s", got)
	}

	tuiKeys(m, tea.KeyEsc)
	tuiSetField(t, m, "账号 · appId", "cli_unsaved")
	tuiType(m, "Y")
	if m.screen != tuiBrowse || !strings.Contains(m.status, "未在已保存配置中") {
		t.Fatal("a draft-only App ID opened an archive")
	}
}
