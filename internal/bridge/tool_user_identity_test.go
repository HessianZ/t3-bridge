package bridge

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func personalToolConfig(t *testing.T, c Config) Config {
	t.Helper()
	c.AppID = "cli_personal"
	c.Users = []string{"ou_personal", "ou_other"}
	target := c.Routes["nas"]
	target.Users = []string{"ou_personal"}
	target.ToolContext = &ToolContext{
		CLIPath: filepath.Join(t.TempDir(), "lark-cli"), EnvironmentID: target.EnvironmentID, Identity: "user",
		UserBinding: &ToolUserBinding{
			AppID: c.AppID, Tenant: "feishu", Profile: "Personal.Work", UserOpenID: "ou_personal", ConfigDir: t.TempDir(),
		},
	}
	c.Routes["nas"] = target
	return c
}

func TestPersonalToolBindingRequiresReceivingAppAndSingleRouteUser(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config, *Target)
	}{
		{"different app", func(c *Config, _ *Target) { c.AppID = "cli_other" }},
		{"different tenant", func(c *Config, _ *Target) { c.Tenant = "lark" }},
		{"other authorized route user", func(_ *Config, target *Target) { target.Users = append(target.Users, "ou_other") }},
		{"mixed identity binding", func(_ *Config, target *Target) { target.ToolContext.BotBinding = &ToolBotBinding{} }},
		{"remote environment", func(_ *Config, target *Target) {
			target.EnvironmentID, target.ToolContext.EnvironmentID = "remote", "remote"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := personalToolConfig(t, testConfig(t))
			if err := c.Validate(); err != nil {
				t.Fatal(err)
			}
			target := c.Routes["nas"]
			tc.change(&c, &target)
			c.Routes["nas"] = target
			if err := c.Validate(); err == nil {
				t.Fatal("accepted an invalid personal tool binding")
			}
		})
	}
}

func TestPersonalToolBindingExcludesGroupAndAutomaticMessages(t *testing.T) {
	c := personalToolConfig(t, testConfig(t))
	target := c.Routes["nas"]
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	// The runtime ACL can grant an additional user, but the tool credential
	// still belongs to the single configured user.
	c.access = &accessRuntime{policy: AccessPolicy{Routes: map[string]map[string]bool{
		"nas": {"ou_personal": true, "ou_other": true},
	}}}
	c.CommentRoute = "nas"
	m := message("synthetic", "NAS检查磁盘")
	m.ChatType, m.UserID, m.InputKind = "p2p", "ou_personal", "text"
	if !c.TargetAllowed(m, target, "nas") {
		t.Fatal("rejected the bound user in a private chat")
	}
	for _, tc := range []struct {
		name   string
		change func(*Message)
	}{
		{"role grant to someone else", func(msg *Message) { msg.UserID = "ou_other" }},
		{"group", func(msg *Message) { msg.ChatType = "group" }},
		{"unverified chat kind", func(msg *Message) { msg.ChatType = "" }},
		{"comment", func(msg *Message) { msg.Comment = &CommentSource{} }},
		{"meeting", func(msg *Message) { msg.Meeting = &meetingSource{} }},
		{"automatic", func(msg *Message) { msg.InputKind = "automatic" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := m
			tc.change(&candidate)
			if c.TargetAllowed(candidate, target, "nas") {
				t.Fatal("granted use of the personal tool credential")
			}
		})
	}
}

func TestPersonalToolIdentityIsCheckedOnContinuation(t *testing.T) {
	e, x, _, _ := testEngine(t)
	e.Config = personalToolConfig(t, e.Config)
	checks := 0
	e.verifyToolUser = func(_ context.Context, path string, user ToolUserBinding) error {
		checks++
		tool := e.Config.Routes["nas"].ToolContext
		if path != tool.CLIPath || user != *tool.UserBinding {
			t.Fatal("verified a different CLI or user binding")
		}
		if checks == 1 {
			return nil
		}
		return errors.New("synthetic token expired")
	}
	first := message("first", "NAS检查磁盘")
	first.UserID, first.ChatType, first.InputKind = "ou_personal", "p2p", "text"
	accept(t, e, first)
	tick(t, e)
	if x.sends != 1 || x.opens != 1 || checks != 1 {
		t.Fatalf("initial dispatch sends/opens/checks = %d/%d/%d", x.sends, x.opens, checks)
	}
	x.observation.Status = "completed"
	tick(t, e)
	next := first
	next.ID, next.Text, next.ParentID = "next", "继续核对", "card"
	accept(t, e, next)
	tick(t, e)
	if x.sends != 1 || x.opens != 1 || checks != 2 || onlyTask(t, e).Phase != "blocked" {
		t.Fatalf("unverified continuation sends/opens/checks = %d/%d/%d", x.sends, x.opens, checks)
	}
}
