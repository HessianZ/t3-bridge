package bridge

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuardedToolIdentityDispatch(t *testing.T) {
	for _, continuation := range []bool{false, true} {
		name := "first request"
		if continuation {
			name = "continuation"
		}
		t.Run(name, func(t *testing.T) {
			e, x, _, _ := testEngine(t)
			tool := installGuardedToolTestRoute(t, e)

			var checks int
			e.verifyToolBot = func(_ context.Context, path string, b ToolBotBinding) error {
				checks++
				if path != tool.CLIPath || b != *tool.BotBinding {
					t.Fatal("dispatch did not verify the selected route binding")
				}
				if continuation && checks == 1 {
					return nil
				}
				return errors.New("synthetic private CLI error")
			}
			accept(t, e, message("one", "NAS检查磁盘"))
			tick(t, e)
			wantSends := 0
			if continuation {
				if x.sends != 1 || checks != 1 {
					t.Fatalf("initial sends/checks = %d/%d, want 1/1", x.sends, checks)
				}
				x.observation.Status = "completed"
				tick(t, e)
				reply := message("two", "继续核对")
				reply.ParentID = "card"
				accept(t, e, reply)
				tick(t, e)
				wantSends = 1
			}
			if x.sends != wantSends || x.opens != wantSends || checks != wantSends+1 {
				t.Fatalf("opens/sends/checks = %d/%d/%d, want %d/%d/%d", x.opens, x.sends, checks, wantSends, wantSends, wantSends+1)
			}
			task := onlyTask(t, e)
			if task.Phase != "blocked" || !strings.Contains(task.Summary, "工具身份核验失败") || strings.Contains(task.Summary, "synthetic private") {
				t.Fatalf("unexpected rejection: %q, %q", task.Phase, task.Summary)
			}
		})
	}
}

func TestGuardedToolIdentityReconcilesOriginalRunAfterBindingChange(t *testing.T) {
	e, x, _, _ := testEngine(t)
	tool := installGuardedToolTestRoute(t, e)
	checks := 0
	e.verifyToolBot = func(context.Context, string, ToolBotBinding) error { checks++; return nil }
	x.sendErr = errors.New("synthetic uncertain send")
	accept(t, e, message("one", "NAS检查磁盘"))
	tick(t, e)
	first := onlyTask(t, e)
	if first.Phase != "needs_reconcile" || x.sends != 1 || checks != 1 {
		t.Fatalf("uncertain send did not retain its identity: %s, sends=%d, checks=%d", first.Phase, x.sends, checks)
	}
	changed := *tool.BotBinding
	changed.BotOpenID = "ou_changed"
	tool.BotBinding = &changed
	x.observation = Observation{Status: "completed", FoundRequest: true, RunID: "run", Summary: "合成结果"}
	tick(t, e)
	task := onlyTask(t, e)
	if task.Phase != "completed" || task.RequestID != first.RequestID || task.RunID != "run" ||
		task.Target.ToolContext.BotBinding.BotOpenID != "ou_tool" || x.sends != 1 || checks != 1 {
		t.Fatal("read-only reconciliation changed identity or submitted a new request")
	}
}

func installGuardedToolTestRoute(t *testing.T, e *Engine) *ToolContext {
	t.Helper()
	target := e.Config.Routes["nas"]
	target.ToolContext = &ToolContext{
		CLIPath: filepath.Join(t.TempDir(), "lark-cli"), EnvironmentID: target.EnvironmentID, Identity: "bot",
		BotBinding: &ToolBotBinding{AppID: "cli_tool", BotOpenID: "ou_tool", Tenant: "feishu", Profile: "tool", ConfigDir: t.TempDir()},
	}
	e.Config.Routes["nas"] = target
	return target.ToolContext
}

func TestGuardedToolIdentityNamesProviderAndSessionBinding(t *testing.T) {
	target := Target{EnvironmentID: "local", ProviderID: "codex-tool"}
	legacy := target
	legacy.ToolContext = &ToolContext{CLIPath: filepath.Join(t.TempDir(), "lark-cli"), EnvironmentID: "local", Identity: "bot", Verified: true, Version: "1.0.96"}
	if targetIdentity(legacy) != targetIdentity(target) {
		t.Fatal("a legacy tool declaration changed its session identity")
	}
	guarded := legacy
	tool := *legacy.ToolContext
	tool.BotBinding = &ToolBotBinding{AppID: "cli_tool", BotOpenID: "ou_tool", Tenant: "lark", ConfigDir: t.TempDir(), Profile: "dedicated"}
	guarded.ToolContext = &tool
	if targetIdentity(guarded) == targetIdentity(legacy) {
		t.Fatal("a guarded route reused an unguarded session identity")
	}
	config := Config{Routes: map[string]Target{"one": guarded}, AdditionalAccounts: []Config{{Routes: map[string]Target{"two": legacy}}}}
	if err := config.validateToolProviders(); err == nil {
		t.Fatal("an unguarded route could select a guarded provider")
	}
	config.AdditionalAccounts[0].Routes["two"] = guarded
	if err := config.validateToolProviders(); err != nil {
		t.Fatal(err)
	}
	changed := guarded
	changedTool := *guarded.ToolContext
	changedBinding := *changedTool.BotBinding
	changedBinding.BotOpenID = "ou_changed"
	changedTool.BotBinding = &changedBinding
	changed.ToolContext = &changedTool
	config.AdditionalAccounts[0].Routes["two"] = changed
	if targetIdentity(changed) == targetIdentity(guarded) || config.validateToolProviders() == nil {
		t.Fatal("changing the known bot did not invalidate the binding")
	}
}
