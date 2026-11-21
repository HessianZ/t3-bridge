package bridge

import (
	"errors"
	"fmt"
	"strconv"
)

type RunSettingsForm struct {
	Settings       RunSettingsView `json:"settings"`
	RequireMention bool            `json:"requireMention"`
}

type RunSettingsFormSubmission struct {
	Settings       RunSettingsPatch `json:"settings"`
	RequireMention *bool            `json:"requireMention,omitempty"`
}

func runSettingsFormElement(cmd *SessionCommand) any {
	form := cmd.Card.RunSettingsForm
	settings := form.Settings
	var elements []any
	plainText := func(content string) map[string]string {
		return map[string]string{"tag": "plain_text", "content": content}
	}
	addField := func(label string, field map[string]any) {
		elements = append(elements, map[string]any{"tag": "div", "text": plainText(label)}, field)
	}
	addNumber := func(name, label string, initial int) {
		addField(label, map[string]any{
			"tag": "input", "name": name, "input_type": "text", "required": true,
			"default_value": strconv.Itoa(initial),
		})
	}
	type option struct{ value, label string }
	addSelect := func(name, label, initial string, choices []option) {
		options := make([]any, 0, len(choices))
		initialIndex := 0
		for i, choice := range choices {
			options = append(options, map[string]any{"text": plainText(choice.label), "value": choice.value})
			if choice.value == initial {
				initialIndex = i + 1
			}
		}
		addField(label, map[string]any{
			"tag": "select_static", "name": name, "initial_index": initialIndex, "options": options,
		})
	}
	addToggle := func(name, label string, initial bool) {
		value := "off"
		if initial {
			value = "on"
		}
		addSelect(name, label, value, []option{{"on", "开启"}, {"off", "关闭"}})
	}

	addNumber("max_concurrent", fmt.Sprintf("并发预算（0–%d，0 不额外限制）", MaxRunConcurrency), settings.MaxConcurrent)
	addNumber("idle_timeout_minutes", "默认空闲超时（0–120 分钟，0 关闭）", settings.IdleTimeoutMinutes)
	addToggle("progress", "卡片进度", settings.Progress)
	addSelect("reply_mode", "回复模式", settings.ReplyMode, []option{
		{"card", "卡片回复"}, {"text", "终态纯文本"}, {"markdown", "终态 Markdown"},
	})
	addSelect("tool_activity", "工具活动", settings.ToolActivity, []option{
		{"default", "默认"}, {"off", "关闭"}, {"brief", "简略"}, {"detailed", "详细"},
	})
	addSelect("process_mode", "过程展示", settings.ProcessMode, []option{
		{"off", "关闭"}, {"brief", "简略过程"}, {"detailed", "详细过程"},
		{string(processStream), "独立卡正文"}, {string(processStreamBrief), "正文＋简略工具"},
		{string(processStreamDetailed), "正文＋详细工具"}, {string(processCard), "本卡正文"},
	})
	addToggle("require_mention", "群聊默认要求 @ 机器人", form.RequireMention)
	elements = append(elements, map[string]any{
		"tag": "button", "name": "run_settings_submit", "text": plainText("保存设置"),
		"type": "primary", "form_action_type": "submit",
		"behaviors": []any{map[string]any{
			"type": "callback", "value": map[string]string{"action": commandCardActionType, "id": commandButtonID(cmd, 0)},
		}},
	})
	return map[string]any{"tag": "form", "name": "run_settings_form", "elements": elements}
}

func parseRunSettingsForm(values map[string]any) (*RunSettingsFormSubmission, error) {
	if len(values) == 0 || len(values) > 7 {
		return nil, errors.New("运行设置表单的字段数量无效")
	}
	var submission RunSettingsFormSubmission
	for field, raw := range values {
		value, ok := raw.(string)
		if !ok || value == "" || len(value) > 32 {
			return nil, errors.New("运行设置表单包含无效值")
		}
		switch field {
		case "max_concurrent", "idle_timeout_minutes":
			number, err := strconv.ParseUint(value, 10, 8)
			if err != nil {
				return nil, errors.New("并发预算和空闲超时须填写范围内的非负整数")
			}
			n := int(number)
			if field == "max_concurrent" {
				submission.Settings.MaxConcurrent = &n
			} else {
				submission.Settings.IdleTimeoutMinutes = &n
			}
		case "progress", "require_mention":
			if value != "on" && value != "off" {
				return nil, errors.New("卡片进度和群聊默认 @ 设置仅支持 on/off")
			}
			on := value == "on"
			if field == "progress" {
				submission.Settings.Progress = &on
			} else {
				submission.RequireMention = &on
			}
		case "reply_mode":
			submission.Settings.ReplyMode = &value
		case "tool_activity":
			submission.Settings.ToolActivity = &value
		case "process_mode":
			submission.Settings.ProcessMode = &value
		default:
			return nil, errors.New("运行设置表单包含未知字段")
		}
	}
	if err := submission.Settings.validate(); err != nil {
		return nil, err
	}
	return &submission, nil
}
