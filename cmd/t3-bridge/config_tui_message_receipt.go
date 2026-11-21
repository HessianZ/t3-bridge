package main

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
)

type configTUIMessageReceipt struct {
	appID   string
	request int
	lookup  func(path, appID, messageID string) (bridge.MessageReceipt, error)
}

type configTUIMessageReceiptResult struct {
	request int
	receipt bridge.MessageReceipt
	err     error
}

func (m *configTUI) openMessageReceipt() tea.Cmd {
	if m.source.Path == "" || m.source.Revision == "" || m.source.Revision == "missing" {
		m.status = "请先保存配置文件，再查询消息回执。"
		return nil
	}
	var appID string
	if json.Unmarshal(m.accounts[m.account]["appId"], &appID) != nil || appID == "" || strings.TrimSpace(appID) != appID {
		m.status = "请核对当前账号的 App ID。"
		return nil
	}
	m.messageReceipt.appID = appID
	m.screen, m.status = tuiMessageReceiptInput, ""
	return m.openInput("")
}

func (m *configTUI) messageReceiptKey(key string) (tea.Cmd, bool) {
	if key == "esc" {
		m.input.Blur()
		m.screen, m.status = tuiBrowse, ""
		return nil, true
	}
	if m.screen != tuiMessageReceiptInput || key != "enter" {
		return nil, false
	}
	messageID := strings.TrimSpace(m.input.Value())
	if messageID == "" {
		m.status = "请输入完整的入站消息 ID（om_…）。"
		return nil, true
	}
	m.input.Blur()
	m.screen, m.status = tuiMessageReceiptLoading, ""
	m.messageReceipt.request++
	request, lookup := m.messageReceipt.request, m.messageReceipt.lookup
	path, appID := m.source.Path, m.messageReceipt.appID
	return func() tea.Msg {
		receipt, err := lookup(path, appID, messageID)
		return configTUIMessageReceiptResult{request: request, receipt: receipt, err: err}
	}, true
}

func (m *configTUI) receiveMessageReceipt(msg configTUIMessageReceiptResult) {
	if m.screen != tuiMessageReceiptLoading || msg.request != m.messageReceipt.request {
		return
	}
	m.screen = tuiMessageReceiptResult
	var content string
	switch {
	case errors.Is(msg.err, bridge.ErrMessageReceiptNotFound):
		content = "未找到该入站消息的回执。"
	case msg.err != nil:
		content = "无法查询消息回执。请核对入站消息 ID、已保存配置和本地状态。"
	default:
		content = messageReceiptText(msg.receipt)
	}
	m.review.Height = m.bodyHeight()
	m.review.SetContent(content)
	m.review.SetXOffset(0)
	m.review.GotoTop()
}

func messageReceiptText(r bridge.MessageReceipt) string {
	yesNo := func(value bool) string {
		if value {
			return "是"
		}
		return "否"
	}
	fields := []struct{ label, value string }{
		{"App ID", r.AppID},
		{"入站消息 ID", r.MessageID},
		{"输入 ID", r.InputID},
		{"任务 ID", r.TaskID},
		{"路由 ID", r.RouteID},
		{"T3 环境 ID", r.EnvironmentID},
		{"T3 项目 ID", r.ProjectID},
		{"阶段", r.Phase},
		{"请求 ID", r.RequestID},
		{"线程 ID", r.ThreadID},
		{"运行 ID", r.RunID},
		{"回复 ID", r.ReplyID},
		{"终态回复投递", messageReceiptDeliveryText(r.DeliveryState)},
		{"历史回执", yesNo(r.Historical)},
		{"需要对账", yesNo(r.NeedsReconcile)},
	}
	lines := make([]string, 0, len(fields)+2)
	for _, field := range fields {
		value := field.value
		if value == "" {
			value = "尚无记录"
		}
		lines = append(lines, field.label+" · "+configTUIPlain(strings.ReplaceAll(value, "\n", " ")))
	}
	if r.ThreadURL != "" {
		lines = append(lines, "T3 线程链接", configTUIPlain(strings.ReplaceAll(r.ThreadURL, "\n", " ")))
	}
	return strings.Join(lines, "\n")
}

func (m *configTUI) messageReceiptView() (title, body, shortcuts string) {
	title = m.language.text("消息回执 · ") + configTUIPlain(strings.ReplaceAll(m.messageReceipt.appID, "\n", " "))
	switch m.screen {
	case tuiMessageReceiptInput:
		return title, m.language.text("入站消息 ID（om_…）\n") + m.input.View(), m.language.text("Enter 查询已保存配置 · Esc 返回")
	case tuiMessageReceiptLoading:
		return title, m.language.text("正在读取本地消息回执…"), m.language.text("Esc 返回")
	default:
		return title, m.review.View(), m.language.text("↑↓←→ 滚动 · Esc 返回")
	}
}
