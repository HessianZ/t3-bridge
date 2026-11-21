package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type configTUIReceiverStatus struct {
	account receiverProcessAccount
	lookup  func(profileRegistry, string, receiverProcessAccount) (receiverStatusView, error)
	request int
}

type configTUIReceiverStatusResult struct {
	request int
	view    receiverStatusView
	err     error
}

func (m *configTUI) openReceiverStatus() tea.Cmd {
	account, err := m.liveAccount()
	if err != nil {
		m.status = err.Error()
		return nil
	}
	m.receiverStatus.account = account
	return m.requestReceiverStatus()
}

func (m *configTUI) requestReceiverStatus() tea.Cmd {
	s := &m.receiverStatus
	s.request++
	request, lookup, account, profiles := s.request, s.lookup, s.account, m.profiles.registry
	m.screen, m.status = tuiReceiverStatusLoading, ""
	m.setReceiverStatusContent(receiverStatusApp(account.Tenant, account.AppID) + "\n正在读取接收状态…")
	return func() tea.Msg {
		view, err := lookup(profiles, "", account)
		return configTUIReceiverStatusResult{request: request, view: view, err: err}
	}
}

func (m *configTUI) receiveReceiverStatus(msg configTUIReceiverStatusResult) {
	if m.screen != tuiReceiverStatusLoading || msg.request != m.receiverStatus.request {
		return
	}
	m.screen = tuiReceiverStatusResult
	if msg.err != nil {
		account := m.receiverStatus.account
		m.setReceiverStatusContent(receiverStatusApp(account.Tenant, account.AppID) + "\n无法读取接收状态。")
		return
	}
	m.setReceiverStatusContent(receiverStatusText(msg.view))
}

func (m *configTUI) receiverStatusKey(key string) (tea.Cmd, bool) {
	switch key {
	case "esc":
		m.screen, m.status = tuiBrowse, ""
		return nil, true
	case "r":
		return m.requestReceiverStatus(), true
	}
	return nil, false
}

func (m *configTUI) setReceiverStatusContent(content string) {
	m.review.Height = m.bodyHeight()
	m.review.SetContent(configTUIPlain(content))
	m.review.SetXOffset(0)
	m.review.GotoTop()
}

func receiverStatusApp(tenant, appID string) string {
	return "App · " + strconv.Quote(tenant+"/"+appID)
}

func receiverStatusText(view receiverStatusView) string {
	lines := []string{
		receiverStatusApp(view.Tenant, view.AppID),
		"进程 · " + view.Process,
		"PID · " + strconv.Itoa(view.PID),
	}
	if worker := view.Worker; worker != nil {
		lines = append(lines, fmt.Sprintf("重试 · 已失败 %d 次", worker.Attempts))
		if failure := receiverFailureText(worker.Failure); failure != "" {
			lines = append(lines, "故障类别 · "+failure)
		}
		switch worker.State {
		case receiverWorkerRetrying:
			lines = append(lines, "下次尝试 · "+worker.RetryAt.Local().Format(time.RFC3339))
		case receiverWorkerExhausted:
			lines = append(lines, "重试 · 已耗尽", "Esc 返回账号页，Ctrl+R 立即重连")
		}
	}
	if view.Sample == nil {
		return strings.Join(append(lines, "状态未知 · 暂无可用诊断样本"), "\n")
	}
	sample := view.Sample
	data := sample.Data
	if data.ReconnectWaiting {
		lines = append(lines, "重连 · 暂停新派发，等待重连")
	}
	mcp, feishu := "尚未核验", "未就绪"
	if data.MCPVerified {
		mcp = "核验通过"
	}
	if data.FeishuReady {
		feishu = "已就绪"
	}
	lines = append(lines,
		"观察时间 · "+sample.At.Local().Format(time.RFC3339),
		"T3 最近建连 · "+mcp,
		"飞书 WebSocket · "+feishu,
		fmt.Sprintf("入站 %d · 在途 %d · 待核对 %d · 送达缺口 %d", data.Inbox, data.InFlight, data.Reconcile, data.DeliveryPending),
	)
	if capacity := data.Capacity; capacity != nil {
		available := "不限"
		if capacity.Limit > 0 {
			available = fmt.Sprintf("%d / %d", max(0, capacity.Limit-capacity.Occupied), capacity.Limit)
		}
		lines = append(lines, "可用并发额度 · "+available, fmt.Sprintf("已占用 %d · 等待额度 %d", capacity.Occupied, capacity.Waiting))
	} else {
		lines = append(lines, "可用并发额度 · 未知")
	}
	return strings.Join(lines, "\n")
}
