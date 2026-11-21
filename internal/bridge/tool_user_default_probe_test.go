package bridge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVerifyToolUserDefaultBinding(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell fixture")
	}
	const (
		strictError  = "专用飞书工具 CLI 未确认 strict-mode 为 off"
		defaultError = "专用飞书工具 CLI 未确认 default-as 为 auto"
		botError     = "专用飞书工具 CLI 的服务端机器人身份核对失败"
		chatsError   = "专用飞书工具 CLI 的机器人群列表能力核验失败"
		userError    = "专用飞书工具 CLI 的服务端用户身份核对失败"
	)
	tests := []struct {
		name    string
		stage   int
		output  string
		wantErr string
	}{
		{name: "dual identity with profile strict mode off"},
		{name: "global off", stage: 1, output: "strict-mode: off (source: global)"},
		{name: "global default off", stage: 1, output: "strict-mode: off (source: global (default))"},
		{name: "provider off", stage: 1, output: "strict-mode: off (source: credential provider)"},
		{name: "strict bot", stage: 1, output: "strict-mode: bot (source: global)", wantErr: strictError},
		{name: "strict user", stage: 1, output: `strict-mode: user (source: profile "tool-dual")`, wantErr: strictError},
		{name: "wrong profile", stage: 1, output: `strict-mode: off (source: profile "wrong")`, wantErr: strictError},
		{name: "default bot", stage: 2, output: "default-as: bot", wantErr: defaultError},
		{name: "default user", stage: 2, output: "default-as: user", wantErr: defaultError},
		{name: "wrong server bot", stage: 4, output: strings.Replace(toolBotInfoOutput, "ou_fixture", "ou_wrong", 1), wantErr: botError},
		{name: "bot cannot list chats", stage: 5, output: `{"ok":false,"identity":"bot"}`, wantErr: chatsError},
		{name: "wrong server user", stage: 7, output: toolUserInfoOutput, wantErr: userError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			configDir := filepath.Join(dir, "private config")
			if err := os.Mkdir(configDir, 0700); err != nil {
				t.Fatal(err)
			}
			bot := ToolBotBinding{AppID: "cli_fixture", BotOpenID: "ou_fixture", Tenant: "feishu", Profile: "tool-dual", ConfigDir: configDir}
			user := ToolUserBinding{AppID: bot.AppID, UserOpenID: "ou_user_fixture", Tenant: bot.Tenant, Profile: bot.Profile, ConfigDir: bot.ConfigDir}
			cliPath := filepath.Join(dir, "fake lark-cli")
			probes := []struct{ args, output string }{
				{"config strict-mode", `strict-mode: off (source: profile "tool-dual")`},
				{"config default-as", "default-as: auto"},
				{"whoami --as bot", strings.Replace(toolBotWhoamiOutput, "tool-bot", bot.Profile, 1)},
				{"api GET /open-apis/bot/v3/info --as bot --format json", toolBotInfoOutput},
				{"im +chat-list --as bot --page-size 1 --format json", toolBotChatsOutput},
				{"whoami --as user", strings.NewReplacer("tool-user", user.Profile, "ou_fixture", user.UserOpenID).Replace(toolUserWhoamiOutput)},
				{"api GET /open-apis/authen/v1/user_info --as user --format json", strings.Replace(toolUserInfoOutput, "ou_fixture", user.UserOpenID, 1)},
			}
			var script strings.Builder
			script.WriteString(`#!/bin/sh
set -eu
test "${LARKSUITE_CLI_CONFIG_DIR}" = "${0%/*}/private config"
test "${LARKSUITE_CLI_CONFIG_DIR}" -ef .
case "$*" in
`)
			for i, probe := range probes {
				if i+1 == tt.stage {
					probe.output = tt.output
				}
				fmt.Fprintf(&script, "'--profile tool-dual %s')\nprintf '%%s\\n' %d >> calls\ncat <<'FIXTURE_OUTPUT'\n%s\nFIXTURE_OUTPUT\n;;\n", probe.args, i+1, probe.output)
			}
			script.WriteString("*) printf '%s\\n' unexpected >> calls; exit 98 ;;\nesac\n")
			if err := os.WriteFile(cliPath, []byte(script.String()), 0700); err != nil {
				t.Fatal(err)
			}

			err := verifyToolUserDefaultBinding(context.Background(), cliPath, bot, user)
			if tt.wantErr == "" && err != nil || tt.wantErr != "" && (err == nil || err.Error() != tt.wantErr) {
				t.Fatalf("verifyToolUserDefaultBinding() = %v, want %q", err, tt.wantErr)
			}
			calls, err := os.ReadFile(filepath.Join(configDir, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			wantCalls := "1\n2\n3\n4\n5\n6\n7\n"
			if tt.wantErr != "" {
				wantCalls = wantCalls[:2*tt.stage]
			}
			if string(calls) != wantCalls {
				t.Fatalf("calls = %q, want %q", calls, wantCalls)
			}
		})
	}
}
