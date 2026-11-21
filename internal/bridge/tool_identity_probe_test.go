package bridge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	toolBotStrictOutput = `strict-mode: bot (source: profile "tool-bot")`
	toolBotWhoamiOutput = `{"profile":"tool-bot","appId":"cli_fixture","brand":"feishu","defaultAs":"auto","identity":"bot","identitySource":"flag","available":true,"tokenStatus":"ready"}`
	toolBotInfoOutput   = `{"ok":true,"identity":"bot","data":{"open_id":"ou_fixture","app_name":"fixture only"}}`
	toolBotChatsOutput  = `{"ok":true,"identity":"bot","data":{"chats":null,"has_more":false,"page_token":""},"meta":{"pagination":{"complete":true,"pages":1,"items":0}}}`
)

func TestVerifyToolBotBinding(t *testing.T) {
	const (
		strictError = "专用飞书工具 CLI 未确认启用 bot-only 策略"
		whoamiError = "专用飞书工具 CLI 的本地机器人身份核对失败"
		infoError   = "专用飞书工具 CLI 的服务端机器人身份核对失败"
		chatsError  = "专用飞书工具 CLI 的机器人群列表能力核验失败"
		runError    = "专用飞书工具 CLI 核验命令失败"
		limitError  = "专用飞书工具 CLI 核验输出超过限制"
	)
	for _, name := range []string{
		"LARKSUITE_CLI_CONFIG_DIR", "LARKSUITE_CLI_PROFILE", "LARKSUITE_CLI_DEFAULT_AS",
		"LARKSUITE_CLI_APP_ID", "LARKSUITE_CLI_USER_ACCESS_TOKEN", "LARKSUITE_CLI_TENANT_ACCESS_TOKEN",
		"LARKSUITE_CLI_AUTH_PROXY", "LARK_CHANNEL", "OPENCLAW_HOME", "HERMES_HOME",
	} {
		t.Setenv(name, "ignored-fixture")
	}

	tests := []struct {
		name    string
		stage   int
		payload string
		shell   string
		wantErr string
	}{
		{name: "empty null chat list"},
		{name: "empty array chat list", stage: 4, payload: strings.Replace(toolBotChatsOutput, "null", "[]", 1)},
		{name: "one chat", stage: 4, payload: `{"ok":true,"identity":"bot","data":{"chats":[{"chat_id":"oc_fixture","name":"fixture only"}],"has_more":true,"page_token":"fixture"},"meta":{"pagination":{"complete":false,"pages":1,"items":1,"next_token":"fixture"}}}`},
		{name: "global bot-only", stage: 1, payload: "strict-mode: bot (source: global)"},
		{name: "strict mode off", stage: 1, payload: "strict-mode: off (source: global (default))", wantErr: strictError},
		{name: "strict mode user", stage: 1, payload: "strict-mode: user (source: global)", wantErr: strictError},
		{name: "strict mode wrong profile", stage: 1, payload: `strict-mode: bot (source: profile "wrong")`, wantErr: strictError},
		{name: "strict mode ambiguous", stage: 1, payload: toolBotStrictOutput + "\nstrict-mode: off (source: global)", wantErr: strictError},
		{name: "wrong app", stage: 2, payload: strings.Replace(toolBotWhoamiOutput, "cli_fixture", "cli_wrong", 1), wantErr: whoamiError},
		{name: "wrong brand", stage: 2, payload: strings.Replace(toolBotWhoamiOutput, "feishu", "lark", 1), wantErr: whoamiError},
		{name: "wrong profile", stage: 2, payload: strings.Replace(toolBotWhoamiOutput, "tool-bot", "wrong", 1), wantErr: whoamiError},
		{name: "wrong identity", stage: 2, payload: strings.Replace(toolBotWhoamiOutput, `"identity":"bot"`, `"identity":"user"`, 1), wantErr: whoamiError},
		{name: "implicit identity", stage: 2, payload: strings.Replace(toolBotWhoamiOutput, `"flag"`, `"strict_mode"`, 1), wantErr: whoamiError},
		{name: "missing readiness", stage: 2, payload: strings.Replace(toolBotWhoamiOutput, `"available":true,`, "", 1), wantErr: whoamiError},
		{name: "bot unavailable", stage: 2, payload: strings.Replace(toolBotWhoamiOutput, `"ready"`, `"not_configured"`, 1), wantErr: whoamiError},
		{name: "wrong open ID", stage: 3, payload: strings.Replace(toolBotInfoOutput, "ou_fixture", "ou_wrong", 1), wantErr: infoError},
		{name: "missing open ID", stage: 3, payload: `{"ok":true,"identity":"bot","data":{"app_name":"ou_fixture"}}`, wantErr: infoError},
		{name: "local identity only", stage: 3, payload: toolBotWhoamiOutput, wantErr: infoError},
		{name: "API success missing", stage: 3, payload: `{"identity":"bot","data":{"open_id":"ou_fixture"}}`, wantErr: infoError},
		{name: "API business rejection at exit zero", stage: 3, payload: `{"ok":false,"identity":"bot","error":{"type":"api","code":230001,"message":"secret-fixture"},"data":{"open_id":"ou_fixture"}}`, wantErr: infoError},
		{name: "API dry run", stage: 3, payload: `{"ok":true,"identity":"bot","dry_run":true,"data":{"open_id":"ou_fixture"}}`, wantErr: infoError},
		{name: "contradictory API success", stage: 3, payload: `{"ok":false,"ok":true,"identity":"bot","data":{"open_id":"ou_fixture"}}`, wantErr: infoError},
		{name: "wrong response field case", stage: 3, payload: strings.Replace(toolBotInfoOutput, `"ok"`, `"OK"`, 1), wantErr: infoError},
		{name: "scope refused at exit zero", stage: 4, payload: `{"ok":false,"identity":"bot","error":{"type":"authorization","message":"secret-fixture"}}`, wantErr: chatsError},
		{name: "scope refused with nonzero exit", stage: 4, shell: `printf '%s\n' '{"ok":false,"error":{"type":"authorization","message":"secret-fixture"}}' >&2; exit 6`, wantErr: runError},
		{name: "stderr rejection at exit zero", stage: 4, payload: toolBotChatsOutput, shell: `printf '%s\n' '{"ok":false,"error":{"type":"api","message":"secret-fixture"}}' >&2`, wantErr: runError},
		{name: "chat list dry run", stage: 4, payload: strings.Replace(toolBotChatsOutput, `"ok":true`, `"ok":true,"dry_run":true`, 1), wantErr: chatsError},
		{name: "chat list delegated identity", stage: 4, payload: strings.Replace(toolBotChatsOutput, `"bot"`, `"user"`, 1), wantErr: chatsError},
		{name: "missing chat list", stage: 4, payload: strings.Replace(toolBotChatsOutput, `"chats":null,`, "", 1), wantErr: chatsError},
		{name: "missing pagination", stage: 4, payload: `{"ok":true,"identity":"bot","data":{"chats":[]}}`, wantErr: chatsError},
		{name: "oversized stdout", stage: 1, shell: "dd if=/dev/zero bs=65536 count=17 2>/dev/null", wantErr: limitError},
		{name: "oversized stderr", stage: 4, payload: toolBotChatsOutput, shell: "dd if=/dev/zero bs=65536 count=17 2>/dev/null >&3", wantErr: limitError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cliPath, binding := fixtureToolBotCLI(t, tt.stage, tt.payload, tt.shell)
			err := verifyToolBotBinding(context.Background(), cliPath, binding)
			if tt.wantErr == "" && err != nil || tt.wantErr != "" && (err == nil || err.Error() != tt.wantErr) {
				t.Fatalf("verifyToolBotBinding() = %v, want %q", err, tt.wantErr)
			}
			log, err := os.ReadFile(filepath.Join(binding.ConfigDir, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			wantSteps := "1\n2\n3\n4\n"
			if tt.wantErr != "" {
				wantSteps = wantSteps[:2*tt.stage]
			}
			if string(log) != wantSteps {
				t.Fatalf("calls = %q, want %q", log, wantSteps)
			}
		})
	}
}

func TestVerifyToolBotBindingCancellation(t *testing.T) {
	cliPath, binding := fixtureToolBotCLI(t, 1, "", "exec sleep 10")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := verifyToolBotBinding(ctx, cliPath, binding)
	if err == nil || err.Error() != "专用飞书工具 CLI 核验超时或已取消" || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("verifyToolBotBinding() error = %v, context error = %v", err, ctx.Err())
	}
}

func fixtureToolBotCLI(t *testing.T, stage int, payload, shell string) (string, ToolBotBinding) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell fixture")
	}
	dir := t.TempDir()
	configDir := filepath.Join(dir, "private config")
	if err := os.Mkdir(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	binding := ToolBotBinding{
		AppID: "cli_fixture", BotOpenID: "ou_fixture", Tenant: "feishu", Profile: "tool-bot", ConfigDir: configDir,
	}
	path := filepath.Join(dir, "fake lark-cli")
	script := `#!/bin/sh
set -eu
test "${LARKSUITE_CLI_CONFIG_DIR}" = "${0%/*}/private config"
test "${LARKSUITE_CLI_CONFIG_DIR}" -ef .
test "${LARKSUITE_CLI_NO_UPDATE_NOTIFIER}" = 1
test "${LARKSUITE_CLI_NO_SKILLS_NOTIFIER}" = 1
test -z "${LARKSUITE_CLI_PROFILE-}${LARKSUITE_CLI_DEFAULT_AS-}${LARKSUITE_CLI_APP_ID-}${LARKSUITE_CLI_USER_ACCESS_TOKEN-}${LARKSUITE_CLI_TENANT_ACCESS_TOKEN-}${LARKSUITE_CLI_AUTH_PROXY-}${LARK_CHANNEL-}${OPENCLAW_HOME-}${HERMES_HOME-}"
exec 3>&2
case "$*" in
`
	probes := []struct{ args, output string }{
		{"config strict-mode", toolBotStrictOutput},
		{"whoami --as bot", toolBotWhoamiOutput},
		{"api GET /open-apis/bot/v3/info --as bot --format json", toolBotInfoOutput},
		{"im +chat-list --as bot --page-size 1 --format json", toolBotChatsOutput},
	}
	for i, probe := range probes {
		script += "'--profile tool-bot " + probe.args + "')\n"
		script += "printf '%s\\n' " + string(rune('1'+i)) + " >> calls\n"
		if i+1 == stage {
			probe.output = payload
			script += shell + "\n"
		}
		script += "cat <<'FIXTURE_OUTPUT'\n" + probe.output + "\nFIXTURE_OUTPUT\n;;\n"
	}
	script += "*) exit 98 ;;\nesac\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path, binding
}
