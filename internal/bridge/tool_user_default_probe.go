package bridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

func verifyToolUserDefaultBinding(ctx context.Context, cliPath string, bot ToolBotBinding, user ToolUserBinding) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Both lark-cli v1.0.96 config commands are read-only without a value.
	out, err := runToolIdentityCLI(ctx, cliPath, bot.Profile, bot.ConfigDir, "config", "strict-mode")
	if err != nil {
		return err
	}
	switch strings.TrimSpace(string(out)) {
	case fmt.Sprintf("strict-mode: off (source: profile %q)", bot.Profile),
		"strict-mode: off (source: global)", "strict-mode: off (source: global (default))",
		"strict-mode: off (source: credential provider)":
	default:
		return errors.New("专用飞书工具 CLI 未确认 strict-mode 为 off")
	}

	out, err = runToolIdentityCLI(ctx, cliPath, bot.Profile, bot.ConfigDir, "config", "default-as")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) != "default-as: auto" {
		return errors.New("专用飞书工具 CLI 未确认 default-as 为 auto")
	}

	if err := probeToolBotIdentity(ctx, cliPath, bot); err != nil {
		return err
	}
	return probeToolUserIdentity(ctx, cliPath, user)
}
