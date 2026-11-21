package main

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func pressServiceManageKey(m *configTUI, key string) tea.Cmd {
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	return cmd
}

func TestConfigTUIServiceManageInventory(t *testing.T) {
	m := newConfigTUI(tuiTestSource(t))
	originalRow := m.row
	m.draft += "\n"
	inventoryCalls := 0
	var items []serviceInventoryEntry
	var inventoryErr error
	m.serviceManage.inventory = func() ([]serviceInventoryEntry, error) {
		inventoryCalls++
		return items, inventoryErr
	}
	m.serviceManage.readLogs = func(string, bool) (string, error) {
		t.Fatal("unmanageable instance was allowed to read logs")
		return "", nil
	}
	if view := m.View(); !strings.Contains(view, "I 安装服务") || !strings.Contains(view, "M 管理实例") {
		t.Fatalf("missing service shortcuts: %s", view)
	}
	oldList := pressServiceManageKey(m, "M")
	if oldList == nil || m.screen != tuiServiceManageLoading || inventoryCalls != 0 {
		t.Fatal("opening management did not queue an asynchronous read")
	}
	tuiKeys(m, tea.KeyEsc)
	list := pressServiceManageKey(m, "M")
	m.Update(oldList())
	if m.screen != tuiServiceManageLoading {
		t.Fatal("a previous inventory read changed the current panel")
	}
	m.Update(list())
	if view := m.View(); !strings.Contains(view, "未找到受管服务实例") || strings.Contains(view, "l 查看日志") {
		t.Fatalf("bad empty inventory view: %s", view)
	}
	inventoryErr = errors.New("private inventory path")
	m.Update(pressServiceManageKey(m, "r")())
	if view := m.View(); !strings.Contains(view, "无法读取本机服务实例清单") || strings.Contains(view, "private inventory path") {
		t.Fatalf("bad inventory error view: %s", view)
	}
	inventoryErr = nil
	items = []serviceInventoryEntry{
		{name: "invalid", err: errors.New("private definition detail"), manage: func(string) error {
			t.Fatal("an inventory entry with an error was allowed to run an action")
			return nil
		}},
		{name: "unowned", status: "running"},
		{name: "work", status: "running\x1b[31m", pid: 123, manage: func(string) error { return nil }},
	}
	m.Update(pressServiceManageKey(m, "r")())
	for range 2 {
		if view := m.View(); !strings.Contains(view, "不可管理") || strings.Contains(view, "l 查看日志") ||
			strings.Contains(view, "u 注销") || strings.Contains(view, "private definition detail") {
			t.Fatalf("bad unmanageable inventory row: %s", view)
		}
		for _, key := range []string{"s", "x", "R", "u", "y", "l"} {
			if pressServiceManageKey(m, key) != nil || m.screen != tuiServiceManagePick {
				t.Fatalf("%q was accepted for an unmanageable entry", key)
			}
		}
		tuiKeys(m, tea.KeyDown)
	}
	if view := m.View(); !strings.Contains(view, "› work · PID 123 · running") ||
		!strings.Contains(view, "l 查看日志") || strings.ContainsRune(view, '\x1b') {
		t.Fatalf("bad selected inventory row: %s", view)
	}
	tuiKeys(m, tea.KeyUp)
	if view := m.View(); !strings.Contains(view, "› unowned") {
		t.Fatalf("up did not select the previous row: %s", view)
	}
	tuiKeys(m, tea.KeyEsc)
	if m.screen != tuiBrowse || m.row != originalRow || m.draft != string(m.source.JSON)+"\n" {
		t.Fatal("management panel changed the configuration selection or draft")
	}
}

func TestConfigTUIServiceManageActions(t *testing.T) {
	for _, tc := range []struct {
		key, action, label, autostart string
		err                           error
	}{
		{key: "s", action: "start", label: "启动", autostart: "开启登录自启"},
		{key: "x", action: "stop", label: "停止", autostart: "关闭登录自启", err: errors.New("private manager details")},
		{key: "R", action: "restart", label: "重启", autostart: "开启登录自启"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			m := newConfigTUI(tuiTestSource(t))
			var actions []string
			item := serviceInventoryEntry{name: "target", status: "running", manage: func(action string) error {
				actions = append(actions, action)
				return tc.err
			}}
			other := serviceInventoryEntry{name: "other", manage: func(string) error {
				t.Fatal("action ran on a different instance")
				return nil
			}}
			listCalls := 0
			m.serviceManage.inventory = func() ([]serviceInventoryEntry, error) {
				listCalls++
				if listCalls == 1 {
					return []serviceInventoryEntry{other, item}, nil
				}
				item.status = "refreshed"
				return []serviceInventoryEntry{item, other}, nil
			}
			m.Update(pressServiceManageKey(m, "M")())
			tuiKeys(m, tea.KeyDown)
			for range 2 {
				if pressServiceManageKey(m, tc.key) != nil || m.screen != tuiServiceManageConfirm {
					t.Fatal("selecting an action did not open a separate confirmation")
				}
				for _, want := range []string{"确认" + tc.label, "实例名 · target", "整个实例", "所有账号", tc.autostart} {
					if view := m.View(); !strings.Contains(view, want) {
						t.Fatalf("confirmation missing %q: %s", want, view)
					}
				}
				if len(actions) != 0 {
					t.Fatal("action ran before confirmation")
				}
				tuiKeys(m, tea.KeyEsc)
			}
			tuiType(m, tc.key)
			apply := pressServiceManageKey(m, "y")
			if apply == nil || m.screen != tuiServiceManageApplying || len(actions) != 0 || pressServiceManageKey(m, "y") != nil {
				t.Fatal("confirmation did not queue exactly one action")
			}
			_, refresh := m.Update(apply())
			if !slices.Equal(actions, []string{tc.action}) || refresh == nil || listCalls != 1 || m.screen != tuiServiceManageLoading {
				t.Fatalf("action result did not queue a refresh: actions=%v reads=%d", actions, listCalls)
			}
			m.Update(refresh())
			result := "已提交" + tc.label
			if tc.err != nil {
				result = tc.label + "未确认"
			}
			if view := m.View(); m.screen != tuiServiceManagePick || listCalls != 2 ||
				!strings.Contains(view, "› target · PID — · refreshed") ||
				!strings.Contains(view, result) || strings.Contains(view, "private manager details") {
				t.Fatalf("action result or refreshed selection missing: %s", view)
			}
		})
	}
}

func TestConfigTUIServiceManageUnregister(t *testing.T) {
	for _, tc := range []struct {
		name, status          string
		remains               bool
		actionErr, refreshErr error
	}{
		{name: "removed", status: "已注销实例 target"},
		{name: "still present", remains: true, status: "注销未确认，清单仍有该实例"},
		{name: "action failed after removal", actionErr: errors.New("private manager details"), status: "注销未确认，请核对刷新后的实例状态"},
		{name: "refresh failed", refreshErr: errors.New("private inventory details"), status: "注销未确认，清单读取失败"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newConfigTUI(tuiTestSource(t))
			var actions []string
			target := serviceInventoryEntry{name: "target", manage: func(action string) error {
				actions = append(actions, action)
				return tc.actionErr
			}}
			other := serviceInventoryEntry{name: "other", manage: func(string) error {
				t.Fatal("unregister ran on a different instance")
				return nil
			}}
			listCalls := 0
			m.serviceManage.inventory = func() ([]serviceInventoryEntry, error) {
				listCalls++
				if listCalls == 1 {
					return []serviceInventoryEntry{other, target}, nil
				}
				if tc.remains {
					return []serviceInventoryEntry{target, other}, nil
				}
				return []serviceInventoryEntry{other}, tc.refreshErr
			}
			m.Update(pressServiceManageKey(m, "M")())
			tuiKeys(m, tea.KeyDown)
			const narrowWidth = 24
			m.Update(tea.WindowSizeMsg{Width: narrowWidth, Height: 16})
			if view := m.View(); !strings.Contains(view, "u 注销") {
				t.Fatalf("unregister shortcut missing: %s", view)
			}
			if pressServiceManageKey(m, "u") != nil || m.screen != tuiServiceManageConfirm {
				t.Fatal("unregister did not open confirmation")
			}
			view := m.View()
			for _, line := range strings.Split(view, "\n") {
				if ansi.StringWidth(line) > narrowWidth {
					t.Fatalf("confirmation line exceeds terminal width: %q", line)
				}
			}
			for _, want := range []string{
				"确认注销服务实例", "实例名 · target", "整个实例将停止", "实例内所有 App 将停止接收",
				"服务定义将移除", "配置、凭据、状态和日志保留", "T3 在途 run 可能继续", "y 确认 · Esc 取消",
			} {
				if !strings.Contains(strings.ReplaceAll(view, "\n", ""), want) {
					t.Fatalf("confirmation missing %q: %s", want, view)
				}
			}
			tuiKeys(m, tea.KeyEsc)
			if m.screen != tuiServiceManagePick || pressServiceManageKey(m, "y") != nil || len(actions) != 0 {
				t.Fatal("cancelled unregister could still run")
			}
			pressServiceManageKey(m, "u")
			apply := pressServiceManageKey(m, "y")
			if apply == nil || m.screen != tuiServiceManageApplying || pressServiceManageKey(m, "y") != nil || len(actions) != 0 {
				t.Fatal("confirmation did not queue exactly one unregister")
			}
			_, refresh := m.Update(apply())
			if refresh == nil || m.screen != tuiServiceManageLoading || listCalls != 1 || !slices.Equal(actions, []string{"unregister"}) ||
				strings.Contains(m.status, "已注销") {
				t.Fatalf("unregister did not wait for readback: actions=%v reads=%d status=%q", actions, listCalls, m.status)
			}
			m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			m.Update(refresh())
			view = m.View()
			if m.screen != tuiServiceManagePick || listCalls != 2 || !strings.Contains(view, tc.status) || strings.Contains(view, "private ") {
				t.Fatalf("unexpected unregister result: reads=%d view=%s", listCalls, view)
			}
			if tc.refreshErr != nil {
				if !strings.Contains(view, "无法读取本机服务实例清单") || strings.Contains(view, "› target") {
					t.Fatalf("failed refresh displayed stale inventory: %s", view)
				}
			} else if tc.remains {
				if !strings.Contains(view, "› target") {
					t.Fatalf("remaining instance lost its selection: %s", view)
				}
			} else if !strings.Contains(view, "› other") || strings.Contains(view, "target · PID") {
				t.Fatalf("removed instance remained in the list: %s", view)
			}
		})
	}
}

func TestConfigTUIServiceManageLogs(t *testing.T) {
	const metricRest = `,"type":"receiver-metric","at":"2026-11-19T02:03:04Z","value":1}`
	telemetryLogs := strings.Join([]string{
		`{"name":"pool_active","tenant":"feishu","appId":"cli_same"` + metricRest,
		`{"name":"pool_waiting","tenant":"lark","appId":"cli_same"` + metricRest,
		`{"name":"ws_reconnect","tenant":"lark","appId":"cli_same_more"` + metricRest,
	}, "\n")
	m := newConfigTUI(tuiTestSource(t))
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	listCalls, logCalls := 0, 0
	var logErr error
	m.serviceManage.inventory = func() ([]serviceInventoryEntry, error) {
		listCalls++
		return []serviceInventoryEntry{{name: "target", manage: func(string) error {
			t.Fatal("viewing logs triggered a lifecycle action")
			return nil
		}}}, nil
	}
	m.serviceManage.readLogs = func(instance string, telemetry bool) (string, error) {
		logCalls++
		if instance != "target" {
			t.Fatalf("read logs from %q", instance)
		}
		if telemetry {
			return telemetryLogs, nil
		}
		return "first\x1b[31m\u009b\r\n" + strings.Repeat("log line\n", 8) + "last", logErr
	}
	m.Update(pressServiceManageKey(m, "M")())
	stale := pressServiceManageKey(m, "l")
	tuiKeys(m, tea.KeyEsc)
	logs := pressServiceManageKey(m, "l")
	m.Update(stale())
	if view := m.View(); m.screen != tuiServiceManageLogs || !strings.Contains(view, "正在读取实例日志") ||
		strings.Contains(view, "first") || !strings.Contains(view, "每个日志流最多最近 100 行") {
		t.Fatalf("unexpected log loading view: %s", view)
	}
	m.Update(logs())
	if view := m.View(); !strings.Contains(view, "实例日志 · target") || !strings.Contains(view, "first") ||
		strings.ContainsAny(view, "\x1b\u009b\r") || strings.Contains(view, "last") || strings.Contains(view, "Tab/Shift+Tab") {
		t.Fatalf("unexpected log viewport: %q", view)
	}
	tuiKeys(m, tea.KeyPgDown, tea.KeyPgDown)
	if view := m.View(); !strings.Contains(view, "last") {
		t.Fatalf("could not scroll to the end of the log: %s", view)
	}
	tuiKeys(m, tea.KeyEsc)
	if view := m.View(); m.screen != tuiServiceManagePick || !strings.Contains(view, "› target") || listCalls != 1 || logCalls != 2 {
		t.Fatalf("logs did not return to the same selected service: %s", view)
	}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	m.Update(pressServiceManageKey(m, "t")())
	if view := m.View(); !strings.Contains(view, "实例遥测 · target") || !strings.Contains(view, "范围 · 全部 App") ||
		!strings.Contains(view, "Tab/Shift+Tab 切换 · ↑↓←→ · Esc 返回") || strings.Count(view, "\n") >= 12 || strings.Contains(view, "first") {
		t.Fatalf("unexpected telemetry view: %s", view)
	}
	top := m.review.View()
	tuiKeys(m, tea.KeyRight)
	if m.review.View() == top {
		t.Fatal("telemetry did not scroll horizontally")
	}
	for _, tc := range []struct {
		key    tea.KeyType
		scope  string
		metric string
	}{
		{tea.KeyTab, "feishu / cli_same", "pool_active"},
		{tea.KeyTab, "lark / cli_same", "pool_waiting"},
		{tea.KeyTab, "lark / cli_same_more", "ws_reconnect"},
		{tea.KeyTab, "全部 App", ""},
		{tea.KeyShiftTab, "lark / cli_same_more", "ws_reconnect"},
		{tea.KeyShiftTab, "lark / cli_same", "pool_waiting"},
	} {
		_, cmd := m.Update(tea.KeyMsg{Type: tc.key})
		view := m.View()
		if cmd != nil || !slices.Contains(strings.Split(view, "\n"), "范围 · "+tc.scope) {
			t.Fatalf("switching to %q returned a command or showed the wrong scope: %s", tc.scope, view)
		}
		// Scroll past the account and timestamp in the canonical JSON line.
		m.review.SetXOffset(90)
		view = m.View()
		for _, metric := range []string{"pool_active", "pool_waiting", "ws_reconnect"} {
			if strings.Contains(view, metric) != (tc.metric == "" || tc.metric == metric) {
				t.Fatalf("unexpected visibility of %q in %q: %s", metric, tc.scope, view)
			}
		}
	}
	if logCalls != 3 || listCalls != 1 {
		t.Fatalf("switching telemetry scopes reread logs or inventory: logs=%d inventory=%d", logCalls, listCalls)
	}

	tuiKeys(m, tea.KeyEsc)
	staleTelemetry := pressServiceManageKey(m, "t")
	tuiKeys(m, tea.KeyEsc)
	staleResult := staleTelemetry()
	m.Update(staleResult)
	if m.screen != tuiServiceManagePick {
		t.Fatal("a telemetry result reopened the view after Escape")
	}
	freshTelemetry := pressServiceManageKey(m, "t")
	m.Update(staleResult)
	tuiKeys(m, tea.KeyTab)
	if view := m.View(); !strings.Contains(view, "正在读取实例遥测") || !strings.Contains(view, "范围 · 全部 App") || strings.Contains(view, "cli_same") {
		t.Fatalf("a fresh read retained the old snapshot or scope: %s", view)
	}
	telemetryLogs = `{"name":"pool_active","tenant":"lark","appId":"cli_fresh"` + metricRest
	m.Update(freshTelemetry())
	tuiKeys(m, tea.KeyTab)
	m.review.SetXOffset(90)
	if view := m.View(); !strings.Contains(view, "范围 · lark / cli_fresh") || !strings.Contains(view, "pool_active") || strings.Contains(view, "cli_same") {
		t.Fatalf("fresh telemetry did not replace the old snapshot: %s", view)
	}

	tuiKeys(m, tea.KeyEsc)
	logErr = errors.New("private log path")
	m.Update(pressServiceManageKey(m, "l")())
	if view := m.View(); !strings.Contains(view, "读取日志失败") || strings.Contains(view, "private log path") ||
		strings.Contains(view, "first") || strings.Contains(view, "last") {
		t.Fatalf("log error showed raw details or stale logs: %s", view)
	}
	tuiKeys(m, tea.KeyEsc, tea.KeyEsc)
	if m.screen != tuiBrowse {
		t.Fatal("could not return to configuration browsing from logs")
	}
}
