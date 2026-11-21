package bridge

import (
	"fmt"
	"strconv"
	"strings"
)

const timeoutSettingsCommand = "/timeout apply"

type timeoutSettingsForm struct {
	DeadlineSeconds int  `json:"deadlineSeconds"`
	IdleMinutes     int  `json:"idleMinutes"`
	IdleInherited   bool `json:"idleInherited"`
	DefaultIdle     int  `json:"defaultIdle"`
}

type timeoutSettingsSubmission struct {
	DeadlineSeconds int  `json:"deadlineSeconds"`
	IdleMinutes     int  `json:"idleMinutes"`
	IdleInherited   bool `json:"idleInherited"`
}

func timeoutSettingsSnapshot(st *State, cmd *SessionCommand) timeoutSettingsForm {
	scope := idleTimeoutScope(cmd.Source, cmd.RouteID, cmd.Target)
	_, override := st.IdleTimeouts[scope]
	return timeoutSettingsForm{
		DeadlineSeconds: st.Timeouts[sessionKey(cmd.Source, cmd.RouteID, cmd.Target)],
		IdleMinutes:     st.idleTimeout(scope), IdleInherited: !override, DefaultIdle: st.Settings.IdleTimeoutMinutes,
	}
}

func timeoutSettingsSummary(form timeoutSettingsForm) string {
	source := "会话覆盖"
	if form.IdleInherited {
		source = "跟随应用默认"
	}
	return fmt.Sprintf("此范围后续轮次\n绝对期限：%d 秒（0 关闭）\n空闲超时：%d 分钟（%s，0 关闭）", form.DeadlineSeconds, form.IdleMinutes, source)
}

func (e *Engine) timeoutCardTask(st *State, m Message, cmd *SessionCommand) (*Task, bool) {
	m.RouteAtReceive, m.ProjectChoices = "", nil
	route := cmd.RouteID
	if route == "" || m.Comment != nil || m.Meeting != nil || selectedRoute(e.Config, st, m) != route ||
		!sameResumeProject(effectiveTarget(e.Config, st, m, route), cmd.Target) {
		return nil, false
	}
	task := currentTask(e.Config, st, m, route)
	if task != nil && (task.RequestID == "" || (task.ReceiverAppID != "" && task.ReceiverAppID != e.Config.AppID) ||
		!sameResumeProject(task.Target, cmd.Target)) {
		return nil, false
	}
	return task, true
}

func (e *Engine) prepareTimeoutSettings(st *State, m Message, cmd *SessionCommand, current *Task) {
	form := timeoutSettingsSnapshot(st, cmd)
	cmd.Reply = timeoutSettingsSummary(form)
	if current != nil && !terminal(current.Phase) && !current.StopRequested {
		cmd.Reply += fmt.Sprintf("\n当前轮：绝对期限 %d 秒 · 空闲超时 %d 分钟。", current.TimeoutSeconds, current.IdleTimeoutMinutes)
	}
	cmd.Reply += "\n命令：/timeout deadline <1..86400秒|off> · /timeout idle <1..120分钟|off|default>。旧版习惯的分钟数请用 idle。"
	task, ok := e.timeoutCardTask(st, m, cmd)
	if !ok || (current != nil && (task == nil || current.ID != task.ID || current.RequestID != task.RequestID)) {
		return
	}
	button := CommandButton{Label: "保存超时", Command: timeoutSettingsCommand}
	if task != nil {
		button.TaskID, button.RequestID = task.ID, task.RequestID
	}
	cmd.Card = &CommandCard{AppID: e.Config.AppID, TimeoutSettings: &form, Buttons: []CommandButton{button}}
}

func timeoutSettingsElement(cmd *SessionCommand) any {
	base := cmd.Card.TimeoutSettings
	idle := strconv.Itoa(base.IdleMinutes)
	if base.IdleInherited {
		idle = "default"
	}
	input := func(name, value string) any {
		return map[string]any{"tag": "input", "name": name, "input_type": "text", "required": true, "default_value": value}
	}
	button := commandButtonElement(cmd, 0)
	button["name"], button["type"], button["form_action_type"] = "timeout_submit", "primary", "submit"
	return map[string]any{"tag": "form", "name": "timeout_settings", "elements": []any{
		commandCardSection("绝对期限（秒，0 关闭，最大 86400）"),
		input("deadline_seconds", strconv.Itoa(base.DeadlineSeconds)),
		commandCardSection(fmt.Sprintf("空闲超时（分钟，0 关闭，最大 120；应用默认 %d 分钟，填 default 继承）", base.DefaultIdle)),
		input("idle_minutes", idle),
		button,
	}}
}

func parseTimeoutSettings(values map[string]any) *timeoutSettingsSubmission {
	deadline, deadlineOK := values["deadline_seconds"].(string)
	idle, idleOK := values["idle_minutes"].(string)
	if len(values) != 2 || !deadlineOK || !idleOK || len(deadline) > 16 || len(idle) > 16 {
		return nil
	}
	normalize := func(value string) string {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "0" {
			return "off"
		}
		return value
	}
	deadline, idle = normalize(deadline), normalize(idle)
	seconds, absoluteOK := parseAbsoluteTimeout(deadline)
	minutes, minutesOK := parseIdleTimeout(idle)
	inherited := idle == "default"
	if !absoluteOK || (!minutesOK && !inherited) {
		return nil
	}
	return &timeoutSettingsSubmission{DeadlineSeconds: seconds, IdleMinutes: minutes, IdleInherited: inherited}
}

// Returns the task whose idle sampling must be cleared after publication.
func (e *Engine) applyTimeoutSettings(st *State, m Message, cmd *SessionCommand) string {
	cmd.Reply = "会话、轮次或超时设置已变化。请重新发送 /timeout。"
	origin, button := commandCardAction(e.Config, st, m)
	if origin == nil || origin.Card.Used || origin.Card.TimeoutSettings == nil ||
		button.Command != timeoutSettingsCommand || userScope(origin.Source) != userScope(m) {
		return ""
	}
	task, ok := e.timeoutCardTask(st, m, origin)
	if !ok || (task == nil && button.TaskID != "") ||
		(task != nil && (task.ID != button.TaskID || task.RequestID != button.RequestID)) {
		return ""
	}
	base := *origin.Card.TimeoutSettings
	if base != timeoutSettingsSnapshot(st, origin) {
		return ""
	}
	values := m.TimeoutSettings
	if values == nil {
		cmd.Reply = "超时输入无效。绝对期限填 0 至 86400 秒，空闲超时填 0 至 120 分钟或 default。"
		return ""
	}
	deadlineChanged := base.DeadlineSeconds != values.DeadlineSeconds
	idleChanged := base.IdleInherited != values.IdleInherited || (!values.IdleInherited && base.IdleMinutes != values.IdleMinutes)
	if deadlineChanged {
		e.setAbsoluteTimeout(st, sessionKey(m, origin.RouteID, origin.Target), task, values.DeadlineSeconds)
	}
	if idleChanged {
		st.setIdleTimeout(idleTimeoutScope(m, origin.RouteID, origin.Target), task, values.IdleMinutes, values.IdleInherited)
	}
	origin.Card.Used = true
	cmd.Reply = "超时设置没有变化。"
	if deadlineChanged || idleChanged {
		cmd.Reply = "已保存超时设置。"
	}
	cmd.Reply += "\n" + timeoutSettingsSummary(timeoutSettingsSnapshot(st, origin))
	if idleChanged && task != nil {
		return task.ID
	}
	return ""
}
