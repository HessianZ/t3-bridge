package main

import (
	"context"
	_ "embed"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

//go:embed config_runtime_settings.html
var configRuntimeSettingsHTML string

type runtimeSettingsTarget struct {
	Process string
	Account receiverProcessAccount
	PID     int
}

func (t runtimeSettingsTarget) URL() string {
	return "/runtime/settings?" + url.Values{
		"process": {t.Process}, "tenant": {t.Account.Tenant}, "appId": {t.Account.AppID},
	}.Encode()
}

type runtimeSettingChoice struct{ Value, Label string }

type runtimeSettingField struct {
	Name, Label, Hint, Value string
	Max                      int
	Choices                  []runtimeSettingChoice
}

func runtimeSettingsFields(settings bridge.RunSettingsView) []runtimeSettingField {
	progress := "off"
	if settings.Progress {
		progress = "on"
	}
	activityChoices := []runtimeSettingChoice{{"off", "关闭"}, {"brief", "简要"}, {"detailed", "详细"}}
	return []runtimeSettingField{
		{Name: "maxConcurrent", Label: "并发预算", Hint: "0 表示不额外限制。影响后续派发。", Value: strconv.Itoa(settings.MaxConcurrent), Max: bridge.MaxRunConcurrency},
		{Name: "progress", Label: "卡片进度", Hint: "影响后续卡片进度更新，包括在途轮次。", Value: progress,
			Choices: []runtimeSettingChoice{{"on", "显示"}, {"off", "隐藏"}}},
		{Name: "replyMode", Label: "回复模式", Hint: "适用于后续新建的回复。", Value: settings.ReplyMode,
			Choices: []runtimeSettingChoice{{"card", "卡片"}, {"text", "终态纯文本"}, {"markdown", "终态 Markdown"}}},
		{Name: "toolActivity", Label: "工具活动显示", Hint: "影响后续卡片和结果中的工具活动，包括在途轮次。", Value: settings.ToolActivity,
			Choices: append([]runtimeSettingChoice{{"default", "默认"}}, activityChoices...)},
		{Name: "processMode", Label: "过程显示", Hint: "后续轮次生效。本卡正文仅用于卡片回复。纯文本或 Markdown 可选独立正文预览。", Value: settings.ProcessMode,
			Choices: append(activityChoices,
				runtimeSettingChoice{"card", "本卡正文"}, runtimeSettingChoice{"stream", "独立正文预览"},
				runtimeSettingChoice{"stream-brief", "独立正文＋简略工具"}, runtimeSettingChoice{"stream-detailed", "独立正文＋详细工具"})},
		{Name: "idleTimeoutMinutes", Label: "默认空闲超时（分钟）", Hint: "0 表示关闭。适用于没有会话覆盖的后续新轮。", Value: strconv.Itoa(settings.IdleTimeoutMinutes), Max: 120},
	}
}

func configRuntimeSettingsHandler(list func() ([]receiverProcess, error), apply func(context.Context, string, string, string, bridge.RunSettingsPatch) (bridge.RunSettingsView, error)) http.HandlerFunc {
	page := newWorkspacePage("runtime settings").Parse(configRuntimeSettingsHTML)
	return func(w http.ResponseWriter, r *http.Request) {
		if !runtimeRequestOriginAllowed(r) {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "same-origin")
		post := r.Method == http.MethodPost
		values := r.URL.Query()
		var patch bridge.RunSettingsPatch
		if post {
			r.Body = http.MaxBytesReader(w, r.Body, 4096)
			if r.URL.RawQuery != "" || r.ParseForm() != nil {
				http.Error(w, "无法读取运行设置。", http.StatusBadRequest)
				return
			}
			var valid bool
			values = r.PostForm
			patch, valid = parseRuntimeSettingsPatch(values)
			if !valid {
				http.Error(w, "运行设置不完整或取值无效。", http.StatusBadRequest)
				return
			}
		}
		var view struct {
			Targets  []runtimeSettingsTarget
			Selected *runtimeSettingsTarget
			Fields   []runtimeSettingField
			Notice   string
		}
		processes, err := list()
		status := http.StatusOK
		if err != nil {
			status, view.Notice = http.StatusServiceUnavailable, "接收进程清单暂不可用。"
		} else {
			for _, process := range processes {
				if !process.RunSettings {
					continue
				}
				for _, account := range process.Accounts {
					view.Targets = append(view.Targets, runtimeSettingsTarget{Process: process.ID, Account: account, PID: process.PID})
				}
			}
			selected := -1
			switch {
			case post || len(values) != 0:
				selected = slices.IndexFunc(view.Targets, func(target runtimeSettingsTarget) bool {
					return target.Process == values.Get("process") && target.Account.Tenant == values.Get("tenant") &&
						target.Account.AppID == values.Get("appId")
				})
				if selected < 0 {
					status, view.Notice = http.StatusNotFound, "该接收进程或账号已不可用，请重新选择。"
				}
			case len(view.Targets) == 1:
				selected = 0
			}
			if selected >= 0 {
				target := view.Targets[selected]
				view.Selected = &target
				ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
				settings, err := apply(ctx, target.Process, target.Account.Tenant, target.Account.AppID, patch)
				cancel()
				switch {
				case err != nil && post:
					status, view.Notice = http.StatusBadGateway, "更新结果未确认。请重新读取生效值。"
				case err != nil:
					status, view.Notice = http.StatusBadGateway, "暂时无法读取运行设置。"
				case post:
					http.Redirect(w, r, target.URL()+"&updated=1", http.StatusSeeOther)
					return
				default:
					view.Fields = runtimeSettingsFields(settings)
					if values.Get("updated") == "1" {
						view.Notice = "运行设置已保存。"
					}
				}
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_ = page.Execute(w, r, view)
	}
}

func parseRuntimeSettingsPatch(values url.Values) (bridge.RunSettingsPatch, bool) {
	var patch bridge.RunSettingsPatch
	fields := runtimeSettingsFields(bridge.RunSettingsView{})
	for _, key := range []string{"process", "appId", "tenant"} {
		if len(values[key]) != 1 {
			return patch, false
		}
	}
	if len(values) != 3+2*len(fields) || !receiverProcessID.MatchString(values.Get("process")) || values.Get("appId") == "" ||
		!slices.Contains([]string{"feishu", "lark"}, values.Get("tenant")) {
		return patch, false
	}
	for _, field := range fields {
		before, after := values["before-"+field.Name], values[field.Name]
		if len(before) != 1 || len(after) != 1 {
			return patch, false
		}
		if field.Max > 0 {
			old, oldErr := strconv.Atoi(before[0])
			next, err := strconv.Atoi(after[0])
			if oldErr != nil || err != nil || old < 0 || old > field.Max || next < 0 || next > field.Max {
				return patch, false
			}
			if old != next {
				if field.Name == "maxConcurrent" {
					patch.MaxConcurrent = &next
				} else {
					patch.IdleTimeoutMinutes = &next
				}
			}
			continue
		}
		old, next := before[0], after[0]
		valid := func(value string) bool {
			return slices.ContainsFunc(field.Choices, func(choice runtimeSettingChoice) bool { return choice.Value == value })
		}
		if !valid(old) || !valid(next) {
			return patch, false
		}
		if old == next {
			continue
		}
		switch field.Name {
		case "progress":
			on := next == "on"
			patch.Progress = &on
		case "replyMode":
			patch.ReplyMode = &next
		case "toolActivity":
			patch.ToolActivity = &next
		case "processMode":
			patch.ProcessMode = &next
		}
	}
	return patch, true
}
