package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type configTUIReceiverReadiness struct {
	request int
	lookup  func(profileRegistry, string, string) (receiverReadinessReport, error)
}

type configTUIReceiverReadinessResult struct {
	request        int
	path, revision string
	report         receiverReadinessReport
	err            error
}

func (m *configTUI) hasSavedReceiverReadinessSource() bool {
	return m.source.Path != "" && m.source.Revision != "" && m.source.Revision != "missing"
}

func (m *configTUI) requestReceiverReadiness() tea.Cmd {
	if !m.hasSavedReceiverReadinessSource() {
		m.status = "请先保存配置，再检查接收就绪。"
		return nil
	}
	m.readiness.request++
	request, lookup := m.readiness.request, m.readiness.lookup
	path, revision, profiles := m.source.Path, m.source.Revision, m.profiles.registry
	m.screen, m.status = tuiReceiverReadinessLoading, ""
	m.setReceiverStatusContent("正在读取全部 App 的接收就绪状态…")
	return func() tea.Msg {
		report, err := lookup(profiles, path, revision)
		return configTUIReceiverReadinessResult{request: request, path: path, revision: revision, report: report, err: err}
	}
}

func (m *configTUI) receiveReceiverReadiness(msg configTUIReceiverReadinessResult) {
	if m.screen != tuiReceiverReadinessLoading || msg.request != m.readiness.request {
		return
	}
	m.screen = tuiReceiverReadinessResult
	var content string
	switch {
	case msg.path != m.source.Path, msg.revision != m.source.Revision, errors.Is(msg.err, errWorkspaceServiceConfigChanged):
		content = "已保存配置已变更或不可用。\n请重新打开配置后再检查。"
	case msg.err != nil:
		content = "无法读取接收就绪状态。\n请核对已保存配置和本机接收进程。"
	default:
		content = receiverReadinessText(msg.report)
	}
	m.setReceiverStatusContent(content)
}

func (m *configTUI) receiverReadinessKey(key string) (tea.Cmd, bool) {
	switch key {
	case "esc":
		m.screen, m.status = tuiBrowse, ""
		return nil, true
	case "r":
		return m.requestReceiverReadiness(), true
	}
	return nil, false
}

func receiverReadinessText(report receiverReadinessReport) string {
	summary := "整体 · 接收尚未全部就绪"
	if report.Ready {
		summary = "整体 · 已启用 App 均已就绪"
	}
	lines := []string{summary, "保存版本 · " + report.Revision}
	for _, account := range report.Accounts {
		lines = append(lines, "", receiverStatusApp(account.Tenant, account.AppID), receiverReadinessStatusText(account.State))
		if account.Process != "" {
			lines = append(lines, "进程 · "+account.Process, "PID · "+strconv.Itoa(account.PID))
			if account.Version != "" {
				lines = append(lines, "程序版本 · "+strconv.Quote(account.Version))
			}
			if account.State == "different-revision" {
				lines = append(lines, "进程配置版本 · "+account.ProcessRevision)
			}
		}
		if worker := account.Worker; worker != nil {
			lines = append(lines, fmt.Sprintf("重试 · 已失败 %d 次", worker.Attempts))
			if failure := receiverFailureText(worker.Failure); failure != "" {
				lines = append(lines, "故障类别 · "+failure)
			}
			if worker.State == receiverWorkerRetrying {
				lines = append(lines, "下次尝试 · "+worker.RetryAt.Local().Format(time.RFC3339))
			}
		}
		if sample := account.Sample; sample != nil {
			mcp, feishu := "尚未核验", "未就绪"
			if sample.Data.MCPVerified {
				mcp = "核验通过"
			}
			if sample.Data.FeishuReady {
				feishu = "已就绪"
			}
			lines = append(lines, "观察时间 · "+sample.At.Local().Format(time.RFC3339), "T3 最近建连 · "+mcp, "飞书 WebSocket · "+feishu)
		}
	}
	return strings.Join(lines, "\n")
}
