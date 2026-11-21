package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

type legacyImportEdit struct {
	Path  []string `json:"path"`
	Value any      `json:"value"`
}

type legacyConfigImport struct {
	SourcePath    string
	Profile       string
	StateDir      string
	AppSecretFile string
	ReuseT3       bool
}

func runConfigImportLegacy(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("t3-bridge config import-legacy", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	source := flags.String("legacy-config", "", "legacy JSON file (defaults to the installed lark-channel-bridge configuration)")
	profile := flags.String("profile", "", "old profile name (V2 defaults to activeProfile, single-account must omit)")
	base := flags.String("config", "", "t3-bridge JSON base (defaults to the single-route template)")
	state := flags.String("state-dir", "", "absolute path for a new state directory")
	secret := flags.String("app-secret-file", "", "absolute path for the private App Secret file")
	legacyBridge := flags.String("legacy-bridge", "", "absolute path to the old executable to copy the selected App Secret")
	copyAppSecret := flags.Bool("copy-app-secret", false, "copy the literal App Secret from a single-account JSON into its private file")
	reuseT3 := flags.Bool("reuse-t3", false, "reuse the old T3 profile's MCP connection and OAuth file reference")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(output)
			flags.Usage()
			return nil
		}
		return errors.New("invalid import-legacy flags; use -help")
	}
	if flags.NArg() != 0 {
		return errors.New("import-legacy takes no positional arguments")
	}
	if *legacyBridge != "" && !filepath.IsAbs(*legacyBridge) {
		return errors.New("-legacy-bridge requires the absolute path to the old executable")
	}
	if *legacyBridge != "" && *copyAppSecret {
		return errors.New("choose either -legacy-bridge or -copy-app-secret")
	}
	request := legacyConfigImport{SourcePath: *source, Profile: *profile, StateDir: *state, AppSecretFile: *secret, ReuseT3: *reuseT3}
	var baseJSON []byte
	var err error
	if *base == "" {
		baseJSON, err = loadConfigTemplate("generic")
		if err != nil {
			return err
		}
	} else {
		baseJSON, err = bridge.ReadConfigJSON(*base)
		if err != nil {
			return fmt.Errorf("-config: %w", err)
		}
	}
	readSource := bridge.ReadConfigJSON
	if request.SourcePath == "" {
		request.SourcePath, err = detectLegacyConfig()
		if err != nil {
			return fmt.Errorf("could not discover the old configuration; specify -legacy-config: %w", err)
		}
		readSource = bridge.ReadRegularConfigJSON
	}
	sourceJSON, err := readSource(request.SourcePath)
	if err != nil {
		return fmt.Errorf("-legacy-config: %w", err)
	}
	candidate, err := request.candidate(baseJSON, sourceJSON)
	if err != nil {
		return err
	}
	if *legacyBridge != "" || *copyAppSecret {
		plan, err := request.credentialPlan(sourceJSON, candidate)
		if err != nil {
			return err
		}
		if *copyAppSecret != plan.Inline {
			return errors.New("use -copy-app-secret for a single-account literal secret or -legacy-bridge for a V2 profile")
		}
		if err := plan.copy(context.Background(), *legacyBridge, candidate); err != nil {
			return err
		}
	}
	_, err = output.Write(candidate)
	return err
}

func (request legacyConfigImport) apply(base []byte) ([]byte, error) {
	source, err := request.readSource()
	if err != nil {
		return nil, err
	}
	return request.candidate(base, source)
}

func (request legacyConfigImport) preview(base []byte) ([]byte, legacyCredentialPlan, error) {
	source, err := request.readSource()
	if err != nil {
		return nil, legacyCredentialPlan{}, err
	}
	candidate, err := request.candidate(base, source)
	if err != nil {
		return nil, legacyCredentialPlan{}, err
	}
	plan, err := request.credentialPlan(source, candidate)
	return candidate, plan, err
}

func (request legacyConfigImport) readSource() ([]byte, error) {
	if !filepath.IsAbs(request.SourcePath) {
		return nil, errors.New("旧配置必须填写绝对 JSON 文件路径")
	}
	source, err := bridge.ReadRegularConfigJSON(request.SourcePath)
	if err != nil {
		return nil, fmt.Errorf("旧配置文件: %w", err)
	}
	return source, nil
}

func (request legacyConfigImport) candidate(baseJSON, sourceJSON []byte) ([]byte, error) {
	if !filepath.IsAbs(request.AppSecretFile) {
		return nil, errors.New("-app-secret-file requires an explicit absolute path")
	}
	if !filepath.IsAbs(request.StateDir) {
		return nil, errors.New("-state-dir requires an explicit absolute path")
	}
	stateDir := filepath.Clean(request.StateDir)
	if _, err := os.Lstat(stateDir); !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("-state-dir must name a new path that does not exist")
	}
	config, err := bridge.ParseConfig(baseJSON)
	if err != nil {
		return nil, fmt.Errorf("-config: %w", err)
	}
	for _, account := range config.Accounts() {
		if filepath.Clean(account.StateDir) == stateDir {
			return nil, errors.New("-state-dir must differ from every stateDir in -config")
		}
	}
	var templateRoute string
	for _, id := range slices.Sorted(maps.Keys(config.Routes)) {
		route := config.Routes[id]
		if config.AppID == "REPLACE_APP_ID" && len(config.Routes) == 1 && id == config.DefaultRoute &&
			route.Kind == "" && route.RuntimeMode == "approval-required" && route.ToolContext == nil && len(route.Checks) == 0 &&
			config.CommentRoute == "" && config.MeetingRoute == "" && len(config.EventRoutes) == 0 &&
			slices.Equal(route.Users, []string{"REPLACE_OPEN_ID"}) {
			templateRoute = id
			continue
		}
		for _, user := range route.Users {
			if !validLegacyImportID(user, "ou_") {
				return nil, fmt.Errorf("-config routes[%q].users requires explicit Feishu Open IDs", id)
			}
		}
	}

	source, err := selectLegacyImportSource(sourceJSON, request.Profile)
	if err != nil {
		return nil, err
	}
	edits, err := source.profileEdits(config.Users, templateRoute)
	if err != nil {
		return nil, err
	}
	if request.ReuseT3 {
		t3Edits, err := source.t3ConnectionEdits(config, baseJSON)
		if err != nil {
			return nil, err
		}
		edits = append(edits, t3Edits...)
	}
	edits = append(edits,
		legacyImportEdit{[]string{"appSecretFile"}, request.AppSecretFile},
		legacyImportEdit{[]string{"stateDir"}, stateDir},
		legacyImportEdit{[]string{"receiverAuthorized"}, false},
		legacyImportEdit{[]string{"directChats"}, []string{}},
	)
	for i, account := range config.AdditionalAccounts {
		if account.ReceiverAuthorized {
			edits = append(edits, legacyImportEdit{[]string{"additionalAccounts", strconv.Itoa(i), "receiverAuthorized"}, false})
		}
	}
	changes, err := json.Marshal(edits)
	if err != nil {
		return nil, err
	}
	candidate, err := bridge.EditConfigJSON(baseJSON, changes)
	if err != nil {
		return nil, fmt.Errorf("candidate config: %w", err)
	}
	return candidate, nil
}

type legacyImportObject map[string]json.RawMessage

type legacyImportSource struct {
	fields        legacyImportObject
	name          string
	singleAccount bool
}

func selectLegacyImportSource(raw []byte, name string) (legacyImportSource, error) {
	var root legacyImportObject
	if !utf8.Valid(raw) || json.Unmarshal(raw, &root) != nil || root == nil {
		return legacyImportSource{}, errors.New("-legacy-config must contain a JSON object")
	}
	version := 1
	if value, exists := root["schemaVersion"]; exists {
		version = 0
		if json.Unmarshal(value, &version) != nil || (version != 1 && version != 2) {
			return legacyImportSource{}, errors.New("legacy schemaVersion must be 1 or 2")
		}
	}
	if version == 1 {
		if root["profiles"] != nil || root["activeProfile"] != nil {
			return legacyImportSource{}, errors.New("legacy profiles require schemaVersion 2")
		}
		if name != "" {
			return legacyImportSource{}, errors.New("旧单账号配置没有 profile，请留空旧配置名称")
		}
		return legacyImportSource{fields: root, singleAccount: true}, nil
	}
	if name == "" {
		if json.Unmarshal(root["activeProfile"], &name) != nil || strings.TrimSpace(name) == "" {
			return legacyImportSource{}, errors.New("legacy activeProfile must name a profile, or specify -profile")
		}
	}
	var profiles legacyImportObject
	if json.Unmarshal(root["profiles"], &profiles) != nil || profiles == nil {
		return legacyImportSource{}, errors.New("legacy profiles must be an object")
	}
	return selectLegacyNamedProfile(profiles, name)
}

func selectLegacyNamedProfile(profiles legacyImportObject, name string) (legacyImportSource, error) {
	var profile legacyImportObject
	if json.Unmarshal(profiles[name], &profile) != nil || profile == nil {
		return legacyImportSource{}, errors.New("legacy profiles must contain the selected profile as an object")
	}
	var profileVersion int
	if json.Unmarshal(profile["schemaVersion"], &profileVersion) != nil || profileVersion != 2 {
		return legacyImportSource{}, errors.New("legacy profile.schemaVersion must be 2")
	}
	return legacyImportSource{fields: profile, name: name}, nil
}

func (source legacyImportSource) app() (legacyImportObject, error) {
	var accounts, app legacyImportObject
	if raw := source.fields["accounts"]; raw != nil && json.Unmarshal(raw, &accounts) != nil {
		return nil, errors.New("legacy accounts must be an object")
	}
	raw := accounts["app"]
	if source.singleAccount && (raw == nil || bytes.Equal(raw, []byte("null"))) {
		raw = source.fields["app"]
	}
	if json.Unmarshal(raw, &app) != nil || app == nil {
		return nil, errors.New("legacy app must be an object")
	}
	return app, nil
}

func (source legacyImportSource) appIdentity(app legacyImportObject) (string, string, error) {
	var appID string
	if json.Unmarshal(app["id"], &appID) != nil || !validLegacyImportID(appID, "cli_") {
		return "", "", errors.New("legacy app.id must be a Feishu App ID")
	}
	tenant := "feishu"
	if source.singleAccount {
		tenant = ""
	}
	if value := app["tenant"]; value != nil || source.singleAccount {
		if json.Unmarshal(value, &tenant) != nil || (tenant != "feishu" && tenant != "lark") {
			return "", "", errors.New("legacy app.tenant must be feishu or lark")
		}
	}
	return appID, tenant, nil
}

func (source legacyImportSource) t3ConnectionEdits(config bridge.Config, baseJSON []byte) ([]legacyImportEdit, error) {
	const placeholder = "REPLACE_ENVIRONMENT_ID"
	route := config.DefaultRoute
	if len(config.Routes) != 1 || config.MCP.EnvironmentID != placeholder || config.Routes[route].EnvironmentID != placeholder {
		return nil, errors.New("仅可在环境尚未选定的单默认路由草稿中沿用旧 T3 连接")
	}
	var kind string
	if source.singleAccount || json.Unmarshal(source.fields["agentKind"], &kind) != nil || kind != "t3" {
		return nil, errors.New("沿用旧 T3 连接需要 V2 T3 profile")
	}
	var old bridge.MCPConfig
	if json.Unmarshal(source.fields["t3"], &old) != nil ||
		strings.TrimSpace(old.EnvironmentID) == "" || old.EnvironmentID == placeholder ||
		!strings.EqualFold(filepath.Ext(old.TokenFile), ".json") {
		return nil, errors.New("旧 T3 profile 需要明确的环境 ID 和 JSON OAuth 文件路径")
	}
	if old.Endpoint != "" {
		// The old adapter uses an explicit endpoint before consulting runtimeFile.
		old.RuntimeFile = ""
	}
	edits := []legacyImportEdit{
		{[]string{"mcp"}, old},
		{[]string{"routes", route, "environmentId"}, old.EnvironmentID},
	}
	if old.LocalEnvironment(old.EnvironmentID) {
		// Keep an explicit draft value, including an opt-out of local caching.
		var draft struct {
			Routes map[string]map[string]json.RawMessage `json:"routes"`
		}
		if err := json.Unmarshal(baseJSON, &draft); err != nil {
			return nil, errors.New("-config: cannot read the default route fields")
		}
		const field = "cacheFollowupAttachments"
		if _, present := draft.Routes[route][field]; !present {
			edits = append(edits, legacyImportEdit{[]string{"routes", route, field}, true})
		}
	}
	target := config.Routes[route]
	if target.ProjectID == "REPLACE_PROJECT_ID" && target.ProjectPath == "" {
		var workspace struct {
			Default string `json:"default"`
		}
		if json.Unmarshal(source.fields["workspaces"], &workspace) == nil {
			dir := strings.TrimSpace(workspace.Default)
			if bridge.IsAbsoluteWorkspacePath(dir) {
				edits = append(edits, legacyImportEdit{[]string{"routes", route, "projectPath"}, dir})
			}
		}
	}
	return edits, nil
}

func (source legacyImportSource) profileEdits(draftUsers []string, templateRoute string) ([]legacyImportEdit, error) {
	app, err := source.app()
	if err != nil {
		return nil, err
	}
	appID, tenant, err := source.appIdentity(app)
	if err != nil {
		return nil, err
	}
	edits := []legacyImportEdit{{[]string{"appId"}, appID}, {[]string{"tenant"}, tenant}}

	var preferences legacyProfilePreferences
	if value := source.fields["preferences"]; value != nil && json.Unmarshal(value, &preferences) != nil {
		return nil, errors.New("legacy profile.preferences must contain valid preference values")
	}
	accessRaw, accessPath := source.fields["access"], "profile.access"
	if source.singleAccount || len(accessRaw) == 0 || bytes.Equal(accessRaw, []byte("null")) {
		// The legacy profile normalizer falls back to preferences.access.
		accessRaw, accessPath = preferences.Access, "profile.preferences.access"
		if source.singleAccount {
			accessPath = "preferences.access"
		}
	}
	var access legacyImportObject
	if (accessRaw != nil && json.Unmarshal(accessRaw, &access) != nil) || (!source.singleAccount && access == nil) {
		return nil, fmt.Errorf("legacy %s must be an object with explicit allowedUsers", accessPath)
	}
	var allowedChats []string
	for _, field := range []struct{ source, target, prefix string }{
		{"allowedUsers", "users", "ou_"}, {"admins", "admins", "ou_"}, {"allowedChats", "chats", "oc_"},
	} {
		ids := []string{}
		if rawIDs, ok := access[field.source]; ok {
			if json.Unmarshal(rawIDs, &ids) != nil {
				return nil, fmt.Errorf("legacy %s.%s must be an array of IDs", accessPath, field.source)
			}
		} else if source.singleAccount && field.target == "users" {
			// Early single-account files may only contain the app. The operator
			// must supply an explicit account ACL in the target draft.
			if len(draftUsers) == 0 || slices.ContainsFunc(draftUsers, func(user string) bool {
				return !validLegacyImportID(user, "ou_")
			}) {
				return nil, errors.New("旧配置没有用户名单，请在候选主账号的 users 中填写明确的飞书 Open ID")
			}
			ids = draftUsers
		}
		if field.target == "users" && len(ids) == 0 {
			return nil, fmt.Errorf("legacy %s.%s must list at least one explicit Open ID", accessPath, field.source)
		}
		for _, id := range ids {
			if !validLegacyImportID(id, field.prefix) {
				return nil, fmt.Errorf("legacy %s.%s must contain only %s IDs", accessPath, field.source, field.prefix)
			}
		}
		if ids == nil {
			ids = []string{}
		}
		if field.target == "chats" {
			allowedChats = ids
		}
		edits = append(edits, legacyImportEdit{[]string{field.target}, ids})
		if field.target == "users" && templateRoute != "" {
			edits = append(edits, legacyImportEdit{[]string{"routes", templateRoute, "users"}, ids})
		}
	}
	initial, err := legacyInitialRuntime(source.singleAccount, preferences, access, allowedChats)
	if err != nil {
		return nil, err
	}
	return append(edits, legacyImportEdit{[]string{"initialRuntime"}, initial}), nil
}

var legacyImportIDSuffix = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func validLegacyImportID(value, prefix string) bool {
	suffix, ok := strings.CutPrefix(value, prefix)
	return ok && legacyImportIDSuffix.MatchString(suffix)
}
