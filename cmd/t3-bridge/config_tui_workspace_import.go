package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type configTUIWorkspaceImport struct {
	request      workspaceImportRequest
	review       string
	inspection   workspaceImportInspection
	returnScreen configTUIScreen
	alias, field int
	entry        workspaceImportDraftEntry
}

var configTUIWorkspaceImportLabels = [...]string{
	"目标用户 Open ID（ou_…）",
	"目标聊天 ID（oc_…）",
	"目标聊天类型（p2p / group）",
	"目标路由 ID",
	"目标话题 ID（如适用）",
	"目标项目 ID（可选）",
}

func (f *configTUIWorkspaceImport) entryField() *string {
	e := &f.entry
	return [...]*string{&e.UserID, &e.ChatID, &e.ChatType, &e.RouteID, &e.TopicID, &e.ProjectID}[f.field]
}

func (m *configTUI) openWorkspaceImport() tea.Cmd {
	switch {
	case m.source.Path == "":
		m.status = "请先保存配置。按 p 校验并导出，再打开已保存的文件。"
		return nil
	case m.draft != string(m.source.JSON), m.source.Revision == "", m.source.Revision == "missing":
		m.status = "配置尚未保存。请按 p 校验，再按 s 保存。"
		return nil
	}
	var appID string
	_ = json.Unmarshal(m.accounts[m.account]["appId"], &appID)
	if appID == "" {
		m.status = "请在目标账号填写并保存 App ID。"
		return nil
	}
	m.workspaceImport = configTUIWorkspaceImport{request: workspaceImportRequest{AppID: appID}}
	m.screen, m.status = tuiWorkspaceImportPath, ""
	return m.openInput("")
}

func (m *configTUI) workspaceImportKey(key string) (tea.Cmd, bool) {
	f := &m.workspaceImport
	switch m.screen {
	case tuiWorkspaceImportPath:
		switch key {
		case "enter", "tab":
			f.request.LegacyPath = m.input.Value()
			m.input.Blur()
			m.screen, m.status = tuiWorkspaceImportMapping, ""
			return m.openJSONEditor(f.request.MappingJSON), true
		case "f3":
			f.request.LegacyPath = m.input.Value()
			m.inspectWorkspaceAliases()
		case "f2":
			m.input.Reset()
		case "esc":
			m.input.Blur()
			m.workspaceImport = configTUIWorkspaceImport{}
			m.screen, m.status = tuiBrowse, ""
		default:
			return nil, false
		}
	case tuiWorkspaceImportMapping:
		switch key {
		case "ctrl+s":
			m.previewWorkspaceImport()
		case "f3":
			if m.editor.LineCount() >= configTUIEditorLines {
				m.status = "映射超过编辑器的行数上限。请压缩 JSON。"
				break
			}
			f.request.MappingJSON = m.editor.Value()
			m.inspectWorkspaceAliases()
		case "f2":
			m.editor.Reset()
		case "esc", "shift+tab":
			f.request.MappingJSON = m.editor.Value()
			m.editor.Blur()
			m.screen, m.status = tuiWorkspaceImportPath, ""
			return m.openInput(f.request.LegacyPath), true
		default:
			return nil, false
		}
	case tuiWorkspaceImportAliases:
		switch key {
		case "esc":
			m.screen, m.status = f.returnScreen, ""
			if m.screen == tuiWorkspaceImportPath {
				return m.openInput(f.request.LegacyPath), true
			}
			m.editor.SetWidth(m.width)
			m.editor.SetHeight(m.bodyHeight())
			return m.editor.Focus(), true
		case "up":
			f.alias = max(0, f.alias-1)
		case "down":
			f.alias = min(len(f.inspection.Aliases)-1, f.alias+1)
		case "enter":
			f.entry = workspaceImportDraftEntry{SourceKey: f.inspection.Aliases[f.alias].SourceKey}
			f.field = 0
			m.screen, m.status = tuiWorkspaceImportEntry, ""
			return m.openInput(""), true
		default:
			return nil, false
		}
	case tuiWorkspaceImportEntry:
		switch key {
		case "esc":
			m.input.Blur()
			m.screen, m.status = tuiWorkspaceImportAliases, ""
		case "f2":
			m.input.Reset()
		case "enter", "tab", "shift+tab", "up", "down":
			*f.entryField() = m.input.Value()
			delta, count := 1, len(configTUIWorkspaceImportLabels)
			if key == "up" || key == "shift+tab" {
				delta = -1
			}
			f.field = (f.field + delta + count) % count
			m.status = ""
			return m.openInput(*f.entryField()), true
		case "ctrl+s":
			*f.entryField() = m.input.Value()
			return m.addWorkspaceImportEntry(), true
		default:
			return nil, false
		}
	case tuiWorkspaceImportReview:
		switch key {
		case "s":
			if f.review == "" {
				break
			}
			result, err := applyWorkspaceImport(m.source, f.request, f.review)
			if err != nil {
				m.status = err.Error()
				break
			}
			f.review, m.status = "", ""
			m.review.SetContent(configTUIPlain("旧工作区来源\n" + result.LegacyPath))
			m.review.SetXOffset(0)
			m.review.GotoTop()
		case "esc":
			if f.review == "" {
				m.workspaceImport = configTUIWorkspaceImport{}
				m.screen, m.status = tuiBrowse, ""
				break
			}
			f.review = ""
			m.screen, m.status = tuiWorkspaceImportMapping, ""
			return m.openJSONEditor(f.request.MappingJSON), true
		default:
			return nil, false
		}
	}
	return nil, true
}

func (m *configTUI) inspectWorkspaceAliases() {
	f := &m.workspaceImport
	inspection, err := inspectWorkspaceImport(m.source, f.request)
	if err != nil {
		m.status = err.Error()
		return
	}
	if len(inspection.Aliases) == 0 {
		m.status = fmt.Sprintf("未找到可用旧别名 · 无效 %d 个", inspection.Invalid)
		return
	}
	f.inspection, f.alias, f.returnScreen = inspection, 0, m.screen
	m.input.Blur()
	m.editor.Blur()
	m.screen, m.status = tuiWorkspaceImportAliases, ""
}

func (m *configTUI) addWorkspaceImportEntry() tea.Cmd {
	f := &m.workspaceImport
	if f.inspection.Aliases[f.alias].TopicRequired && strings.TrimSpace(f.entry.TopicID) == "" {
		m.status = "此旧别名需要填写目标话题 ID"
		return nil
	}
	draft, err := appendWorkspaceImportMapping(f.request.MappingJSON, f.entry)
	if err != nil {
		m.status = err.Error()
		return nil
	}
	if strings.Count(draft, "\n") >= configTUIEditorLines-1 {
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(draft)); err != nil {
			m.status = err.Error()
			return nil
		}
		draft = compact.String()
	}
	f.request.MappingJSON = draft
	m.input.Blur()
	m.screen, m.status = tuiWorkspaceImportMapping, "已添加 1 条映射到草稿 · 按 Ctrl+S 校验权限并预览"
	return m.openJSONEditor(draft)
}

func (m *configTUI) previewWorkspaceImport() {
	if m.editor.LineCount() >= configTUIEditorLines {
		m.status = "映射超过编辑器的行数上限。请压缩 JSON。"
		return
	}
	f := &m.workspaceImport
	f.request.MappingJSON = m.editor.Value()
	result, review, err := previewWorkspaceImport(m.source, f.request)
	if err != nil {
		m.status = err.Error()
		return
	}
	var summary strings.Builder
	fmt.Fprintf(&summary, "旧工作区来源\n%s\n", result.LegacyPath)
	fmt.Fprintf(&summary, "已映射 %d 个 · 未映射 %d 个\n", len(result.Aliases), result.Unmapped)
	for _, alias := range result.Aliases {
		action := "新增"
		if alias.Action == "unchanged" {
			action = "保持原样"
		}
		fmt.Fprintf(&summary, "\n%s · 别名 %s\n项目 %s · 路由 %s\n目录 %s\n会话 %s · %s\n用户 %s\n",
			action, alias.Name, alias.ProjectID, alias.RouteID, alias.Path, alias.ChatType, alias.ChatID, alias.UserID)
		if alias.TopicID != "" {
			fmt.Fprintf(&summary, "话题 %s\n", alias.TopicID)
		}
	}
	f.review = review
	m.editor.Blur()
	m.review.SetContent(configTUIPlain(strings.TrimRight(summary.String(), "\n")))
	m.review.SetXOffset(0)
	m.review.GotoTop()
	m.screen, m.status = tuiWorkspaceImportReview, "只读预览 · 等待确认写入"
}

func (m *configTUI) workspaceImportView() (title, body, shortcuts, actions string) {
	f := &m.workspaceImport
	app := " · App " + f.request.AppID
	const writeNote = "写入前先停止目标接收端。源文件和配置原样保留。"
	switch m.screen {
	case tuiWorkspaceImportPath:
		title = "旧 workspaces.json 绝对路径" + app
		body = ansi.Truncate(m.input.View(), m.width, "")
		shortcuts = "F3 查看旧别名 · Enter / Tab 编辑 JSON · Esc 取消 · F2 清空"
		actions = "留空时，预览按目标 App 和租户匹配旧单账号配置或唯一 V2 profile。"
	case tuiWorkspaceImportMapping:
		title = "工作区映射 JSON" + app
		body = m.editor.View()
		shortcuts = "F3 查看旧别名 · Ctrl+S 预览 · Esc 返回路径 · F2 清空"
		actions = "T3 环境内唯一匹配旧目录时可省略 projectId。见 docs/CONFIGURATION.md"
	case tuiWorkspaceImportAliases:
		title = "选择旧工作区别名" + app
		body = m.workspaceImportAliasesView()
		shortcuts = "↑↓ 滚动 · Enter 选中 · Esc 返回"
		actions = fmt.Sprintf("%d/%d · 无效 %d 个 · %s", f.alias+1, len(f.inspection.Aliases), f.inspection.Invalid, f.inspection.LegacyPath)
	case tuiWorkspaceImportEntry:
		label := configTUIWorkspaceImportLabels[f.field]
		alias := f.inspection.Aliases[f.alias]
		if f.field == 4 && alias.TopicRequired {
			label = "目标话题 ID（必填）"
		}
		title = fmt.Sprintf("添加映射 · %d/%d · %s", f.field+1, len(configTUIWorkspaceImportLabels), label)
		body = ansi.Truncate(m.input.View(), m.width, "")
		shortcuts = "Ctrl+S 加入草稿 · Esc 返回 · ↑↓ / Tab / Enter 换字段 · F2 清空"
		actions = "别名 " + alias.Name + " · 可用路由 " + strings.Join(f.inspection.Routes, "、")
	case tuiWorkspaceImportReview:
		title = "工作区迁移预览" + app
		body = m.review.View()
		shortcuts = "s 写入目标状态 · ↑↓←→ 滚动 · Esc 返回映射"
		actions = writeNote
		if m.workspaceImport.review == "" {
			title = "已写入目标状态" + app
			shortcuts = "←→ 滚动 · Esc 返回配置"
			actions = ""
		}
	}
	return
}

func (m *configTUI) workspaceImportAliasesView() string {
	f := &m.workspaceImport
	aliases := f.inspection.Aliases
	limit := max(1, m.bodyHeight()-2)
	first := max(0, f.alias-limit+1)
	var lines []string
	for i := first; i < min(len(aliases), first+limit); i++ {
		marker := "  "
		if i == f.alias {
			marker = "› "
		}
		if aliases[i].TopicRequired {
			marker += "[话题] "
		}
		lines = append(lines, marker+aliases[i].Name+" · "+aliases[i].Path)
	}
	selected := aliases[f.alias]
	if m.bodyHeight() > 1 {
		lines = append(lines, "源键 · "+strings.ReplaceAll(selected.SourceKey, "\x1f", " / "))
	}
	if m.bodyHeight() > 2 {
		lines = append(lines, "目录 · "+selected.Path)
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(configTUIPlain(strings.ReplaceAll(line, "\n", " ")), m.width, "…")
	}
	return strings.Join(lines, "\n")
}
