package bridge

import "fmt"

const displaySettingsCommand = "/config display"

func (e *Engine) prepareDisplaySettingsCard(m Message, cmd *SessionCommand, words []string) {
	if len(words) != 2 {
		cmd.Reply = "用法：/config display"
		return
	}
	if !e.privateAdminAllowed(m) {
		cmd.Reply = "请由所有者或管理员在已授权私聊发送 /config display。"
		return
	}
	settings := e.Store.runSettings().view()
	cmd.Kind, cmd.RouteID, cmd.Target = displaySettingsCommand, "", Target{}
	cmd.Reply = fmt.Sprintf("飞书展示设置\n回复：%s · 过程显示：%s\n每张卡可改一项，从后续轮次生效。\n%s\n%s\n%s", settings.ReplyMode, settings.ProcessMode, runReplyCommand, runProcessCommand, runProcessPreview)
	card := &CommandCard{AppID: e.Config.AppID}
	for _, choice := range []struct{ value, label string }{{"card", "卡片回复"}, {"text", "终态纯文本"}, {"markdown", "终态 Markdown"}} {
		if choice.value != settings.ReplyMode {
			card.Buttons = append(card.Buttons, CommandButton{Label: choice.label, Command: displaySettingsCommand, Settings: &RunSettingsPatch{ReplyMode: &choice.value}})
		}
	}
	for _, choice := range []struct{ value, label string }{
		{"off", "关闭过程显示"}, {"brief", "简略过程"}, {"detailed", "详细过程"},
		{string(processStream), "独立卡正文"}, {string(processStreamBrief), "正文＋简略工具"},
		{string(processStreamDetailed), "正文＋详细工具"}, {string(processCard), "本卡正文"},
	} {
		if choice.value != settings.ProcessMode {
			card.Buttons = append(card.Buttons, CommandButton{Label: choice.label, Command: displaySettingsCommand, Settings: &RunSettingsPatch{ProcessMode: &choice.value}})
		}
	}
	cmd.Card = card
}

func (e *Engine) applyDisplaySettingsCard(st *State, m Message, cmd *SessionCommand) {
	origin, button := commandCardAction(e.Config, st, m)
	if origin == nil || origin.Card.Used || origin.Kind != displaySettingsCommand || userScope(origin.Source) != userScope(m) ||
		!e.privateAdminAllowed(m) || button.Settings == nil || button.Settings.validate() != nil {
		return
	}
	button.Settings.apply(&st.Settings)
	origin.Card.Used = true
	settings := st.Settings.view()
	cmd.Reply = fmt.Sprintf("已保存。后续轮次的回复：%s · 过程显示：%s。\n发送 /config display 可继续调整。", settings.ReplyMode, settings.ProcessMode)
}
