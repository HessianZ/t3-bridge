package main

import (
	"context"
	"slices"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type configTUILiveMeetings struct {
	account receiverProcessAccount
	find    func(receiverProcessAccount) (receiverProcess, error)
	operate liveMeetingOperation
	process string
	view    bridge.LocalMeetingView
	row     int
	confirm bridge.LocalMeetingRequest
	request int
	cancel  context.CancelFunc

	checkPermissions     func(context.Context, webSource, string, string) (bridge.MeetingPermissionReport, error)
	checkAvailability    func(context.Context, webSource, string, string) (bridge.MeetingPermissionReport, error)
	permissionSource     webSource
	checkingAvailability bool
	previousStatus       string
}

type configTUILiveMeetingsResult struct {
	request int
	process string
	view    bridge.LocalMeetingView
	err     error
}

func (m *configTUI) openLiveMeetings(find func(receiverProcessAccount) (receiverProcess, error), operate liveMeetingOperation) tea.Cmd {
	account, err := m.liveAccount()
	if err != nil {
		m.status = err.Error()
		return nil
	}
	s := &m.liveMeetings
	*s = configTUILiveMeetings{
		account: account, find: find, operate: operate, request: s.request,
		checkPermissions: s.checkPermissions, checkAvailability: s.checkAvailability,
	}
	return m.requestLiveMeetings(bridge.LocalMeetingRequest{Action: "status"})
}

func (m *configTUI) requestLiveMeetings(req bridge.LocalMeetingRequest) tea.Cmd {
	s := &m.liveMeetings
	ctx, cancel := context.WithTimeout(context.Background(), liveMeetingTimeout)
	s.request++
	s.cancel = cancel
	m.screen, m.status = tuiLiveMeetingsApplying, ""
	if req.Action == "status" {
		m.screen = tuiLiveMeetingsLoading
	}
	request, process, account, find, operate := s.request, s.process, s.account, s.find, s.operate
	return func() tea.Msg {
		defer cancel()
		result := configTUILiveMeetingsResult{request: request, process: process}
		if req.Action == "status" {
			target, err := find(account)
			result.process, result.err = target.ID, err
		}
		if result.err == nil {
			result.err = ctx.Err()
		}
		if result.err == nil {
			result.view, result.err = operate(ctx, result.process, account.Tenant, account.AppID, req)
		}
		return result
	}
}

func (m *configTUI) receiveLiveMeetings(msg configTUILiveMeetingsResult) {
	s := &m.liveMeetings
	if msg.request != s.request || (m.screen != tuiLiveMeetingsLoading && m.screen != tuiLiveMeetingsApplying) {
		return
	}
	applying := m.screen == tuiLiveMeetingsApplying
	s.cancel = nil
	m.screen = tuiLiveMeetingsPick
	if msg.err != nil {
		s.process = ""
		switch {
		case applying:
			m.status = "会议操作结果未确认。请核对飞书参会名单和等候室。"
		case msg.process == "":
			m.status = msg.err.Error() // Process lookup returns fixed messages.
		default:
			m.status = "暂时无法读取会议状态，请按 r 刷新。"
		}
		return
	}
	var selected string
	if len(s.view.Meetings) > 0 {
		selected = s.view.Meetings[s.row].Number
	}
	s.process, s.view = msg.process, msg.view
	s.row = max(0, slices.IndexFunc(s.view.Meetings, func(entry bridge.LocalMeetingEntry) bool { return entry.Number == selected }))
	if s.view.Outcome != "" {
		m.status = meetingOutcomeText(s.view.Outcome)
	}
}

func (m *configTUI) liveMeetingsKey(key string) (tea.Cmd, bool) {
	s := &m.liveMeetings
	switch m.screen {
	case tuiLiveMeetingsLoading:
		if key == "esc" {
			s.cancel()
			s.cancel = nil
			m.screen, m.status = tuiBrowse, ""
		}
	case tuiLiveMeetingsPick:
		switch key {
		case "esc":
			m.screen, m.status = tuiBrowse, ""
		case "r":
			return m.requestLiveMeetings(bridge.LocalMeetingRequest{Action: "status"}), true
		case "p":
			return m.requestMeetingPermissions(false), true
		case "up", "k":
			s.row = max(0, s.row-1)
		case "down":
			s.row = min(max(0, len(s.view.Meetings)-1), s.row+1)
		case "j":
			if s.process != "" && s.view.Available && s.view.Enabled {
				m.screen, m.status = tuiLiveMeetingsJoin, ""
				return m.openInput(""), true
			}
		case "l", "c":
			if s.process != "" && len(s.view.Meetings) > 0 {
				entry := s.view.Meetings[s.row]
				if action, _ := liveMeetingRowAction(key, entry.Phase, s.view.Available); action != "" {
					m.prepareLiveMeeting(bridge.LocalMeetingRequest{Action: action, Number: entry.Number, Revision: entry.Revision})
				}
			}
		}
	case tuiLiveMeetingsJoin:
		switch key {
		case "esc":
			m.input.Blur()
			m.screen, m.status = tuiLiveMeetingsPick, ""
		case "enter":
			if m.prepareLiveMeeting(bridge.LocalMeetingRequest{Action: "join", Number: m.input.Value()}) {
				m.input.Blur()
			}
		default:
			return nil, false
		}
	case tuiLiveMeetingsConfirm:
		switch key {
		case "esc":
			m.screen, m.status = tuiLiveMeetingsPick, ""
			if s.confirm.Action == "join" {
				m.screen = tuiLiveMeetingsJoin
				return m.input.Focus(), true
			}
		case "y":
			return m.requestLiveMeetings(s.confirm), true
		}
	case tuiMeetingPermissionsLoading, tuiMeetingPermissionsResult:
		if m.screen == tuiMeetingPermissionsResult && key == "v" {
			return m.requestMeetingPermissions(true), true
		}
		if key != "esc" {
			return nil, m.screen == tuiMeetingPermissionsLoading
		}
		if s.cancel != nil {
			s.cancel()
			s.cancel = nil
		}
		m.screen, m.status = tuiLiveMeetingsPick, s.previousStatus
	}
	return nil, true
}

func (m *configTUI) prepareLiveMeeting(req bridge.LocalMeetingRequest) bool {
	if err := req.Validate(); err != nil {
		m.status = "会议回执无法使用。请按 r 刷新。"
		if req.Action == "join" {
			m.status = "请输入恰好 9 位数字的会议号。"
		}
		return false
	}
	m.liveMeetings.confirm = req
	m.screen, m.status = tuiLiveMeetingsConfirm, ""
	return true
}

func liveMeetingRowAction(key, phase string, available bool) (action, hint string) {
	switch {
	case key == "l" && phase == "join_accepted" && available:
		return "leave", "l 离会"
	case key == "c" && (phase == "join_accepted" || phase == "join_pending" || phase == "leave_pending"):
		return "confirm-left", "c 确认已离会"
	default:
		return "", ""
	}
}

func (m *configTUI) liveMeetingAppText() string {
	account := m.liveMeetings.account
	return "App · " + configTUIPlain(strings.ReplaceAll(account.Tenant+"/"+account.AppID, "\n", " "))
}

func (m *configTUI) liveMeetingsView() (title, body, shortcuts, actions string) {
	s := &m.liveMeetings
	title = m.language.text("在线会议")
	app := m.liveMeetingAppText()
	wrap := func(value string) string { return ansi.Hardwrap(value, m.width, true) }
	line := func(value string) string { return ansi.Truncate(value, m.width, "…") }
	switch m.screen {
	case tuiLiveMeetingsLoading:
		return title, wrap(app + m.language.text("\n正在读取会议状态…")), m.language.text("Esc 返回"), ""
	case tuiLiveMeetingsApplying:
		return title, wrap(app + m.language.text("\n正在等待接收进程确认…")), "", ""
	case tuiLiveMeetingsJoin:
		return m.language.text("输入 9 位会议号"), wrap(app) + "\n" + m.input.View(), m.language.text("Enter 核对 · Esc 返回"), ""
	case tuiLiveMeetingsConfirm:
		body = app + m.language.text("\n会议号 · ") + s.confirm.Number + m.language.text("\n进程 · ") + s.process
		switch s.confirm.Action {
		case "join":
			title = m.language.text("确认入会")
			body += m.language.text("\n机器人入会后，参会者可见。")
		case "leave":
			title = m.language.text("确认离会")
			body += m.language.text("\n确认后向飞书发送离会请求。")
		case "confirm-left":
			title = m.language.text("确认机器人不在会中")
			body += m.language.text("\n请先确认机器人不在飞书参会名单和等候室。\n确认后仅关闭本机回执，不调用飞书接口。")
		}
		return title, wrap(body), m.language.text("y 确认 · Esc 返回"), ""
	}
	shortcuts = m.language.text("p 查权限 · r 刷新 · Esc 返回")
	lines := []string{line(app)}
	if s.process == "" {
		return title, strings.Join(append(lines, line(m.language.text("会议状态暂不可用。"))), "\n"), shortcuts, ""
	}
	switch {
	case !s.view.Available:
		lines = append(lines, line(m.language.text("飞书会议接口暂不可用。")))
	case s.view.Enabled:
		actions = m.language.text("j 入会")
	default:
		lines = append(lines, line(m.language.text("当前接收端未开启会议入会。")))
	}
	meetings := s.view.Meetings
	if len(meetings) == 0 {
		lines = append(lines, line(m.language.text("没有会议回执。")))
	} else {
		shortcuts += " · ↑↓"
		for _, key := range []string{"l", "c"} {
			if _, hint := liveMeetingRowAction(key, meetings[s.row].Phase, s.view.Available); hint != "" {
				if actions != "" {
					actions += " · "
				}
				actions += hint
			}
		}
		height := max(1, m.bodyHeight()-len(lines))
		first := max(0, s.row-height+1)
		for i := first; i < min(len(meetings), first+height); i++ {
			entry := meetings[i]
			marker := "  "
			if i == s.row {
				marker = "› "
			}
			lines = append(lines, line(marker+entry.Number+" · "+meetingPhaseText(entry.Phase)+" · "+meetingOriginText(entry.Origin)))
		}
	}
	return title, strings.Join(lines, "\n"), shortcuts, actions
}
