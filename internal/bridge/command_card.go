package bridge

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const commandReplyWindow = 55 * time.Minute
const commandCardActionType = "session_command"

type CommandCard struct {
	AppID           string               `json:"appId"`
	ReplyID         string               `json:"replyId,omitempty"`
	Buttons         []CommandButton      `json:"buttons"`
	ThreadURL       string               `json:"threadUrl,omitempty"`
	Used            bool                 `json:"used,omitempty"`
	RunSettingsForm *RunSettingsForm     `json:"runSettingsForm,omitempty"`
	TaskList        *taskListCard        `json:"taskList,omitempty"`
	TimeoutSettings *timeoutSettingsForm `json:"timeoutSettings,omitempty"`
}

type CommandButton struct {
	Label     string            `json:"label"`
	Command   string            `json:"command"`
	Name      string            `json:"name,omitempty"`
	Workspace *Workspace        `json:"workspace,omitempty"`
	TaskID    string            `json:"taskId,omitempty"`
	RequestID string            `json:"requestId,omitempty"`
	Settings  *RunSettingsPatch `json:"settings,omitempty"`

	ResumePage int          `json:"resumePage,omitempty"`
	ResumeStop *resumeRound `json:"resumeStop,omitempty"`

	WorkspacePage   int                    `json:"workspacePage,omitempty"`
	ProjectPage     int                    `json:"projectPage,omitempty"`
	RemoveWorkspace bool                   `json:"removeWorkspace,omitempty"`
	GroupAccess     *groupAccessCardButton `json:"groupAccess,omitempty"`

	TaskListArgs         []string `json:"taskListArgs,omitempty"`
	DoctorTargetID       string   `json:"doctorTargetId,omitempty"`
	ToolIdentityTargetID string   `json:"toolIdentityTargetId,omitempty"`
	RouteSelectionID     string   `json:"routeSelectionId,omitempty"`
	RouteSelectionPage   int      `json:"routeSelectionPage,omitempty"`
}

func (b CommandButton) changesState() bool {
	return b.Workspace != nil || b.Settings != nil || b.Command == runSettingsCardCommand || b.Command == "/stop" || b.Command == "/new" ||
		b.Command == timeoutSettingsCommand || b.Command == doctorEchoCommand || b.Command == toolIdentityCommand ||
		(b.Command == routeSelectionCommand && b.RouteSelectionID != "") ||
		(b.Command == groupAccessCardCommand && b.GroupAccess != nil && b.GroupAccess.Operation != "") ||
		(b.TaskID != "" && (b.Command == "/resume" || taskListCommand(b.Command)))
}

type commandCardMessenger interface {
	ReplyCommandCard(context.Context, Message, string, bool) (string, error)
}

var errCommandCardRejected = errors.New("interactive command reply rejected")

func (e *Engine) prepareCommandCard(cmd *SessionCommand, task *Task, choices []CommandButton) {
	if cmd.RouteID != "" && !e.Config.TargetAllowed(cmd.Source, e.Config.Routes[cmd.RouteID], cmd.RouteID) {
		return
	}
	buttons := choices
	status := "查看状态"
	var threadURL string
	if cmd.Kind == "/status" {
		status = "刷新状态"
		if scopedTask(e.Config, cmd.Source, task, cmd.RouteID) {
			threadURL = taskThreadURL(task)
			if task.RequestID != "" && !terminal(task.Phase) && !task.StopRequested {
				buttons = append([]CommandButton{{Label: "停止本轮任务", Command: "/stop", TaskID: task.ID, RequestID: task.RequestID}}, buttons...)
			}
		}
	}
	buttons = append(buttons, CommandButton{Label: status, Command: "/status"})
	if cmd.RouteID != "" && cmd.Kind != "/ws" {
		buttons = append(buttons, CommandButton{Label: "工作区", Command: "/ws"})
		if cmd.Kind != "/resume" {
			buttons = append(buttons, CommandButton{Label: "会话历史", Command: "/resume"})
		}
	}
	if cmd.Kind != "/help" {
		buttons = append(buttons, CommandButton{Label: "帮助", Command: "/help"})
	}
	cmd.Card = &CommandCard{AppID: e.Config.AppID, Buttons: buttons, ThreadURL: threadURL}
}

func commandButtonID(cmd *SessionCommand, index int) string {
	return stable("command-card", cmd.Card.AppID, key(cmd.Source, cmd.Source.ID), fmt.Sprint(index))
}

func commandCardJSON(cmd *SessionCommand) string {
	body := cmd.Reply
	if cmd.Card.RunSettingsForm != nil {
		body = "运行设置\n逐群 @ 设置优先于群默认值。用 /config groups 管理指定群。"
	}
	elements := []any{commandCardSection(display(body, 1800))}
	if cmd.Card.RunSettingsForm != nil {
		return card2JSON(append(elements, runSettingsFormElement(cmd)))
	}
	if cmd.Card.TimeoutSettings != nil {
		return card2JSON(append(elements, timeoutSettingsElement(cmd)))
	}
	if list := cmd.Card.TaskList; list != nil {
		elements[0] = commandCardSection(list.Title)
		for _, row := range list.Rows {
			elements = append(elements, commandCardSection(row.Text))
			if row.Stop > 0 && row.Stop <= len(cmd.Card.Buttons) {
				elements = append(elements, commandButtonElement(cmd, row.Stop-1))
			}
		}
		if list.Hint != "" {
			elements = append(elements, commandCardSection(list.Hint))
		}
	}
	if cmd.GroupMessageGrant != nil {
		elements = append(elements, map[string]any{
			"tag": "button", "text": map[string]string{"tag": "plain_text", "content": "打开飞书授权页"},
			"behaviors": []any{map[string]string{"type": "open_url", "default_url": cmd.GroupMessageGrant.URL}},
		})
	}
	for i, button := range cmd.Card.Buttons {
		if cmd.Card.TaskList != nil && button.TaskID != "" && taskListCommand(button.Command) {
			continue // The stop button already follows its row.
		}
		elements = append(elements, commandButtonElement(cmd, i))
	}
	if cmd.Card.ThreadURL != "" {
		elements = append(elements, openThreadButton(cmd.Card.ThreadURL))
	}
	return card2JSON(elements)
}

func commandCardSection(text string) any {
	return map[string]any{"tag": "div", "text": map[string]string{"tag": "plain_text", "content": text}}
}

func commandButtonElement(cmd *SessionCommand, i int) map[string]any {
	button := cmd.Card.Buttons[i]
	element := map[string]any{
		"tag": "button", "text": map[string]string{"tag": "plain_text", "content": button.Label},
		"behaviors": []any{map[string]any{"type": "callback", "value": map[string]string{"action": commandCardActionType, "id": commandButtonID(cmd, i)}}},
	}
	var title, text string
	switch {
	case button.RemoveWorkspace:
		title, text = "删除工作区别名", "确认删除「"+display(button.Name, 64)+"」？项目、目录和会话都会保留。"
	case button.Command == "/new":
		title, text = "开启新会话", "下一条消息将开启新会话。历史记录会保留。若原轮次仍在运行，将请求停止并取消该轮排队的补充。"
	case taskListCommand(button.Command) && button.TaskID != "":
		title, text = "停止指定轮次", "确认"+button.Label+" 对应的任务轮次？"
	case button.Command == doctorEchoCommand:
		title, text = "发起模型回声", "通过「"+display(button.Name, 1800)+"」提交一轮独立诊断？诊断使用固定提示词。"
	case button.Command == toolIdentityCommand:
		title, text = "切换工具身份路由", "确认下一条普通消息使用「"+display(button.Name, 64)+"」及它自己的会话？"
	case button.Command == routeSelectionCommand && button.RouteSelectionID != "":
		title, text = "设置私聊默认路由", "确认选择「"+routeSelectionDisplay(button.Name, routeSelectionIDDisplayLimit)+"」？各路由的会话会保留。"
	case button.Command == workspaceProjectsCommand && button.Workspace != nil:
		title = "切换 T3 项目"
		text = "选择「" + routeSelectionDisplay(button.Workspace.Name, 68) + "」为当前项目？\n项目 ID · " +
			routeSelectionDisplay(button.Workspace.ProjectID, 128) + "\n下条消息会开启新会话。"
	case button.Command == groupAccessCardCommand && button.GroupAccess != nil && button.GroupAccess.Operation != "":
		title, text = "确认群设置", button.Label+"\n群 ID · "+button.GroupAccess.ChatID
	}
	if title != "" {
		element["confirm"] = map[string]any{
			"title": map[string]string{"tag": "plain_text", "content": title},
			"text":  map[string]string{"tag": "plain_text", "content": text},
		}
	}
	return element
}

func commandCardAction(c Config, st *State, m Message) (*SessionCommand, CommandButton) {
	if st.AccountID != "" && st.AccountID != c.AppID {
		return nil, CommandButton{}
	}
	for _, cmd := range st.Commands {
		card := cmd.Card
		if card == nil || card.ReplyID == "" || card.ReplyID != m.ParentID || card.AppID != c.AppID || expired(cmd.ReplyAt, commandReplyWindow) {
			continue
		}
		src := cmd.Source
		if src.UserID != m.UserID || src.ChatID != m.ChatID || !c.Allowed(src) {
			continue
		}
		if cmd.RouteID != "" {
			target := c.Routes[cmd.RouteID]
			if !c.TargetAllowed(src, target, cmd.RouteID) || cmd.BaseTargetID != targetIdentity(target) {
				continue
			}
		}
		for i, button := range card.Buttons {
			if commandButtonID(cmd, i) == m.CommandAction {
				return cmd, button
			}
		}
	}
	return nil, CommandButton{}
}

// The button follows the live selection, even if the command referenced an older result.
func (e *Engine) newSessionCardTask(st *State, m Message, origin *SessionCommand) *Task {
	m.RouteAtReceive, m.ProjectChoices = "", nil
	route := origin.RouteID
	if m.Comment != nil || route == "" || selectedRoute(e.Config, st, m) != route {
		return nil
	}
	target := effectiveTarget(e.Config, st, m, route)
	if !sameResumeProject(target, origin.Target) {
		return nil
	}
	task := currentTask(e.Config, st, m, route)
	if task == nil || task.RequestID == "" || task.Source.Comment != nil ||
		(task.ReceiverAppID != "" && task.ReceiverAppID != e.Config.AppID) ||
		!sameResumeProject(target, task.Target) {
		return nil
	}
	return task
}

func (e *Engine) newSessionButtons(st *State, cmd *SessionCommand) []CommandButton {
	task := e.newSessionCardTask(st, cmd.Source, cmd)
	if task == nil {
		return nil
	}
	return []CommandButton{{Label: "新会话", Command: "/new", TaskID: task.ID, RequestID: task.RequestID}}
}

func (e *Engine) stopFromCommandCard(m Message, id string, cmd *SessionCommand) error {
	return e.Store.update(func(st *State) error {
		origin, button := commandCardAction(e.Config, st, m)
		if origin == nil || origin.Card.Used || userScope(origin.Source) != userScope(m) {
			cmd.Reply = "卡片已失效。请重新发送 /status。"
			return nil
		}
		task := st.Tasks[button.TaskID]
		if !scopedTask(e.Config, m, task, origin.RouteID) || (task.ReceiverAppID != "" && task.ReceiverAppID != e.Config.AppID) || task.RequestID != button.RequestID || terminal(task.Phase) || task.StopRequested {
			cmd.Reply = "卡片中的任务轮次已变化或停止。请用 /status 核对。"
			return nil
		}
		if current := currentTask(e.Config, st, m, task.RouteID); current != nil && current.ID == task.ID {
			cancelQueued(e.Config, st, m, task)
		}
		task.StopRequested = true
		origin.Card.Used = true
		cmd.Reply = "已请求停止卡片中的任务轮次。请用 /status 核对结果。"
		copy := *cmd
		st.Commands[id] = &copy
		return nil
	})
}

func sameResumeProject(a, b Target) bool {
	return targetIdentity(a) == targetIdentity(b) && a.ProjectPath == b.ProjectPath
}

func resumeCardCandidate(c Config, st *State, t *Task) bool {
	return t.RequestID != "" && t.Source.Comment == nil && (t.Source.ChatType == "p2p" || t.Source.ChatType == "") &&
		(t.ReceiverAppID == c.AppID || (t.ReceiverAppID == "" && st.AccountID == c.AppID))
}

// The mapping, consumed card and click receipt commit together under the Store lock.
func (e *Engine) resumeFromCommandCard(st *State, m Message, cmd *SessionCommand) {
	cmd.Reply = staleResumeCard
	origin, button := commandCardAction(e.Config, st, m)
	if origin == nil || origin.Card.Used || button.Command != "/resume" || !e.resumeCardMatches(st, m, origin) {
		return
	}
	task := st.Tasks[button.TaskID]
	if task == nil || !resumeCardCandidate(e.Config, st, task) {
		return
	}
	selection := resumeSelection{Round: resumeRound{TaskID: button.TaskID, RequestID: button.RequestID}, Stop: button.ResumeStop}
	if e.applyResume(st, m, cmd, selection) {
		origin.Card.Used = true
	}
}
