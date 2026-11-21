package bridge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	toolUserStrictOutput = `strict-mode: user (source: profile "tool-user")`
	toolUserWhoamiOutput = `{"profile":"tool-user","appId":"cli_fixture","brand":"feishu","defaultAs":"auto","identity":"user","identitySource":"flag","available":true,"tokenStatus":"ready","onBehalfOf":{"openId":"ou_fixture","userName":"fixture only"}}`
	toolUserInfoOutput   = `{"ok":true,"identity":"user","data":{"open_id":"ou_fixture","name":"fixture only"}}`
)

func TestVerifyToolUserBinding(t *testing.T) {
	const (
		strictError = "专用飞书工具 CLI 未确认启用 user-only 策略"
		whoamiError = "专用飞书工具 CLI 的本地用户身份核对失败"
		infoError   = "专用飞书工具 CLI 的服务端用户身份核对失败"
		runError    = "专用飞书工具 CLI 核验命令失败"
	)
	tests := []struct {
		name    string
		stage   int
		output  string
		shell   string
		wantErr string
	}{
		{name: "ready"},
		{name: "existing token needs refresh", stage: 2, output: strings.Replace(toolUserWhoamiOutput, `"ready"`, `"needs_refresh"`, 1)},
		{name: "global user-only", stage: 1, output: "strict-mode: user (source: global)"},
		{name: "effective provider user-only", stage: 1, output: "strict-mode: user (source: credential provider)"},
		{name: "strict mode off", stage: 1, output: "strict-mode: off (source: global (default))", wantErr: strictError},
		{name: "strict mode bot", stage: 1, output: `strict-mode: bot (source: profile "tool-user")`, wantErr: strictError},
		{name: "strict mode wrong profile", stage: 1, output: `strict-mode: user (source: profile "wrong")`, wantErr: strictError},
		{name: "strict mode command failure", stage: 1, shell: `printf '%s\n' 'fixture-only path and identity details' >&2; exit 1`, wantErr: runError},
		{name: "no user token", stage: 2, output: strings.Replace(toolUserWhoamiOutput, `"available":true,"tokenStatus":"ready"`, `"available":false,"tokenStatus":"missing","hint":"fixture-only token details"`, 1), wantErr: whoamiError},
		{name: "unknown token status", stage: 2, output: strings.Replace(toolUserWhoamiOutput, `"ready"`, `"unknown"`, 1), wantErr: whoamiError},
		{name: "wrong app", stage: 2, output: strings.Replace(toolUserWhoamiOutput, "cli_fixture", "cli_wrong", 1), wantErr: whoamiError},
		{name: "wrong brand", stage: 2, output: strings.Replace(toolUserWhoamiOutput, "feishu", "lark", 1), wantErr: whoamiError},
		{name: "wrong profile", stage: 2, output: strings.Replace(toolUserWhoamiOutput, "tool-user", "wrong", 1), wantErr: whoamiError},
		{name: "wrong user", stage: 2, output: strings.Replace(toolUserWhoamiOutput, "ou_fixture", "ou_wrong", 1), wantErr: whoamiError},
		{name: "unknown user", stage: 2, output: strings.Replace(toolUserWhoamiOutput, `"openId":"ou_fixture",`, "", 1), wantErr: whoamiError},
		{name: "implicit identity", stage: 2, output: strings.Replace(toolUserWhoamiOutput, `"flag"`, `"strict_mode"`, 1), wantErr: whoamiError},
		{name: "wrong local identity", stage: 2, output: strings.Replace(toolUserWhoamiOutput, `"identity":"user"`, `"identity":"bot"`, 1), wantErr: whoamiError},
		{name: "duplicate local open ID", stage: 2, output: strings.Replace(toolUserWhoamiOutput, `"openId":"ou_fixture"`, `"openId":"ou_wrong","openId":"ou_fixture"`, 1), wantErr: whoamiError},
		{name: "wrong server user", stage: 3, output: strings.Replace(toolUserInfoOutput, "ou_fixture", "ou_wrong", 1), wantErr: infoError},
		{name: "missing server open ID", stage: 3, output: strings.Replace(toolUserInfoOutput, `"open_id"`, `"openId"`, 1), wantErr: infoError},
		{name: "wrong explicit API identity", stage: 3, output: strings.Replace(toolUserInfoOutput, `"user"`, `"bot"`, 1), wantErr: infoError},
		{name: "missing API success", stage: 3, output: strings.Replace(toolUserInfoOutput, `"ok":true,`, "", 1), wantErr: infoError},
		{name: "API rejected", stage: 3, output: strings.Replace(toolUserInfoOutput, `"ok":true`, `"ok":false`, 1), wantErr: infoError},
		{name: "API dry run", stage: 3, output: strings.Replace(toolUserInfoOutput, `"ok":true`, `"ok":true,"dry_run":true`, 1), wantErr: infoError},
		{name: "API trailing data", stage: 3, output: toolUserInfoOutput + "\n{}", wantErr: infoError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cliPath, binding := fixtureToolUserCLI(t, tt.stage, tt.output, tt.shell)
			err := verifyToolUserBinding(context.Background(), cliPath, binding)
			if tt.wantErr == "" && err != nil || tt.wantErr != "" && (err == nil || err.Error() != tt.wantErr) {
				t.Fatalf("verifyToolUserBinding() = %v, want %q", err, tt.wantErr)
			}
			calls, err := os.ReadFile(filepath.Join(binding.ConfigDir, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			wantCalls := "1\n2\n3\n"
			if tt.wantErr != "" {
				wantCalls = wantCalls[:2*tt.stage]
			}
			if string(calls) != wantCalls {
				t.Fatalf("calls = %q, want %q", calls, wantCalls)
			}
		})
	}
}

func TestVerifyToolUserBindingCancellation(t *testing.T) {
	cliPath, binding := fixtureToolUserCLI(t, 1, "", "exec sleep 10")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := verifyToolUserBinding(ctx, cliPath, binding)
	if err == nil || err.Error() != "专用飞书工具 CLI 核验超时或已取消" || ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("verifyToolUserBinding() = %v, context error = %v", err, ctx.Err())
	}
}

func fixtureToolUserCLI(t *testing.T, stage int, output, shell string) (string, ToolUserBinding) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell fixture")
	}
	dir := t.TempDir()
	configDir := filepath.Join(dir, "private config")
	if err := os.Mkdir(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	binding := ToolUserBinding{
		AppID: "cli_fixture", UserOpenID: "ou_fixture", Tenant: "feishu", Profile: "tool-user", ConfigDir: configDir,
	}
	path := filepath.Join(dir, "fake lark-cli")
	var script strings.Builder
	script.WriteString(`#!/bin/sh
set -eu
test "${LARKSUITE_CLI_CONFIG_DIR}" = "${0%/*}/private config"
test "${LARKSUITE_CLI_CONFIG_DIR}" -ef .
case "$*" in
`)
	probes := []struct{ args, output string }{
		{"config strict-mode", toolUserStrictOutput},
		{"whoami --as user", toolUserWhoamiOutput},
		{"api GET /open-apis/authen/v1/user_info --as user --format json", toolUserInfoOutput},
	}
	for i, probe := range probes {
		fmt.Fprintf(&script, "'--profile tool-user %s')\nprintf '%%s\\n' %d >> calls\n", probe.args, i+1)
		if i+1 == stage {
			probe.output = output
			script.WriteString(shell + "\n")
		}
		fmt.Fprintf(&script, "cat <<'FIXTURE_OUTPUT'\n%s\nFIXTURE_OUTPUT\n;;\n", probe.output)
	}
	script.WriteString("*) printf '%s\\n' unexpected >> calls; exit 98 ;;\nesac\n")
	if err := os.WriteFile(path, []byte(script.String()), 0700); err != nil {
		t.Fatal(err)
	}
	return path, binding
}
