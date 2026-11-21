package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type configTUITelemetryArchive struct {
	path, revision string
	account        receiverProcessAccount
	day            time.Time
	request        int
	lookup         func(path, appID, revision string, date time.Time) ([]byte, error)
}

type configTUITelemetryArchiveResult struct {
	request int
	data    []byte
	err     error
}

func (m *configTUI) openTelemetryArchive() tea.Cmd {
	source := m.source
	if source.Path == "" || source.Revision == "" || source.Revision == "missing" {
		m.status = "请先保存配置，再查询每日遥测。"
		return nil
	}
	config, err := bridge.ParseConfig(source.JSON)
	if err != nil {
		m.status = "无法解析已打开的保存配置。"
		return nil
	}
	var appID string
	if json.Unmarshal(m.accounts[m.account]["appId"], &appID) != nil || appID == "" {
		m.status = "请核对当前账号的 App ID。"
		return nil
	}
	// The draft can move accounts to different positions, so bind by App ID.
	account, err := selectConfigAccount(config, appID)
	if err != nil {
		m.status = "当前 App 未在已保存配置中，请先保存。"
		return nil
	}
	a := &m.telemetry
	a.path, a.revision = source.Path, source.Revision
	a.account = newReceiverGroupAccount(account.Tenant, account.AppID)
	m.screen, m.status = tuiTelemetryArchiveDate, ""
	return m.openInput(time.Now().UTC().Format(receiverTelemetryArchiveDate))
}

func (m *configTUI) telemetryArchiveKey(key string) (tea.Cmd, bool) {
	if key == "esc" {
		m.input.Blur()
		m.screen, m.status = tuiBrowse, ""
		return nil, true
	}
	if m.screen == tuiTelemetryArchiveDate {
		switch key {
		case "enter":
			return m.requestTelemetryArchive(m.input.Value()), true
		case "f2":
			m.input.Reset()
			return nil, true
		}
		return nil, false
	}
	day := m.telemetry.day
	switch key {
	case "[":
		day = day.AddDate(0, 0, -1)
	case "]":
		day = day.AddDate(0, 0, 1)
	case "r":
	case "e":
		m.screen, m.status = tuiTelemetryArchiveDate, ""
		return m.openInput(day.Format(receiverTelemetryArchiveDate)), true
	default:
		return nil, false
	}
	return m.requestTelemetryArchive(day.Format(receiverTelemetryArchiveDate)), true
}

func (m *configTUI) requestTelemetryArchive(rawDate string) tea.Cmd {
	day, err := parseReceiverTelemetryArchiveDate(rawDate)
	if err != nil {
		m.status = "请输入有效的 UTC 日期（YYYY-MM-DD）。"
		return nil
	}
	m.input.Blur()
	a := &m.telemetry
	a.day = day
	a.request++
	request, lookup := a.request, a.lookup
	path, appID, revision := a.path, a.account.AppID, a.revision
	m.screen, m.status = tuiTelemetryArchiveLoading, ""
	return func() tea.Msg {
		data, err := lookup(path, appID, revision, day)
		return configTUITelemetryArchiveResult{request: request, data: data, err: err}
	}
}

func (m *configTUI) receiveTelemetryArchive(msg configTUITelemetryArchiveResult) {
	a := &m.telemetry
	if m.screen != tuiTelemetryArchiveLoading || msg.request != a.request {
		return
	}
	m.screen, m.status = tuiTelemetryArchiveResult, ""
	var content string
	switch {
	case a.path != m.source.Path, a.revision != m.source.Revision, errors.Is(msg.err, errWorkspaceServiceConfigChanged):
		content = "已保存配置已变更或不可用。\n请重新打开配置后再查询。"
	case msg.err != nil:
		content = "无法读取每日遥测。\n请核对已保存配置和本机归档权限。"
	case len(msg.data) == 0:
		content = "该 UTC 日期暂无遥测记录。"
	default:
		content = strings.TrimSuffix(string(msg.data), "\n")
	}
	m.setReceiverStatusContent(receiverStatusApp(a.account.Tenant, a.account.AppID) + "\n" + content)
}

func (m *configTUI) telemetryArchiveView() (title, body, shortcuts, actions, more string) {
	a := &m.telemetry
	app := ansi.Hardwrap(receiverStatusApp(a.account.Tenant, a.account.AppID), m.width, true)
	more = fmt.Sprintf("最多 %d 行 · 已保存配置", workspaceTelemetryArchiveLines)
	title = "每日遥测归档"
	if m.screen == tuiTelemetryArchiveDate {
		body = app + "\nUTC 日期（YYYY-MM-DD）\n" + ansi.Truncate(m.input.View(), m.width, "")
		return title, body, "Enter 查询 · F2 清空 · Esc 返回", "", more
	}
	title += " · " + a.day.Format(receiverTelemetryArchiveDate) + " UTC"
	shortcuts = "↑↓←→ 滚动 · Esc 返回"
	actions = "[ 上一天 · ] 下一天 · r 刷新 · e 改日期"
	if m.screen == tuiTelemetryArchiveLoading {
		return title, app + "\n正在读取每日遥测…", "Esc 返回", actions, more
	}
	return title, m.review.View(), shortcuts, actions, more
}
