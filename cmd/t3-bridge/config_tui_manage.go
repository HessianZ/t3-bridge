package main

import (
	"bytes"
	"encoding/json"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func (m *configTUI) addRoute() {
	id := strings.TrimSpace(m.input.Value())
	if id == "" || strings.ContainsAny(id, " /\n") {
		m.status = "路由 ID 必填，不能包含空格或斜线"
		return
	}
	for existing := range m.routes {
		if strings.EqualFold(existing, id) {
			m.status = "路由 ID 已存在"
			return
		}
	}
	target := json.RawMessage(`{"runtimeMode":"approval-required","interactionMode":"default","options":{},"users":[]}`)
	edit := configTUIEdit{Path: m.accountPath("routes", id), Value: target}
	if m.routes == nil {
		edit = configTUIEdit{Path: m.accountPath("routes"), Value: configTUIObject{id: target}}
	}
	if m.applyChanges(edit) {
		m.input.Blur()
		m.screen, m.route, m.row = tuiBrowse, id, 0
		m.refreshFields()
	}
}

func (m *configTUI) removeRoute() {
	id := m.route
	if id == "" {
		id = m.rows[m.row].routeID
	}
	if id == "" {
		m.status = "先选择要移除的路由"
		return
	}
	if m.applyChanges(configTUIEdit{Path: m.accountPath("routes", id), Delete: true}) {
		m.status = "已从草稿移除路由。请核对引用该路由的字段。"
	}
}

func (m *configTUI) removeAccount() {
	if m.account == 0 || m.route != "" {
		m.status = "先切换到要移除的附加账号"
		return
	}
	if m.applyChanges(configTUIEdit{Path: m.accountPath(), Delete: true}) {
		m.row = 0
		m.status = "已从草稿移除附加账号"
	}
}

var configTUIAccountLabels = [...]string{"应用 ID", "初始用户 Open ID", "密钥文件路径", "独立状态目录", "租户", "复制主账号路由"}

const (
	tuiAccountTenant = 4
	tuiAccountRoute  = 5
)

type configTUIAccountForm struct {
	values [tuiAccountTenant]string
	routes []string
	row    int
	route  int
	tenant string
}

func (m *configTUI) openAccountForm() tea.Cmd {
	primary := m.accounts[0]
	var routes configTUIObject
	_ = json.Unmarshal(primary["routes"], &routes)
	ids := slices.Sorted(maps.Keys(routes))
	if len(ids) == 0 {
		m.status = "请先为主账号新增一条路由"
		return nil
	}
	var selected string
	_ = json.Unmarshal(primary["defaultRoute"], &selected)
	if m.account == 0 {
		if m.route != "" {
			selected = m.route
		} else if id := m.rows[m.row].routeID; id != "" {
			selected = id
		}
	}
	m.newAccount = configTUIAccountForm{routes: ids, route: max(0, slices.Index(ids, selected)), tenant: "feishu"}
	m.screen, m.status = tuiAddAccount, ""
	return m.openInput("")
}

func (m *configTUI) accountFormKey(key string) (tea.Cmd, bool) {
	f := &m.newAccount
	switch key {
	case "esc":
		m.input.Blur()
		m.screen, m.status = tuiBrowse, ""
	case "ctrl+s":
		m.addAccount()
	case "down", "tab", "enter", "up", "shift+tab":
		if f.row < len(f.values) {
			f.values[f.row] = m.input.Value()
		}
		delta := 1
		if key == "up" || key == "shift+tab" {
			delta = -1
		}
		n := len(configTUIAccountLabels)
		f.row = (f.row + delta + n) % n
		if f.row < len(f.values) {
			return m.openInput(f.values[f.row]), true
		}
		m.input.Blur()
	case "left", "right":
		switch f.row {
		case tuiAccountTenant:
			if f.tenant == "feishu" {
				f.tenant = "lark"
			} else {
				f.tenant = "feishu"
			}
		case tuiAccountRoute:
			delta := 1
			if key == "left" {
				delta = -1
			}
			f.route = (f.route + delta + len(f.routes)) % len(f.routes)
		default:
			return nil, false
		}
	default:
		return nil, false
	}
	return nil, true
}

func (m *configTUI) accountFormView() string {
	f := &m.newAccount
	values := append(f.values[:], f.tenant, f.routes[f.route])
	var lines []string
	for i, label := range configTUIAccountLabels {
		line := "  " + label + "  " + values[i]
		if i == f.row {
			line = "› " + label
			if i >= len(f.values) {
				line += "  ← " + values[i] + " →"
			}
		}
		lines = append(lines, ansi.Truncate(configTUIPlain(line), m.width, "…"))
		if i == f.row && i < len(f.values) {
			lines = append(lines, m.input.View())
		}
	}
	active := f.row
	if f.row < len(f.values) && m.bodyHeight() > 1 {
		active++
	}
	first := max(0, active-m.bodyHeight()+1)
	return strings.Join(lines[first:min(len(lines), first+m.bodyHeight())], "\n")
}

func (m *configTUI) addAccount() {
	f := &m.newAccount
	if f.row < len(f.values) {
		f.values[f.row] = m.input.Value()
	}
	app, user := strings.TrimSpace(f.values[0]), strings.TrimSpace(f.values[1])
	secret, state := strings.TrimSpace(f.values[2]), strings.TrimSpace(f.values[3])
	if app == "" || user == "" || !filepath.IsAbs(secret) || !filepath.IsAbs(state) {
		m.status = "请填写应用 ID、用户 Open ID，以及密钥文件和独立状态目录的绝对路径"
		return
	}
	account := bridge.Config{
		AppID: app, AppSecretFile: secret, StateDir: state, Tenant: f.tenant, Users: []string{user}, Chats: []string{},
	}
	raw, id := []byte(m.draft), f.routes[f.route]
	config, _ := bridge.ParseConfig(raw)
	if next, err := addAccountDraft(raw, config, account, id); err == nil {
		m.setDraft(next)
	} else if !m.appendIncompleteAccount(account, config, id) {
		return
	}
	m.screen, m.account, m.route, m.row = tuiBrowse, len(m.accounts)-1, "", 0
	m.input.Blur()
	m.refreshFields()
}

// addAccountDraft serves the binding flow, which requires a valid candidate.
// In the TUI an incomplete route or unrelated account can still be in progress.
func (m *configTUI) appendIncompleteAccount(account, primary bridge.Config, id string) bool {
	var routes configTUIObject
	_ = json.Unmarshal(m.accounts[0]["routes"], &routes)
	var route bridge.Target
	d := json.NewDecoder(bytes.NewReader(routes[id]))
	d.UseNumber()
	if err := d.Decode(&route); err != nil {
		m.status = "无法复制主账号路由。请先修正草稿。"
		return false
	}
	route.Users, route.Checks, route.ToolContext = account.Users, nil, nil
	route.RuntimeMode = "approval-required"
	account.ReceiverMachine = strings.ToLower(account.AppID)
	account.MCP, account.DefaultRoute = primary.MCP, id
	account.Routes = map[string]bridge.Target{id: route}
	accounts := make([]any, 0, len(m.accounts))
	for _, existing := range m.accounts[1:] {
		accounts = append(accounts, existing)
	}
	return m.applyChanges(configTUIEdit{Path: []string{"additionalAccounts"}, Value: append(accounts, account)})
}
