package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type configTUIUserGroups struct {
	service   *bridge.UserGroups
	account   receiverProcessAccount
	chats     []string
	request   int
	cancel    context.CancelFunc
	action    string
	status    bridge.UserGroupsStatus
	challenge bridge.UserGroupsChallenge
	purpose   string
	qr        string
	query     string
	pages     []string
	page      bridge.UserGroupsPage
	row       int
}

type configTUIUserGroupsResult struct {
	request    int
	status     bridge.UserGroupsStatus
	challenge  bridge.UserGroupsChallenge
	page       bridge.UserGroupsPage
	invitation bridge.UserGroupInvitation
	err        error
}

func (m *configTUI) openUserGroups() tea.Cmd {
	root := m.profiles.registry.root
	if root == "" {
		m.status = "当前用户的配置目录不可用"
		return nil
	}
	var account bridge.Config
	object := m.accounts[m.account]
	for key, target := range map[string]any{
		"appId": &account.AppID, "appSecretFile": &account.AppSecretFile,
		"tenant": &account.Tenant, "chats": &account.Chats,
	} {
		if raw := object[key]; len(raw) != 0 && json.Unmarshal(raw, target) != nil {
			m.status = "请先修正当前账号的 " + key + " 字段"
			return nil
		}
	}
	if account.Tenant == "" {
		account.Tenant = "feishu"
	}
	service, err := bridge.NewUserGroups(account, root)
	if err != nil {
		m.status = err.Error()
		return nil
	}
	m.userGroups = configTUIUserGroups{
		service: service, account: newReceiverGroupAccount(account.Tenant, account.AppID), chats: account.Chats,
		request: m.userGroups.request, pages: []string{""},
	}
	return m.requestUserGroups("status", "")
}

func (m *configTUI) closeUserGroups() {
	g := &m.userGroups
	if g.cancel != nil {
		g.cancel()
	}
	if ticket := g.challenge.Ticket; ticket != "" && time.Now().Before(g.challenge.ExpiresAt) {
		ctx, cancel := context.WithTimeout(context.Background(), configUserGroupsTimeout)
		defer cancel()
		_ = g.service.Cancel(ctx, ticket)
	}
	*g = configTUIUserGroups{request: g.request + 1}
}

func (m *configTUI) requestUserGroups(action, value string) tea.Cmd {
	g := &m.userGroups
	ctx, cancel := context.WithTimeout(context.Background(), configUserGroupsTimeout)
	g.request++
	g.cancel, g.action = cancel, action
	m.screen, m.status = tuiUserGroupsLoading, ""
	request, service := g.request, g.service
	query, pageToken := g.query, g.pages[len(g.pages)-1]
	return func() tea.Msg {
		defer cancel()
		msg := configTUIUserGroupsResult{request: request}
		switch action {
		case "status":
			msg.status, msg.err = service.Status(ctx)
		case "start":
			msg.challenge, msg.err = service.Start(ctx, value)
		case "finish":
			msg.status, msg.err = service.Finish(ctx, value)
		case "cancel":
			msg.err = service.Cancel(ctx, value)
		case "list":
			msg.page, msg.err = service.List(ctx, query, pageToken)
		case "invite":
			msg.invitation, msg.err = service.Invite(ctx, value)
		}
		if msg.err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			msg.err = context.DeadlineExceeded
		}
		return msg
	}
}

func (m *configTUI) receiveUserGroups(msg configTUIUserGroupsResult) tea.Cmd {
	g := &m.userGroups
	if m.screen != tuiUserGroupsLoading || msg.request != g.request {
		return nil
	}
	action := g.action
	g.cancel, g.action = nil, ""
	m.screen = tuiUserGroupsPick
	if action == "list" {
		g.page, g.row = bridge.UserGroupsPage{}, 0
	}
	if action == "cancel" || (action == "finish" && msg.err == nil) {
		g.challenge, g.qr = bridge.UserGroupsChallenge{}, ""
	}
	if msg.err != nil {
		m.status = msg.err.Error()
		switch {
		case action == "finish":
			m.screen = tuiUserGroupsAuthorize
			if !time.Now().Before(g.challenge.ExpiresAt) {
				g.challenge, g.qr = bridge.UserGroupsChallenge{}, ""
				m.screen, m.status = tuiUserGroupsPick, "授权已过期，请重新申请"
			} else if errors.Is(msg.err, context.DeadlineExceeded) {
				m.status = "等待授权超时，请完成授权后按 Enter 重试"
			}
		case action == "invite":
			m.status = "邀请结果未确认，请按 r 核对入群状态"
			if errors.Is(msg.err, context.DeadlineExceeded) {
				m.status = "邀请请求超时，请按 r 核对入群状态"
			}
		case errors.Is(msg.err, context.DeadlineExceeded):
			m.status = "请求超时，请重试"
		}
		return nil
	}
	switch action {
	case "status", "finish":
		g.status = msg.status
		if g.status.ListAuthorized {
			return m.requestUserGroups("list", "")
		}
	case "start":
		g.challenge, g.qr = msg.challenge, terminalQRCode(msg.challenge.URL)
		m.screen = tuiUserGroupsAuthorize
	case "cancel":
		m.status = "已取消授权"
	case "list":
		g.page = msg.page
	case "invite":
		switch msg.invitation.State {
		case "joined":
			g.page.Groups[g.row].BotJoined = true
			m.status = "机器人已入群，按 Enter 可加入草稿"
		case "pending":
			m.status = "邀请待处理，按 r 核对入群状态"
		case "rejected":
			m.status = "邀请被飞书拒绝"
		case "confirmation-required":
			m.status = "邀请需要额外确认，请按 r 核对入群状态"
		default:
			m.status = "邀请结果未确认，请按 r 核对入群状态"
		}
	}
	return nil
}

func (m *configTUI) userGroupsKey(key string) (tea.Cmd, bool) {
	g := &m.userGroups
	switch m.screen {
	case tuiUserGroupsLoading:
		if key == "esc" && (g.action == "status" || g.action == "list") {
			m.closeUserGroups()
			m.screen, m.status = tuiBrowse, ""
		}
	case tuiUserGroupsSearch:
		switch key {
		case "enter":
			g.query, g.pages = strings.TrimSpace(m.input.Value()), []string{""}
			m.input.Blur()
			return m.requestUserGroups("list", ""), true
		case "esc":
			m.input.Blur()
			m.screen = tuiUserGroupsPick
		default:
			return nil, false
		}
	case tuiUserGroupsAuthorize:
		switch key {
		case "enter", "esc":
			if !time.Now().Before(g.challenge.ExpiresAt) {
				g.challenge, g.qr = bridge.UserGroupsChallenge{}, ""
				m.screen, m.status = tuiUserGroupsPick, "授权已过期，请重新申请"
			} else if key == "esc" {
				return m.requestUserGroups("cancel", g.challenge.Ticket), true
			} else {
				return m.requestUserGroups("finish", g.challenge.Ticket), true
			}
		}
	case tuiUserGroupsConfirm:
		switch key {
		case "esc":
			m.screen = tuiUserGroupsPick
		case "y":
			return m.requestUserGroups("invite", g.page.Groups[g.row].ID), true
		}
	case tuiUserGroupsPick:
		return m.userGroupsPickKey(key)
	}
	return nil, true
}

func (m *configTUI) userGroupsPickKey(key string) (tea.Cmd, bool) {
	g := &m.userGroups
	switch key {
	case "esc":
		m.closeUserGroups()
		m.screen, m.status = tuiBrowse, ""
	case "l", "i":
		g.purpose = "list"
		if key == "i" {
			g.purpose = "invite"
		}
		return m.requestUserGroups("start", g.purpose), true
	case "r":
		if g.status.ListAuthorized {
			return m.requestUserGroups("list", ""), true
		}
		return m.requestUserGroups("status", ""), true
	case "/":
		if g.status.ListAuthorized {
			m.screen, m.status = tuiUserGroupsSearch, ""
			return m.openInput(g.query), true
		}
	case "[":
		if len(g.pages) > 1 {
			g.pages = g.pages[:len(g.pages)-1]
			return m.requestUserGroups("list", ""), true
		}
	case "]":
		if g.page.NextPageToken != "" {
			g.pages = append(g.pages, g.page.NextPageToken)
			return m.requestUserGroups("list", ""), true
		}
	case "up", "k":
		g.row = max(0, g.row-1)
	case "down", "j":
		g.row = min(max(0, len(g.page.Groups)-1), g.row+1)
	case "a":
		if len(g.page.Groups) == 0 || !g.page.Groups[g.row].BotJoined {
			m.status = userGroupLiveUnavailable
			return nil, true
		}
		registry := localReceiverProcesses(m.profiles.registry)
		return m.startLiveGroups(g.account, g.page.Groups[g.row].ID, registry.list, registry.groupAccess), true
	case "enter", "v":
		if len(g.page.Groups) == 0 {
			return nil, true
		}
		group := g.page.Groups[g.row]
		joined := group.BotJoined
		if key == "v" {
			switch {
			case joined:
				m.status = "机器人已在群中，可按 Enter 加入草稿"
			case !g.status.InviteAuthorized:
				m.status = "请先按 i 授权邀请机器人"
			default:
				m.screen, m.status = tuiUserGroupsConfirm, ""
			}
		} else if !joined {
			m.status = "请先确认机器人已入群，再加入草稿"
		} else if slices.Contains(g.chats, group.ID) {
			m.status = "该群已在草稿中"
		} else {
			chats := append(slices.Clone(g.chats), group.ID)
			if m.applyChanges(configTUIEdit{Path: m.accountPath("chats"), Value: chats}) {
				g.chats, m.status = chats, "已加入草稿"
			}
		}
	default:
		return nil, false
	}
	return nil, true
}

func (m *configTUI) userGroupsView() (title, body, shortcuts, actions string) {
	g := &m.userGroups
	app := g.account.Tenant + "/" + g.account.AppID
	title = "我的群 · " + app
	line := func(value string) string {
		return ansi.Truncate(configTUIPlain(strings.ReplaceAll(value, "\n", " ")), m.width, "…")
	}
	switch m.screen {
	case tuiUserGroupsLoading:
		switch g.action {
		case "invite":
			return title, "正在等待飞书确认邀请结果…", "", ""
		case "status", "list":
			return title, "正在读取授权或群列表…", "Esc 返回", ""
		default:
			return title, "正在处理群聊授权…", "", ""
		}
	case tuiUserGroupsSearch:
		return title, m.input.View(), "输入搜索词 · Enter 搜索 · Esc 返回", ""
	case tuiUserGroupsAuthorize:
		purpose := "读取群列表"
		if g.purpose == "invite" {
			purpose = "邀请机器人入群"
		}
		title += " / " + purpose + "授权"
		return title, m.terminalAuthorizationBody(g.challenge.URL, g.qr),
			"应用所有者完成授权后按 Enter 核实 · Esc 取消", "有效至 " + g.challenge.ExpiresAt.Local().Format("15:04:05")
	case tuiUserGroupsConfirm:
		group := g.page.Groups[g.row]
		body = line("App · "+app) + "\n" + line("群 · "+group.Name) + "\n" + line("群 ID · "+group.ID)
		return "确认邀请机器人", body, "y 确认发送邀请 · Esc 返回", ""
	}
	if !g.status.ListAuthorized {
		return title, "需要应用所有者授权读取群列表。", "l 列表授权 · i 邀请授权 · r 刷新 · Esc 返回", ""
	}
	var lines []string
	if g.query != "" {
		lines = append(lines, line("搜索 · "+g.query))
	}
	if len(g.page.Groups) == 0 {
		lines = append(lines, "当前页没有可显示的群。")
	}
	height := max(1, m.bodyHeight()-len(lines))
	first := max(0, g.row-height+1)
	for i := first; i < min(len(g.page.Groups), first+height); i++ {
		group := g.page.Groups[i]
		marker := "  "
		if i == g.row {
			marker = "› "
		}
		membership := "入群状态未知"
		if group.BotJoined {
			membership = "机器人已入群"
		} else if g.page.BotKnown {
			membership = "机器人未入群"
		}
		if slices.Contains(g.chats, group.ID) {
			membership += " · 已在草稿"
		}
		name := group.ID
		if group.Name != "" {
			name = group.Name + " · " + name
		}
		lines = append(lines, line(marker+membership+" · "+name))
	}
	shortcuts = fmt.Sprintf("第 %d 页 · ↑↓ 选群 · / 搜索 · [ 上页 · ] 下页 · r 刷新", len(g.pages))
	if len(g.page.Groups) > 0 && g.page.Groups[g.row].BotJoined {
		shortcuts = "a 在线群授权 · " + shortcuts
	}
	return title, strings.Join(lines, "\n"), shortcuts, "Enter 加入草稿 · v 邀请 · l 列表授权 · i 邀请授权 · Esc 返回"
}
