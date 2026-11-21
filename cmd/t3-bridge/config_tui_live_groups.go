package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type configTUILiveGroups struct {
	account     receiverProcessAccount
	process     string
	userGroupID string
	operate     liveGroupOperation
	view        bridge.GroupAccessView
	row, choice int
	filter      string
	target      bridge.GroupAccessEntry
	marked      []bridge.GroupAccessEntry
	batchGrant  bool
	batchReview bool
	request     int
	cancel      context.CancelFunc
	applying    bool
}

type configTUILiveGroupsResult struct {
	request    int
	process    string
	batchGrant bool
	view       bridge.GroupAccessView
	err        error
}

const userGroupLiveUnavailable = "所选群的在线权限暂不可用，请核对运行实例并按 r 刷新入群状态"

func (m *configTUI) openLiveGroups(list func() ([]receiverProcess, error), operate liveGroupOperation) tea.Cmd {
	identity, err := m.liveAccount()
	if err != nil {
		m.status = err.Error()
		return nil
	}
	return m.startLiveGroups(identity, "", list, operate)
}

func (m *configTUI) startLiveGroups(account receiverProcessAccount, userGroupID string, list func() ([]receiverProcess, error), operate liveGroupOperation) tea.Cmd {
	m.liveGroups = configTUILiveGroups{account: account, userGroupID: userGroupID, operate: operate, request: m.liveGroups.request}
	return m.requestLiveGroups(bridge.GroupAccessRequest{}, list)
}

func (m *configTUI) liveAccount() (receiverProcessAccount, error) {
	account := m.accounts[m.account]
	identity := receiverProcessAccount{Tenant: "feishu"}
	appErr := json.Unmarshal(account["appId"], &identity.AppID)
	var tenantErr error
	if raw := account["tenant"]; len(raw) != 0 {
		tenantErr = json.Unmarshal(raw, &identity.Tenant)
	}
	if identity.Tenant == "" {
		identity.Tenant = "feishu"
	}
	if appErr != nil || tenantErr != nil || identity.AppID == "" || strings.TrimSpace(identity.AppID) != identity.AppID ||
		(identity.Tenant != "feishu" && identity.Tenant != "lark") {
		return receiverProcessAccount{}, errors.New("请核对当前账号的应用 ID 和租户")
	}
	return identity, nil
}

func liveGroupProcess(list func() ([]receiverProcess, error), account receiverProcessAccount) (receiverProcess, error) {
	processes, err := list()
	if err != nil {
		return receiverProcess{}, err
	}
	var match receiverProcess
	for _, process := range processes {
		if !process.GroupAccess || !slices.Contains(process.Accounts, account) {
			continue
		}
		if match.ID != "" {
			return receiverProcess{}, errors.New("多个在线进程匹配，请用 config live-groups -process 指定")
		}
		match = process
	}
	if match.ID == "" {
		return receiverProcess{}, errors.New("当前账号没有支持在线群权限的运行实例")
	}
	return match, nil
}

func (m *configTUI) requestLiveGroups(req bridge.GroupAccessRequest, list func() ([]receiverProcess, error)) tea.Cmd {
	g := &m.liveGroups
	ctx, cancel := context.WithTimeout(context.Background(), receiverGroupTimeout)
	g.request++
	g.cancel, g.applying = cancel, req.Access != "" || req.Mention != ""
	m.screen, m.status = tuiLiveGroupsLoading, ""
	request, process, account, operate := g.request, g.process, g.account, g.operate
	batchGrant := g.batchGrant
	return func() tea.Msg {
		defer cancel()
		result := configTUILiveGroupsResult{request: request, process: process, batchGrant: batchGrant}
		if list != nil {
			match, err := liveGroupProcess(list, account)
			result.process, result.batchGrant, result.err = match.ID, match.GroupBatchGrant, err
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

func (m *configTUI) receiveLiveGroups(msg configTUILiveGroupsResult) {
	g := &m.liveGroups
	if m.screen != tuiLiveGroupsLoading || msg.request != g.request {
		return
	}
	g.cancel = nil
	if msg.err != nil {
		status := msg.err.Error()
		switch {
		case g.userGroupID != "" && g.applying:
			status = "在线更新未确认，请重新打开在线群权限并核对目标群 · " + status
		case g.userGroupID != "":
			status = userGroupLiveUnavailable
		case g.applying:
			status = "在线更新未确认，请按 F9 重新查询 · " + status
		}
		m.leaveLiveGroups(status)
		return
	}
	var selectedID string
	if g.process == "" {
		selectedID = g.userGroupID
	} else if g.row > 0 {
		selectedID = g.filtered()[g.row-1].ID
	}
	g.process, g.view, g.batchGrant = msg.process, msg.view, msg.batchGrant
	if g.batchReview || !g.batchAvailable() {
		g.marked = nil
	} else {
		marked := g.marked[:0]
		for _, previous := range g.marked {
			i := slices.IndexFunc(g.view.Groups, func(group bridge.GroupAccessEntry) bool { return group.ID == previous.ID })
			if i >= 0 && liveGroupGrantEligible(g.view.Groups[i]) {
				marked = append(marked, g.view.Groups[i])
			}
		}
		g.marked = marked
	}
	g.batchReview = false
	groups := g.filtered()
	g.row = slices.IndexFunc(groups, func(group bridge.GroupAccessEntry) bool { return group.ID == selectedID }) + 1
	if g.applying {
		m.status = "在线群权限已更新"
	}
	if g.userGroupID != "" && selectedID == g.userGroupID &&
		(!g.view.JoinedAvailable || g.row == 0 || !groups[g.row-1].Joined) {
		status := userGroupLiveUnavailable
		if g.applying {
			status = m.status + " · " + status
		}
		m.leaveLiveGroups(status)
		return
	}
	m.screen = tuiLiveGroupsPick
}

func (m *configTUI) leaveLiveGroups(status string) {
	m.screen, m.status = tuiBrowse, status
	if m.liveGroups.userGroupID != "" {
		m.screen = tuiUserGroupsPick
	}
}

func (g *configTUILiveGroups) filtered() []bridge.GroupAccessEntry {
	if g.filter == "" {
		return g.view.Groups
	}
	query := strings.ToLower(g.filter)
	var matches []bridge.GroupAccessEntry
	for _, group := range g.view.Groups {
		if strings.Contains(strings.ToLower(group.Name), query) || strings.Contains(strings.ToLower(group.ID), query) {
			matches = append(matches, group)
		}
	}
	return matches
}

var configTUILiveGroupActions = []struct{ label, access, mention string }{
	{label: "授权群聊", access: "allow"},
	{label: "撤销群授权", access: "deny"},
	{label: "恢复群授权配置", access: "inherit"},
	{label: "需要 @ 机器人", mention: "on"},
	{label: "无需 @ 机器人", mention: "off"},
	{label: "恢复默认 @ 规则", mention: "inherit"},
}

func (g *configTUILiveGroups) firstAction() int {
	if g.target.ID == "" {
		return 3
	}
	return 0
}

func (g *configTUILiveGroups) batchAvailable() bool {
	return g.userGroupID == "" && g.batchGrant && g.view.JoinedAvailable
}

func liveGroupGrantEligible(group bridge.GroupAccessEntry) bool {
	return group.ID != "" && group.Joined && !group.Allowed
}

func (m *configTUI) liveGroupsKey(key string) (tea.Cmd, bool) {
	g := &m.liveGroups
	switch m.screen {
	case tuiLiveGroupsLoading:
		if key == "esc" && !g.applying {
			g.cancel()
			g.cancel = nil
			m.leaveLiveGroups("")
		}
	case tuiLiveGroupsPick:
		groups := g.filtered()
		switch key {
		case "esc":
			m.leaveLiveGroups("")
		case "up", "k":
			g.row = max(0, g.row-1)
		case "down", "j":
			g.row = min(len(groups), g.row+1)
		case "home":
			g.row = 0
		case "end":
			g.row = len(groups)
		case "r":
			return m.requestLiveGroups(bridge.GroupAccessRequest{}, nil), true
		case "/":
			m.screen = tuiLiveGroupsFilter
			return m.openInput(g.filter), true
		case " ":
			if g.batchAvailable() && g.row > 0 {
				group := groups[g.row-1]
				if liveGroupGrantEligible(group) {
					i := slices.IndexFunc(g.marked, func(marked bridge.GroupAccessEntry) bool { return marked.ID == group.ID })
					m.status = ""
					switch {
					case i >= 0:
						g.marked = slices.Delete(g.marked, i, i+1)
					case len(g.marked) == bridge.MaxGroupGrantBatch:
						m.status = fmt.Sprintf("批量授权最多选择 %d 个群", bridge.MaxGroupGrantBatch)
					default:
						g.marked = append(g.marked, group)
					}
				}
			}
		case "b":
			if g.batchAvailable() && len(g.marked) > 0 {
				g.batchReview, g.choice = true, 0
				m.screen, m.status = tuiLiveGroupsConfirm, ""
			}
		case "enter":
			g.target = bridge.GroupAccessEntry{}
			if g.row > 0 {
				g.target = groups[g.row-1]
				if g.target.ID == "" {
					m.status = "所选群缺少 ID，无法应用群权限"
					return nil, true
				}
			}
			g.choice = g.firstAction()
			m.screen, m.status = tuiLiveGroupsConfirm, ""
		}
	case tuiLiveGroupsFilter:
		switch key {
		case "enter":
			g.filter = strings.TrimSpace(m.input.Value())
			g.row = 0
		case "esc":
		default:
			return nil, false
		}
		m.input.Blur()
		m.screen = tuiLiveGroupsPick
	case tuiLiveGroupsConfirm:
		if g.batchReview {
			return m.liveGroupsBatchKey(key), true
		}
		switch key {
		case "esc":
			m.screen = tuiLiveGroupsPick
		case "up", "k":
			g.choice = max(g.firstAction(), g.choice-1)
		case "down", "j":
			g.choice = min(len(configTUILiveGroupActions)-1, g.choice+1)
		case "y":
			action := configTUILiveGroupActions[g.choice]
			return m.requestLiveGroups(bridge.GroupAccessRequest{ChatID: g.target.ID, Access: action.access, Mention: action.mention}, nil), true
		}
	}
	return nil, true
}

func (m *configTUI) liveGroupsBatchKey(key string) tea.Cmd {
	g := &m.liveGroups
	switch key {
	case "esc":
		g.batchReview = false
		m.screen, m.status = tuiLiveGroupsPick, ""
	case "y":
		req := bridge.GroupAccessRequest{ChatIDs: make([]string, 0, len(g.marked)), Access: "allow"}
		for _, group := range g.marked {
			req.ChatIDs = append(req.ChatIDs, group.ID)
		}
		if err := req.ValidateBatchGrant(); err != nil {
			m.status = err.Error()
			return nil
		}
		return m.requestLiveGroups(req, nil)
	case "up", "k", "down", "j", "pgup", "pgdown":
		height := m.bodyHeight()
		last := max(0, len(m.liveGroupsBatchLines())-height)
		delta := 1
		switch key {
		case "up", "k":
			delta = -1
		case "pgup":
			delta = -height
		case "pgdown":
			delta = height
		}
		g.choice = max(0, min(last, min(g.choice, last)+delta))
	}
	return nil
}

func (m *configTUI) liveGroupsBatchLines() []string {
	g := &m.liveGroups
	lines := []string{
		fmt.Sprintf("批量授权 · %d 个群", len(g.marked)),
		"应用 · " + g.account.Tenant + "/" + g.account.AppID,
		"进程 · " + g.process,
	}
	for _, group := range g.marked {
		lines = append(lines, "群名 · "+strings.ReplaceAll(group.Name, "\n", " "), "群 ID · "+group.ID)
	}
	return strings.Split(ansi.Hardwrap(configTUIPlain(strings.Join(lines, "\n")), m.width, true), "\n")
}

func (m *configTUI) liveGroupsView() (title, body, shortcuts, actions string) {
	g := &m.liveGroups
	title = "在线群权限 · " + g.account.Tenant + "/" + g.account.AppID
	line := func(value string) string {
		return ansi.Truncate(configTUIPlain(strings.ReplaceAll(value, "\n", " ")), m.width, "…")
	}
	switch m.screen {
	case tuiLiveGroupsLoading:
		if g.applying {
			return title, "正在等待接收进程确认更新…", "", ""
		}
		return title, "正在读取在线群权限…", "Esc 取消", ""
	case tuiLiveGroupsFilter:
		return title, m.input.View(), "输入群名或 ID · Enter 搜索 · Esc 返回", ""
	case tuiLiveGroupsConfirm:
		if g.batchReview {
			lines := m.liveGroupsBatchLines()
			height := m.bodyHeight()
			first := min(g.choice, max(0, len(lines)-height))
			body = strings.Join(lines[first:min(len(lines), first+height)], "\n")
			return title, body, "↑↓ 浏览 · y 授权 · Esc 返回", ""
		}
		target := "应用默认 @ 规则"
		if g.target.ID != "" {
			target = g.target.ID + " · " + g.target.Name
		}
		lines := []string{line("目标 · " + target), line("进程 · " + g.process)}
		height := max(1, m.bodyHeight()-len(lines))
		first := max(g.firstAction(), g.choice-height+1)
		for i := first; i < min(len(configTUILiveGroupActions), first+height); i++ {
			label := configTUILiveGroupActions[i].label
			if i == g.choice {
				label = "› " + label
			} else {
				label = "  " + label
			}
			lines = append(lines, line(label))
		}
		return title, strings.Join(lines, "\n"), "↑↓ 选操作 · y 确认即时应用 · Esc 返回", "操作写入在线权限，与配置草稿独立"
	}
	groups := g.filtered()
	scopeStatus, scopeGuidance := groupMessageScopeText(g.view.MessageScope)
	lines := []string{
		line("飞书无 @ 群消息权限 · " + scopeStatus),
		line("线上消息事件订阅 · " + groupMessageEventText(g.view.MessageEvent)),
	}
	if scopeGuidance != "" {
		if ansi.StringWidth(scopeGuidance) > m.width {
			scopeGuidance = "管理员私聊 /config mention authorize"
		}
		lines = append(lines, line(scopeGuidance))
	}
	var details []string
	bodyHeight := m.bodyHeight()
	// Leave room for the selected row after up to three list details.
	compact := bodyHeight-len(lines) <= 3
	membership, empty := "暂无法读取机器人所在群，显示已知群权限。", "没有可显示的群。"
	if compact {
		membership, empty = "入群未知", "无可显示群"
	}
	if !g.view.JoinedAvailable {
		details = append(details, membership)
	}
	if len(groups) == 0 {
		details = append(details, empty)
	}
	if g.filter != "" {
		details = append(details, "搜索 · "+g.filter)
	}
	if compact && len(details) > 0 {
		details = []string{strings.Join(details, " · ")}
	}
	for _, detail := range details {
		lines = append(lines, line(detail))
	}
	height := max(1, bodyHeight-len(lines))
	first := max(0, g.row-height+1)
	batch := g.batchAvailable()
	for i := first; i < min(len(groups)+1, first+height); i++ {
		label := "应用默认 · " + liveGroupMentionLabel(g.view.DefaultRequiresMention)
		if i > 0 {
			group := groups[i-1]
			label = liveGroupEntryLabel(group, g.view.JoinedAvailable)
			if batch && liveGroupGrantEligible(group) {
				mark := "[ ] "
				if slices.ContainsFunc(g.marked, func(marked bridge.GroupAccessEntry) bool { return marked.ID == group.ID }) {
					mark = "[x] "
				}
				label = mark + label
			}
		}
		marker := "  "
		if i == g.row {
			marker = "› "
		}
		lines = append(lines, line(marker+label))
	}
	actions = "r 刷新 · Esc 返回草稿"
	if g.userGroupID != "" {
		actions = "r 刷新 · Esc 返回我的群"
	}
	if batch {
		actions = fmt.Sprintf("Space 标记 · b 预览(%d) · r 刷新 · Esc", len(g.marked))
		return title, strings.Join(lines, "\n"), "↑↓ 移动 · / 搜索 · Enter 操作", actions
	}
	return title, strings.Join(lines, "\n"), "↑↓ 移动 · / 搜索 · Enter 选择操作", actions
}

func liveGroupEntryLabel(group bridge.GroupAccessEntry, joinedAvailable bool) string {
	access := "未授权"
	if group.Allowed {
		access = "已授权"
	}
	if group.AccessOverride == "allow" || group.AccessOverride == "deny" {
		access += "(覆盖)"
	}
	mention := liveGroupMentionLabel(group.RequiresMention)
	if group.MentionOverride == "on" || group.MentionOverride == "off" {
		mention += "(覆盖)"
	}
	joined := ""
	if group.Joined {
		joined = "已入群 · "
	} else if joinedAvailable {
		joined = "未入群 · "
	}
	name := group.ID
	if group.Name != "" {
		name = group.Name + " · " + name
	}
	return access + " · " + mention + " · " + joined + name
}

func liveGroupMentionLabel(required bool) string {
	if required {
		return "需要 @"
	}
	return "无需 @"
}
