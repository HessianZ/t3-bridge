package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type configTUIObject map[string]json.RawMessage

type configTUIField struct {
	label   string
	path    []string
	value   string
	text    bool
	routeID string
}

func (field configTUIField) labelIn(lang workspaceLanguage) string {
	if group, key, ok := strings.Cut(field.label, " · "); ok {
		return lang.text(group) + " · " + key
	}
	return lang.text(field.label)
}

type configTUIEdit struct {
	Path   []string `json:"path"`
	Value  any      `json:"value,omitempty"`
	Delete bool     `json:"delete,omitempty"`
}

func (m *configTUI) accountPath(keys ...string) []string {
	if m.account == 0 {
		return keys
	}
	return append([]string{"additionalAccounts", strconv.Itoa(m.account - 1)}, keys...)
}

func (m *configTUI) refreshFields() {
	var primary configTUIObject
	var additional []configTUIObject
	if json.Unmarshal([]byte(m.draft), &primary) == nil {
		_ = json.Unmarshal(primary["additionalAccounts"], &additional)
	}
	m.accounts = append([]configTUIObject{primary}, additional...)
	m.account = min(m.account, len(m.accounts)-1)
	account := m.accounts[m.account]
	m.routes = nil
	_ = json.Unmarshal(account["routes"], &m.routes)
	m.rows = m.rows[:0]
	addFields := func(group string, object configTUIObject, prefix []string, text bool, keys ...string) {
		for _, key := range keys {
			m.rows = append(m.rows, configTUIField{
				label: group + " · " + key, path: append(slices.Clone(prefix), key), value: string(object[key]), text: text,
			})
		}
	}
	if raw, ok := m.routes[m.route]; m.route != "" && ok {
		var target configTUIObject
		_ = json.Unmarshal(raw, &target)
		prefix := m.accountPath("routes", m.route)
		addFields("目标", target, prefix, true, "environmentId", "projectId", "driver", "providerId", "model")
		addFields("执行", target, prefix, false, "options")
		addFields("执行", target, prefix, true, "runtimeMode", "interactionMode")
		addFields("执行", target, prefix, false, "cacheFollowupAttachments")
		addFields("ACL", target, prefix, false, "users")
		addFields("路由", target, prefix, true, "hostName", "projectName", "projectPath", "kind", "unavailableReason")
		addFields("路由", target, prefix, false, "aliases", "checks", "toolContext")
	} else {
		m.route = ""
		for _, id := range slices.Sorted(maps.Keys(m.routes)) {
			if id != "" {
				m.rows = append(m.rows, configTUIField{label: "路由 · " + strconv.Quote(id), routeID: id})
			}
		}
		prefix := m.accountPath()
		var connection configTUIObject
		_ = json.Unmarshal(account["mcp"], &connection)
		addFields("账号", account, prefix, true, "appId", "appSecretFile", "tenant", "receiverMachine", "stateDir")
		addFields("账号", account, prefix, false, "receiverAuthorized")
		addFields("首次启动运行设置", account, prefix, false, "initialRuntime")
		addFields("会议", account, prefix, false, "meetingEnabled", "meetingRoomReplies")
		addFields("会议", account, prefix, false, "meetingAutoJoinUsers")
		addFields("会议", account, prefix, true, "meetingRoute", "meetingSummaryTarget")
		addFields("T3 MCP", connection, m.accountPath("mcp"), true, "endpoint", "runtimeFile", "tokenFile", "environmentId")
		addFields("ACL", account, prefix, false, "users", "admins", "chats", "directChats", "topics")
		addFields("路由", account, prefix, true, "defaultRoute", "reviewRoute", "commentRoute")
	}
	m.rows = append(m.rows, configTUIField{label: "完整 JSON"})
	m.row = min(m.row, len(m.rows)-1)
}

func (m *configTUI) leaveRoute() {
	id := m.route
	m.route, m.row = "", 0
	m.refreshFields()
	m.row = max(0, slices.IndexFunc(m.rows, func(field configTUIField) bool { return field.routeID == id }))
}

func (m *configTUI) accountTitle() string {
	label := m.language.text("主账号")
	if m.account != 0 {
		label = fmt.Sprintf(m.language.text("附加账号 %d/%d"), m.account, len(m.accounts)-1)
	} else if len(m.accounts) > 1 {
		label = fmt.Sprintf(m.language.text("主账号（另有 %d 个附加账号）"), len(m.accounts)-1)
	}
	if id := m.accounts[m.account]["appId"]; len(id) != 0 {
		label += " · " + string(id)
	}
	return label
}

func (m *configTUI) fieldList() string {
	height := m.bodyHeight()
	first := max(0, m.row-height+1)
	var body []string
	for i := first; i < min(len(m.rows), first+height); i++ {
		marker := "  "
		if i == m.row {
			marker = "› "
		}
		field := m.rows[i]
		line := marker + field.labelIn(m.language)
		if len(field.path) != 0 {
			value := m.language.text("（未设置）")
			if field.value != "" {
				value = strings.ReplaceAll(field.value, "\n", " ")
			}
			line += "  " + value
		}
		body = append(body, ansi.Truncate(configTUIPlain(line), m.width, "…"))
	}
	return strings.Join(body, "\n")
}

// Bubbles v0.21.1 cuts pasted input at 10,000 lines. Keep the source or use
// compact JSON at the limit, and refuse to apply input that might be truncated.
const configTUIEditorLines = 10000

func (m *configTUI) openEditor(field configTUIField) tea.Cmd {
	value := field.value
	if field.text {
		var plain string
		_ = json.Unmarshal([]byte(value), &plain)
		m.screen, m.editField, m.status = tuiEdit, field, ""
		cmd := m.openInput(plain)
		m.editStart = m.input.Value()
		return cmd
	}
	if len(field.path) == 0 {
		value = m.draft
	}
	if strings.Count(value, "\n") >= configTUIEditorLines-1 {
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(value)); err != nil {
			m.status = "JSON 无法压缩到编辑器的行数上限"
			return nil
		}
		value = compact.String()
	}
	cmd := m.openJSONEditor(value)
	m.editStart = m.editor.Value()
	m.screen, m.editField, m.status = tuiEdit, field, ""
	return cmd
}

func (m *configTUI) openJSONEditor(value string) tea.Cmd {
	m.editor = textarea.New()
	m.editor.MaxHeight = 0
	m.editor.CharLimit = bridge.MaxConfigBytes + 1
	m.editor.SetWidth(m.width)
	m.editor.SetHeight(m.bodyHeight())
	m.editor.SetValue(value)
	cmd := m.editor.Focus()
	m.editor, _ = m.editor.Update(tea.KeyMsg{Type: tea.KeyCtrlHome})
	return cmd
}

func (m *configTUI) editorValue() string {
	if m.editField.text {
		return m.input.Value()
	}
	return m.editor.Value()
}

func (m *configTUI) applyEdit() {
	value := m.editorValue()
	if value != m.editStart {
		if len(value) > bridge.MaxConfigBytes || (!m.editField.text && m.editor.LineCount() >= configTUIEditorLines) {
			m.status = "草稿超过编辑器上限。请压缩 JSON 或缩短内容。"
			return
		}
		if !m.applyField(value) {
			return
		}
	} else {
		m.status = ""
	}
	m.editor.Blur()
	m.input.Blur()
	m.screen = tuiBrowse
}

func (m *configTUI) applyField(input string) bool {
	path := m.editField.path
	if len(path) == 0 {
		candidate, err := bridge.EditConfigJSON([]byte(input), nil)
		if candidate == nil {
			m.status = err.Error()
			return false
		}
		m.setDraft(candidate)
		return true
	}
	value := strings.TrimSpace(input)
	edit := configTUIEdit{Path: path, Delete: value == ""}
	if !edit.Delete {
		if m.editField.text {
			edit.Value = value
		} else if json.Valid([]byte(value)) {
			edit.Value = json.RawMessage(value)
		} else {
			m.status = "字段值必须是完整的 JSON"
			return false
		}
	}
	return m.applyChanges(edit)
}

func (m *configTUI) applyChanges(changes ...configTUIEdit) bool {
	edits, err := json.Marshal(changes)
	if err != nil {
		m.status = err.Error()
		return false
	}
	candidate, err := bridge.EditConfigJSON([]byte(m.draft), edits)
	if candidate != nil && err != nil {
		// Keep drafts with incomplete targets or stale references editable.
		// A structural error would prevent further structured edits.
		if checked, shapeErr := bridge.EditConfigJSON(candidate, nil); checked == nil {
			candidate, err = nil, shapeErr
		}
	}
	if candidate == nil {
		m.status = err.Error()
		return false
	}
	m.setDraft(candidate)
	return true
}

func (m *configTUI) setDraft(candidate []byte) {
	m.draft, m.reviewed, m.status = string(candidate), nil, "已更新草稿"
	m.refreshFields()
}

func (m *configTUI) preview() {
	m.previewDraft([]byte(m.draft))
}

func (m *configTUI) previewDraft(input []byte) {
	candidate, err := bridge.EditConfigJSON(input, nil)
	original, draft := string(m.source.JSON), string(input)
	if m.source.Revision == "missing" {
		original = ""
	}
	if candidate != nil {
		draft = string(candidate)
	}
	var diff strings.Builder
	m.reviewed, m.status = candidate, "配置校验通过"
	if err != nil {
		m.reviewed, m.status = nil, "配置校验失败"
		fmt.Fprintln(&diff, err)
		fmt.Fprintln(&diff)
	}
	changes := configChanges(original, draft)
	if len(changes) == 0 {
		fmt.Fprintln(&diff, "配置内容无变化")
	}
	for _, change := range changes {
		fmt.Fprintln(&diff, change.Header)
		diff.WriteString(change.Removed)
		diff.WriteString(change.Added)
		fmt.Fprintln(&diff)
	}
	if len(changes) != 0 {
		fmt.Fprintln(&diff, "启动配置 · 保存后重启实例读取")
		fmt.Fprintln(&diff, "在线设置 · 回账号页用 F9 / F11 / F12")
		fmt.Fprintln(&diff, "按 App 重连 · 回账号页用 Ctrl+R")
		fmt.Fprintln(&diff, "initialRuntime · 仅给新绑定的状态赋初值")
	}
	m.review.SetContent(configTUIPlain(diff.String()))
	m.review.SetXOffset(0)
	m.review.GotoTop()
	m.screen = tuiReview
}

func (m *configTUI) save() {
	if m.reviewed == nil {
		return
	}
	if m.source.Path == "" {
		m.status = "可导出到新文件，或通过 -config 指定保存路径"
		return
	}
	revision, err := saveConfigCandidate(m.source.Path, m.reviewed, m.source.Revision)
	if err != nil {
		m.status = err.Error()
		return
	}
	m.source.Revision, m.source.JSON = revision, m.reviewed
	m.draft = string(m.source.JSON)
	m.refreshFields()
	m.screen, m.status = tuiBrowse, "已保存 · 重启接收实例后读取"
}

func (m *configTUI) export() {
	path := m.input.Value()
	if !strings.EqualFold(filepath.Ext(path), ".json") {
		m.status = "导出路径须以 .json 结尾"
		return
	}
	absolute, err := filepath.Abs(path)
	if err == nil {
		_, err = saveConfigCandidate(absolute, m.reviewed, "missing")
	}
	if err != nil {
		m.status = err.Error()
		return
	}
	m.input.Blur()
	m.screen, m.status = tuiReview, "已导出到 "+absolute
}
