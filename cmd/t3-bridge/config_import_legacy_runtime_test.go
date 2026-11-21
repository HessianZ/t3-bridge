package main

import (
	"bytes"
	"maps"
	"path/filepath"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestLegacyImportPreservesRuntimePreferences(t *testing.T) {
	const defaultSingle = `{"app":{"id":"cli_legacy","tenant":"feishu"}}`
	for _, tc := range []struct {
		name, source          string
		reply, tools, process string
		parallel, idle        int
		defaultMention        *bool
		groupMentions         map[string]bool
	}{
		{
			name: "V2 old markdown and group defaults",
			source: `{
				"schemaVersion":2, "activeProfile":"work",
				"profiles":{"work":{"schemaVersion":2,"accounts":{"app":{"id":"cli_legacy"}},
					"preferences":{"messageReply":"text","requireMentionInGroup":true,
						"maxConcurrentRuns":50,"runIdleTimeoutMinutes":244},
					"access":{"allowedUsers":["ou_legacy"],"allowedChats":["oc_alpha","oc_beta"],"requireMentionInGroup":false,
						"chatRequireMention":{"oc_alpha":true,"oc_beta":false,"oc_never_granted":false}}
				}}
			}`,
			reply: "markdown", tools: "detailed", process: "stream",
			parallel: 50, idle: bridge.MaxRunIdleTimeoutMinutes,
			defaultMention: new(bool), groupMentions: map[string]bool{"oc_alpha": true, "oc_beta": false},
		},
		{
			name: "V1 explicit text and abbreviated process",
			source: `{
				"accounts":{"app":{"id":"cli_legacy","tenant":"feishu"}},
				"preferences":{"messageReply":"text","messageReplyMigrated":true,"showToolCalls":false,
					"cotMessages":"simple","maxConcurrentRuns":4.9,"runIdleTimeoutMinutes":0.2,
					"requireMentionInGroup":false,"access":{"allowedUsers":["ou_legacy"],"allowedChats":["oc_alpha"],
						"requireMentionInGroup":true,"chatRequireMention":{"oc_alpha":true}}}
			}`,
			reply: "text", tools: "off", process: "stream-brief", parallel: 4, idle: 1,
			defaultMention: new(bool), groupMentions: map[string]bool{"oc_alpha": true},
		},
		{
			name:   "V1 concurrency above the previous limit",
			source: `{"app":{"id":"cli_legacy","tenant":"feishu"},"preferences":{"maxConcurrentRuns":17}}`,
			reply:  "markdown", tools: "detailed", process: "stream", parallel: 17,
		},
		{
			name:   "V1 result card with brief process",
			source: `{"app":{"id":"cli_legacy","tenant":"feishu"},"preferences":{"messageReply":"card","showToolCalls":true,"cotMessages":"brief"}}`,
			reply:  "card", tools: "detailed", process: "stream-brief", parallel: 10,
		},
		{
			name:   "V1 markdown with process on",
			source: `{"app":{"id":"cli_legacy","tenant":"feishu"},"preferences":{"showToolCalls":false,"cotMessages":"on"}}`,
			reply:  "markdown", tools: "off", process: "stream-detailed", parallel: 10,
		},
		{
			name:   "V1 result card with detailed process",
			source: `{"app":{"id":"cli_legacy","tenant":"feishu"},"preferences":{"messageReply":"card","cotMessages":"detailed"}}`,
			reply:  "card", tools: "detailed", process: "stream-detailed", parallel: 10,
		},
		{
			name:   "V1 explicit text without process preferences",
			source: `{"app":{"id":"cli_legacy","tenant":"feishu"},"preferences":{"messageReply":"text","messageReplyMigrated":true}}`,
			reply:  "text", tools: "detailed", process: "off", parallel: 10,
		},
		{
			name:   "V1 original result card",
			source: `{"accounts":{"app":{"id":"cli_legacy","tenant":"feishu"}},"preferences":{"messageReply":"card"}}`,
			reply:  "card", tools: "detailed", process: "card", parallel: 10,
		},
		{
			name: "old configuration with no preferences", source: defaultSingle,
			reply: "markdown", tools: "detailed", process: "stream", parallel: 10,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, err := loadConfigTemplate("generic")
			if err != nil {
				t.Fatal(err)
			}
			base = bytes.ReplaceAll(base, []byte("REPLACE_OPEN_ID"), []byte("ou_draft"))
			dir := t.TempDir()
			request := legacyConfigImport{StateDir: filepath.Join(dir, "state"), AppSecretFile: filepath.Join(dir, "app.secret")}
			draft, err := request.candidate(base, []byte(tc.source))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(draft, []byte("oc_never_granted")) {
				t.Fatal("import included a group absent from its allowlist")
			}
			settings, err := bridge.ParseConfig(draft)
			if err != nil || settings.ReceiverAuthorized || settings.InitialRuntime == nil {
				t.Fatalf("missing reviewed initial runtime settings: %v", err)
			}
			initial := settings.InitialRuntime
			p := initial.Settings
			if p.ReplyMode == nil || *p.ReplyMode != tc.reply || p.ToolActivity == nil || *p.ToolActivity != tc.tools ||
				p.ProcessMode == nil || *p.ProcessMode != tc.process || p.MaxConcurrent == nil || *p.MaxConcurrent != tc.parallel ||
				p.IdleTimeoutMinutes == nil || *p.IdleTimeoutMinutes != tc.idle {
				t.Fatalf("incorrect imported preferences: %s", draft)
			}
			if (initial.RequireMention == nil) != (tc.defaultMention == nil) ||
				(initial.RequireMention != nil && *initial.RequireMention != *tc.defaultMention) ||
				!maps.Equal(initial.GroupMentions, tc.groupMentions) {
				t.Fatalf("incorrect imported mention rules: %s", draft)
			}
		})
	}
}
