package bridge

import (
	"context"
	"errors"
	"maps"
)

// PrepareLegacySessionTarget verifies the configuration that would continue the
// original thread. The caller can turn its execution fields into a saved-route draft.
func PrepareLegacySessionTarget(ctx context.Context, c Config, catalog []byte, mapping LegacySessionMapping) (Target, error) {
	plan, err := legacySessionCheckPlan(c, catalog, mapping)
	if err != nil {
		return Target{}, err
	}
	saved := c.Routes[mapping.RouteID]
	if saved.EnvironmentID != plan.target.EnvironmentID || saved.ProjectID != plan.target.ProjectID || !sameWorkspacePath(saved.ProjectPath, plan.target.ProjectPath) {
		return Target{}, errors.New("当前会话使用了其他工作区。请选择已保存且指向原目录的路由。")
	}
	client, err := ConnectMCP(ctx, c.MCP)
	if err != nil {
		return Target{}, errors.New("无法连接已保存的 T3，请检查连接和 OAuth 授权。")
	}
	defer client.Session.Close()
	t3 := &T3{Calls: client, Config: c.MCP}
	target, err := t3.prepareLegacySessionTarget(ctx, plan.result, saved)
	if err != nil {
		return Target{}, err
	}
	c.Routes = maps.Clone(c.Routes)
	c.Routes[mapping.RouteID] = target
	if _, err := previewLegacySessionPlan(c, mapping, plan.result.ProjectPath); err != nil {
		return Target{}, err
	}
	return target, nil
}

func (t *T3) prepareLegacySessionTarget(ctx context.Context, result LegacySessionImportResult, saved Target) (Target, error) {
	check := t.checkLegacySession(ctx, saved, result.ThreadID)
	if check.Status == "matched" {
		return Target{}, errors.New("所选路由的执行配置已与原线程一致。可继续预览会话转接。")
	}
	if check.Status != "mismatch" {
		return Target{}, errors.New(check.Message)
	}
	actual := check.Actual
	if actual.ProjectID != saved.ProjectID || !sameWorkspacePath(saved.ProjectPath, actual.ProjectPath) {
		return Target{}, errors.New("原线程与所选路由的项目或目录不同。请先选择匹配的路由。")
	}
	catalog, err := t.capabilities(ctx, saved.EnvironmentID, actual.Model)
	if err != nil {
		return Target{}, errors.New("无法读取原线程模型的 T3 能力目录。")
	}
	driver := ""
	for _, provider := range catalog.Providers {
		if provider.ID == actual.ProviderID {
			if driver != "" || provider.Driver == "" {
				return Target{}, errors.New("原线程的 T3 provider 对应关系不明确。")
			}
			driver = provider.Driver
		}
	}
	if driver == "" {
		return Target{}, errors.New("能力目录中无法找到原线程的 T3 provider。")
	}
	if saved.ToolContext != nil && (saved.Driver != driver || saved.ProviderID != actual.ProviderID) {
		return Target{}, errors.New("所选路由的飞书工具已绑定到其他 provider。请在配置工作台为原 provider 配置专用路由。")
	}
	target := saved
	target.Driver, target.ProviderID, target.Model, target.Options = driver, actual.ProviderID, actual.Model, actual.Options
	target.RuntimeMode, target.InteractionMode = actual.RuntimeMode, actual.InteractionMode
	if err := t.Preflight(ctx, target); err != nil {
		return Target{}, errors.New("原线程执行配置未通过 T3 派发预检，请检查 OAuth、关联权限和模型能力。")
	}
	if err := t.verifyLegacySession(ctx, target, result.ThreadID); err != nil {
		return Target{}, errors.New("无法确认原线程仍空闲且项目与执行配置一致，请重新核验。")
	}
	check.Status, check.Message = "matched", ""
	t.checkLegacySessionGroup(ctx, target, result, &check)
	if check.Status != "matched" {
		return Target{}, errors.New(check.Message)
	}
	return target, nil
}
