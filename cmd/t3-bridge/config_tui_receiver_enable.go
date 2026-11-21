package main

import (
	"context"
	"encoding/json"
	"errors"

	tea "github.com/charmbracelet/bubbletea"
)

type configTUIReceiverEnableResult struct {
	draft receiverEnableDraft
	err   error
}

func (m *configTUI) openReceiverEnable() tea.Cmd {
	if issue := m.savedServiceConfigIssue(); issue != "" {
		m.status = issue
		return nil
	}
	var appID string
	if err := json.Unmarshal(m.accounts[m.account]["appId"], &appID); err != nil || appID == "" {
		m.status = "请核对当前账号的 App ID"
		return nil
	}
	m.screen, m.status = tuiReceiverEnableLoading, ""
	prepare, path, revision := m.prepareReceiver, m.source.Path, m.source.Revision
	return func() tea.Msg {
		draft, err := prepare(context.Background(), path, revision, appID)
		return configTUIReceiverEnableResult{draft: draft, err: err}
	}
}

func (m *configTUI) receiveReceiverEnable(msg configTUIReceiverEnableResult) {
	if m.screen != tuiReceiverEnableLoading {
		return
	}
	if msg.err == nil && msg.draft.Candidate != nil {
		m.previewDraft(msg.draft.Candidate)
		if m.reviewed != nil {
			m.draft = string(m.reviewed)
			m.refreshFields()
			m.status = "接收预检通过 · 请核对差异并按 s 保存"
		}
		return
	}
	var lines []string
	switch {
	case errors.Is(msg.err, errWorkspaceServiceConfigChanged):
		lines = []string{"配置文件已更新或不可用，请重新打开配置后再预检。"}
	case msg.err != nil:
		lines = []string{msg.err.Error()}
	default:
		lines = append([]string{"所选应用未通过接收预检"}, receiverPreflightDetails(msg.draft.Report)...)
	}
	m.service.preflightReturn = tuiBrowse
	m.showServicePreflightResult(lines)
}
