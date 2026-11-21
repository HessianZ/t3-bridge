package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type configTUIAuthorization struct {
	connection bridge.MCPConfig
	cancel     context.CancelFunc
	events     chan configTUIAuthorizationResult
	address    string
	replace    bool
}

type configTUIAuthorizationResult struct {
	source  chan configTUIAuthorizationResult
	address string
	err     error
}

func (m *configTUI) openAuthorization() {
	var connection bridge.MCPConfig
	if err := json.Unmarshal(m.accounts[m.account]["mcp"], &connection); err != nil {
		m.status = "请先填写当前账号的 T3 MCP 连接"
		return
	}
	m.authorization = configTUIAuthorization{connection: connection}
	m.screen, m.status = tuiAuthorizeReview, ""
}

func (m *configTUI) authorizationKey(key string) (tea.Cmd, bool) {
	if key == "esc" {
		if cancel := m.authorization.cancel; cancel != nil {
			cancel()
			m.status = "已请求取消"
		} else {
			m.screen, m.status = tuiBrowse, ""
		}
		return nil, true
	}
	if m.screen == tuiAuthorizeReview {
		switch key {
		case "r":
			m.authorization.replace = !m.authorization.replace
			return nil, true
		case "enter":
			return m.startAuthorization(), true
		}
	}
	return nil, false
}

func (m *configTUI) startAuthorization() tea.Cmd {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	events := make(chan configTUIAuthorizationResult, 2)
	connection, replace := m.authorization.connection, m.authorization.replace
	m.authorization.cancel, m.authorization.events = cancel, events
	m.screen, m.status = tuiAuthorizePending, ""
	go func() {
		defer cancel()
		err := authorizeConfiguredT3(ctx, connection, replace, func(address string) error {
			events <- configTUIAuthorizationResult{source: events, address: address}
			return nil
		})
		events <- configTUIAuthorizationResult{source: events, err: err}
	}()
	return func() tea.Msg { return <-events }
}

func (m *configTUI) receiveAuthorization(result configTUIAuthorizationResult) tea.Cmd {
	if m.screen != tuiAuthorizePending || result.source != m.authorization.events {
		return nil
	}
	if result.address != "" {
		m.authorization.address = result.address
		return func() tea.Msg { return <-result.source }
	}
	m.authorization = configTUIAuthorization{}
	m.screen, m.status = tuiBrowse, "凭据已保存，可查询项目和模型"
	switch {
	case errors.Is(result.err, context.DeadlineExceeded):
		m.status = "Bridge 等待授权已超时。按 F6 重新开始。"
	case errors.Is(result.err, context.Canceled):
		m.status = "已取消 T3 授权。"
	case result.err != nil:
		m.status = result.err.Error()
	}
	return nil
}

func (m *configTUI) authorizationView() (title, body, shortcuts string) {
	title = m.accountTitle() + m.language.text(" / T3 授权")
	if m.screen == tuiAuthorizeReview {
		mode := m.language.text("创建新的凭据文件")
		if m.authorization.replace {
			mode = m.language.text("替换已有凭据文件")
		}
		body = ansi.Hardwrap(mode+"\n"+configTUIPlain(m.authorization.connection.TokenFile)+
			m.language.text("\n派发受监督的任务时，在 T3 选择 Supervised。")+
			m.language.text("\n如授权页要求 Sign-in code，请在同一 T3 环境取码。")+
			"\nSettings → Sharing & access → Use T3 Code in another app (MCP)", m.width, true)
		return title, body, m.language.text("Enter 开始 · r 切换是否替换凭据 · Esc 返回")
	}
	if address := m.authorization.address; address != "" {
		body = ansi.SetHyperlink(address) + m.language.text("用本机浏览器打开 T3 授权页") + ansi.ResetHyperlink() +
			"\n" + ansi.Hardwrap(m.language.text("等待最多 5 分钟。码过期或已用时，从同一 T3 环境重取。"), m.width, true) +
			"\n" + ansi.Hardwrap(address, m.width, true)
	} else {
		body = m.language.text("正在准备 T3 授权…")
	}
	return title, body, m.language.text("Esc 取消")
}
