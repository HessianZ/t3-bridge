package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

var configTUILegacySessionLabels = [...]string{
	"旧 config.json 绝对路径",
	"旧 T3 profile 名称（可留空）",
	"原 T3 Thread ID",
	"原聊天 ID（oc_…）",
	"用户 Open ID（ou_…）",
	"目标对话路由 ID",
	"聊天类型（私聊留空或 p2p，群聊填 group）",
	"原话题 ID（仅群聊可填）",
}

type configTUILegacySession struct {
	sourcePath string
	request    legacySessionRequest
	row        int
	review     string
	readID     uint64
	candidates []bridge.LegacySessionCandidate
	scopes     []bridge.LegacySessionScope
	selected   int
}

type configTUILegacySessionInspected struct {
	id         uint64
	inspection legacySessionInspection
	err        error
}

type configTUILegacySessionPreview struct {
	id         uint64
	candidate  []byte
	result     bridge.LegacySessionImportResult
	review     string
	verifyT3   bool
	checkLines []string
	err        error
}

type configTUILegacySessionApplied struct {
	result bridge.LegacySessionImportResult
	err    error
}

func (f *configTUILegacySession) field() *string {
	r := &f.request
	return [...]*string{&r.LegacyConfig, &r.LegacyProfile, &r.ThreadID, &r.ChatID, &r.UserID, &r.RouteID, &r.ChatType, &r.TopicID}[f.row]
}

func (m *configTUI) openLegacySession() tea.Cmd {
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
	f := &m.legacySession
	if f.sourcePath != m.source.Path || f.request.AppID != appID {
		f.sourcePath, f.row = m.source.Path, 0
		f.request = legacySessionRequest{AppID: appID, RouteID: m.route}
		if path, err := detectLegacyConfig(); err == nil {
			f.request.LegacyConfig = path
		}
	}
	return m.editLegacySession("")
}

func (m *configTUI) editLegacySession(status string) tea.Cmd {
	f := &m.legacySession
	f.review, f.candidates, f.scopes = "", nil, nil
	m.screen, m.status = tuiLegacySessionForm, status
	return m.openInput(*f.field())
}

func (m *configTUI) legacySessionKey(key string) (tea.Cmd, bool) {
	f := &m.legacySession
	switch m.screen {
	case tuiLegacySessionForm:
		switch key {
		case "esc":
			*f.field() = m.input.Value()
			m.input.Blur()
			m.screen, m.status = tuiBrowse, ""
		case "f2":
			m.input.Reset()
		case "down", "tab", "enter", "up", "shift+tab":
			*f.field() = m.input.Value()
			delta, count := 1, len(configTUILegacySessionLabels)
			if key == "up" || key == "shift+tab" {
				delta = -1
			}
			f.row = (f.row + delta + count) % count
			return m.editLegacySession(""), true
		case "f3":
			*f.field() = m.input.Value()
			return m.inspectLegacySession(), true
		case "ctrl+s", "ctrl+t", "ctrl+r":
			*f.field() = m.input.Value()
			return m.previewLegacySession(key), true
		default:
			return nil, false
		}
	case tuiLegacySessionInspecting, tuiLegacySessionLoading:
		if key == "esc" {
			return m.editLegacySession(""), true
		}
	case tuiLegacySessionPick:
		switch key {
		case "esc":
			return m.editLegacySession(""), true
		case "up":
			f.selected = max(0, f.selected-1)
		case "down":
			f.selected = min(len(f.candidates)+len(f.scopes)-1, f.selected+1)
		case "enter":
			if len(f.scopes) > 0 {
				scope := f.scopes[f.selected]
				f.request.ChatID, f.request.TopicID = scope.ChatID, scope.TopicID
				if scope.TopicID != "" {
					f.request.ChatType = "group"
					return m.editLegacySession("已填入群聊和话题 ID。按 F3 查找该话题的线程。"), true
				}
				return m.editLegacySession("已填入聊天 ID，请确认聊天类型。私聊留空或填 p2p，群聊填 group。"), true
			}
			f.request.ThreadID = f.candidates[f.selected].ThreadID
			return m.editLegacySession("已填入原 T3 Thread ID"), true
		}
	case tuiLegacySessionReview:
		switch key {
		case "s":
			if f.review != "" {
				return m.applyLegacySession(), true
			}
		case "esc":
			return m.editLegacySession(""), true
		default:
			return nil, false
		}
	}
	return nil, true
}

func (m *configTUI) inspectLegacySession() tea.Cmd {
	f := &m.legacySession
	request := f.request
	if !filepath.IsAbs(request.LegacyConfig) {
		m.status = "请填写旧配置绝对路径。"
		return nil
	}
	f.readID++
	source, id := m.source, f.readID
	m.input.Blur()
	m.screen, m.status = tuiLegacySessionInspecting, ""
	return func() tea.Msg {
		inspection, err := inspectLegacySession(source, request)
		return configTUILegacySessionInspected{id: id, inspection: inspection, err: err}
	}
}

func (m *configTUI) receiveLegacySessionInspection(msg configTUILegacySessionInspected) tea.Cmd {
	f := &m.legacySession
	if m.screen != tuiLegacySessionInspecting || msg.id != f.readID {
		return nil
	}
	if msg.err != nil {
		return m.editLegacySession("查找失败 · " + msg.err.Error())
	}
	if f.request.ChatID == "" {
		if len(msg.inspection.Scopes) == 0 {
			return m.editLegacySession("旧目录没有可盘点的活动 T3 会话范围。")
		}
		f.scopes = msg.inspection.Scopes
	} else if len(msg.inspection.Candidates) == 0 {
		return m.editLegacySession("所选范围没有可用的旧 T3 线程。")
	} else {
		f.candidates = msg.inspection.Candidates
	}
	f.selected = 0
	m.screen, m.status = tuiLegacySessionPick, ""
	return nil
}

func (m *configTUI) previewLegacySession(key string) tea.Cmd {
	f := &m.legacySession
	request := f.request
	if request.ChatID == "" || request.UserID == "" || request.ThreadID == "" || request.RouteID == "" {
		m.status = "请填写原聊天 ID、用户 Open ID、原 T3 Thread ID 和目标路由 ID。"
		return nil
	}
	f.readID++
	source, id := m.source, f.readID
	m.input.Blur()
	m.screen, m.status = tuiLegacySessionLoading, ""
	switch key {
	case "ctrl+r":
		m.status = "正在只读核验 T3 并准备路由配置…"
	case "ctrl+t":
		m.status = "正在只读核验 T3…"
	}
	return func() tea.Msg {
		switch key {
		case "ctrl+r":
			candidate, err := prepareLegacySessionRoute(context.Background(), source, request)
			return configTUILegacySessionPreview{id: id, candidate: candidate, err: err}
		case "ctrl+t":
			result, review, lines, err := previewVerifiedLegacySession(context.Background(), source, request)
			return configTUILegacySessionPreview{id: id, result: result, review: review, verifyT3: true, checkLines: lines, err: err}
		default:
			result, review, err := previewLegacySession(source, request)
			return configTUILegacySessionPreview{id: id, result: result, review: review, err: err}
		}
	}
}

func (m *configTUI) receiveLegacySessionPreview(msg configTUILegacySessionPreview) tea.Cmd {
	f := &m.legacySession
	if m.screen != tuiLegacySessionLoading || msg.id != f.readID {
		return nil
	}
	if msg.err != nil {
		return m.editLegacySession("预览失败 · " + msg.err.Error())
	}
	if msg.candidate != nil {
		m.previewDraft(msg.candidate)
		if m.reviewed != nil {
			m.draft = string(m.reviewed)
			m.refreshFields()
			m.status = "路由配置待保存 · 会话须另行离线登记"
		}
		return nil
	}
	f.review = msg.review
	r := msg.result
	action := "新增待转接记录"
	if r.Action == "unchanged" {
		action = "保留已有待转接记录"
	}
	if r.ArchivedContext {
		action += "\n当前会话已归档，可登记原 T3 线程。"
	}
	scope := configTUILegacySessionScope(r.ChatType, r.TopicID)
	verification := "新接收端收到该私聊的首条输入时核验原 T3 会话。"
	if r.ChatType == "group" {
		verification = "新接收端收到所选用户在该范围的首条输入时，\n须逐条交叉核对原 T3 会话的输入与飞书原始消息。\n每条旧输入都须属于所选用户。混有其他发送者时，运行时会拒绝续接。"
	}
	details := fmt.Sprintf("%s\n范围 · %s\nApp ID · %s\n聊天 ID · %s\n用户 Open ID · %s\n路由 ID · %s\nT3 Thread ID · %s\nT3 环境 ID · %s\nT3 项目 ID · %s\n项目目录 · %s\n\n仅写入会话元数据。\n%s",
		action, scope, r.AppID, r.ChatID, r.UserID, r.RouteID, r.ThreadID, r.EnvironmentID, r.ProjectID, r.ProjectPath, verification)
	if len(msg.checkLines) > 0 {
		details += "\n\nT3 核验结果\n" + strings.Join(msg.checkLines, "\n")
	}
	m.screen = tuiLegacySessionReview
	switch {
	case f.review == "":
		m.status = "当前预览无法确认写入"
	case msg.verifyT3:
		m.status = "T3 核验通过 · 等待确认写入"
	default:
		m.status = "只读预览 · 等待确认写入"
	}
	m.review.Height = m.bodyHeight()
	m.review.SetContent(configTUIPlain(details))
	m.review.SetXOffset(0)
	m.review.GotoTop()
	return nil
}

func (m *configTUI) applyLegacySession() tea.Cmd {
	source, request, review := m.source, m.legacySession.request, m.legacySession.review
	m.screen, m.status = tuiLegacySessionApplying, ""
	return func() tea.Msg {
		result, err := applyLegacySession(source, request, review)
		return configTUILegacySessionApplied{result: result, err: err}
	}
}

func (m *configTUI) receiveLegacySessionApplied(msg configTUILegacySessionApplied) tea.Cmd {
	if m.screen != tuiLegacySessionApplying {
		return nil
	}
	if msg.err != nil {
		return m.editLegacySession("写入失败 · " + msg.err.Error())
	}
	f := &m.legacySession
	f.sourcePath, f.review = "", ""
	f.request = legacySessionRequest{}
	action := "已写入转接元数据"
	if msg.result.Action == "unchanged" {
		action = "已确认现有转接记录"
	}
	m.screen, m.status = tuiBrowse, action+" · "+configTUILegacySessionScope(msg.result.ChatType, msg.result.TopicID)+" · 待首条输入核验"
	return nil
}

func configTUILegacySessionScope(chatType, topicID string) string {
	if chatType != "group" {
		return "私聊"
	}
	if topicID == "" {
		return "群聊 · 无话题"
	}
	return "群聊 · 话题 ID " + topicID
}

func (m *configTUI) legacySessionView() (title, body, shortcuts, actions string) {
	f := &m.legacySession
	app := " · App " + f.request.AppID
	const catalogLimit = "旧目录无法验证聊天类型或成员。"
	title = m.language.text("旧 T3 会话转接") + app
	actions = m.language.text("写入前请停止旧接收端，并确认新 Store 已离线。")
	switch m.screen {
	case tuiLegacySessionForm:
		title = configTUILegacySessionLabels[f.row] + app
		body = ansi.Truncate(m.input.View(), m.width, "")
		shortcuts = m.language.text("F3 盘点/查找 · Ctrl+S 离线预览 · Ctrl+T 预览并核验 T3 · F2 清空 · Esc · ↑↓/Tab")
		switch f.row {
		case 1:
			actions = m.language.text("留空时仅自动选用同 App、同租户的唯一旧 T3 V2 profile。")
		case 3:
			actions = m.language.text("留空按 F3 盘点范围。填好聊天 ID、类型和可选话题后按 F3 查找。")
		case 7:
			actions = m.language.text("群聊留空时查找无话题的线程。转接话题须填完整话题 ID。")
		}
		actions = m.language.text("Ctrl+R 准备路由配置 · ") + actions
	case tuiLegacySessionInspecting:
		body, shortcuts, actions = m.language.text("正在按所选范围查找旧 T3 线程…"), m.language.text("Esc 返回表单"), catalogLimit
		if f.request.ChatID == "" {
			body = m.language.text("正在只读盘点旧 T3 会话范围…")
		}
	case tuiLegacySessionPick:
		title = m.language.text("选择旧 T3 线程") + app + " · " + f.request.ChatID
		body = m.legacySessionPickerView()
		shortcuts = fmt.Sprintf(m.language.text("↑↓ 选择 · %d/%d · Enter 填入 · Esc 返回"), f.selected+1, len(f.candidates))
		actions = catalogLimit
		if len(f.scopes) > 0 {
			title = m.language.text("旧 T3 会话范围（只读盘点）") + app
			selection := fmt.Sprintf(m.language.text("↑↓ 选择 · %d/%d"), f.selected+1, len(f.scopes))
			shortcuts = selection + m.language.text(" · Enter 填入聊天 ID · Esc 返回")
			if f.scopes[f.selected].TopicID != "" {
				shortcuts = selection + m.language.text(" · Enter 填入群聊和话题 · Esc 返回")
			}
		} else if f.request.ChatType == "group" {
			title += " · " + configTUILegacySessionScope(f.request.ChatType, f.request.TopicID)
		}
	case tuiLegacySessionLoading:
		body, shortcuts = m.language.text("正在预览旧 T3 会话…"), m.language.text("Esc 返回表单")
	case tuiLegacySessionReview:
		body, shortcuts = m.review.View(), m.language.text("↑↓←→ 滚动 · Esc 返回表单")
		if f.review != "" {
			shortcuts = m.language.text("s 确认写入 · ") + shortcuts
		} else {
			actions = m.language.text("核对结果后返回表单，按 Ctrl+T 重新核验或 Ctrl+S 离线预览。")
		}
	case tuiLegacySessionApplying:
		body, actions = m.language.text("正在确认新 Store 离线并写入会话元数据…"), ""
	}
	return
}

func (m *configTUI) legacySessionPickerView() string {
	f := &m.legacySession
	height := m.bodyHeight()
	limit := max(1, height-1)
	first := max(0, f.selected-limit+1)
	var lines []string
	for i := first; i < min(len(f.candidates)+len(f.scopes), first+limit); i++ {
		marker := "  "
		if i == f.selected {
			marker = "› "
		}
		if len(f.scopes) > 0 {
			scope := f.scopes[i]
			label := fmt.Sprintf(m.language.text("%d 个线程 · %s"), scope.ActiveThreads, scope.ChatID)
			if scope.TopicID != "" {
				label += " / " + scope.TopicID
			}
			lines = append(lines, marker+label)
		} else {
			candidate := f.candidates[i]
			lines = append(lines, marker+candidate.ThreadID+" · "+legacySessionUpdatedAt(candidate.UpdatedAt)+" · "+candidate.ProjectPath)
		}
	}
	if height > 1 {
		if len(f.scopes) > 0 {
			scope := f.scopes[f.selected]
			detail := legacySessionUpdatedAt(scope.UpdatedAt)
			if scope.TopicID != "" {
				detail = m.language.text("话题 · ") + scope.TopicID + " · " + detail
			}
			lines = append(lines, detail)
		} else {
			lines = append(lines, m.language.text("项目目录 · ")+f.candidates[f.selected].ProjectPath)
		}
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(configTUIPlain(strings.ReplaceAll(line, "\n", " ")), m.width, "…")
	}
	return strings.Join(lines, "\n")
}

func legacySessionUpdatedAt(value int64) string {
	if value <= 0 {
		return "更新时间未知"
	}
	return time.UnixMilli(value).UTC().Format(time.RFC3339)
}
