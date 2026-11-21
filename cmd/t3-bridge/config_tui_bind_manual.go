package main

import (
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

const manualBindingCredentialRow = 2

var configTUIManualBindingLabels = [...]string{
	"新应用 ID",
	"新应用的初始用户 Open ID",
	"已有私有凭据文件绝对路径",
	"租户",
}

type configTUIManualBinding struct {
	fields      manualReplacementFields
	row         int
	enterSecret bool
	secretInput textinput.Model
}

func (f *configTUIManualBinding) textFields() [3]*string {
	return [3]*string{&f.fields.AppID, &f.fields.User, &f.fields.AppSecretFile}
}

func (f *configTUIManualBinding) editingSecret() bool {
	return f.enterSecret && f.row == manualBindingCredentialRow
}

func (m *configTUI) openManualBinding() tea.Cmd {
	id := m.binding.selectedAppID
	if err := validateReplacementBinding([]byte(m.draft), id); err != nil {
		m.status = err.Error()
		return nil
	}
	m.binding.manual = configTUIManualBinding{fields: manualReplacementFields{ReplaceAppID: id, Tenant: "feishu"}}
	m.screen, m.status = tuiBindManual, ""
	return m.openInput("")
}

func (m *configTUI) manualBindingKey(key string) (tea.Cmd, bool) {
	f := &m.binding.manual
	fields := f.textFields()
	switch key {
	case "esc":
		m.input.Blur()
		m.binding.manual = configTUIManualBinding{}
		m.screen, m.status = tuiBindReview, ""
	case "f2":
		if f.enterSecret {
			f.secretInput = textinput.Model{}
		} else {
			if f.row == manualBindingCredentialRow {
				f.fields.AppSecretFile = m.input.Value()
				m.input.Blur()
			}
			f.secretInput = textinput.New()
			f.secretInput.Prompt, f.secretInput.Width, f.secretInput.EchoMode = "", m.width, textinput.EchoPassword
			f.secretInput.Cursor.SetMode(cursor.CursorStatic)
			f.secretInput.KeyMap.Paste.SetEnabled(false)
		}
		f.enterSecret = !f.enterSecret
		m.status = ""
		if f.row == manualBindingCredentialRow {
			return m.focusManualBindingRow(), true
		}
	case "ctrl+s":
		if f.row < len(fields) && !f.editingSecret() {
			*fields[f.row] = m.input.Value()
		}
		request := f.fields
		request.AppID = strings.TrimSpace(request.AppID)
		request.User = strings.TrimSpace(request.User)
		request.AppSecretFile = strings.TrimSpace(request.AppSecretFile)
		raw, path := []byte(m.draft), m.source.Path
		var next []byte
		var err error
		if f.enterSecret {
			if path != "" {
				current, readErr := configFileSource(path, nil)
				if readErr != nil || current.Revision != m.source.Revision {
					m.status = "所选配置已变化或无法读取，请重新打开配置后换绑"
					return nil, true
				}
			}
			request.AppSecretFile = ""
			next, err = request.applyWithSecret(raw, path, f.secretInput.Value())
		} else {
			next, err = request.apply(raw, path)
		}
		if err != nil {
			m.status = err.Error()
			return nil, true
		}
		m.input.Blur()
		m.closeFeishuBinding()
		m.setDraft(next)
		m.preview()
	case "down", "tab", "enter", "up", "shift+tab":
		if f.editingSecret() {
			f.secretInput.Blur()
		} else if f.row < len(fields) {
			*fields[f.row] = m.input.Value()
		}
		delta, count := 1, len(configTUIManualBindingLabels)
		if key == "up" || key == "shift+tab" {
			delta = -1
		}
		f.row = (f.row + delta + count) % count
		return m.focusManualBindingRow(), true
	case "left", "right":
		if f.row < len(fields) {
			return nil, false
		}
		if f.fields.Tenant == "feishu" {
			f.fields.Tenant = "lark"
		} else {
			f.fields.Tenant = "feishu"
		}
	default:
		return nil, false
	}
	return nil, true
}

func (m *configTUI) focusManualBindingRow() tea.Cmd {
	f := &m.binding.manual
	fields := f.textFields()
	switch {
	case f.editingSecret():
		m.input.Blur()
		return f.secretInput.Focus()
	case f.row < len(fields):
		return m.openInput(*fields[f.row])
	default:
		m.input.Blur()
		return nil
	}
}

func (m *configTUI) updateManualBindingInput(msg tea.Msg) tea.Cmd {
	f := &m.binding.manual
	if !f.editingSecret() {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return cmd
	}
	if key, ok := msg.(tea.KeyMsg); ok {
		// Textinput replaces pasted line breaks and tabs with spaces.
		if strings.ContainsAny(string(key.Runes), "\x00\r\n\t") {
			f.secretInput.Reset()
			m.status = "凭据输入须为单行文本"
		} else {
			m.status = ""
			f.secretInput, _ = f.secretInput.Update(key)
		}
	}
	return nil
}

func (m *configTUI) manualBindingView() string {
	f := &m.binding.manual
	fields := f.textFields()
	height := m.bodyHeight()
	var lines []string
	active := f.row
	input := &m.input
	if f.editingSecret() {
		input = &f.secretInput
	}
	for i, label := range configTUIManualBindingLabels {
		secretRow := f.enterSecret && i == manualBindingCredentialRow
		if secretRow {
			label = m.language.text("新应用 App Secret")
		}
		line := "  " + label
		if i == f.row {
			line = "› " + label
			if i == len(fields) {
				line += "  ← " + f.fields.Tenant + " →"
			} else if height == 1 {
				line += "  " + input.View()
			}
		} else if secretRow {
			if f.secretInput.Value() != "" {
				line += m.language.text("  已输入")
			}
		} else if i < len(fields) {
			line += "  " + configTUIPlain(*fields[i])
		} else {
			line += "  " + f.fields.Tenant
		}
		lines = append(lines, ansi.Truncate(line, m.width, "…"))
		if i == f.row && i < len(fields) && height > 1 {
			lines = append(lines, input.View())
			active++
		}
	}
	first := max(0, active-height+1)
	return strings.Join(lines[first:min(len(lines), first+height)], "\n")
}
