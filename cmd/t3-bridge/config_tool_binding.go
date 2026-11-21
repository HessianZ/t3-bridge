package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

type toolBindingDraft struct {
	Mode       string `json:"mode"`
	CLIPath    string `json:"cliPath"`
	AppID      string `json:"appId"`
	Tenant     string `json:"tenant"`
	Profile    string `json:"profile"`
	ConfigDir  string `json:"configDir"`
	BotOpenID  string `json:"botOpenId"`
	UserOpenID string `json:"userOpenId"`
}

func toolBindingFields(current *bridge.ToolContext, appID, tenant string) toolBindingDraft {
	if tenant == "" {
		tenant = "feishu"
	}
	draft := toolBindingDraft{AppID: appID, Tenant: tenant}
	if current == nil {
		return draft
	}
	draft.CLIPath = current.CLIPath
	if bot := current.BotBinding; bot != nil {
		draft.Mode = current.Identity
		draft.AppID, draft.Tenant, draft.Profile, draft.ConfigDir = bot.AppID, bot.Tenant, bot.Profile, bot.ConfigDir
		draft.BotOpenID = bot.BotOpenID
	}
	if user := current.UserBinding; user != nil {
		draft.Mode = current.Identity
		draft.AppID, draft.Tenant, draft.Profile, draft.ConfigDir = user.AppID, user.Tenant, user.Profile, user.ConfigDir
		draft.UserOpenID = user.UserOpenID
	}
	return draft
}

func (d toolBindingDraft) toolContext(environmentID string, current *bridge.ToolContext) (*bridge.ToolContext, error) {
	if d.Mode == "off" {
		return nil, nil
	}
	if d.Mode != "bot" && d.Mode != "user" && d.Mode != "user-default" {
		return nil, errors.New("请选择 bot、user、user-default 或关闭")
	}
	if strings.TrimSpace(environmentID) == "" {
		return nil, errors.New("请先填写路由的 T3 环境 ID")
	}
	tool := &bridge.ToolContext{CLIPath: d.CLIPath, EnvironmentID: environmentID, Identity: d.Mode}
	if current != nil {
		tool.Version, tool.Verified = current.Version, current.Verified
	}
	app, tenant := strings.TrimSpace(d.AppID), strings.TrimSpace(d.Tenant)
	profile, dir := d.Profile, d.ConfigDir
	if d.Mode == "bot" || d.Mode == "user-default" {
		tool.BotBinding = &bridge.ToolBotBinding{
			AppID: app, Tenant: tenant, Profile: profile, ConfigDir: dir, BotOpenID: strings.TrimSpace(d.BotOpenID),
		}
	}
	if d.Mode == "user" || d.Mode == "user-default" {
		tool.UserBinding = &bridge.ToolUserBinding{
			AppID: app, Tenant: tenant, Profile: profile, ConfigDir: dir, UserOpenID: strings.TrimSpace(d.UserOpenID),
		}
	}
	if err := tool.Validate(environmentID); err != nil {
		return nil, err
	}
	return tool, nil
}

func configToolBindingDraftHandler(w http.ResponseWriter, r *http.Request) {
	if !runtimeRequestOriginAllowed(r) {
		http.Error(w, "invalid origin", http.StatusForbidden)
		return
	}
	var request struct {
		Draft         toolBindingDraft    `json:"draft"`
		EnvironmentID string              `json:"environmentId"`
		Current       *bridge.ToolContext `json:"current"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	d.DisallowUnknownFields()
	if r.URL.RawQuery != "" || d.Decode(&request) != nil || d.Decode(new(json.RawMessage)) != io.EOF {
		http.Error(w, "无法读取工具身份草稿", http.StatusBadRequest)
		return
	}
	tool, err := request.Draft.toolContext(request.EnvironmentID, request.Current)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(struct {
		ToolContext *bridge.ToolContext `json:"toolContext"`
	}{tool})
}

func runConfigToolBinding(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("t3-bridge config tool-binding", flag.ContinueOnError)
	flags.SetOutput(output)
	path := flags.String("config", "", "JSON configuration file")
	profile := flags.String("profile", "", "use the named configuration profile")
	app := flags.String("app-id", "", "select the receiving App (defaults to the primary App)")
	route := flags.String("route", "", "route ID to edit")
	var draft toolBindingDraft
	flags.StringVar(&draft.Mode, "mode", "", "bot, user, user-default, or off")
	flags.StringVar(&draft.CLIPath, "cli-path", "", "absolute lark-cli executable path")
	flags.StringVar(&draft.AppID, "tool-app-id", "", "tool App ID (defaults to the receiving App)")
	flags.StringVar(&draft.Tenant, "tenant", "", "feishu or lark (defaults to the receiving tenant)")
	flags.StringVar(&draft.Profile, "cli-profile", "", "lark-cli profile name")
	flags.StringVar(&draft.ConfigDir, "cli-config-dir", "", "absolute lark-cli configuration directory")
	flags.StringVar(&draft.BotOpenID, "bot-open-id", "", "expected bot Open ID")
	flags.StringVar(&draft.UserOpenID, "user-open-id", "", "expected user Open ID")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || *route == "" || draft.Mode == "" {
		return errors.New("tool-binding requires -route and -mode")
	}
	resolved, err := resolveConfigPath(*path, *profile, defaultConfigPath)
	if err != nil {
		return err
	}
	raw, err := bridge.ReadConfigJSON(resolved)
	if err != nil {
		return err
	}
	config, err := bridge.ParseConfig(raw)
	if err != nil && config.Routes == nil {
		return err
	}
	for i, account := range config.Accounts() {
		if *app != "" && *app != account.AppID {
			continue
		}
		target, ok := account.Routes[*route]
		if !ok {
			return errors.New("selected route is not configured")
		}
		if draft.AppID == "" {
			draft.AppID = account.AppID
		}
		if draft.Tenant == "" {
			draft.Tenant = account.Tenant
			if draft.Tenant == "" {
				draft.Tenant = "feishu"
			}
		}
		tool, err := draft.toolContext(target.EnvironmentID, target.ToolContext)
		if err != nil {
			return err
		}
		keys := []string{"routes", *route, "toolContext"}
		if i != 0 {
			keys = append([]string{"additionalAccounts", strconv.Itoa(i - 1)}, keys...)
		}
		edit := configTUIEdit{Path: keys, Delete: tool == nil}
		if tool != nil {
			edit.Value = tool
		}
		edits, err := json.Marshal([]configTUIEdit{edit})
		if err != nil {
			return err
		}
		candidate, err := bridge.EditConfigJSON(raw, edits)
		if err != nil {
			return err
		}
		_, err = output.Write(candidate)
		return err
	}
	return errors.New("app-id is not configured")
}
