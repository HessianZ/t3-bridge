package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
)

type configTUIDrain struct {
	request int
	check   func(path, revision string) (bridge.DrainReport, error)
}

type configTUIDrainResult struct {
	request int
	report  bridge.DrainReport
	err     error
}

func (m *configTUI) openDrain() tea.Cmd {
	path, revision := m.source.Path, m.source.Revision
	if path == "" || revision == "" || revision == "missing" {
		m.status = "请先保存配置文件，再执行排空检查。"
		return nil
	}
	m.screen, m.status = tuiDrainLoading, ""
	m.drain.request++
	request, check := m.drain.request, m.drain.check
	return func() tea.Msg {
		report, err := check(path, revision)
		return configTUIDrainResult{request: request, report: report, err: err}
	}
}

func (m *configTUI) drainKey(key string) bool {
	if key != "esc" {
		return false
	}
	m.screen, m.status = tuiBrowse, ""
	return true
}

func (m *configTUI) receiveDrain(msg configTUIDrainResult) {
	if m.screen != tuiDrainLoading || msg.request != m.drain.request {
		return
	}
	m.screen = tuiDrainResult
	var content string
	switch {
	case errors.Is(msg.err, errWorkspaceServiceConfigChanged):
		content = "配置文件已更新或不可用，请重新打开配置后再检查。"
	case msg.err != nil:
		content = "无法执行排空检查，请核对已保存的配置文件。"
	default:
		content = drainReportText(msg.report)
	}
	m.review.Height = m.bodyHeight()
	m.review.SetContent(content)
	m.review.SetXOffset(0)
	m.review.GotoTop()
}

func drainReportText(report bridge.DrainReport) string {
	summary := "整体 · 未能确认全部 App 已排空"
	if report.Drained {
		summary = "整体 · 全部 App 已排空"
	}
	lines := []string{summary, "已保存配置 · 含未启用接收的 App"}
	for _, account := range report.Accounts {
		appID := configTUIPlain(strings.ReplaceAll(account.AppID, "\n", " "))
		lines = append(lines, "", appID+" · "+drainStatusText(account.Status))
		for _, blocker := range account.Blockers {
			lines = append(lines, fmt.Sprintf("  %s · %d", drainBlockerText(blocker.Code), blocker.Count))
		}
		if len(account.DeliverySamples) > 0 {
			lines = append(lines, "  投递待核对样本（总数以待核对计数为准）")
			for _, sample := range account.DeliverySamples {
				messageID := configTUIPlain(strings.ReplaceAll(sample.MessageID, "\n", " "))
				lines = append(lines, "  "+drainDeliverySampleText(sample.Kind)+" · "+messageID)
			}
			lines = append(lines, "  返回后选中此 App，按 v 查回执")
		}
	}
	return strings.Join(lines, "\n")
}

func (m *configTUI) drainView() (title, body, shortcuts, actions string) {
	actions = m.language.text("停止接收端后运行 · 仅反映当前本地状态快照")
	if m.screen == tuiDrainLoading {
		return m.language.text("离线排空检查"), m.language.text("正在检查已保存配置的全部 App…"), m.language.text("Esc 取消"), actions
	}
	return m.language.text("离线排空检查结果"), m.review.View(), m.language.text("↑↓←→ 滚动 · Esc 返回"), actions
}
