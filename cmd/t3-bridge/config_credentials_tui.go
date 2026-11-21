package main

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type configTUICredentials struct {
	items    []appCredential
	row      int
	input    textinput.Model
	fromFile bool
	request  int
	applying bool
}

type configTUICredentialsResult struct {
	request            int
	items              []appCredential
	action             string
	updateErr, listErr error
}

func (m *configTUI) openCredentials() tea.Cmd {
	if m.source.Path == "" {
		m.status = "凭据维护需要已保存的 JSON 配置"
		return nil
	}
	m.credentials = configTUICredentials{request: m.credentials.request}
	return m.requestCredentials("")
}

func (m *configTUI) requestCredentials(action string) tea.Cmd {
	c := &m.credentials
	var item appCredential
	var input string
	fromFile := action == "set" && c.fromFile
	if action != "" {
		item = c.items[c.row]
		input = strings.TrimSpace(c.input.Value())
		c.input, c.fromFile = textinput.Model{}, false
	}
	c.request++
	c.applying = action != ""
	m.screen, m.status = tuiCredentialsLoading, ""
	request, path := c.request, m.source.Path
	return func() tea.Msg {
		result := configTUICredentialsResult{request: request, action: action}
		value := input
		if fromFile {
			raw, err := readAppCredential(value)
			if err != nil {
				result.updateErr = errors.New("无法读取来源凭据文件，请核对文件权限和大小")
			} else {
				value = string(raw)
			}
		}
		if action != "" && result.updateErr == nil {
			result.updateErr = item.update(action, value)
		}
		result.items, result.listErr = listAppCredentials(path)
		return result
	}
}

func (m *configTUI) receiveCredentials(msg configTUICredentialsResult) {
	c := &m.credentials
	if m.screen != tuiCredentialsLoading || msg.request != c.request {
		return
	}
	var selected string
	if len(c.items) > 0 {
		selected = c.items[c.row].AppID
	}
	c.items = msg.items
	c.row = max(0, slices.IndexFunc(c.items, func(item appCredential) bool { return item.AppID == selected }))
	c.applying = false
	m.screen = tuiCredentialsPick
	switch {
	case msg.updateErr != nil:
		m.status = msg.updateErr.Error()
	case msg.action == "set":
		m.status = "App Secret 已写入"
	case msg.action == "remove":
		m.status = "App Secret 已删除"
	}
	if msg.listErr != nil {
		if m.status != "" {
			m.status += " · "
		}
		m.status += "读取凭据状态失败 · " + msg.listErr.Error()
	}
}

func (m *configTUI) credentialsKey(key string) (tea.Cmd, bool) {
	c := &m.credentials
	switch m.screen {
	case tuiCredentialsLoading:
		if key == "esc" && !c.applying {
			m.screen, m.status = tuiBrowse, ""
		}
	case tuiCredentialsPick:
		switch key {
		case "esc":
			m.screen, m.status = tuiBrowse, ""
		case "r":
			return m.requestCredentials(""), true
		}
		if len(c.items) == 0 {
			return nil, true
		}
		item := c.items[c.row]
		switch key {
		case "up", "k":
			c.row = max(0, c.row-1)
		case "down", "j":
			c.row = min(len(c.items)-1, c.row+1)
		case "enter", "p":
			if item.Editable {
				c.fromFile = key == "p"
				c.input = textinput.New()
				c.input.Prompt, c.input.Width, c.input.EchoMode = "", m.width, textinput.EchoPassword
				c.input.Cursor.SetMode(cursor.CursorStatic)
				c.input.KeyMap.Paste.SetEnabled(false)
				c.input.Focus()
				m.screen, m.status = tuiCredentialsInput, ""
			}
		case "d":
			if item.Editable && item.Status != "missing" {
				m.screen, m.status = tuiCredentialRemoveConfirm, ""
			}
		}
	case tuiCredentialsInput:
		switch key {
		case "esc":
			m.cancelCredentialEdit()
		case "enter":
			value := strings.TrimSpace(c.input.Value())
			if c.fromFile {
				if !filepath.IsAbs(value) {
					m.status = "请输入私有凭据文件的绝对路径"
					return nil, true
				}
			} else if _, ok := appCredentialText(value); !ok {
				m.status = "请输入非空单行 App Secret，且小于 64 KiB"
				return nil, true
			}
			c.input.Blur()
			m.screen, m.status = tuiCredentialSetConfirm, ""
		default:
			return nil, false
		}
	case tuiCredentialSetConfirm, tuiCredentialRemoveConfirm:
		switch key {
		case "esc":
			m.cancelCredentialEdit()
		case "y":
			if m.screen == tuiCredentialRemoveConfirm {
				return m.requestCredentials("remove"), true
			}
			return m.requestCredentials("set"), true
		}
	}
	return nil, true
}

func (m *configTUI) updateCredentialInput(key tea.KeyMsg) {
	c := &m.credentials
	// Textinput replaces pasted line breaks and tabs with spaces.
	if strings.ContainsAny(string(key.Runes), "\x00\r\n\t") {
		c.input.Reset()
		m.status = "凭据输入须为单行文本"
		return
	}
	m.status = ""
	c.input, _ = c.input.Update(key)
}

func (m *configTUI) cancelCredentialEdit() {
	m.credentials.input, m.credentials.fromFile = textinput.Model{}, false
	m.screen, m.status = tuiCredentialsPick, ""
}

func (m *configTUI) credentialsView() (title, body, shortcuts, actions string) {
	c := &m.credentials
	title = "已保存配置的 App 凭据"
	if m.screen == tuiCredentialsLoading {
		if c.applying {
			return title, "正在维护凭据并重新读取状态…", "", ""
		}
		return title, "正在读取凭据状态…", "Esc 返回", ""
	}
	if len(c.items) == 0 {
		return title, "没有可显示的 App 凭据", "r 刷新 · Esc 返回", ""
	}

	item := c.items[c.row]
	clean := func(value string) string { return configTUIPlain(strings.ReplaceAll(value, "\n", " ")) }
	target := ansi.Hardwrap(clean("目标文件 · "+item.Path), m.width, true)
	identity := func(item appCredential) string {
		return clean(item.AppID + " · " + item.Tenant + " · " + item.Status)
	}
	body = ansi.Hardwrap(identity(item), m.width, true) + "\n" + target
	switch m.screen {
	case tuiCredentialsInput:
		label := "输入新的 App Secret"
		if c.fromFile {
			label = "输入私有来源文件的绝对路径"
		}
		return label, body + "\n\n" + c.input.View(), "Enter 核对目标 · Esc 取消", ""
	case tuiCredentialSetConfirm:
		title = "确认写入 App Secret"
		if c.fromFile {
			title = "确认从私有文件导入 App Secret"
		}
		return title, body, "y 写入 · Esc 取消", ""
	case tuiCredentialRemoveConfirm:
		return "确认删除 App Secret 文件", body, "y 删除文件 · Esc 取消", ""
	}

	visible := max(1, m.bodyHeight()-strings.Count(target, "\n")-1)
	start := max(0, c.row-visible+1)
	var lines []string
	for i := start; i < min(start+visible, len(c.items)); i++ {
		prefix := "  "
		if i == c.row {
			prefix = "> "
		}
		lines = append(lines, ansi.Truncate(prefix+identity(c.items[i]), m.width, "…"))
	}
	shortcuts = "↑↓ 选择 App · r 刷新 · Esc 返回"
	if item.Editable {
		actions = "Enter 设置 · p 导入私有文件"
		if item.Status != "missing" {
			actions += " · d 删除"
		}
	}
	return title, strings.Join(append(lines, target), "\n"), shortcuts, actions
}
