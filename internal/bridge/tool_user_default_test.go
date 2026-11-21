package bridge

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func toolUserDefaultConfig(t *testing.T, c Config) Config {
	t.Helper()
	c = personalToolConfig(t, c)
	tool := c.Routes["nas"].ToolContext
	user := tool.UserBinding
	tool.Identity = "user-default"
	tool.BotBinding = &ToolBotBinding{
		AppID: user.AppID, Tenant: user.Tenant, Profile: user.Profile, BotOpenID: "ou_personal_bot", ConfigDir: user.ConfigDir,
	}
	return c
}

func TestToolUserDefaultConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(*Config, *Target)
		wantErr bool
	}{
		{"matching cleaned directories", func(_ *Config, target *Target) {
			target.ToolContext.BotBinding.ConfigDir += string(filepath.Separator) + "."
		}, false},
		{"missing bot", func(_ *Config, target *Target) { target.ToolContext.BotBinding = nil }, true},
		{"missing user", func(_ *Config, target *Target) { target.ToolContext.UserBinding = nil }, true},
		{"different app within pair", func(_ *Config, target *Target) { target.ToolContext.BotBinding.AppID = "cli_other" }, true},
		{"different tenant within pair", func(_ *Config, target *Target) { target.ToolContext.BotBinding.Tenant = "lark" }, true},
		{"different receiving app", func(c *Config, _ *Target) { c.AppID = "cli_other" }, true},
		{"different receiving tenant", func(c *Config, _ *Target) { c.Tenant = "lark" }, true},
		{"different profile", func(_ *Config, target *Target) { target.ToolContext.BotBinding.Profile = "Other.Work" }, true},
		{"different directory", func(_ *Config, target *Target) {
			bot := target.ToolContext.BotBinding
			bot.ConfigDir = filepath.Join(bot.ConfigDir, "other")
		}, true},
		{"same Open ID", func(_ *Config, target *Target) {
			target.ToolContext.BotBinding.BotOpenID = target.ToolContext.UserBinding.UserOpenID
		}, true},
		{"extra route user", func(_ *Config, target *Target) { target.Users = append(target.Users, "ou_other") }, true},
		{"wrong single route user", func(_ *Config, target *Target) { target.Users = []string{"ou_other"} }, true},
		{"remote environment", func(_ *Config, target *Target) {
			target.EnvironmentID, target.ToolContext.EnvironmentID = "remote", "remote"
		}, true},
		{"both bindings in bot mode", func(_ *Config, target *Target) { target.ToolContext.Identity = "bot" }, true},
		{"both bindings in user mode", func(_ *Config, target *Target) { target.ToolContext.Identity = "user" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := toolUserDefaultConfig(t, testConfig(t))
			target := c.Routes["nas"]
			tc.change(&c, &target)
			c.Routes["nas"] = target
			if err := c.Validate(); (err != nil) != tc.wantErr {
				t.Fatalf("Validate() = %v, want error: %t", err, tc.wantErr)
			}
		})
	}
}

func TestToolUserDefaultProviderBinding(t *testing.T) {
	c := toolUserDefaultConfig(t, testConfig(t))
	other := c.Routes["nas"]
	tool := *other.ToolContext
	bot := *tool.BotBinding
	bot.ConfigDir += string(filepath.Separator) + "."
	tool.BotBinding = &bot
	other.ToolContext = &tool
	c.Routes["other"] = other
	if err := c.Validate(); err != nil {
		t.Fatalf("same binding on shared provider: %v", err)
	}
	bot.BotOpenID = "ou_other_bot"
	if err := c.Validate(); err == nil {
		t.Fatal("accepted a different bot binding on the same environment and provider")
	}
	bot.BotOpenID = c.Routes["nas"].ToolContext.BotBinding.BotOpenID
	user := *tool.UserBinding
	user.UserOpenID = "ou_other"
	tool.UserBinding = &user
	other.Users = []string{user.UserOpenID}
	c.Routes["other"] = other
	if err := c.Validate(); err == nil {
		t.Fatal("accepted a different user binding on the same environment and provider")
	}
	other.ProviderID = "other-provider"
	c.Routes["other"] = other
	if err := c.Validate(); err != nil {
		t.Fatalf("different binding on separate provider: %v", err)
	}
}

func TestToolUserDefaultSessionKeyDiffersFromSingleIdentities(t *testing.T) {
	c := toolUserDefaultConfig(t, testConfig(t))
	target := c.Routes["nas"]
	m := message("synthetic", "NAS检查磁盘")
	m.UserID, m.ChatType = target.ToolContext.UserBinding.UserOpenID, "p2p"
	dual := sessionKey(m, "nas", target)
	botOnly, userOnly := *target.ToolContext, *target.ToolContext
	botOnly.Identity, botOnly.UserBinding = "bot", nil
	userOnly.Identity, userOnly.BotBinding = "user", nil
	for _, old := range []ToolContext{botOnly, userOnly} {
		target.ToolContext = &old
		if sessionKey(m, "nas", target) == dual {
			t.Fatalf("dual binding reused the %s session key", old.Identity)
		}
	}
}

func TestToolUserDefaultDispatchFailsClosed(t *testing.T) {
	for _, stage := range []struct {
		name      string
		wantSends int
	}{{"onboarding", 0}, {"continuation", 1}} {
		t.Run(stage.name, func(t *testing.T) {
			e, x, _, _ := testEngine(t)
			e.Config = toolUserDefaultConfig(t, e.Config)
			tool := e.Config.Routes["nas"].ToolContext
			e.verifyToolBot = func(context.Context, string, ToolBotBinding) error {
				t.Fatal("dual route called the bot-only verifier")
				return nil
			}
			e.verifyToolUser = func(context.Context, string, ToolUserBinding) error {
				t.Fatal("dual route called the user-only verifier")
				return nil
			}
			checks := 0
			e.verifyToolUserDefault = func(_ context.Context, path string, bot ToolBotBinding, user ToolUserBinding) error {
				if path != tool.CLIPath || bot != *tool.BotBinding || user != *tool.UserBinding {
					t.Fatal("dispatch did not verify the configured CLI and both identity bindings")
				}
				checks++
				if checks <= stage.wantSends {
					return nil
				}
				return errors.New("synthetic identity mismatch")
			}
			m := message("first", "NAS检查磁盘")
			m.UserID, m.ChatType, m.InputKind = tool.UserBinding.UserOpenID, "p2p", "text"
			accept(t, e, m)
			tick(t, e)
			if stage.wantSends == 1 {
				if x.opens != 1 || x.sends != 1 || checks != 1 {
					t.Fatalf("initial dispatch opens/sends/checks = %d/%d/%d, want 1/1/1", x.opens, x.sends, checks)
				}
				x.observation.Status = "completed"
				tick(t, e)
				m.ID, m.Text, m.ParentID = "next", "继续核对", onlyTask(t, e).CardID
				accept(t, e, m)
				tick(t, e)
			}
			if x.opens != stage.wantSends || x.sends != stage.wantSends || checks != stage.wantSends+1 {
				t.Fatalf("opens/sends/checks = %d/%d/%d, want %d/%d/%d", x.opens, x.sends, checks, stage.wantSends, stage.wantSends, stage.wantSends+1)
			}
			task := onlyTask(t, e)
			if task.Phase != "blocked" || !strings.Contains(task.Summary, "工具身份核验失败") || strings.Contains(task.Summary, "synthetic identity mismatch") {
				t.Fatalf("unexpected identity rejection: %q, %q", task.Phase, task.Summary)
			}
		})
	}
}

func TestToolUserDefaultDispatchRejectsOtherUserAndGroup(t *testing.T) {
	for _, tc := range []struct{ name, user, chatType string }{
		{"other user in private chat", "ou_other", "p2p"},
		{"bound user in allowed group", "ou_personal", "group"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, x, _, _ := testEngine(t)
			e.Config = toolUserDefaultConfig(t, e.Config)
			e.Config.access = &accessRuntime{policy: AccessPolicy{Routes: map[string]map[string]bool{
				"nas": {"ou_personal": true, "ou_other": true},
			}}}
			e.verifyToolUserDefault = func(context.Context, string, ToolBotBinding, ToolUserBinding) error {
				t.Fatal("identity verification reached for an out-of-scope message")
				return nil
			}
			m := message("synthetic", "/nas 检查磁盘")
			m.UserID, m.ChatType, m.InputKind = tc.user, tc.chatType, "text"
			accept(t, e, m)
			tick(t, e)
			if x.preflights != 0 || x.opens != 0 || x.sends != 0 || len(e.Store.snapshot().Inbox) != 0 {
				t.Fatalf("out-of-scope message was not rejected: preflights/opens/sends = %d/%d/%d", x.preflights, x.opens, x.sends)
			}
		})
	}
}
