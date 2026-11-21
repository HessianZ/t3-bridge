package main

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type configTUILiveAccess struct {
	account          receiverProcessAccount
	process          string
	operate          liveAccessOperation
	view             bridge.LocalAccessPolicyView
	row, scope, mode int
	target           bridge.LocalAccessPolicyEntry
	request          int
	cancel           context.CancelFunc
	applying         bool
}

type configTUILiveAccessResult struct {
	request int
	process string
	view    bridge.LocalAccessPolicyView
	err     error
}

func (m *configTUI) openLiveAccess(list func() ([]receiverProcess, error), operate liveAccessOperation) tea.Cmd {
	account, err := m.liveAccount()
	if err != nil {
		m.status = err.Error()
		return nil
	}
	m.liveAccess = configTUILiveAccess{account: account, operate: operate, request: m.liveAccess.request}
	return m.requestLiveAccess(bridge.LocalAccessPolicyRequest{}, list)
}

func liveAccessProcess(list func() ([]receiverProcess, error), account receiverProcessAccount) (string, error) {
	processes, err := list()
	if err != nil {
		return "", err
	}
	var id string
	for _, process := range processes {
		if !process.AccessPolicy || !slices.Contains(process.Accounts, account) {
			continue
		}
		if id != "" {
			return "", errors.New("多个在线进程匹配，请用 config live-access -process 指定")
		}
		id = process.ID
	}
	if id == "" {
		return "", errors.New("当前账号没有支持在线用户权限的接收进程")
	}
	return id, nil
}

func (m *configTUI) requestLiveAccess(req bridge.LocalAccessPolicyRequest, list func() ([]receiverProcess, error)) tea.Cmd {
	s := &m.liveAccess
	ctx, cancel := context.WithTimeout(context.Background(), receiverIPCTimeout)
	s.request++
	s.cancel, s.applying = cancel, req != (bridge.LocalAccessPolicyRequest{})
	m.screen, m.status = tuiLiveAccessLoading, ""
	request, process, account, operate := s.request, s.process, s.account, s.operate
	return func() tea.Msg {
		defer cancel()
		result := configTUILiveAccessResult{request: request, process: process}
		if list != nil {
			result.process, result.err = liveAccessProcess(list, account)
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

func (m *configTUI) receiveLiveAccess(msg configTUILiveAccessResult) {
	s := &m.liveAccess
	if m.screen != tuiLiveAccessLoading || msg.request != s.request {
		return
	}
	s.cancel = nil
	if msg.err != nil {
		m.screen, m.status = tuiBrowse, msg.err.Error()
		if s.applying {
			m.status = "在线更新未确认，请按 F12 重新读取生效值 · " + m.status
		}
		return
	}
	selected := s.target
	if !s.applying && len(s.view.Entries) > 0 {
		selected = s.view.Entries[s.row]
	}
	s.process, s.view = msg.process, msg.view
	s.row = max(0, s.entryIndex(selected))
	m.screen = tuiLiveAccessPick
	if s.applying {
		m.status = "在线用户权限已更新"
	}
}

func (s *configTUILiveAccess) entryIndex(target bridge.LocalAccessPolicyEntry) int {
	return slices.IndexFunc(s.view.Entries, func(entry bridge.LocalAccessPolicyEntry) bool {
		return entry.Kind == target.Kind && entry.RouteID == target.RouteID && entry.UserID == target.UserID
	})
}

func (s *configTUILiveAccess) scopeEntry(index int) bridge.LocalAccessPolicyEntry {
	switch index {
	case 0:
		return bridge.LocalAccessPolicyEntry{Kind: "user", Override: "inherit"}
	case 1:
		return bridge.LocalAccessPolicyEntry{Kind: "admin", Override: "inherit"}
	default:
		return bridge.LocalAccessPolicyEntry{Kind: "route", RouteID: s.view.RouteIDs[index-2], Override: "inherit"}
	}
}

var liveAccessModes = []string{"inherit", "allow", "deny"}

func liveAccessChange(target bridge.LocalAccessPolicyEntry, mode string) bridge.LocalAccessPolicyRequest {
	return bridge.LocalAccessPolicyRequest{Kind: target.Kind, UserID: target.UserID, RouteID: target.RouteID, Mode: mode}
}

func (m *configTUI) editLiveAccess(target bridge.LocalAccessPolicyEntry) {
	if err := liveAccessChange(target, target.Override).ValidateChange(); err != nil {
		m.status = err.Error()
		return
	}
	m.input.Blur()
	m.liveAccess.target = target
	m.liveAccess.mode = slices.Index(liveAccessModes, target.Override)
	m.screen, m.status = tuiLiveAccessEdit, ""
}

func (m *configTUI) liveAccessKey(key string) (tea.Cmd, bool) {
	s := &m.liveAccess
	switch m.screen {
	case tuiLiveAccessLoading:
		if key == "esc" && !s.applying {
			s.cancel()
			s.cancel = nil
			m.screen = tuiBrowse
		}
	case tuiLiveAccessPick:
		switch key {
		case "esc":
			m.screen, m.status = tuiBrowse, ""
		case "up", "k":
			s.row = max(0, s.row-1)
		case "down", "j":
			s.row = min(max(0, len(s.view.Entries)-1), s.row+1)
		case "home":
			s.row = 0
		case "end":
			s.row = max(0, len(s.view.Entries)-1)
		case "r":
			return m.requestLiveAccess(bridge.LocalAccessPolicyRequest{}, nil), true
		case "a":
			s.scope = 0
			m.screen, m.status = tuiLiveAccessScope, ""
		case "enter":
			if len(s.view.Entries) > 0 {
				m.editLiveAccess(s.view.Entries[s.row])
			}
		}
	case tuiLiveAccessScope:
		switch key {
		case "esc":
			m.screen, m.status = tuiLiveAccessPick, ""
		case "up", "k":
			s.scope = max(0, s.scope-1)
		case "down", "j":
			s.scope = min(len(s.view.RouteIDs)+1, s.scope+1)
		case "enter":
			m.screen, m.status = tuiLiveAccessUser, ""
			return m.openInput(""), true
		}
	case tuiLiveAccessUser:
		switch key {
		case "esc":
			m.input.Blur()
			m.screen, m.status = tuiLiveAccessScope, ""
		case "enter":
			target := s.scopeEntry(s.scope)
			target.UserID = strings.TrimSpace(m.input.Value())
			if i := s.entryIndex(target); i >= 0 {
				target = s.view.Entries[i]
			}
			m.editLiveAccess(target)
		default:
			return nil, false
		}
	case tuiLiveAccessEdit:
		switch key {
		case "esc":
			m.screen, m.status = tuiLiveAccessPick, ""
		case "up", "k":
			s.mode = max(0, s.mode-1)
		case "down", "j":
			s.mode = min(len(liveAccessModes)-1, s.mode+1)
		case "enter":
			if liveAccessModes[s.mode] == s.target.Override {
				m.screen, m.status = tuiLiveAccessPick, "未修改在线权限"
			} else {
				m.screen, m.status = tuiLiveAccessConfirm, ""
			}
		}
	case tuiLiveAccessConfirm:
		switch key {
		case "esc":
			m.screen, m.status = tuiLiveAccessEdit, ""
		case "y":
			return m.requestLiveAccess(liveAccessChange(s.target, liveAccessModes[s.mode]), nil), true
		}
	}
	return nil, true
}

func (m *configTUI) liveAccessView() (title, body, shortcuts, actions string) {
	s := &m.liveAccess
	app := s.account.Tenant + "/" + s.account.AppID
	title = m.language.text("在线用户权限 · ") + app
	line := func(value string) string {
		return ansi.Truncate(configTUIPlain(strings.ReplaceAll(value, "\n", " ")), m.width, "…")
	}
	switch m.screen {
	case tuiLiveAccessLoading:
		if s.applying {
			return title, m.language.text("正在等待接收进程确认更新…"), "", ""
		}
		return title, m.language.text("正在读取在线用户权限…"), m.language.text("Esc 取消"), ""
	case tuiLiveAccessScope:
		lines := []string{m.language.text("选择权限类型")}
		height := max(1, m.bodyHeight()-len(lines))
		first := max(0, s.scope-height+1)
		for i := first; i < min(len(s.view.RouteIDs)+2, first+height); i++ {
			lines = append(lines, line(liveSettingMarker(i == s.scope)+liveAccessScopeLabel(s.scopeEntry(i))))
		}
		return title, strings.Join(lines, "\n"), m.language.text("↑↓ 选择 · Enter 输入 Open ID · Esc 返回"), ""
	case tuiLiveAccessUser:
		return title, line(liveAccessScopeLabel(s.scopeEntry(s.scope))) + "\n" + m.input.View(), m.language.text("输入应用 Open ID · Enter 继续 · Esc 返回"), ""
	case tuiLiveAccessEdit:
		lines := []string{line(liveAccessScopeLabel(s.target)), line("Open ID · " + s.target.UserID), line(liveAccessEntryState(s.target))}
		for i, mode := range liveAccessModes {
			lines = append(lines, line(liveSettingMarker(i == s.mode)+liveAccessModeLabel(mode)))
		}
		return title, strings.Join(lines, "\n"), m.language.text("↑↓ 选覆盖规则 · Enter 预览修改 · Esc 返回"), ""
	case tuiLiveAccessConfirm:
		lines := []string{
			"App · " + app, m.language.text("进程 · ") + s.process,
			m.language.text("权限 · ") + liveAccessScopeLabel(s.target), "Open ID · " + s.target.UserID,
			m.language.text("覆盖 · ") + liveAccessModeLabel(s.target.Override) + " → " + liveAccessModeLabel(liveAccessModes[s.mode]),
		}
		for i, value := range lines {
			lines[i] = ansi.Hardwrap(configTUIPlain(strings.ReplaceAll(value, "\n", " ")), m.width, true)
		}
		return m.language.text("确认在线权限修改"), strings.Join(lines, "\n"), m.language.text("y 确认即时应用 · Esc 返回"), m.language.text("操作写入此 App 的运行状态")
	}
	owner := s.view.OwnerID
	if owner == "" {
		owner = m.language.text("未识别")
	}
	lines := []string{line(m.language.text("进程 · ") + s.process), line(m.language.text("应用所有者 · ") + owner)}
	if len(s.view.Entries) == 0 {
		lines = append(lines, m.language.text("没有可显示的权限规则。"))
	}
	height := max(1, (m.bodyHeight()-len(lines))/2)
	first := max(0, s.row-height+1)
	for i := first; i < min(len(s.view.Entries), first+height); i++ {
		entry := s.view.Entries[i]
		label := liveAccessScopeLabel(entry) + " · " + entry.UserID
		lines = append(lines, line(liveSettingMarker(i == s.row)+label), line("  "+liveAccessEntryState(entry)))
	}
	return title, strings.Join(lines, "\n"), m.language.text("↑↓ 移动 · Enter 编辑 · a 添加规则 · r 刷新 · Esc 返回草稿"), m.language.text("生效值仅表示该层权限")
}

func liveAccessScopeLabel(entry bridge.LocalAccessPolicyEntry) string {
	switch entry.Kind {
	case "user":
		return "用户"
	case "admin":
		return "管理员"
	default:
		return "路由 · " + entry.RouteID
	}
}

func liveAccessModeLabel(mode string) string {
	switch mode {
	case "allow":
		return "授权"
	case "deny":
		return "拒绝"
	default:
		return "跟随配置"
	}
}

func liveAccessEntryState(entry bridge.LocalAccessPolicyEntry) string {
	configured, effective := "未列入", "未授权"
	if entry.Configured {
		configured = "已列入"
	}
	if entry.Effective {
		effective = "已授权"
	}
	return "配置 " + configured + " · 覆盖 " + liveAccessModeLabel(entry.Override) + " · 生效 " + effective
}
