package main

import (
	"context"
	"errors"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type configTUIMeetingPermissionsResult struct {
	request int
	report  bridge.MeetingPermissionReport
	err     error
}

func (m *configTUI) requestMeetingPermissions(availability bool) tea.Cmd {
	s := &m.liveMeetings
	check := s.checkPermissions
	if availability {
		check = s.checkAvailability
	} else {
		source := m.source
		if source.Path == "" || source.Revision == "" || source.Revision == "missing" {
			m.status = "请返回配置页并保存配置，再检查会议权限。"
			return nil
		}
		s.permissionSource, s.previousStatus = source, m.status
	}
	ctx, cancel := context.WithTimeout(context.Background(), meetingPermissionTimeout)
	s.request++
	s.cancel, s.checkingAvailability = cancel, availability
	m.screen, m.status = tuiMeetingPermissionsLoading, ""
	request, source, account := s.request, s.permissionSource, s.account
	return func() tea.Msg {
		defer cancel()
		report, err := check(ctx, source, account.AppID, account.Tenant)
		return configTUIMeetingPermissionsResult{request: request, report: report, err: err}
	}
}

func (m *configTUI) receiveMeetingPermissions(msg configTUIMeetingPermissionsResult) {
	s := &m.liveMeetings
	if m.screen != tuiMeetingPermissionsLoading || msg.request != s.request {
		return
	}
	s.cancel = nil
	m.screen = tuiMeetingPermissionsResult
	lines := []string{m.liveMeetingAppText()}
	switch {
	case errors.Is(msg.err, errMeetingPermissionSourceChanged):
		lines = append(lines, "已保存配置发生变化。请重新打开配置后再检查。")
	case errors.Is(msg.err, context.DeadlineExceeded) && s.checkingAvailability:
		lines = append(lines, "查询超时。请按 v 重试。")
	case errors.Is(msg.err, context.DeadlineExceeded):
		lines = append(lines, "检查超时。请返回会议页重试。")
	case msg.err != nil:
		lines = append(lines, "无法检查会议权限。请核对已保存的 App 和凭据。")
	default:
		config := "已保存配置 · 会议未启用"
		if msg.report.Enabled {
			config = "已保存配置 · 会议已启用"
		}
		lines = append(lines, config)
		if msg.report.Availability != "" {
			lines = append(lines, meetingAvailabilityText(msg.report.Availability), "未检查入会放行或实际推送。")
		} else {
			lines = append(lines, "未检查平台资格、入会放行或实际推送。")
		}
		lines = append(lines, meetingPublicationStateText(msg.report.PublicationState))
		if msg.report.PublishedVersionID != "" {
			lines = append(lines, "版本 ID · "+msg.report.PublishedVersionID)
		}
		lines = append(lines, "", "应用权限项与状态")
		for _, scope := range msg.report.Scopes {
			lines = append(lines, meetingPermissionStateText(scope.State)+" · "+scope.Scope+" · "+meetingPermissionScopeText(scope.Scope))
			if scope.State == "missing" && scope.AuthorizationURL != "" {
				lines = append(lines, scope.AuthorizationURL)
			}
		}
		if len(msg.report.Events) != 0 {
			lines = append(lines, "", "事件订阅状态")
			for _, event := range msg.report.Events {
				lines = append(lines, event+" · "+meetingEventStateText(msg.report.EventStates[event]))
			}
		}
	}
	m.review.Height = m.bodyHeight()
	m.review.SetContent(configTUIPlain(strings.Join(lines, "\n")))
	m.review.SetXOffset(0)
	m.review.GotoTop()
}

func (m *configTUI) meetingPermissionsView() (title, body, shortcuts, actions string) {
	if m.screen == tuiMeetingPermissionsLoading {
		title = "正在检查会议权限…"
		if m.liveMeetings.checkingAvailability {
			title = "正在查询活动会议接口…"
		}
		return title, ansi.Hardwrap(m.liveMeetingAppText(), m.width, true), "Esc 取消", ""
	}
	return "会议权限检查", m.review.View(), "v 查接口 · ↑↓←→ 滚动 · Esc 返回", "查所选 App 所有者的活动会议，仅显示状态"
}
