package bridge

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"unicode"
)

func validateToolBinding(appID, tenant, openID, profile, configDir string) error {
	if (tenant != "feishu" && tenant != "lark") || !userGroupsAppID.MatchString(appID) || !openIDPattern.MatchString(openID) {
		return errors.New("toolContext binding requires feishu or lark, a CLI app ID and an expected Open ID")
	}
	if !filepath.IsAbs(configDir) || strings.ContainsFunc(configDir, unicode.IsControl) || profile == "" ||
		strings.TrimSpace(profile) != profile || strings.HasPrefix(profile, "-") || strings.ContainsFunc(profile, unicode.IsControl) {
		return errors.New("toolContext binding requires an absolute CLI config directory and an explicit profile")
	}
	return nil
}

func (t *ToolContext) guarded() bool {
	return t != nil && (t.BotBinding != nil || t.UserBinding != nil)
}

func (t *ToolContext) allowsToolUser(m Message) bool {
	if t == nil || t.UserBinding == nil {
		return true
	}
	if m.UserID != t.UserBinding.UserOpenID || m.ChatType != "p2p" || m.Comment != nil || m.Meeting != nil {
		return false
	}
	switch m.InputKind {
	case "", "text", "post", "image", "file", "merge_forward", "form", "review":
		return true
	default:
		return false
	}
}

func (c Config) validateToolProviders() error {
	type toolProviderBinding struct {
		CLIPath string
		Bot     ToolBotBinding
		User    ToolUserBinding
	}
	providers := map[[2]string]toolProviderBinding{}
	for _, account := range c.Accounts() {
		for _, target := range account.Routes {
			key := [2]string{target.EnvironmentID, target.ProviderID}
			var current toolProviderBinding
			if tool := target.ToolContext; tool.guarded() {
				current.CLIPath = filepath.Clean(tool.CLIPath)
				if tool.BotBinding != nil {
					current.Bot = *tool.BotBinding
					current.Bot.ConfigDir = filepath.Clean(current.Bot.ConfigDir)
				}
				if tool.UserBinding != nil {
					current.User = *tool.UserBinding
					current.User.ConfigDir = filepath.Clean(current.User.ConfigDir)
				}
			}
			if previous, exists := providers[key]; exists && previous != current {
				return errors.New("a guarded T3 provider must use the same tool identity binding in every route")
			}
			providers[key] = current
		}
	}
	return nil
}

func (t *ToolContext) verifyIdentity(ctx context.Context, bot func(context.Context, string, ToolBotBinding) error, user func(context.Context, string, ToolUserBinding) error, userDefault func(context.Context, string, ToolBotBinding, ToolUserBinding) error) error {
	if t.Identity == "user-default" {
		if userDefault == nil {
			userDefault = verifyToolUserDefaultBinding
		}
		botBinding, userBinding := *t.BotBinding, *t.UserBinding
		botBinding.ConfigDir = filepath.Clean(botBinding.ConfigDir)
		userBinding.ConfigDir = botBinding.ConfigDir
		return userDefault(ctx, t.CLIPath, botBinding, userBinding)
	}
	if t.BotBinding != nil {
		if bot == nil {
			bot = verifyToolBotBinding
		}
		return bot(ctx, t.CLIPath, *t.BotBinding)
	}
	if user == nil {
		user = verifyToolUserBinding
	}
	return user(ctx, t.CLIPath, *t.UserBinding)
}

// CheckToolIdentity verifies the local CLI credentials for an explicitly guarded route.
func CheckToolIdentity(ctx context.Context, c Config, route string) error {
	target, ok := c.Routes[route]
	if !ok || !target.ToolContext.guarded() {
		return errors.New("the selected route has no tool identity binding")
	}
	if !c.MCP.LocalEnvironment(target.EnvironmentID) {
		return errors.New("tool identity verification requires a local T3 environment")
	}
	return target.ToolContext.verifyIdentity(ctx, nil, nil, nil)
}
