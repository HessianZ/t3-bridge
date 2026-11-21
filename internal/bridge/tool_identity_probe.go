package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ToolBotBinding identifies the dedicated lark-cli bot and its private profile.
type ToolBotBinding struct {
	AppID     string `json:"appId"`
	BotOpenID string `json:"botOpenId"`
	Tenant    string `json:"tenant"`
	Profile   string `json:"profile"`
	ConfigDir string `json:"configDir"`
}

func verifyToolBotBinding(ctx context.Context, cliPath string, binding ToolBotBinding) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// In lark-cli v1.0.96, strict-mode without a value only queries the
	// effective policy. Check it before running identity diagnostics or APIs.
	out, err := runToolIdentityCLI(ctx, cliPath, binding.Profile, binding.ConfigDir, "config", "strict-mode")
	if err != nil {
		return err
	}
	switch strings.TrimSpace(string(out)) {
	case fmt.Sprintf("strict-mode: bot (source: profile %q)", binding.Profile),
		"strict-mode: bot (source: global)", "strict-mode: bot (source: credential provider)":
	default:
		return errors.New("专用飞书工具 CLI 未确认启用 bot-only 策略")
	}
	return probeToolBotIdentity(ctx, cliPath, binding)
}

func probeToolBotIdentity(ctx context.Context, cliPath string, binding ToolBotBinding) error {
	out, err := runToolIdentityCLI(ctx, cliPath, binding.Profile, binding.ConfigDir, "whoami", "--as", "bot")
	if err != nil {
		return err
	}
	var who struct {
		AppID          string `json:"appId"`
		Brand          string `json:"brand"`
		Profile        string `json:"profile"`
		DefaultAs      string `json:"defaultAs"`
		Identity       string `json:"identity"`
		IdentitySource string `json:"identitySource"`
		Available      bool   `json:"available"`
		TokenStatus    string `json:"tokenStatus"`
		Hint           string `json:"hint"`
	}
	if decodeConfigObject(out, &who) != nil || who.AppID != binding.AppID ||
		who.Brand != binding.Tenant || who.Profile != binding.Profile ||
		who.Identity != "bot" || who.IdentitySource != "flag" || !who.Available || who.TokenStatus != "ready" {
		return errors.New("专用飞书工具 CLI 的本地机器人身份核对失败")
	}

	out, err = runToolIdentityCLI(ctx, cliPath, binding.Profile, binding.ConfigDir, "api", "GET", "/open-apis/bot/v3/info", "--as", "bot", "--format", "json")
	if err != nil {
		return err
	}
	// v1.0.96 unwraps the legacy API's bot object into data.
	var info toolBotCLIResponse
	var openID string
	if !info.isSuccess(out, "bot") || json.Unmarshal(info.Data["open_id"], &openID) != nil || openID != binding.BotOpenID {
		return errors.New("专用飞书工具 CLI 的服务端机器人身份核对失败")
	}

	out, err = runToolIdentityCLI(ctx, cliPath, binding.Profile, binding.ConfigDir, "im", "+chat-list", "--as", "bot", "--page-size", "1", "--format", "json")
	if err != nil {
		return err
	}
	var chatList toolBotCLIResponse
	var chats []json.RawMessage
	// v1.0.96 uses null for an empty chat slice. The presence of chats and
	// one successfully fetched page distinguish it from a missing payload.
	if !chatList.isSuccess(out, "bot") || chatList.Meta.Pagination.Pages != 1 ||
		json.Unmarshal(chatList.Data["chats"], &chats) != nil {
		return errors.New("专用飞书工具 CLI 的机器人群列表能力核验失败")
	}
	return nil
}

type toolBotCLIResponse struct {
	OK       bool                       `json:"ok"`
	Identity string                     `json:"identity"`
	DryRun   bool                       `json:"dry_run"`
	Data     map[string]json.RawMessage `json:"data"`
	Meta     struct {
		Pagination struct {
			Pages     int    `json:"pages"`
			Items     int    `json:"items"`
			Complete  bool   `json:"complete"`
			NextToken string `json:"next_token"`
		} `json:"pagination"`
	} `json:"meta"`
	Notice             json.RawMessage `json:"_notice"`
	ContentSafetyAlert json.RawMessage `json:"_content_safety_alert"`
}

func (response *toolBotCLIResponse) isSuccess(out []byte, identity string) bool {
	return decodeConfigObject(out, response) == nil && response.OK && response.Identity == identity && !response.DryRun
}

func runToolIdentityCLI(ctx context.Context, cliPath, profile, configDir string, args ...string) ([]byte, error) {
	_, searchPath := userGroupsCLIPaths()
	cmd := exec.CommandContext(ctx, cliPath, append([]string{"--profile", profile}, args...)...)
	cmd.Dir = configDir
	cmd.Env = userGroupsCLIEnv(os.Environ(), searchPath, configDir)
	cmd.WaitDelay = time.Second
	var stdout, stderr userGroupsOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, errors.New("专用飞书工具 CLI 核验超时或已取消")
	}
	if stdout.truncated || stderr.truncated {
		return nil, errors.New("专用飞书工具 CLI 核验输出超过限制")
	}
	var failure struct {
		OK *bool `json:"ok"`
	}
	if err != nil || (json.Unmarshal(stderr.Bytes(), &failure) == nil && failure.OK != nil && !*failure.OK) {
		return nil, errors.New("专用飞书工具 CLI 核验命令失败")
	}
	return stdout.Bytes(), nil
}
