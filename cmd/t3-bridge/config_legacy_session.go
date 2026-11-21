package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

type legacySessionRequest struct {
	LegacyConfig, LegacyProfile, AppID, ThreadID, ChatID, UserID, RouteID string
	ChatType                                                              string `json:",omitempty"`
	TopicID                                                               string `json:",omitempty"`
}

type legacySessionInput struct {
	account      bridge.Config
	accountIndex int
	raw          []byte
	catalog      []byte
	mapping      bridge.LegacySessionMapping
	profile      string
	fingerprint  string
}

type legacySessionInspection struct {
	AppID         string                          `json:"appId"`
	LegacyProfile string                          `json:"legacyProfile"`
	ChatID        string                          `json:"chatId,omitempty"`
	Candidates    []bridge.LegacySessionCandidate `json:"candidates,omitzero"`
	Scopes        []bridge.LegacySessionScope     `json:"scopes,omitzero"`
	ChatType      string                          `json:"chatType,omitempty"`
	TopicID       string                          `json:"topicId,omitempty"`
}

type legacySessionPreview struct {
	bridge.LegacySessionImportResult
	Review  string                       `json:"review"`
	T3Check *bridge.LegacySessionT3Check `json:"t3Check,omitempty"`
}

var errLegacySessionChanged = errors.New("旧会话来源或目标已变化，请重新预览")

func prepareLegacySession(source webSource, request legacySessionRequest) (legacySessionInput, error) {
	var input legacySessionInput
	if !filepath.IsAbs(source.Path) || !filepath.IsAbs(request.LegacyConfig) {
		return input, errors.New("请选择已保存配置并填写旧配置的绝对路径")
	}
	raw, err := bridge.ReadRegularConfigJSON(source.Path)
	if err != nil {
		return input, errors.New("无法读取已保存配置")
	}
	revision := configRevision(raw)
	if source.Revision != "" && source.Revision != revision {
		return input, errLegacySessionChanged
	}
	config, err := bridge.ParseConfig(raw)
	if err != nil {
		return input, err
	}
	for index, account := range config.Accounts() {
		if request.AppID == "" || account.AppID == request.AppID {
			input.account = account
			input.accountIndex = index
			break
		}
	}
	if input.account.AppID == "" {
		return input, errors.New("目标 App 未出现在已保存配置中")
	}
	old, err := bridge.ReadRegularConfigJSON(request.LegacyConfig)
	if err != nil {
		return input, errors.New("无法读取旧配置")
	}
	input.profile, err = legacySessionProfile(old, request.LegacyProfile, input.account)
	if err != nil {
		return input, err
	}
	root, err := filepath.EvalSymlinks(filepath.Dir(request.LegacyConfig))
	if err != nil {
		return input, errors.New("无法定位旧 profile 目录")
	}
	catalogPath := filepath.Join(root, "profiles", input.profile, "sessions.json.catalog.json")
	resolved, err := filepath.EvalSymlinks(catalogPath)
	if err != nil || resolved != catalogPath {
		return input, errors.New("无法确认旧目录文件属于已选 profile")
	}
	input.catalog, err = bridge.ReadPrivateConfigJSON(catalogPath)
	if err != nil {
		return input, errors.New("无法读取旧 profile 的私有会话目录 JSON")
	}
	input.mapping = bridge.LegacySessionMapping{
		ThreadID: request.ThreadID, UserID: request.UserID, ChatID: request.ChatID, RouteID: request.RouteID,
		ChatType: request.ChatType, TopicID: request.TopicID,
	}
	identity, _ := json.Marshal([]any{source.Path, revision, request, configRevision(old), configRevision(input.catalog)})
	input.fingerprint = configRevision(identity)
	input.raw = raw
	return input, nil
}

func legacySessionProfile(raw []byte, name string, account bridge.Config) (string, error) {
	tenant := account.Tenant
	if tenant == "" {
		tenant = "feishu"
	}
	matches := func(profile legacyImportSource) error {
		var agent string
		if profile.singleAccount || json.Unmarshal(profile.fields["agentKind"], &agent) != nil || agent != "t3" {
			return errors.New("仅支持旧桥的 T3 profile")
		}
		app, err := profile.app()
		if err != nil {
			return err
		}
		id, brand, err := profile.appIdentity(app)
		if err != nil {
			return err
		}
		if id != account.AppID || brand != tenant {
			return errors.New("旧 profile 必须对应目标 App 和租户")
		}
		return nil
	}
	if name != "" {
		profile, err := selectLegacyImportSource(raw, name)
		if err != nil {
			return "", err
		}
		if err := matches(profile); err != nil {
			return "", err
		}
	} else {
		var root, profiles legacyImportObject
		var version int
		if !utf8.Valid(raw) || json.Unmarshal(raw, &root) != nil || json.Unmarshal(root["schemaVersion"], &version) != nil ||
			version != 2 || json.Unmarshal(root["profiles"], &profiles) != nil || profiles == nil {
			return "", errors.New("旧配置需包含 V2 T3 profile")
		}
		for candidate := range profiles {
			profile, err := selectLegacyNamedProfile(profiles, candidate)
			if err != nil || matches(profile) != nil {
				continue
			}
			if name != "" || candidate == "" {
				return "", errors.New("无法唯一确认旧 T3 profile，请填写旧 profile 名称")
			}
			name = candidate
		}
		if name == "" {
			return "", errors.New("没有与目标 App 和租户匹配的旧 T3 profile")
		}
	}
	if !filepath.IsLocal(name) || filepath.Base(name) != name || name == "." {
		return "", errors.New("旧 profile 名称无法用于查找会话目录")
	}
	return name, nil
}

func legacySessionPlanID(result bridge.LegacySessionImportResult) string {
	raw, _ := json.Marshal(result)
	return configRevision(raw)
}

func inspectLegacySession(source webSource, request legacySessionRequest) (legacySessionInspection, error) {
	input, err := prepareLegacySession(source, request)
	if err != nil {
		return legacySessionInspection{}, err
	}
	result := legacySessionInspection{AppID: input.account.AppID, LegacyProfile: input.profile, ChatID: request.ChatID,
		ChatType: request.ChatType, TopicID: request.TopicID}
	if request.ChatID == "" {
		if request.TopicID != "" || request.ChatType != "" && request.ChatType != "p2p" && request.ChatType != "group" {
			return legacySessionInspection{}, errors.New("请为话题指定聊天 ID，并选择有效的聊天类型")
		}
		result.Scopes, err = bridge.InspectLegacySessionScopes(input.catalog)
	} else {
		result.Candidates, err = bridge.InspectLegacySessionScope(input.catalog, input.mapping)
	}
	return result, err
}

func previewLegacySession(source webSource, request legacySessionRequest) (bridge.LegacySessionImportResult, string, error) {
	input, err := prepareLegacySession(source, request)
	if err != nil {
		return bridge.LegacySessionImportResult{}, "", err
	}
	result, err := bridge.ImportLegacySession(input.account, input.catalog, input.mapping, nil)
	if err != nil {
		return bridge.LegacySessionImportResult{}, "", err
	}
	return result, input.fingerprint + "." + legacySessionPlanID(result), nil
}

func checkLegacySessionPreview(ctx context.Context, source webSource, request legacySessionRequest) (legacySessionPreview, error) {
	input, err := prepareLegacySession(source, request)
	if err != nil {
		return legacySessionPreview{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	result, check, err := bridge.CheckLegacySession(ctx, input.account, input.catalog, input.mapping)
	if err != nil {
		return legacySessionPreview{}, err
	}
	preview := legacySessionPreview{LegacySessionImportResult: result, T3Check: &check}
	if check.Status == "matched" {
		preview.Review = input.fingerprint + "." + legacySessionPlanID(result)
	}
	return preview, nil
}

func previewVerifiedLegacySession(ctx context.Context, source webSource, request legacySessionRequest) (bridge.LegacySessionImportResult, string, []string, error) {
	preview, err := checkLegacySessionPreview(ctx, source, request)
	if err != nil {
		return bridge.LegacySessionImportResult{}, "", nil, err
	}
	check := preview.T3Check
	lines := []string{check.Message}
	if a := check.Actual; a != nil {
		options, _ := json.Marshal(a.Options)
		lines = append(lines,
			"T3 项目 · "+a.ProjectID,
			"T3 项目目录 · "+a.ProjectPath,
			"Provider 实例 · "+a.ProviderID,
			"模型 · "+a.Model,
			"模型选项 · "+string(options),
			"运行模式 · "+a.RuntimeMode,
			"交互模式 · "+a.InteractionMode,
		)
	}
	for _, difference := range check.Differences {
		lines = append(lines, fmt.Sprintf("%s · 路由 %s · 原线程 %s", difference.Field, difference.Route, difference.Thread))
	}
	return preview.LegacySessionImportResult, preview.Review, lines, nil
}

func prepareLegacySessionRoute(ctx context.Context, source webSource, request legacySessionRequest) ([]byte, error) {
	input, err := prepareLegacySession(source, request)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	target, err := bridge.PrepareLegacySessionTarget(ctx, input.account, input.catalog, input.mapping)
	if err != nil {
		return nil, err
	}
	latest, err := prepareLegacySession(source, request)
	if err != nil || latest.fingerprint != input.fingerprint {
		return nil, errLegacySessionChanged
	}
	prefix := []string{"routes", input.mapping.RouteID}
	if input.accountIndex > 0 {
		prefix = []string{"additionalAccounts", strconv.Itoa(input.accountIndex - 1), "routes", input.mapping.RouteID}
	}
	edits, err := json.Marshal([]configTUIEdit{
		{Path: append(prefix, "driver"), Value: target.Driver},
		{Path: append(prefix, "providerId"), Value: target.ProviderID},
		{Path: append(prefix, "model"), Value: target.Model},
		{Path: append(prefix, "options"), Value: target.Options},
		{Path: append(prefix, "runtimeMode"), Value: target.RuntimeMode},
		{Path: append(prefix, "interactionMode"), Value: target.InteractionMode},
	})
	if err != nil {
		return nil, err
	}
	return bridge.EditConfigJSON(input.raw, edits)
}

func applyLegacySession(source webSource, request legacySessionRequest, review string) (bridge.LegacySessionImportResult, error) {
	fingerprint, plan, ok := strings.Cut(review, ".")
	if !ok || len(fingerprint) != 64 || len(plan) != 64 || !filepath.IsAbs(source.Path) {
		return bridge.LegacySessionImportResult{}, errors.New("请先预览旧会话转接")
	}
	lock, err := lockConfigFile(source.Path)
	if err != nil {
		return bridge.LegacySessionImportResult{}, err
	}
	defer lock.Close()
	input, err := prepareLegacySession(source, request)
	if err != nil {
		return bridge.LegacySessionImportResult{}, err
	}
	if fingerprint != input.fingerprint {
		return bridge.LegacySessionImportResult{}, errLegacySessionChanged
	}
	return bridge.ImportLegacySession(input.account, input.catalog, input.mapping, func(result bridge.LegacySessionImportResult) error {
		if legacySessionPlanID(result) != plan {
			return errLegacySessionChanged
		}
		return nil
	})
}

func runConfigImportSession(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("t3-bridge config import-session", flag.ContinueOnError)
	flags.SetOutput(output)
	config := flags.String("config", "", "saved target JSON configuration")
	profile := flags.String("profile", "", "use a named target profile")
	list := flags.Bool("list", false, "list old chat/topic scopes, or list threads for an exact chat or topic (read-only)")
	checkT3 := flags.Bool("check-t3", false, "preview and check the original thread against the saved route (read-only)")
	prepareRoute := flags.Bool("prepare-route", false, "print a configuration draft using the verified original thread's execution settings (read-only)")
	review := flags.String("confirm", "", "apply a reviewed plan to the stopped receiver's state")
	var request legacySessionRequest
	flags.StringVar(&request.AppID, "app-id", "", "target App ID (defaults to the saved configuration's primary App)")
	flags.StringVar(&request.LegacyConfig, "legacy-config", "", "absolute path to old bridge config.json (defaults to the installed configuration)")
	flags.StringVar(&request.LegacyProfile, "legacy-profile", "", "old T3 profile name (defaults to the unique matching App and tenant)")
	flags.StringVar(&request.ChatID, "chat-id", "", "original chat ID (oc_…)")
	flags.StringVar(&request.ChatType, "chat-type", "", "p2p (default) or group")
	flags.StringVar(&request.TopicID, "topic-id", "", "exact original topic ID, with -chat-type group")
	flags.StringVar(&request.UserID, "user-id", "", "user Open ID with target route access (ou_…)")
	flags.StringVar(&request.ThreadID, "thread-id", "", "exact original T3 thread ID")
	flags.StringVar(&request.RouteID, "route", "", "configured destination conversation route")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("config import-session accepts flags only")
	}
	if *prepareRoute && (*list || *checkT3 || *review != "") {
		return errors.New("-prepare-route cannot be combined with -list, -check-t3 or -confirm")
	}
	if *checkT3 && (*list || *review != "") {
		return errors.New("-check-t3 applies only to preview and cannot be combined with -list or -confirm")
	}
	if *list && (*review != "" || request.ThreadID != "" || request.UserID != "" || request.RouteID != "") {
		return errors.New("-list cannot be combined with -confirm, -thread-id, -user-id or -route")
	}
	if request.LegacyConfig == "" {
		path, err := detectLegacyConfig()
		if err != nil {
			return fmt.Errorf("could not discover the old configuration; specify -legacy-config: %w", err)
		}
		request.LegacyConfig = path
	}
	path, err := resolveConfigPath(*config, *profile, defaultConfigPath)
	if err != nil {
		return err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return err
	}
	source := webSource{Path: path}
	if *prepareRoute {
		candidate, err := prepareLegacySessionRoute(context.Background(), source, request)
		if err != nil {
			return err
		}
		_, err = output.Write(candidate)
		return err
	}
	enc := json.NewEncoder(output)
	if *list {
		result, err := inspectLegacySession(source, request)
		if err != nil {
			return err
		}
		return enc.Encode(result)
	}
	if *review != "" {
		result, err := applyLegacySession(source, request, *review)
		if err != nil {
			return err
		}
		return enc.Encode(result)
	}
	if *checkT3 {
		preview, err := checkLegacySessionPreview(context.Background(), source, request)
		if err != nil {
			return err
		}
		return enc.Encode(preview)
	}
	result, token, err := previewLegacySession(source, request)
	if err != nil {
		return err
	}
	return enc.Encode(legacySessionPreview{LegacySessionImportResult: result, Review: token})
}
