package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

const serviceManageAllTelemetryScope = "范围 · 全部 App"

type configTUIServiceManage struct {
	inventory func() ([]serviceInventoryEntry, error)
	readLogs  func(string, bool) (string, error)

	entries         []serviceInventoryEntry
	row, request    int
	listFailed      bool
	checkUnregister bool
	telemetry       bool
	action, label   string

	telemetrySnapshot string
	telemetryAccounts []receiverProcessAccount
	telemetryScope    int
}

type configTUIServiceManageInventory struct {
	request int
	entries []serviceInventoryEntry
	err     error
}

type configTUIServiceManageResult struct{ err error }

type configTUIServiceManageLogResult struct {
	request int
	logs    string
	err     error
}

func (m *configTUI) openServiceManage() tea.Cmd {
	m.serviceManage.entries, m.serviceManage.row, m.status = nil, 0, ""
	return m.refreshServiceManage()
}

func (m *configTUI) refreshServiceManage() tea.Cmd {
	s := &m.serviceManage
	m.screen = tuiServiceManageLoading
	s.request++
	request, inventory := s.request, s.inventory
	return func() tea.Msg {
		entries, err := inventory()
		return configTUIServiceManageInventory{request: request, entries: entries, err: err}
	}
}

func (m *configTUI) receiveServiceManageInventory(msg configTUIServiceManageInventory) {
	s := &m.serviceManage
	if m.screen != tuiServiceManageLoading || msg.request != s.request {
		return
	}
	m.screen = tuiServiceManagePick
	var selected string
	if len(s.entries) != 0 {
		selected = s.entries[s.row].name
	}
	s.row, s.entries, s.listFailed = 0, nil, msg.err != nil
	checkUnregister := s.checkUnregister
	s.checkUnregister = false
	if s.listFailed {
		if checkUnregister {
			m.status = "注销未确认，清单读取失败。"
		}
		return
	}
	s.entries = msg.entries
	for i, entry := range s.entries {
		if entry.name == selected {
			s.row = i
			if checkUnregister {
				m.status = "注销未确认，清单仍有该实例。"
			}
			return
		}
	}
	if checkUnregister {
		m.status = "已注销实例 " + selected + "。"
	}
}

func (m *configTUI) serviceManageKey(key string) (tea.Cmd, bool) {
	s := &m.serviceManage
	switch m.screen {
	case tuiServiceManageLoading, tuiServiceManagePick:
		if key == "esc" {
			s.checkUnregister = false
			m.screen, m.status = tuiBrowse, ""
			return nil, true
		}
	case tuiServiceManageConfirm, tuiServiceManageLogs:
		if key == "esc" {
			m.screen, m.status = tuiServiceManagePick, ""
			return nil, true
		}
	}
	switch m.screen {
	case tuiServiceManagePick:
		switch key {
		case "r":
			m.status = ""
			return m.refreshServiceManage(), true
		case "up", "k":
			s.row = max(0, s.row-1)
			return nil, true
		case "down", "j":
			s.row = min(max(0, len(s.entries)-1), s.row+1)
			return nil, true
		}
		if len(s.entries) == 0 {
			return nil, true
		}
		entry := s.entries[s.row]
		if entry.err != nil || entry.manage == nil {
			return nil, true
		}
		switch key {
		case "l", "t":
			return m.openServiceManageLogs(entry.name, key == "t"), true
		case "s":
			s.action, s.label = "start", "启动"
		case "x":
			s.action, s.label = "stop", "停止"
		case "R":
			s.action, s.label = "restart", "重启"
		case "u":
			s.action, s.label = "unregister", "注销"
		default:
			return nil, true
		}
		m.screen, m.status = tuiServiceManageConfirm, ""
	case tuiServiceManageConfirm:
		if key == "y" {
			m.screen = tuiServiceManageApplying
			manage, action := s.entries[s.row].manage, s.action
			return func() tea.Msg { return configTUIServiceManageResult{err: manage(action)} }, true
		}
	case tuiServiceManageLogs:
		if s.telemetry {
			switch key {
			case "tab":
				m.cycleServiceManageTelemetry(1)
				return nil, true
			case "shift+tab":
				m.cycleServiceManageTelemetry(-1)
				return nil, true
			}
		}
		return nil, false
	}
	return nil, true
}

func (m *configTUI) receiveServiceManageResult(msg configTUIServiceManageResult) tea.Cmd {
	if m.screen != tuiServiceManageApplying {
		return nil
	}
	s := &m.serviceManage
	switch {
	case msg.err != nil:
		m.status = s.label + "未确认，请核对刷新后的实例状态。"
	case s.action == "unregister":
		s.checkUnregister = true
		m.status = "正在核对注销结果…"
	default:
		m.status = "已提交" + s.label + "请求。"
	}
	return m.refreshServiceManage()
}

func (m *configTUI) openServiceManageLogs(instance string, telemetry bool) tea.Cmd {
	s := &m.serviceManage
	s.telemetry = telemetry
	s.telemetrySnapshot, s.telemetryAccounts, s.telemetryScope = "", nil, 0
	m.screen, m.status = tuiServiceManageLogs, ""
	m.review.Height = m.bodyHeight()
	loading := "正在读取实例日志…"
	if telemetry {
		loading = "正在读取实例遥测…"
		m.status = serviceManageAllTelemetryScope
	}
	m.setServiceManageLogContent(loading)
	s.request++
	request, readLogs := s.request, s.readLogs
	return func() tea.Msg {
		logs, err := readLogs(instance, telemetry)
		return configTUIServiceManageLogResult{request: request, logs: logs, err: err}
	}
}

func (m *configTUI) receiveServiceManageLogs(msg configTUIServiceManageLogResult) {
	s := &m.serviceManage
	if m.screen != tuiServiceManageLogs || msg.request != s.request {
		return
	}
	switch {
	case msg.err != nil:
		m.status = "读取日志失败，请核对实例是否仍有效及本机日志权限。"
		msg.logs = ""
	case strings.TrimSpace(msg.logs) == "":
		msg.logs = "暂无记录。"
	case s.telemetry:
		s.telemetrySnapshot = msg.logs
		msg.logs, s.telemetryAccounts = serviceTelemetryView(s.telemetrySnapshot, receiverProcessAccount{})
	}
	m.setServiceManageLogContent(msg.logs)
}

func (m *configTUI) cycleServiceManageTelemetry(step int) {
	s := &m.serviceManage
	if len(s.telemetryAccounts) == 0 {
		return
	}
	scopes := len(s.telemetryAccounts) + 1
	s.telemetryScope = (s.telemetryScope + step + scopes) % scopes
	var selected receiverProcessAccount
	m.status = serviceManageAllTelemetryScope
	if s.telemetryScope > 0 {
		selected = s.telemetryAccounts[s.telemetryScope-1]
		m.status = "范围 · " + selected.Tenant + " / " + selected.AppID
	}
	body, _ := serviceTelemetryView(s.telemetrySnapshot, selected)
	m.setServiceManageLogContent(body)
}

func (m *configTUI) setServiceManageLogContent(content string) {
	m.review.SetContent(configTUIPlain(content))
	m.review.SetXOffset(0)
	m.review.GotoTop()
}

func (m *configTUI) serviceManageView() (title, body, shortcuts, actions string) {
	s := &m.serviceManage
	title = m.language.text("管理后台服务实例")
	switch m.screen {
	case tuiServiceManageLoading:
		return title, m.language.text("正在读取本机实例清单…"), m.language.text("Esc 返回配置"), ""
	case tuiServiceManagePick:
		shortcuts = m.language.text("↑↓ 选择 · r 刷新 · Esc 返回配置")
		switch {
		case s.listFailed:
			body = m.language.text("无法读取本机服务实例清单。")
		case len(s.entries) == 0:
			body = m.language.text("未找到受管服务实例。")
		default:
			body = m.serviceManageList()
			entry := s.entries[s.row]
			if entry.err == nil && entry.manage != nil {
				actions = m.language.text("u 注销 · l 查看日志 · t 查看遥测 · s 启动 · x 停止 · R 重启")
			}
		}
		return title, body, shortcuts, actions
	case tuiServiceManageConfirm:
		if s.action == "unregister" {
			body = strings.Join([]string{
				m.language.text("实例名 · ") + s.entries[s.row].name,
				m.language.text("整个实例将停止。"),
				m.language.text("实例内所有 App 将停止接收。"),
				m.language.text("服务定义将移除。"),
				m.language.text("配置、凭据、状态和日志保留。"),
				m.language.text("T3 在途 run 可能继续。"),
			}, "\n")
			return m.language.text("确认注销服务实例"), ansi.Hardwrap(configTUIPlain(body), m.width, true), m.language.text("y 确认 · Esc 取消"), ""
		}
		autostart := m.language.text("此操作会开启登录自启。")
		if s.action == "stop" {
			autostart = m.language.text("此操作会关闭登录自启。")
		}
		body = strings.Join([]string{
			m.serviceManageLine(m.language.text("实例名 · ") + s.entries[s.row].name),
			m.serviceManageLine(m.language.text("此操作影响整个实例及其中的所有账号。")),
			m.serviceManageLine(autostart),
		}, "\n")
		return m.language.text("确认") + s.label + m.language.text("服务实例"), body, m.language.text("y 确认 · Esc 取消"), ""
	case tuiServiceManageApplying:
		return m.language.text("正在") + s.label + m.language.text("服务实例…"), m.serviceManageLine(m.language.text("实例名 · ") + s.entries[s.row].name), "", ""
	default:
		title = m.language.text("实例日志 · ") + s.entries[s.row].name
		shortcuts = m.language.text("↑↓←→ 滚动 · Esc 返回实例清单")
		if s.telemetry {
			title = m.language.text("实例遥测 · ") + s.entries[s.row].name
			shortcuts = m.language.text("Tab/Shift+Tab 切换 · ↑↓←→ · Esc 返回")
		}
		actions = fmt.Sprintf(m.language.text("只读 · 每个日志流最多最近 %d 行"), serviceLogViewerLines)
		return title, m.review.View(), shortcuts, actions
	}
}

func (m *configTUI) serviceManageList() string {
	s := &m.serviceManage
	height := m.bodyHeight()
	first := max(0, s.row-height+1)
	var lines []string
	for i, entry := range s.entries[first:min(len(s.entries), first+height)] {
		marker := "  "
		if first+i == s.row {
			marker = "› "
		}
		status, pid := entry.status, "—"
		switch {
		case entry.err != nil || entry.manage == nil:
			status = "不可管理（无法核实归属或状态）"
		case status == "":
			status = "状态未知"
		}
		if entry.pid > 0 {
			pid = fmt.Sprint(entry.pid)
		}
		lines = append(lines, m.serviceManageLine(marker+entry.name+" · PID "+pid+" · "+status))
	}
	return strings.Join(lines, "\n")
}

func (m *configTUI) serviceManageLine(line string) string {
	return ansi.Truncate(configTUIPlain(strings.ReplaceAll(line, "\n", " ")), m.width, "…")
}
