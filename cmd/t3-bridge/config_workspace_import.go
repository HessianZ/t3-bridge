package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

type workspaceImportRequest struct {
	AppID, LegacyPath, MappingJSON string
}

type workspaceImportResult struct {
	bridge.WorkspaceImportResult
	LegacyPath string `json:"legacyPath"`
}

type workspaceImportInspection struct {
	AppID      string                        `json:"appId"`
	LegacyPath string                        `json:"legacyPath"`
	Aliases    []bridge.LegacyWorkspaceAlias `json:"aliases"`
	Invalid    int                           `json:"invalid"`
	Routes     []string                      `json:"routes"`
}

type workspaceImportDraftEntry struct {
	SourceKey string `json:"sourceKey"`
	UserID    string `json:"userId"`
	ChatID    string `json:"chatId"`
	ChatType  string `json:"chatType"`
	TopicID   string `json:"topicId,omitempty"`
	RouteID   string `json:"routeId"`
	ProjectID string `json:"projectId,omitempty"`
}

var errWorkspaceImportChanged = errors.New("配置、导入内容或目标状态已变化，请重新预览")

type workspaceImportInput struct {
	account     bridge.Config
	legacy      []byte
	mapping     []byte
	legacyPath  string
	revision    string
	fingerprint string
}

func detectLegacyWorkspaces(account bridge.Config) (string, error) {
	configPath, err := detectLegacyConfig()
	if err != nil {
		return "", errors.New("未找到已安装的旧配置，请填写旧 workspaces.json 路径")
	}
	raw, err := bridge.ReadRegularConfigJSON(configPath)
	if err != nil {
		return "", fmt.Errorf("旧配置文件: %w", err)
	}
	selected, err := legacyWorkspaceProfile(raw, account)
	if err != nil {
		return "", err
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(configPath))
	if err != nil {
		return "", errors.New("无法定位旧配置目录")
	}
	if selected != "" {
		dir = filepath.Join(dir, "profiles", selected)
	}
	path := filepath.Join(dir, "workspaces.json")
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return "", errors.New("无法确认旧工作区文件的位置，请填写旧 workspaces.json 路径")
	}
	return path, nil
}

func legacyWorkspaceProfile(raw []byte, account bridge.Config) (string, error) {
	var root, profiles legacyImportObject
	version := 1
	if json.Unmarshal(raw, &root) != nil || root == nil ||
		(root["schemaVersion"] != nil && json.Unmarshal(root["schemaVersion"], &version) != nil) {
		return "", errors.New("旧配置格式无效，请填写旧 workspaces.json 路径")
	}
	tenant := account.Tenant
	if tenant == "" {
		tenant = "feishu"
	}
	matches := func(profile legacyImportSource) bool {
		app, err := profile.app()
		if err != nil {
			return false
		}
		appID, sourceTenant, err := profile.appIdentity(app)
		return err == nil && appID == account.AppID && sourceTenant == tenant
	}
	const noMatch = "没有与目标应用和租户匹配的旧配置，请填写旧 workspaces.json 路径"
	if version == 1 {
		profile, err := selectLegacyImportSource(raw, "")
		if err == nil && matches(profile) {
			return "", nil
		}
		return "", errors.New(noMatch)
	}
	if version != 2 || json.Unmarshal(root["profiles"], &profiles) != nil || profiles == nil {
		return "", errors.New("旧配置格式无效，请填写旧 workspaces.json 路径")
	}
	var selected string
	for name := range profiles {
		profile, err := selectLegacyNamedProfile(profiles, name)
		if err != nil || !matches(profile) {
			continue
		}
		if selected != "" {
			return "", errors.New("目标应用和租户匹配多个旧 profile，请填写旧 workspaces.json 路径")
		}
		if !filepath.IsLocal(name) || filepath.Base(name) != name || name == "." {
			return "", errors.New("旧 profile 名称无法用于查找工作区，请填写旧 workspaces.json 路径")
		}
		selected = name
	}
	if selected == "" {
		return "", errors.New(noMatch)
	}
	return selected, nil
}

func readWorkspaceImportSource(source webSource, request workspaceImportRequest) (workspaceImportInput, error) {
	var input workspaceImportInput
	if source.Path == "" || source.Revision == "missing" {
		return input, errors.New("请选择已保存的目标配置")
	}
	if request.AppID == "" {
		return input, errors.New("请填写目标应用 ID")
	}
	if request.LegacyPath != "" && !filepath.IsAbs(request.LegacyPath) {
		return input, errors.New("请填写旧工作区 JSON 的绝对路径")
	}
	rawConfig, err := bridge.ReadRegularConfigJSON(source.Path)
	if err != nil {
		return input, errors.New("无法读取已保存的配置文件")
	}
	input.revision = configRevision(rawConfig)
	if source.Revision != "" && source.Revision != input.revision {
		return input, errors.New("目标配置已更新，请重新打开该配置后再预览")
	}
	config, err := bridge.ParseConfig(rawConfig)
	if err != nil {
		return input, fmt.Errorf("目标配置: %w", err)
	}
	for _, account := range config.Accounts() {
		if account.AppID == request.AppID {
			input.account = account
			break
		}
	}
	if input.account.AppID == "" {
		return input, errors.New("目标应用 ID 未出现在已保存配置中")
	}
	input.legacyPath = request.LegacyPath
	if input.legacyPath == "" {
		input.legacyPath, err = detectLegacyWorkspaces(input.account)
		if err != nil {
			return input, err
		}
	}
	input.legacy, err = bridge.ReadRegularConfigJSON(input.legacyPath)
	if err != nil {
		return input, fmt.Errorf("旧工作区文件: %w", err)
	}
	return input, nil
}

func inspectWorkspaceImport(source webSource, request workspaceImportRequest) (workspaceImportInspection, error) {
	input, err := readWorkspaceImportSource(source, request)
	if err != nil {
		return workspaceImportInspection{}, err
	}
	aliases, invalid, err := bridge.InspectLegacyWorkspaces(input.legacy)
	if err != nil {
		return workspaceImportInspection{}, err
	}
	routes := make([]string, 0, len(input.account.Routes))
	for route := range input.account.Routes {
		if route != input.account.ReviewRoute {
			routes = append(routes, route)
		}
	}
	slices.Sort(routes)
	return workspaceImportInspection{
		AppID: input.account.AppID, LegacyPath: input.legacyPath, Aliases: aliases, Invalid: invalid, Routes: routes,
	}, nil
}

func appendWorkspaceImportMapping(draft string, entry workspaceImportDraftEntry) (string, error) {
	if entry.SourceKey == "" || strings.TrimSpace(entry.UserID) == "" || strings.TrimSpace(entry.ChatID) == "" || strings.TrimSpace(entry.RouteID) == "" {
		return "", errors.New("请选择旧别名，填写目标用户、聊天和路由")
	}
	if entry.ChatType != "p2p" && entry.ChatType != "group" {
		return "", errors.New("聊天类型须为 p2p 或 group")
	}
	if len(draft) > bridge.MaxConfigBytes {
		return "", errors.New("映射 JSON 超过 1 MiB")
	}
	var entries []json.RawMessage
	if trimmed := strings.TrimSpace(draft); trimmed != "" &&
		(!strings.HasPrefix(trimmed, "[") || json.Unmarshal([]byte(trimmed), &entries) != nil) {
		return "", errors.New("请先修正映射草稿中的 JSON 数组")
	}
	raw, _ := json.Marshal(entry)
	result, err := json.MarshalIndent(append(entries, raw), "", "  ")
	if err != nil {
		return "", err
	}
	if len(result)+1 > bridge.MaxConfigBytes {
		return "", errors.New("映射 JSON 超过 1 MiB")
	}
	return string(result) + "\n", nil
}

func prepareWorkspaceImport(source webSource, request workspaceImportRequest) (workspaceImportInput, error) {
	if len(request.MappingJSON) == 0 || len(request.MappingJSON) > bridge.MaxConfigBytes {
		return workspaceImportInput{}, errors.New("请输入不超过 1 MiB 的映射 JSON")
	}
	input, err := readWorkspaceImportSource(source, request)
	if err != nil {
		return input, err
	}
	input.mapping = []byte(request.MappingJSON)
	// Encode the paths and hashes as separate values to bind review to their exact boundaries.
	identity, _ := json.Marshal([]string{source.Path, input.revision, request.AppID, input.legacyPath, configRevision(input.legacy), configRevision(input.mapping)})
	input.fingerprint = configRevision(identity)
	return input, nil
}

func workspaceImportPlanID(result bridge.WorkspaceImportResult) string {
	raw, _ := json.Marshal(result)
	return configRevision(raw)
}

func previewWorkspaceImport(source webSource, request workspaceImportRequest) (workspaceImportResult, string, error) {
	input, err := prepareWorkspaceImport(source, request)
	if err != nil {
		return workspaceImportResult{}, "", err
	}
	result, err := bridge.ImportLegacyWorkspaces(input.account, input.legacy, input.mapping, false)
	if err != nil {
		return workspaceImportResult{}, "", err
	}
	return workspaceImportResult{WorkspaceImportResult: result, LegacyPath: input.legacyPath}, input.fingerprint + "." + workspaceImportPlanID(result), nil
}

func applyWorkspaceImport(source webSource, request workspaceImportRequest, review string) (workspaceImportResult, error) {
	fingerprint, plan, ok := strings.Cut(review, ".")
	if !ok || len(fingerprint) != 64 || len(plan) != 64 {
		return workspaceImportResult{}, errors.New("请先预览工作区迁移")
	}
	if source.Path == "" || source.Revision == "missing" {
		return workspaceImportResult{}, errors.New("请选择已保存的目标配置")
	}
	// Serialize with other workbench writes to the selected configuration while committing its state.
	lock, err := lockConfigFile(source.Path)
	if err != nil {
		return workspaceImportResult{}, err
	}
	defer lock.Close()
	input, err := prepareWorkspaceImport(source, request)
	if err != nil {
		return workspaceImportResult{}, err
	}
	if fingerprint != input.fingerprint {
		return workspaceImportResult{}, errWorkspaceImportChanged
	}
	result, err := bridge.ApplyReviewedLegacyWorkspaces(input.account, input.legacy, input.mapping, func(result bridge.WorkspaceImportResult) error {
		if workspaceImportPlanID(result) != plan {
			return errWorkspaceImportChanged
		}
		return nil
	})
	if err != nil {
		return workspaceImportResult{}, err
	}
	return workspaceImportResult{WorkspaceImportResult: result, LegacyPath: input.legacyPath}, nil
}
