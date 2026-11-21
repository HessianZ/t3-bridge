package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ToolUserBinding identifies the dedicated lark-cli user and its private profile.
type ToolUserBinding struct {
	AppID      string `json:"appId"`
	UserOpenID string `json:"userOpenId"`
	Tenant     string `json:"tenant"`
	Profile    string `json:"profile"`
	ConfigDir  string `json:"configDir"`
}

func verifyToolUserBinding(ctx context.Context, cliPath string, binding ToolUserBinding) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// In lark-cli v1.0.96, strict-mode without a value queries the effective policy.
	out, err := runToolIdentityCLI(ctx, cliPath, binding.Profile, binding.ConfigDir, "config", "strict-mode")
	if err != nil {
		return err
	}
	switch strings.TrimSpace(string(out)) {
	case fmt.Sprintf("strict-mode: user (source: profile %q)", binding.Profile),
		"strict-mode: user (source: global)", "strict-mode: user (source: credential provider)":
	default:
		return errors.New("专用飞书工具 CLI 未确认启用 user-only 策略")
	}
	return probeToolUserIdentity(ctx, cliPath, binding)
}

func probeToolUserIdentity(ctx context.Context, cliPath string, binding ToolUserBinding) error {
	out, err := runToolIdentityCLI(ctx, cliPath, binding.Profile, binding.ConfigDir, "whoami", "--as", "user")
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
		OnBehalfOf     struct {
			OpenID   string `json:"openId"`
			UserName string `json:"userName"`
		} `json:"onBehalfOf"`
		Hint string `json:"hint"`
	}
	// v1.0.96 marks an existing, refreshable token available. The API call
	// will refresh it through the CLI's normal token handling if needed.
	if decodeConfigObject(out, &who) != nil || who.AppID != binding.AppID ||
		who.Brand != binding.Tenant || who.Profile != binding.Profile ||
		who.Identity != "user" || who.IdentitySource != "flag" || !who.Available ||
		(who.TokenStatus != "ready" && who.TokenStatus != "needs_refresh") ||
		who.OnBehalfOf.OpenID == "" || who.OnBehalfOf.OpenID != binding.UserOpenID {
		return errors.New("专用飞书工具 CLI 的本地用户身份核对失败")
	}

	out, err = runToolIdentityCLI(ctx, cliPath, binding.Profile, binding.ConfigDir,
		"api", "GET", "/open-apis/authen/v1/user_info", "--as", "user", "--format", "json")
	if err != nil {
		return err
	}
	var info toolBotCLIResponse
	var openID string
	if !info.isSuccess(out, "user") || json.Unmarshal(info.Data["open_id"], &openID) != nil || openID != binding.UserOpenID {
		return errors.New("专用飞书工具 CLI 的服务端用户身份核对失败")
	}
	return nil
}
