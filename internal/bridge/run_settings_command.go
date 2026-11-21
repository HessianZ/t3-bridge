package bridge

import (
	"context"
	"fmt"
	"strconv"
)

func (e *Engine) configureRunSettings(ctx context.Context, id string, cmd *SessionCommand, args []string) error {
	setting := args[0]
	if len(args) == 1 {
		settings := e.Store.runSettings().view()
		switch setting {
		case "reply":
			cmd.Reply = "当前回复模式：" + settings.ReplyMode + "。用 " + runReplyCommand + " 设置。"
		case "tools":
			cmd.Reply = "当前工具活动显示：" + settings.ToolActivity + "。用 /config tools off|brief|detailed|default 设置。"
		case "process":
			cmd.Reply = "当前过程显示：" + settings.ProcessMode + "。" + runProcessUsage
		case "idle-timeout":
			cmd.Reply = fmt.Sprintf("后续新轮默认空闲超时为%d分钟（0关闭）。", settings.IdleTimeoutMinutes)
		}
		if cmd.Reply != "" {
			return nil
		}
	}
	if len(args) != 2 {
		switch setting {
		case "process":
			cmd.Reply = runProcessUsage
		case "idle-timeout":
			cmd.Reply = runIdleTimeoutUsage
		default:
			cmd.Reply = fmt.Sprintf("用法：/config concurrency 0..%d（0不额外限制） · /config progress on|off · ", MaxRunConcurrency) + runReplyCommand + " · /config tools off|brief|detailed|default · " + runProcessCommand
		}
		return nil
	}

	value := args[1]
	var patch RunSettingsPatch
	switch setting {
	case "concurrency":
		n, err := strconv.Atoi(value)
		if err != nil {
			cmd.Reply = fmt.Sprintf(runConcurrencyUsage, MaxRunConcurrency)
			return nil
		}
		patch.MaxConcurrent = &n
	case "progress":
		if value != "on" && value != "off" {
			cmd.Reply = runProgressUsage
			return nil
		}
		on := value == "on"
		patch.Progress = &on
	case "reply":
		patch.ReplyMode = &value
	case "tools":
		patch.ToolActivity = &value
	case "process":
		patch.ProcessMode = &value
	case "idle-timeout":
		minutes := 0
		if value != "off" {
			n, err := strconv.Atoi(value)
			if err != nil || n == 0 {
				cmd.Reply = runIdleTimeoutUsage
				return nil
			}
			minutes = n
		}
		patch.IdleTimeoutMinutes = &minutes
	}
	if err := patch.validate(); err != nil {
		cmd.Reply = err.Error()
		return nil
	}
	_, err := e.Store.updateRunSettings(ctx, patch, func(st *State, settings RunSettingsView) {
		switch setting {
		case "reply":
			cmd.Reply = "已保存回复模式：" + settings.ReplyMode + "。适用于后续新建的消息回复。纯文本和 Markdown 只在终态发送，可用 /stop、/cancel 停止，用 /result 取全文。"
		case "tools":
			cmd.Reply = "已保存工具活动显示：" + settings.ToolActivity + "。"
		case "process":
			cmd.Reply = "已保存过程显示：" + settings.ProcessMode + "。适用于后续飞书聊天轮次。"
		case "idle-timeout":
			cmd.Reply = fmt.Sprintf("已保存后续新轮默认空闲超时为%d分钟（0关闭）。", settings.IdleTimeoutMinutes)
		default:
			cmd.Reply = fmt.Sprintf("已保存：Bridge并发预算=%d（0不限）；卡片可见进度=%t。卡片仍显示阶段、审批和终态。", settings.MaxConcurrent, settings.Progress)
		}
		saveCommandState(st, id, cmd)
	})
	return err
}
