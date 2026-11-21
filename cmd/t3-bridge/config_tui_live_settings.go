package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type configTUILiveSettings struct {
	account     receiverProcessAccount
	process     string
	operate     liveSettingsOperation
	fields      []runtimeSettingField
	row, choice int
	next        string
	patch       bridge.RunSettingsPatch
	request     int
	cancel      context.CancelFunc
	applying    bool
}

type configTUILiveSettingsResult struct {
	request int
	process string
	view    bridge.RunSettingsView
	err     error
}

func (m *configTUI) openLiveSettings(list func() ([]receiverProcess, error), operate liveSettingsOperation) tea.Cmd {
	account, err := m.liveAccount()
	if err != nil {
		m.status = err.Error()
		return nil
	}
	m.liveSettings = configTUILiveSettings{account: account, operate: operate, request: m.liveSettings.request}
	return m.requestLiveSettings(bridge.RunSettingsPatch{}, list)
}

func liveSettingsProcess(list func() ([]receiverProcess, error), account receiverProcessAccount) (string, error) {
	processes, err := list()
	if err != nil {
		return "", err
	}
	var id string
	for _, process := range processes {
		if !process.RunSettings || !slices.Contains(process.Accounts, account) {
			continue
		}
		if id != "" {
			return "", errors.New("多个在线进程匹配，请用 config live-settings -process 指定")
		}
		id = process.ID
	}
	if id == "" {
		return "", errors.New("当前账号没有支持在线运行设置的接收进程")
	}
	return id, nil
}

func (m *configTUI) requestLiveSettings(patch bridge.RunSettingsPatch, list func() ([]receiverProcess, error)) tea.Cmd {
	s := &m.liveSettings
	ctx, cancel := context.WithTimeout(context.Background(), liveSettingsTimeout)
	s.request++
	s.cancel, s.applying = cancel, patch != (bridge.RunSettingsPatch{})
	m.screen, m.status = tuiLiveSettingsLoading, ""
	request, process, account, operate := s.request, s.process, s.account, s.operate
	return func() tea.Msg {
		defer cancel()
		result := configTUILiveSettingsResult{request: request, process: process}
		if list != nil {
			result.process, result.err = liveSettingsProcess(list, account)
		}
		if result.err == nil {
			result.err = ctx.Err()
		}
		if result.err == nil {
			result.view, result.err = operate(ctx, result.process, account.Tenant, account.AppID, patch)
		}
		return result
	}
}

func (m *configTUI) receiveLiveSettings(msg configTUILiveSettingsResult) {
	s := &m.liveSettings
	if m.screen != tuiLiveSettingsLoading || msg.request != s.request {
		return
	}
	s.cancel = nil
	if msg.err != nil {
		m.screen, m.status = tuiBrowse, msg.err.Error()
		if s.applying {
			m.status = "在线更新未确认，请按 F11 重新读取生效值 · " + m.status
		}
		return
	}
	s.process, s.fields = msg.process, runtimeSettingsFields(msg.view)
	m.screen = tuiLiveSettingsPick
	if s.applying {
		m.status = "在线运行设置已更新"
	}
}

func (m *configTUI) liveSettingsKey(key string) (tea.Cmd, bool) {
	s := &m.liveSettings
	switch m.screen {
	case tuiLiveSettingsLoading:
		if key == "esc" && !s.applying {
			s.cancel()
			s.cancel = nil
			m.screen = tuiBrowse
		}
	case tuiLiveSettingsPick:
		switch key {
		case "esc":
			m.screen, m.status = tuiBrowse, ""
		case "up", "k":
			s.row = max(0, s.row-1)
		case "down", "j":
			s.row = min(len(s.fields)-1, s.row+1)
		case "home":
			s.row = 0
		case "end":
			s.row = len(s.fields) - 1
		case "r":
			return m.requestLiveSettings(bridge.RunSettingsPatch{}, nil), true
		case "enter":
			field := s.fields[s.row]
			m.screen, m.status = tuiLiveSettingsEdit, ""
			if field.Max > 0 {
				return m.openInput(field.Value), true
			}
			s.choice = max(0, slices.IndexFunc(field.Choices, func(choice runtimeSettingChoice) bool { return choice.Value == field.Value }))
		}
	case tuiLiveSettingsEdit:
		field := s.fields[s.row]
		switch key {
		case "esc":
			m.input.Blur()
			m.screen, m.status = tuiLiveSettingsPick, ""
		case "enter":
			m.reviewLiveSetting(field)
		default:
			if field.Max > 0 {
				return nil, false
			}
			switch key {
			case "up", "k":
				s.choice = max(0, s.choice-1)
			case "down", "j":
				s.choice = min(len(field.Choices)-1, s.choice+1)
			}
		}
	case tuiLiveSettingsConfirm:
		switch key {
		case "esc":
			m.screen, m.status = tuiLiveSettingsPick, ""
		case "y":
			return m.requestLiveSettings(s.patch, nil), true
		}
	}
	return nil, true
}

func (m *configTUI) reviewLiveSetting(field runtimeSettingField) {
	s := &m.liveSettings
	var next string
	var number int
	if field.Max > 0 {
		var err error
		number, err = strconv.Atoi(strings.TrimSpace(m.input.Value()))
		if err != nil || number < 0 || number > field.Max {
			m.status = fmt.Sprintf("请输入 0 至 %d 的整数", field.Max)
			return
		}
		next = strconv.Itoa(number)
		m.input.Blur()
	} else {
		next = field.Choices[s.choice].Value
	}
	if next == field.Value {
		m.screen, m.status = tuiLiveSettingsPick, "未修改运行设置"
		return
	}
	var patch bridge.RunSettingsPatch
	switch field.Name {
	case "maxConcurrent":
		patch.MaxConcurrent = &number
	case "idleTimeoutMinutes":
		patch.IdleTimeoutMinutes = &number
	case "progress":
		on := next == "on"
		patch.Progress = &on
	case "replyMode":
		patch.ReplyMode = &next
	case "toolActivity":
		patch.ToolActivity = &next
	case "processMode":
		patch.ProcessMode = &next
	}
	s.next, s.patch = next, patch
	m.screen, m.status = tuiLiveSettingsConfirm, ""
}

func (m *configTUI) liveSettingsView() (title, body, shortcuts, actions string) {
	s := &m.liveSettings
	title = "在线运行设置 · " + s.account.Tenant + "/" + s.account.AppID
	if m.screen == tuiLiveSettingsLoading {
		if s.applying {
			return title, "正在等待接收进程确认更新…", "", ""
		}
		return title, "正在读取运行设置…", "Esc 取消", ""
	}
	line := func(value string) string {
		return ansi.Truncate(configTUIPlain(strings.ReplaceAll(value, "\n", " ")), m.width, "…")
	}
	field := s.fields[s.row]
	process := line("进程 · " + s.process)
	scope := line(field.Hint)
	current := liveSettingLabel(field, field.Value)
	switch m.screen {
	case tuiLiveSettingsEdit:
		heading := line(field.Label + " · 当前 " + current)
		if field.Max > 0 {
			return title, strings.Join([]string{heading, scope, m.input.View()}, "\n"), "Enter 预览修改 · Esc 返回", ""
		}
		lines := []string{heading, scope}
		height := max(1, m.bodyHeight()-len(lines))
		first := max(0, s.choice-height+1)
		for i := first; i < min(len(field.Choices), first+height); i++ {
			lines = append(lines, line(liveSettingMarker(i == s.choice)+field.Choices[i].Label))
		}
		return title, strings.Join(lines, "\n"), "↑↓ 选择 · Enter 预览修改 · Esc 返回", ""
	case tuiLiveSettingsConfirm:
		change := line(field.Label + " · " + current + " → " + liveSettingLabel(field, s.next))
		app := line("App · " + s.account.Tenant + "/" + s.account.AppID)
		return "确认在线运行设置", strings.Join([]string{app, process, change, scope}, "\n"), "y 确认即时应用 · Esc 返回", "设置写入此 App 的运行状态"
	}
	lines := []string{process, scope}
	height := max(1, m.bodyHeight()-len(lines))
	first := max(0, s.row-height+1)
	for i := first; i < min(len(s.fields), first+height); i++ {
		field := s.fields[i]
		lines = append(lines, line(liveSettingMarker(i == s.row)+field.Label+" · "+liveSettingLabel(field, field.Value)))
	}
	return title, strings.Join(lines, "\n"), "↑↓ 移动 · Enter 编辑 · r 刷新 · Esc 返回草稿", ""
}

func liveSettingLabel(field runtimeSettingField, value string) string {
	for _, choice := range field.Choices {
		if choice.Value == value {
			return choice.Label
		}
	}
	return value
}

func liveSettingMarker(selected bool) string {
	if selected {
		return "› "
	}
	return "  "
}
