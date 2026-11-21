package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type configTUIService struct {
	instance        string
	install         func(string, webSource) error
	run             func([]string, io.Writer) error
	preflight       func(context.Context, string, string, bool) (bridge.ReceiverPreflight, error)
	preflightApp    func(context.Context, string, string, string, bool) (bridge.ReceiverPreflight, error)
	preflightAppID  string
	preflightReturn configTUIScreen
}

type configTUIServiceResult struct{ err error }

type configTUIServicePreflight struct {
	report bridge.ReceiverPreflight
	err    error
}

func (m *configTUI) savedServiceConfigIssue() string {
	switch {
	case m.source.Path == "":
		return "请先保存配置。按 p 校验并导出，再打开已保存的文件。"
	case m.draft != string(m.source.JSON), m.source.Revision == "", m.source.Revision == "missing":
		return "配置尚未保存。请按 p 校验，再按 s 保存。"
	}
	return ""
}

func (m *configTUI) openServiceInstall() tea.Cmd {
	if issue := m.savedServiceConfigIssue(); issue != "" {
		m.status = issue
		return nil
	}
	m.service.instance = ""
	m.screen, m.status = tuiServiceName, ""
	return m.openInput("")
}

func (m *configTUI) openServicePreflight(key string) tea.Cmd {
	if issue := m.savedServiceConfigIssue(); issue != "" {
		m.status = issue
		return nil
	}
	var appID string
	if key == "A" {
		if err := json.Unmarshal(m.accounts[m.account]["appId"], &appID); err != nil || appID == "" {
			m.status = "请核对当前账号的 App ID"
			return nil
		}
	}
	m.service.preflightAppID = appID
	m.service.preflightReturn = m.screen
	m.screen, m.status = tuiServicePreflightLoading, ""
	check, checkApp := m.service.preflight, m.service.preflightApp
	path, revision := m.source.Path, m.source.Revision
	return func() tea.Msg {
		ctx := context.Background()
		if appID != "" {
			report, err := checkApp(ctx, path, revision, appID, true)
			return configTUIServicePreflight{report, err}
		}
		report, err := check(ctx, path, revision, key == "P")
		return configTUIServicePreflight{report, err}
	}
}

func (m *configTUI) serviceKey(key string) (tea.Cmd, bool) {
	switch m.screen {
	case tuiServiceName:
		switch key {
		case "esc":
			m.input.Blur()
			m.screen, m.status = tuiBrowse, ""
		case "enter":
			instance := strings.TrimSpace(m.input.Value())
			if !serviceInstanceName.MatchString(instance) {
				m.status = "实例名须为 1–48 个小写字母、数字或连字符，并以字母或数字开头。"
				break
			}
			m.input.Blur()
			m.service.instance = instance
			m.screen, m.status = tuiServiceConfirm, ""
		default:
			return nil, false
		}
	case tuiServiceConfirm:
		switch key {
		case "esc":
			m.screen, m.status = tuiServiceName, ""
			return m.openInput(m.service.instance), true
		case "p", "P":
			return m.openServicePreflight(key), true
		case "y":
			m.screen, m.status = tuiServiceInstalling, ""
			install, instance, source := m.service.install, m.service.instance, m.source
			return func() tea.Msg { return configTUIServiceResult{err: install(instance, source)} }, true
		}
	case tuiServicePreflightResult:
		if key != "esc" {
			return nil, false
		}
		m.service.preflightAppID = ""
		m.screen, m.status = m.service.preflightReturn, ""
	case tuiServiceInstalled:
		switch key {
		case "esc":
			m.screen, m.status = tuiBrowse, "后台服务已安装 · "+m.service.instance
		case "s":
			m.screen, m.status = tuiServiceStarting, ""
			run, instance := m.service.run, m.service.instance
			return func() tea.Msg {
				return configTUIServiceResult{err: run([]string{"start", "-instance", instance}, io.Discard)}
			}, true
		}
	}
	return nil, true
}

func (m *configTUI) receiveServicePreflight(msg configTUIServicePreflight) {
	if m.screen != tuiServicePreflightLoading {
		return
	}
	var lines []string
	switch {
	case errors.Is(msg.err, errWorkspaceServiceConfigChanged):
		lines = []string{"配置文件已更新或不可用，请重新打开配置后再预检。"}
	case msg.err != nil:
		lines = []string{"预检无法读取有效的配置。请回到配置工作台校验。"}
	default:
		summary := "启动预检未通过"
		if msg.report.Passed {
			summary = "启动预检通过"
		}
		allAccounts := msg.report.AllAccounts
		lines = []string{summary}
		if allAccounts != nil {
			scope := "全部账号预检"
			if m.service.preflightAppID != "" {
				scope = "所选 App 预检"
				lines[0] += "（所选 App）"
				if len(msg.report.Accounts) == 1 && !msg.report.Accounts[0].Enabled {
					lines[0] = "启动未就绪（所选 App 接收授权未启用）"
				}
			} else {
				lines[0] += "（已启用账号）"
			}
			outcome := scope + "未通过"
			if allAccounts.Passed {
				outcome = scope + "通过"
			}
			lines = append(lines, outcome)
		}
		lines = append(lines, receiverPreflightDetails(msg.report)...)
	}
	m.showServicePreflightResult(lines)
}

func receiverPreflightDetails(report bridge.ReceiverPreflight) []string {
	lines := []string{"模板字段和接收锁为本机即时检查。接收连接和消息收发请在启动后核对。"}
	for _, account := range report.Accounts {
		lines = append(lines, "", account.Tenant+" · "+account.AppID)
		if !account.Enabled {
			if report.AllAccounts != nil {
				lines = append(lines, "  接收授权未启用 · 纳入预检")
			} else {
				lines = append(lines, "  接收授权未启用 · 跳过飞书和 T3 核验")
			}
		}
		for _, check := range account.Checks {
			label := "T3 路由 " + check.Route
			switch check.Check {
			case "setup":
				label = "启动配置"
			case "feishu":
				label = "飞书应用身份"
			case "t3":
				label = "T3 环境"
			case "receiver-lock":
				label = "本机 t3-bridge 接收锁"
			case "legacy-receiver-lock":
				label = "本机旧桥接收锁"
			case "state-lock":
				label = "状态目录锁"
			}
			status := "跳过"
			switch check.Status {
			case "ok":
				status = "通过"
			case "failed":
				status = "失败"
			case "unknown":
				status = "未确认"
			case "disabled":
				status = "已停用"
			}
			line := "  " + label + " · " + status
			if check.Detail != "" {
				line += " · " + check.Detail
			}
			lines = append(lines, line)
			for _, field := range check.Fields {
				lines = append(lines, "    "+field)
			}
			if holder := check.LockHolder; holder != nil {
				lines = append(lines, fmt.Sprintf("    旧锁记录 · profile %s · PID %d", holder.Profile, holder.PID))
			}
		}
	}
	return lines
}

func (m *configTUI) showServicePreflightResult(lines []string) {
	m.screen = tuiServicePreflightResult
	m.review.Height = m.bodyHeight()
	m.review.SetContent(configTUIPlain(strings.Join(lines, "\n")))
	m.review.SetXOffset(0)
	m.review.GotoTop()
}

func (m *configTUI) receiveServiceResult(msg configTUIServiceResult) {
	switch m.screen {
	case tuiServiceInstalling:
		switch {
		case errors.Is(msg.err, errWorkspaceServiceConfigChanged):
			m.screen, m.status = tuiBrowse, "配置文件已更新或不可用，请重新打开配置后再安装。"
		case msg.err != nil:
			m.screen, m.status = tuiServiceConfirm, "安装未确认。请核对实例名、配置文件权限和系统服务状态。"
		default:
			m.screen, m.status = tuiServiceInstalled, "后台服务已安装。"
		}
	case tuiServiceStarting:
		if msg.err != nil {
			m.screen, m.status = tuiServiceInstalled, "启动未确认。请核对系统服务状态和配置。"
			return
		}
		m.screen, m.status = tuiBrowse, "已提交启动请求 · "+m.service.instance
	}
}

func (m *configTUI) serviceView() (title, body, shortcuts string) {
	title = m.language.text("安装用户级后台服务")
	if m.screen == tuiServiceName {
		return title + m.language.text(" · 实例名"), m.input.View(), m.language.text("Enter 预览安装 · Esc 返回")
	}
	body = ansi.Truncate(m.language.text("实例名 · ")+m.service.instance, m.width, "…")
	switch m.screen {
	case tuiServiceConfirm:
		return title, body, m.language.text("p 检查启动条件 · P 预检全部账号 · y 确认安装 · Esc 修改实例名")
	case tuiServicePreflightLoading:
		if appID := m.service.preflightAppID; appID != "" {
			body = ansi.Truncate(configTUIPlain(m.language.text("检查已保存配置的所选 App · ")+appID), m.width, "…")
		} else if m.service.preflightReturn == tuiBrowse {
			body = ansi.Truncate(m.language.text("检查已保存配置的全部账号（含未启用接收）"), m.width, "…")
		}
		return m.language.text("正在执行只读 API 启动预检…"), body, ""
	case tuiServicePreflightResult:
		shortcuts = m.language.text("↑↓←→ 滚动 · Esc 返回")
		if m.service.preflightReturn == tuiServiceConfirm {
			shortcuts += m.language.text("安装确认")
		}
		title = m.language.text("只读 API 启动预检结果")
		if m.service.preflightAppID != "" {
			title += m.language.text(" · 所选 App")
		}
		return title, m.review.View(), shortcuts
	case tuiServiceInstalling:
		return m.language.text("正在安装用户级后台服务…"), body, ""
	case tuiServiceStarting:
		return m.language.text("正在启动用户级后台服务…"), body, ""
	default:
		return m.language.text("用户级后台服务"), body, m.language.text("s 启动服务 · Esc 返回")
	}
}
