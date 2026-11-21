package bridge

import (
	"context"
	"errors"
	"fmt"
)

const (
	processStream         toolActivityMode = "stream"
	processStreamBrief    toolActivityMode = "stream-brief"
	processStreamDetailed toolActivityMode = "stream-detailed"
	processCard           toolActivityMode = "card"
)

const (
	MaxRunConcurrency        = 50
	MaxRunIdleTimeoutMinutes = 120
)

func processEnabled(mode toolActivityMode) bool {
	return mode.enabled() || processHasPreview(mode) || mode == processCard
}

type RunSettings struct {
	MaxConcurrent      int              `json:"maxConcurrent,omitempty"`
	Progress           *bool            `json:"progress,omitempty"`
	ReplyMode          string           `json:"replyMode,omitempty"`
	ToolActivity       toolActivityMode `json:"toolActivity,omitempty"`
	ProcessMode        toolActivityMode `json:"processMode,omitempty"`
	IdleTimeoutMinutes int              `json:"idleTimeoutMinutes,omitempty"`
}

type RunSettingsPatch struct {
	MaxConcurrent      *int    `json:"maxConcurrent,omitempty"`
	Progress           *bool   `json:"progress,omitempty"`
	ReplyMode          *string `json:"replyMode,omitempty"`
	ToolActivity       *string `json:"toolActivity,omitempty"`
	ProcessMode        *string `json:"processMode,omitempty"`
	IdleTimeoutMinutes *int    `json:"idleTimeoutMinutes,omitempty"`
}

type RunSettingsView struct {
	MaxConcurrent      int    `json:"maxConcurrent"`
	Progress           bool   `json:"progress"`
	ReplyMode          string `json:"replyMode"`
	ToolActivity       string `json:"toolActivity"`
	ProcessMode        string `json:"processMode"`
	IdleTimeoutMinutes int    `json:"idleTimeoutMinutes"`
}

// LocalRunSettings reads or updates the settings for this receiver's bound App.
func (e *Engine) LocalRunSettings(ctx context.Context, patch RunSettingsPatch) (RunSettingsView, error) {
	s := e.Store
	if s == nil || e.Config.AppID == "" {
		return RunSettingsView{}, errors.New("接收端没有绑定应用状态")
	}
	if err := patch.validate(); err != nil {
		return RunSettingsView{}, err
	}
	s.mu.Lock()
	settings, appID := s.state.Settings, s.state.AccountID
	s.mu.Unlock()
	// BindAccount prevents changing an established binding.
	if appID != e.Config.AppID {
		return RunSettingsView{}, errors.New("接收端状态未绑定到当前应用")
	}
	if err := ctx.Err(); err != nil {
		return RunSettingsView{}, err
	}
	if (patch == RunSettingsPatch{}) {
		return settings.view(), nil
	}
	return s.updateRunSettings(ctx, patch, nil)
}

const (
	runConcurrencyUsage = "并发预算为0..%d。0不额外限制，只影响后续派发。"
	runProgressUsage    = "进度开关仅支持on/off。"
	runReplyCommand     = "/config reply card|text|markdown"
	runReplyUsage       = "用法：" + runReplyCommand
	runToolsUsage       = "用法：/config tools off|brief|detailed|default"
	runProcessCommand   = "/config process off|brief|detailed|stream|stream-brief|stream-detailed|card"
	runProcessPreview   = "stream 在独立卡预览公开正文。stream-brief 和 stream-detailed 同时显示工具状态。card 在结果卡预览，需开启进度并使用卡片回复。"
	runProcessUsage     = "用法：" + runProcessCommand + "。" + runProcessPreview
	runIdleTimeoutUsage = "用法：/config idle-timeout <1..120分钟|off>。"
)

func (p RunSettingsPatch) validate() error {
	if p.MaxConcurrent != nil && (*p.MaxConcurrent < 0 || *p.MaxConcurrent > MaxRunConcurrency) {
		return fmt.Errorf(runConcurrencyUsage, MaxRunConcurrency)
	}
	if p.IdleTimeoutMinutes != nil && (*p.IdleTimeoutMinutes < 0 || *p.IdleTimeoutMinutes > MaxRunIdleTimeoutMinutes) {
		return errors.New(runIdleTimeoutUsage)
	}
	if p.ReplyMode != nil && *p.ReplyMode != "card" && *p.ReplyMode != "text" && *p.ReplyMode != "markdown" {
		return errors.New(runReplyUsage)
	}
	if p.ToolActivity != nil {
		mode := toolActivityMode(*p.ToolActivity)
		if mode != "default" && mode != toolActivityOff && !mode.enabled() {
			return errors.New(runToolsUsage)
		}
	}
	if p.ProcessMode != nil {
		mode := toolActivityMode(*p.ProcessMode)
		if mode != toolActivityOff && !processEnabled(mode) {
			return errors.New(runProcessUsage)
		}
	}
	return nil
}

func (s *Store) runSettings() RunSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Settings
}

func (s RunSettings) replyMode(m Message) string {
	if m.Comment != nil || m.InputKind == "automatic" || (s.ReplyMode == "markdown" && m.Meeting != nil) {
		return "card"
	}
	return s.ReplyMode
}

func (s RunSettings) view() RunSettingsView {
	v := RunSettingsView{
		MaxConcurrent: s.MaxConcurrent, Progress: s.Progress == nil || *s.Progress,
		ReplyMode: s.ReplyMode, ToolActivity: string(s.ToolActivity), ProcessMode: string(s.ProcessMode),
		IdleTimeoutMinutes: s.IdleTimeoutMinutes,
	}
	if v.ReplyMode == "" {
		v.ReplyMode = "card"
	}
	if v.ToolActivity == "" {
		v.ToolActivity = "default"
	}
	if !processEnabled(s.ProcessMode) {
		v.ProcessMode = string(toolActivityOff)
	}
	return v
}

func (s *Store) updateRunSettings(ctx context.Context, patch RunSettingsPatch, record func(*State, RunSettingsView)) (RunSettingsView, error) {
	var view RunSettingsView
	err := s.update(func(st *State) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		patch.apply(&st.Settings)
		view = st.Settings.view()
		if record != nil {
			record(st, view)
		}
		return nil
	})
	if err != nil {
		return RunSettingsView{}, err
	}
	return view, nil
}

func (p RunSettingsPatch) apply(settings *RunSettings) {
	if p.MaxConcurrent != nil {
		settings.MaxConcurrent = *p.MaxConcurrent
	}
	if p.Progress != nil {
		on := *p.Progress
		settings.Progress = &on
	}
	if p.ReplyMode != nil {
		settings.ReplyMode = *p.ReplyMode
	}
	if p.ToolActivity != nil {
		settings.ToolActivity = toolActivityMode(*p.ToolActivity)
		if settings.ToolActivity == "default" {
			settings.ToolActivity = ""
		}
	}
	if p.ProcessMode != nil {
		settings.ProcessMode = toolActivityMode(*p.ProcessMode)
	}
	if p.IdleTimeoutMinutes != nil {
		settings.IdleTimeoutMinutes = *p.IdleTimeoutMinutes
	}
}
