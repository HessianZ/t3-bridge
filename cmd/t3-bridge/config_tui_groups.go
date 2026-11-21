package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type configTUIFeishuGroups struct {
	request  int
	cancel   context.CancelFunc
	groups   []bridge.FeishuGroup
	chats    []string
	selected map[string]bool
	row      int
	filter   string
}

type configTUIFeishuGroupsResult struct {
	request int
	groups  []bridge.FeishuGroup
	err     error
}

func (m *configTUI) queryFeishuGroups(list func(context.Context, string, string, string) ([]bridge.FeishuGroup, error)) tea.Cmd {
	account := m.accounts[m.account]
	var appID, secretFile, tenant string
	_ = json.Unmarshal(account["appId"], &appID)
	_ = json.Unmarshal(account["appSecretFile"], &secretFile)
	_ = json.Unmarshal(account["tenant"], &tenant)
	var chats []string
	if raw := account["chats"]; len(raw) != 0 && json.Unmarshal(raw, &chats) != nil {
		m.status = "请先修正当前账号的 chats 字段"
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	request := m.groups.request + 1
	m.groups = configTUIFeishuGroups{request: request, cancel: cancel, chats: chats}
	m.screen, m.status = tuiGroupsLoading, ""
	return func() tea.Msg {
		defer cancel()
		groups, err := list(ctx, appID, secretFile, tenant)
		return configTUIFeishuGroupsResult{request: request, groups: groups, err: err}
	}
}

func (m *configTUI) receiveFeishuGroups(msg configTUIFeishuGroupsResult) {
	if m.screen != tuiGroupsLoading || msg.request != m.groups.request {
		return
	}
	m.groups.cancel = nil
	switch {
	case msg.err != nil:
		m.screen, m.status = tuiBrowse, msg.err.Error()
	case len(msg.groups) == 0:
		m.screen, m.status = tuiBrowse, "机器人当前没有可选择的群"
	default:
		m.groups.groups, m.groups.selected = msg.groups, make(map[string]bool)
		m.screen = tuiGroupsPick
	}
}

func (m *configTUI) feishuGroupsKey(key string) bool {
	picker := &m.groups
	if key == "esc" {
		if picker.cancel != nil {
			picker.cancel()
			picker.cancel = nil
		}
		m.screen, m.status = tuiBrowse, ""
		return true
	}
	if m.screen == tuiGroupsLoading {
		return false
	}
	groups := picker.filtered()
	if len(groups) == 0 && key != "enter" {
		return false
	}
	switch key {
	case "up", "k":
		picker.row = max(0, picker.row-1)
	case "down", "j":
		picker.row = min(len(groups)-1, picker.row+1)
	case "home":
		picker.row = 0
	case "end":
		picker.row = len(groups) - 1
	case " ":
		id := groups[picker.row].ID
		m.status = ""
		if picker.selected[id] {
			delete(picker.selected, id)
		} else if !slices.Contains(picker.chats, id) {
			picker.selected[id] = true
		}
	case "enter":
		m.applyFeishuGroups()
	default:
		return false
	}
	return true
}

func (g configTUIFeishuGroups) filtered() []bridge.FeishuGroup {
	if g.filter == "" {
		return g.groups
	}
	query := strings.ToLower(g.filter)
	var groups []bridge.FeishuGroup
	for _, group := range g.groups {
		if strings.Contains(strings.ToLower(group.Name), query) || strings.Contains(strings.ToLower(group.ID), query) {
			groups = append(groups, group)
		}
	}
	return groups
}

func (m *configTUI) applyFeishuGroups() {
	picker := &m.groups
	if len(picker.selected) == 0 {
		m.status = "请先用空格选择要添加的群"
		return
	}
	chats := slices.Clone(picker.chats)
	for _, group := range picker.groups {
		if picker.selected[group.ID] && !slices.Contains(chats, group.ID) {
			chats = append(chats, group.ID)
		}
	}
	if m.applyChanges(configTUIEdit{Path: m.accountPath("chats"), Value: chats}) {
		m.screen, m.status = tuiBrowse, fmt.Sprintf("已向草稿添加 %d 个群", len(chats)-len(picker.chats))
	}
}

func (m *configTUI) feishuGroupsView() (title, body, shortcuts, actions string) {
	title = m.accountTitle() + " / 机器人所在群"
	if m.screen == tuiGroupsLoading {
		return title, "正在读取机器人所在群…", "Esc 取消", ""
	}
	picker := &m.groups
	groups := picker.filtered()
	if picker.filter != "" {
		title += " / " + configTUIPlain(picker.filter)
	}
	if len(groups) == 0 {
		return title, "没有匹配的群。", "/ 重新搜索", "Enter 确认添加已选群 · Esc 取消"
	}
	first := max(0, picker.row-m.bodyHeight()+1)
	var lines []string
	for i := first; i < min(len(groups), first+m.bodyHeight()); i++ {
		group := groups[i]
		marker, checkbox := "  ", "[ ] "
		if i == picker.row {
			marker = "› "
		}
		if slices.Contains(picker.chats, group.ID) {
			checkbox = "[已在草稿] "
		} else if picker.selected[group.ID] {
			checkbox = "[x] "
		}
		label := group.ID
		if group.Name != "" {
			label = strings.ReplaceAll(group.Name, "\n", " ") + " · " + group.ID
		}
		lines = append(lines, ansi.Truncate(configTUIPlain(marker+checkbox+label), m.width, "…"))
	}
	shortcuts = fmt.Sprintf("↑↓ 移动 · / 搜索 · 空格 勾选 · 已选 %d · %d/%d", len(picker.selected), picker.row+1, len(groups))
	return title, strings.Join(lines, "\n"), shortcuts, "Enter 确认添加到草稿 · Esc 取消"
}
