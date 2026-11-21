package main

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestConfigLiveSettingsCommand(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	base := []string{"-process", id, "-app-id", "cli_settings"}
	zero, disabled, textMode, tools, processMode := 0, false, "text", "default", "stream"
	maxBudget := 50
	markdownMode, cardProcessMode := "markdown", "card"
	view := bridge.RunSettingsView{MaxConcurrent: 4, Progress: true, ReplyMode: "card", ToolActivity: "default", ProcessMode: "off", IdleTimeoutMinutes: 7}
	for _, tc := range []struct {
		name, tenant string
		args         []string
		patch        bridge.RunSettingsPatch
	}{
		{name: "read", tenant: "feishu"},
		{name: "clear", tenant: "lark", args: []string{"-tenant", "lark", "-progress=false", "-idle-timeout-minutes", "0"},
			patch: bridge.RunSettingsPatch{Progress: &disabled, IdleTimeoutMinutes: &zero}},
		{name: "modes and budget", tenant: "feishu", args: []string{"-max-concurrent", "0", "-reply-mode", "text", "-tool-activity", "default", "-process-mode", "stream"},
			patch: bridge.RunSettingsPatch{MaxConcurrent: &zero, ReplyMode: &textMode, ToolActivity: &tools, ProcessMode: &processMode}},
		{name: "maximum budget", tenant: "feishu", args: []string{"-max-concurrent", "50"}, patch: bridge.RunSettingsPatch{MaxConcurrent: &maxBudget}},
		{name: "inline preview", tenant: "feishu", args: []string{"-process-mode", "card"}, patch: bridge.RunSettingsPatch{ProcessMode: &cardProcessMode}},
		{name: "markdown", tenant: "feishu", args: []string{"-reply-mode", "markdown"}, patch: bridge.RunSettingsPatch{ReplyMode: &markdownMode}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			err := runConfigLiveSettings(append(base, tc.args...), &output, func(ctx context.Context, process, tenant, appID string, patch bridge.RunSettingsPatch) (bridge.RunSettingsView, error) {
				if _, timed := ctx.Deadline(); !timed || process != id || tenant != tc.tenant || appID != "cli_settings" {
					t.Fatalf("wrong target %s %s/%s", process, tenant, appID)
				}
				if !reflect.DeepEqual(patch, tc.patch) {
					t.Fatalf("wrong patch: %+v, want %+v", patch, tc.patch)
				}
				return view, nil
			})
			var got bridge.RunSettingsView
			if err != nil || json.Unmarshal(output.Bytes(), &got) != nil || got != view {
				t.Fatalf("unexpected output %q, error %v", &output, err)
			}
		})
	}
	var help bytes.Buffer
	if err := runConfig([]string{"live-settings", "-help"}, nil, &help); err != nil || !strings.Contains(help.String(), "-max-concurrent") ||
		!strings.Contains(help.String(), "card (inline preview for card replies)") || !strings.Contains(help.String(), "stream (separate preview card)") ||
		!strings.Contains(help.String(), "card, text or markdown") || !strings.Contains(help.String(), "budget from 0 to 50") {
		t.Fatalf("config did not expose live-settings help: %v, %s", err, &help)
	}
	for _, args := range [][]string{
		{"-process", id}, {"-process", id[:8], "-app-id", "cli_settings"},
		append(base, "-tenant", "other"), append(base, "-progress=invalid"), append(base, "-max-concurrent=invalid"),
	} {
		var output bytes.Buffer
		if err := runConfigLiveSettings(args, &output, nil); err == nil {
			t.Errorf("invalid arguments accepted: %v", args)
		}
	}
}
