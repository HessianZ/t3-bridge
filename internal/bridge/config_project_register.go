package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"unicode"
)

var errConfigProjectRegistrationUnknown = errors.New("T3 项目登记结果未知，请先在 T3 核对项目列表、目标目录和授权，再重新执行")

// WithProjectSetupToken selects an OAuth credential for a single registration request.
func (c MCPConfig) WithProjectSetupToken(path string) (MCPConfig, error) {
	if path == "" {
		return c, nil
	}
	if !filepath.IsAbs(path) || !strings.EqualFold(filepath.Ext(path), ".json") {
		return MCPConfig{}, errors.New("请填写私有 T3 OAuth JSON 文件的绝对路径")
	}
	c.TokenFile = path
	c.projectSetup = true
	return c, nil
}

// RegisterConfigProject resolves an existing project or registers an existing
// directory on the selected T3 environment, then reads the project back.
func RegisterConfigProject(ctx context.Context, c MCPConfig, environmentID, directory string) (Project, error) {
	if !configProjectEnvironmentSelected(environmentID) {
		return Project{}, errors.New("请选择目标 T3 环境并填写实际环境 ID")
	}
	if !configProjectEnvironmentSelected(c.EnvironmentID) {
		return Project{}, errors.New("请填写 MCP 端点自身的实际 T3 环境 ID")
	}
	root := normalizeWorkspacePath(directory)
	if root == "" {
		return Project{}, errors.New("请填写目标机器上已有目录的绝对路径")
	}
	for _, part := range strings.FieldsFunc(directory, func(r rune) bool { return r == '/' || r == '\\' }) {
		if strings.HasPrefix(part, "REPLACE_") {
			return Project{}, errors.New("请将目录占位值替换为目标机器上已有目录的绝对路径")
		}
	}
	if err := c.validateConnection(); err != nil {
		return Project{}, errors.New("请检查 T3 MCP 端点或运行文件设置，以及私有令牌文件的绝对路径")
	}
	client, err := ConnectVerifiedMCP(ctx, c)
	if err != nil {
		if c.projectSetup {
			return Project{}, errors.New("无法使用登记凭据连接并核对 T3。请检查私有 OAuth JSON、端点绑定和环境 ID")
		}
		return Project{}, errors.New("无法连接并核对 MCP 端点的 T3 环境，请检查连接设置、环境 ID 和授权")
	}
	defer client.Session.Close()
	return registerConfigProject(ctx, client, c.EnvironmentID, environmentID, root)
}

func configProjectEnvironmentSelected(id string) bool {
	return id != "" && id == strings.TrimSpace(id) && !strings.HasPrefix(id, "REPLACE_") && !strings.ContainsFunc(id, unicode.IsControl)
}

func registerConfigProject(ctx context.Context, caller ToolCaller, localID, environmentID, root string) (Project, error) {
	t3 := T3{Calls: configProjectCaller{caller}}
	var linkAccess string
	if environmentID != localID {
		access, err := t3.linkedEnvironmentAccess(ctx, environmentID)
		if err != nil {
			return Project{}, errors.New("目标 T3 环境未关联或不可达，请在 T3 检查环境关联")
		}
		linkAccess = access
		var env struct {
			EnvironmentID string `json:"environmentId"`
		}
		if err := caller.Call(ctx, "t3_environment_read", map[string]any{"environmentId": environmentID}, &env); err != nil || env.EnvironmentID != environmentID {
			return Project{}, errors.New("无法核对关联环境的身份，请在 T3 确认目标环境 ID")
		}
	}
	target := Target{EnvironmentID: environmentID}
	projects, err := t3.Projects(ctx, target)
	if err != nil {
		return Project{}, errors.New("无法完整读取目标 T3 项目列表，请检查目标环境和授权，并在 T3 核对项目")
	}
	var matches []Project
	for _, project := range projects {
		if sameWorkspacePath(project.Root, root) {
			matches = append(matches, project)
		}
	}
	if len(matches) > 1 {
		return Project{}, errors.New("同一目录对应多个 T3 项目，请在 T3 核对项目登记")
	}
	unavailable := errors.New("目录对应的 T3 项目已归档、删除或不可用，请在 T3 核对项目状态")
	var candidate Project
	readbackErr := unavailable
	if len(matches) == 1 {
		candidate = matches[0]
		if !validProject(candidate) {
			return Project{}, unavailable
		}
	} else {
		if linkAccess != "" && linkAccess != "full-access" {
			return Project{}, errors.New("在关联环境新建项目需要 T3 full-access 的关联权限。请先在目标 T3 登记该目录，或核对关联权限后重试")
		}
		capabilities, err := t3.capabilities(ctx, localID, "")
		if err != nil || capabilities.RuntimeMode == "" {
			return Project{}, errors.New("无法核对当前 MCP 授权，尚未提交项目登记")
		}
		if capabilities.RuntimeMode != "full-access" {
			return Project{}, errors.New("新建项目需要 T3 full-access 授权。请选用登记凭据，或先在 T3 登记该目录后重试")
		}
		if ctx.Err() != nil {
			return Project{}, errors.New("T3 项目登记前已取消或超时，请检查连接后重新执行")
		}
		candidate, err = t3.RegisterProject(ctx, target, root)
		if err != nil || !validProject(candidate) || !sameWorkspacePath(candidate.Root, root) {
			return Project{}, errConfigProjectRegistrationUnknown
		}
		readbackErr = errConfigProjectRegistrationUnknown
	}
	verified, err := t3.Project(ctx, target, candidate.ID)
	if err != nil || verified.ID != candidate.ID || !validProject(verified) || !sameWorkspacePath(verified.Root, root) {
		return Project{}, readbackErr
	}
	return verified, nil
}

type configProjectCaller struct{ ToolCaller }

func (c configProjectCaller) Call(ctx context.Context, name string, args, out any) error {
	if name != "t3_project_list" {
		return c.ToolCaller.Call(ctx, name, args, out)
	}
	// A missing page or cursor must never be treated as a complete empty list.
	var raw json.RawMessage
	if err := c.ToolCaller.Call(ctx, name, args, &raw); err != nil {
		return err
	}
	var page struct {
		Projects json.RawMessage `json:"projects"`
		Next     json.RawMessage `json:"nextCursor"`
	}
	if err := json.Unmarshal(raw, &page); err != nil || len(page.Projects) == 0 || page.Projects[0] != '[' || len(page.Next) == 0 {
		return errors.New("invalid project page")
	}
	return json.Unmarshal(raw, out)
}
