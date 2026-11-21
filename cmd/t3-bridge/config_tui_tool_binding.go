package main

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

var configTUIToolBindingModes = [...]string{"bot", "user", "user-default", "off"}

type configTUIToolBindingForm struct {
	draft         toolBindingDraft
	environmentID string
	current       *bridge.ToolContext
	row           int
}

type configTUIToolBindingField struct {
	label string
	value *string
}

func (f *configTUIToolBindingForm) fields() []configTUIToolBindingField {
	d := &f.draft
	fields := []configTUIToolBindingField{{label: "身份模式"}}
	if d.Mode == "" || d.Mode == "off" {
		return fields
	}
	fields = append(fields,
		configTUIToolBindingField{"CLI 路径", &d.CLIPath},
		configTUIToolBindingField{"工具 App ID", &d.AppID},
		configTUIToolBindingField{"租户（feishu / lark）", &d.Tenant},
		configTUIToolBindingField{"CLI profile", &d.Profile},
		configTUIToolBindingField{"CLI 配置目录", &d.ConfigDir},
	)
	if d.Mode == "bot" || d.Mode == "user-default" {
		fields = append(fields, configTUIToolBindingField{"机器人 Open ID", &d.BotOpenID})
	}
	if d.Mode == "user" || d.Mode == "user-default" {
		fields = append(fields, configTUIToolBindingField{"用户 Open ID", &d.UserOpenID})
	}
	return fields
}

func (m *configTUI) openToolBinding() tea.Cmd {
	if m.route == "" {
		m.status = "先打开要编辑工具绑定的路由"
		return nil
	}
	var target struct {
		EnvironmentID string              `json:"environmentId"`
		ToolContext   *bridge.ToolContext `json:"toolContext"`
	}
	if err := json.Unmarshal(m.routes[m.route], &target); err != nil {
		m.status = err.Error()
		return nil
	}
	account := m.accounts[m.account]
	var appID, tenant string
	_ = json.Unmarshal(account["appId"], &appID)
	_ = json.Unmarshal(account["tenant"], &tenant)
	m.toolBinding = configTUIToolBindingForm{
		draft:         toolBindingFields(target.ToolContext, appID, tenant),
		environmentID: target.EnvironmentID, current: target.ToolContext,
	}
	m.input.Blur()
	m.screen, m.status = tuiToolBinding, ""
	return nil
}

func (m *configTUI) toolBindingKey(key string) (tea.Cmd, bool) {
	f := &m.toolBinding
	switch key {
	case "ctrl+s", "esc":
		if key == "ctrl+s" {
			if f.row != 0 {
				*f.fields()[f.row].value = m.input.Value()
			}
			if !m.applyToolBinding() {
				return nil, true
			}
		} else {
			m.status = ""
		}
		m.input.Blur()
		m.toolBinding = configTUIToolBindingForm{}
		m.screen = tuiBrowse
	case "down", "tab", "enter", "up", "shift+tab":
		fields := f.fields()
		if f.row != 0 {
			*fields[f.row].value = m.input.Value()
		}
		delta := 1
		if key == "up" || key == "shift+tab" {
			delta = -1
		}
		f.row = (f.row + delta + len(fields)) % len(fields)
		if f.row != 0 {
			return m.openInput(*fields[f.row].value), true
		}
		m.input.Blur()
	case "left", "right":
		if f.row != 0 {
			return nil, false
		}
		modes := configTUIToolBindingModes[:]
		mode := slices.Index(modes, f.draft.Mode)
		if key == "right" {
			mode = (mode + 1) % len(modes)
		} else {
			mode = (max(0, mode) + len(modes) - 1) % len(modes)
		}
		f.draft.Mode = modes[mode]
	case "f2":
		if f.row != 0 {
			m.input.Reset()
		}
	default:
		return nil, false
	}
	return nil, true
}

func (m *configTUI) applyToolBinding() bool {
	f := &m.toolBinding
	tool, err := f.draft.toolContext(f.environmentID, f.current)
	if err != nil {
		m.status = err.Error()
		return false
	}
	edit := configTUIEdit{Path: m.accountPath("routes", m.route, "toolContext"), Delete: tool == nil}
	if tool != nil {
		edit.Value = tool
	}
	return m.applyChanges(edit)
}

func (m *configTUI) toolBindingView() (title, body, shortcuts, actions string) {
	f := &m.toolBinding
	mode := f.draft.Mode
	switch {
	case mode == "off":
		mode = m.language.text("off（移除 toolContext）")
	case mode == "" && f.current != nil:
		mode = m.language.text("原始 JSON · 请选择模式")
	case mode == "":
		mode = m.language.text("请选择模式")
	}
	fields := f.fields()
	lines := make([]string, 0, len(fields)+1)
	for i, field := range fields {
		line := "  " + field.label
		if i == f.row {
			line = "› " + field.label
		}
		switch {
		case i == 0:
			line += "  ← " + mode + " →"
		case i != f.row:
			line += "  " + *field.value
		}
		lines = append(lines, ansi.Truncate(configTUIPlain(strings.ReplaceAll(line, "\n", " ")), m.width, "…"))
		if i == f.row && i != 0 {
			lines = append(lines, m.input.View())
		}
	}
	height := m.bodyHeight()
	active := f.row
	if f.row != 0 && height > 1 {
		active++
	}
	first := max(0, active-height+1)
	title = m.accountTitle() + " / " + m.route + m.language.text(" / 飞书工具绑定")
	body = strings.Join(lines[first:min(len(lines), first+height)], "\n")
	return title, body, m.language.text("↑↓ / Tab / Shift+Tab / Enter 选字段 · ←→ 选模式"), m.language.text("F2 清空字段 · Ctrl+S 应用到草稿 · Esc 取消")
}
