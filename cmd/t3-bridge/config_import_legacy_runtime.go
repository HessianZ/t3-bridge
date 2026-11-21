package main

import (
	"encoding/json"
	"errors"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

type legacyProfilePreferences struct {
	Access                json.RawMessage `json:"access"`
	MessageReply          string          `json:"messageReply"`
	MessageReplyMigrated  bool            `json:"messageReplyMigrated"`
	ShowToolCalls         *bool           `json:"showToolCalls"`
	CotMessages           string          `json:"cotMessages"`
	MaxConcurrentRuns     float64         `json:"maxConcurrentRuns"`
	RunIdleTimeoutMinutes float64         `json:"runIdleTimeoutMinutes"`
	RequireMentionInGroup *bool           `json:"requireMentionInGroup"`
}

func legacyInitialRuntime(singleAccount bool, preferences legacyProfilePreferences, access legacyImportObject, allowedChats []string) (bridge.InitialRuntimeConfig, error) {
	reply := "markdown"
	if preferences.MessageReply == "card" || (preferences.MessageReply == "text" && preferences.MessageReplyMigrated) {
		reply = preferences.MessageReply
	}
	tools := "detailed"
	if preferences.ShowToolCalls != nil && !*preferences.ShowToolCalls {
		tools = "off"
	}
	process := "off"
	switch preferences.CotMessages {
	case "brief", "simple":
		process = "stream-brief"
	case "detailed", "on":
		process = "stream-detailed"
	default:
		switch reply {
		case "card":
			process = "card"
		case "markdown":
			process = "stream"
		}
	}

	concurrency, idle := 10, 0
	if preferences.MaxConcurrentRuns >= 1 {
		concurrency = int(min(preferences.MaxConcurrentRuns, bridge.MaxRunConcurrency))
	}
	if preferences.RunIdleTimeoutMinutes > 0 {
		idle = int(min(max(preferences.RunIdleTimeoutMinutes, 1), bridge.MaxRunIdleTimeoutMinutes))
	}
	result := bridge.InitialRuntimeConfig{
		Settings: bridge.RunSettingsPatch{
			MaxConcurrent: &concurrency, ReplyMode: &reply, ToolActivity: &tools, ProcessMode: &process, IdleTimeoutMinutes: &idle,
		},
		RequireMention: preferences.RequireMentionInGroup,
	}
	if raw := access["requireMentionInGroup"]; !singleAccount && raw != nil {
		var required *bool
		if json.Unmarshal(raw, &required) != nil {
			return result, errors.New("legacy access.requireMentionInGroup must be a boolean")
		}
		if required != nil {
			result.RequireMention = required
		}
	}

	var perGroup legacyImportObject
	if raw := access["chatRequireMention"]; raw != nil && json.Unmarshal(raw, &perGroup) != nil {
		return result, errors.New("legacy access.chatRequireMention must be an object")
	}
	for _, chat := range allowedChats {
		var required *bool
		if raw := perGroup[chat]; raw != nil && json.Unmarshal(raw, &required) == nil && required != nil {
			if result.GroupMentions == nil {
				result.GroupMentions = map[string]bool{}
			}
			result.GroupMentions[chat] = *required
		}
	}
	return result, nil
}
