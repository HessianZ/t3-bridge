package bridge

import "fmt"

const runSettingsCardCommand = "/config run"

func runSettingsFormSnapshot(st *State) RunSettingsForm {
	return RunSettingsForm{Settings: st.Settings.view(), RequireMention: st.GroupAccess.defaultRequiresMention()}
}

func (e *Engine) prepareRunSettingsCard(m Message, cmd *SessionCommand, words []string) {
	if len(words) > 2 {
		cmd.Reply = "用法：/config run"
		return
	}
	if !e.privateAdminAllowed(m) {
		cmd.Reply = "请由所有者或管理员在已授权私聊发送 /config run。"
		return
	}
	e.Store.mu.Lock()
	form := runSettingsFormSnapshot(&e.Store.state)
	e.Store.mu.Unlock()
	cmd.Kind, cmd.RouteID, cmd.Target = runSettingsCardCommand, "", Target{}
	cmd.Reply = "运行设置\n" + runSettingsFormSummary(form) +
		"\n可在卡片保存，也可使用命令逐项修改。其他入口更新设置后，请重新打开卡片。\n" +
		"命令：/config concurrency、/config idle-timeout、/config progress、/config reply、/config tools、/config process、/config mention all。群授权见 /config access。"
	cmd.Card = &CommandCard{
		AppID: e.Config.AppID, RunSettingsForm: &form,
		Buttons: []CommandButton{{Label: "保存设置", Command: runSettingsCardCommand}},
	}
}

func runSettingsFormSummary(form RunSettingsForm) string {
	s := form.Settings
	return fmt.Sprintf("并发预算 %d（0 不额外限制） · 空闲超时 %d 分钟（0 关闭）\n进度 %s · 回复 %s · 工具 %s · 过程 %s\n群默认需要 @ %s",
		s.MaxConcurrent, s.IdleTimeoutMinutes, runSettingsOnOff(s.Progress), s.ReplyMode, s.ToolActivity, s.ProcessMode, runSettingsOnOff(form.RequireMention))
}

func runSettingsOnOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// applyRunSettingsCard runs in the transaction that records the command reply.
// It returns whether the committed group default needs publishing to the receiver.
func (e *Engine) applyRunSettingsCard(st *State, m Message, cmd *SessionCommand) bool {
	origin, button := commandCardAction(e.Config, st, m)
	if origin == nil || origin.Card.Used || origin.Kind != runSettingsCardCommand || origin.Card.RunSettingsForm == nil ||
		button.Command != runSettingsCardCommand || userScope(origin.Source) != userScope(m) || !e.privateAdminAllowed(m) {
		return false
	}
	base := *origin.Card.RunSettingsForm
	if runSettingsFormSnapshot(st) != base {
		cmd.Reply = "运行设置已变化。此次提交未保存，请用 /config run 重新打开卡片。"
		return false
	}
	submission := m.RunSettingsForm
	if submission == nil {
		cmd.Reply = fmt.Sprintf("运行设置输入无效。并发预算为 0 至 %d，空闲超时为 0 至 %d 分钟。请检查表单后重新提交。", MaxRunConcurrency, MaxRunIdleTimeoutMinutes)
		return false
	}
	mentionChanged := submission.RequireMention != nil && *submission.RequireMention != base.RequireMention
	if mentionChanged && e.Config.access == nil {
		cmd.Reply = "接收端群策略尚未初始化。此次提交未保存。"
		return false
	}
	next := st.Settings
	submission.Settings.apply(&next)
	settingsChanged := next.view() != base.Settings
	if settingsChanged {
		st.Settings = next
	}
	if mentionChanged {
		request := GroupAccessRequest{Mention: runSettingsOnOff(*submission.RequireMention)}
		request.apply(&st.GroupAccess)
		cmd.GroupMessageScopePending = !*submission.RequireMention
	}
	origin.Card.Used = true
	if settingsChanged || mentionChanged {
		cmd.Reply = "已保存运行设置。"
	} else {
		cmd.Reply = "运行设置没有变化。"
	}
	cmd.Reply += "\n" + runSettingsFormSummary(runSettingsFormSnapshot(st)) + "\n继续修改请发送 /config run。"
	return mentionChanged
}
