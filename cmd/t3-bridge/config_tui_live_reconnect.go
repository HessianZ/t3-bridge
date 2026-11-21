package main

import (
	"context"
	"errors"
	"slices"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type configTUILiveReconnect struct {
	account receiverProcessAccount
	process receiverProcess
	action  bridge.ReconnectAction
	operate liveReconnectOperation
	request int
}

type configTUILiveReconnectResult struct {
	request int
	process receiverProcess
	view    bridge.LocalReconnectView
	err     error
}

func (m *configTUI) openLiveReconnect(list func() ([]receiverProcess, error), operate liveReconnectOperation) tea.Cmd {
	account, err := m.liveAccount()
	if err != nil {
		m.status = err.Error()
		return nil
	}
	request := m.liveReconnect.request + 1
	m.liveReconnect = configTUILiveReconnect{account: account, action: bridge.ReconnectNow, operate: operate, request: request}
	m.screen, m.status = tuiLiveReconnectLoading, ""
	return func() tea.Msg {
		process, err := liveReconnectProcess(list, account)
		return configTUILiveReconnectResult{request: request, process: process, err: err}
	}
}

func liveReconnectProcess(list func() ([]receiverProcess, error), account receiverProcessAccount) (receiverProcess, error) {
	processes, err := list()
	if err != nil {
		return receiverProcess{}, err
	}
	var target receiverProcess
	for _, process := range processes {
		if !slices.Contains(process.Accounts, account) {
			continue
		}
		if target.ID != "" {
			return receiverProcess{}, errors.New("当前账号匹配多个接收进程，无法确定重连目标")
		}
		target = process
	}
	if target.ID == "" {
		return receiverProcess{}, errors.New("当前账号没有匹配的运行中接收进程")
	}
	if !target.Reconnect {
		return receiverProcess{}, errors.New("当前账号的接收进程版本不支持在线重连")
	}
	return target, nil
}

func (m *configTUI) liveReconnectKey(key string) (tea.Cmd, bool) {
	if key == "esc" {
		m.screen, m.status = tuiBrowse, ""
		return nil, true
	}
	if m.screen != tuiLiveReconnectConfirm {
		return nil, true
	}
	if s := &m.liveReconnect; s.process.ReconnectWait && (key == "up" || key == "down") {
		actions := [...]bridge.ReconnectAction{bridge.ReconnectNow, bridge.ReconnectAfterRuns, bridge.ReconnectCancelWait}
		delta := 1
		if key == "up" {
			delta = len(actions) - 1
		}
		s.action = actions[(slices.Index(actions[:], s.action)+delta)%len(actions)]
		return nil, true
	}
	if key != "y" {
		return nil, true
	}
	m.liveReconnect.request++
	s := m.liveReconnect
	m.screen, m.status = tuiLiveReconnectApplying, ""
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), liveReconnectTimeout)
		defer cancel()
		view, err := s.operate(ctx, s.process.ID, s.account.Tenant, s.account.AppID, s.action)
		return configTUILiveReconnectResult{request: s.request, view: view, err: err}
	}, true
}

func (m *configTUI) receiveLiveReconnect(msg configTUILiveReconnectResult) {
	s := &m.liveReconnect
	if msg.request != s.request {
		return
	}
	switch m.screen {
	case tuiLiveReconnectLoading:
		if msg.err != nil {
			m.screen, m.status = tuiBrowse, msg.err.Error()
			return
		}
		s.process = msg.process
		m.screen = tuiLiveReconnectConfirm
	case tuiLiveReconnectApplying:
		m.screen = tuiBrowse
		m.status = "重连未确认。按 Ctrl+D 核对接收状态，再决定是否重发。"
		if msg.err != nil {
			return
		}
		switch msg.view.State {
		case bridge.ReconnectStarted:
			m.status = "T3 MCP 已核验，飞书连接中。按 Ctrl+D 查看 WebSocket 状态。"
		case bridge.ReconnectRecoveryRequested:
			m.status = "已受理恢复请求。Ctrl+D 核对连接"
		case bridge.ReconnectWorkerOffline:
			m.status = "App 离线，等待设置未变。Ctrl+R 立即重连"
		case bridge.ReconnectPending:
			m.status = "已有等待重连请求，继续等待 T3 收口。"
		case bridge.ReconnectScheduled:
			m.status = "已暂停新派发，等待 T3 收口后重连。"
		case bridge.ReconnectCancelled:
			m.status = "已取消等待重连，恢复新派发。"
		case bridge.ReconnectIdle:
			m.status = "当前没有等待重连请求。"
		}
	}
}

func (m *configTUI) liveReconnectView() (title, body, shortcuts string) {
	s := m.liveReconnect
	target := receiverStatusApp(s.account.Tenant, s.account.AppID)
	var detail string
	if s.process.ID != "" {
		action := "立即重连"
		detail = "重建此 App 的飞书 WebSocket 和 T3 MCP 连接。"
		switch s.action {
		case bridge.ReconnectAfterRuns:
			action, detail = "等待 T3 收口后重连", "暂停此 App 的新派发。"
		case bridge.ReconnectCancelWait:
			action, detail = "取消等待", "恢复此 App 的新派发。"
		}
		target = "动作 · " + action + "\n" + target + "\n进程 · " + s.process.ID
	}
	switch m.screen {
	case tuiLiveReconnectLoading:
		title, detail, shortcuts = "查找接收进程", "正在查找所选 App 的接收进程…", "Esc 取消"
	case tuiLiveReconnectConfirm:
		title, shortcuts = "确认重连接收端", "y 确认 · Esc 取消"
		if s.process.ReconnectWait {
			shortcuts = "↑↓ 选动作 · " + shortcuts
		}
	case tuiLiveReconnectApplying:
		title, detail = "重连接收端", "正在等待接收进程确认…"
	}
	return title, ansi.Hardwrap(target+"\n"+detail, m.width, true), shortcuts
}
