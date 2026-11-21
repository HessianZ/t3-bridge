package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

type LegacySessionT3Check struct {
	Status              string                    `json:"status"`
	Message             string                    `json:"message"`
	Actual              *LegacySessionT3Execution `json:"actual,omitempty"`
	Differences         []LegacySessionDifference `json:"differences,omitempty"`
	HistoryMessageCount int                       `json:"historyMessageCount,omitempty"`
}

type LegacySessionT3Execution struct {
	ProjectID       string         `json:"projectId"`
	ProjectPath     string         `json:"projectPath"`
	ProviderID      string         `json:"providerId"`
	Model           string         `json:"model"`
	Options         map[string]any `json:"options"`
	RuntimeMode     string         `json:"runtimeMode"`
	InteractionMode string         `json:"interactionMode"`
}

type LegacySessionDifference struct {
	Field  string `json:"field"`
	Route  string `json:"route"`
	Thread string `json:"thread"`
}

// CheckLegacySession validates the offline plan before making any T3 requests.
// The actual target comes from the same state snapshot as the review.
func CheckLegacySession(ctx context.Context, c Config, catalog []byte, mapping LegacySessionMapping) (LegacySessionImportResult, LegacySessionT3Check, error) {
	plan, err := legacySessionCheckPlan(c, catalog, mapping)
	if err != nil {
		return LegacySessionImportResult{}, LegacySessionT3Check{}, err
	}
	client, err := ConnectMCP(ctx, c.MCP)
	if err != nil {
		return plan.result, LegacySessionT3Check{Status: "unavailable", Message: "无法连接已保存的 T3，请检查连接和 OAuth 授权。"}, nil
	}
	defer client.Session.Close()
	t3 := &T3{Calls: client, Config: c.MCP}
	result := plan.result
	check := t3.checkLegacySession(ctx, plan.target, result.ThreadID)
	if check.Status == "matched" {
		t3.checkLegacySessionGroup(ctx, plan.target, result, &check)
	}
	return result, check, nil
}

func legacySessionCheckPlan(c Config, catalog []byte, mapping LegacySessionMapping) (legacySessionPlan, error) {
	cwd, err := legacySessionPath(catalog, mapping)
	if err != nil {
		return legacySessionPlan{}, err
	}
	return previewLegacySessionPlan(c, mapping, cwd)
}

func (t *T3) checkLegacySessionGroup(ctx context.Context, target Target, result LegacySessionImportResult, check *LegacySessionT3Check) {
	if result.ChatType != "group" {
		return
	}
	scope := Message{ChatType: result.ChatType, ChatID: result.ChatID, TopicID: result.TopicID, UserID: result.UserID}
	evidence, err := t.scanLegacyGroupEvidence(ctx, target, result.ThreadID, scope, "")
	switch {
	case errors.Is(err, errLegacyGroupAuthor):
		check.Status, check.Message = "history_author_mismatch", "原线程的 T3 历史记录中有输入来自所选发起人以外的用户。请核对所选发起人。多人历史可另开会话。"
	case err != nil:
		check.Status, check.Message = "history_unverified", "无法核对原线程的 T3 群聊来源记录。请检查原聊天、话题及历史完整性，再重新核验。"
	default:
		check.HistoryMessageCount = len(evidence.MessageIDs)
		check.Message += fmt.Sprintf("已核对 %d 条原消息的 T3 来源记录。首条续接仍会通过飞书核对原消息与机器人身份。", check.HistoryMessageCount)
	}
}

func (t *T3) checkLegacySession(ctx context.Context, target Target, id string) LegacySessionT3Check {
	var env struct {
		ID string `json:"environmentId"`
	}
	if err := t.Calls.Call(ctx, "t3_environment_read", map[string]any{}, &env); err != nil || env.ID != t.Config.EnvironmentID || env.ID == "" {
		return LegacySessionT3Check{Status: "unavailable", Message: "无法核对已保存的本端 T3 环境。"}
	}
	if target.EnvironmentID != env.ID {
		_, linkErr := t.linkedEnvironmentAccess(ctx, target.EnvironmentID)
		env.ID = ""
		if linkErr != nil || t.Calls.Call(ctx, "t3_environment_read", map[string]any{"environmentId": target.EnvironmentID}, &env) != nil || env.ID != target.EnvironmentID {
			return LegacySessionT3Check{Status: "unavailable", Message: "无法核对目标 T3 关联环境。"}
		}
	}
	page, err := t.readThreadMetadata(ctx, target.EnvironmentID, id, 50)
	if err != nil || page.Thread.ID != id {
		return LegacySessionT3Check{Status: "unavailable", Message: "无法读取所选原线程，请核对线程 ID 与读取授权。"}
	}
	identity := page.Thread.threadIdentity
	actual := LegacySessionT3Execution{
		ProjectID: identity.ProjectID, ProviderID: identity.ProviderID, Model: identity.Model,
		RuntimeMode: identity.RuntimeMode, InteractionMode: identity.InteractionMode,
	}
	config, err := t.readThreadConfiguration(ctx, target.EnvironmentID, id)
	if err != nil || config.ThreadID != id || config.ModelSelection.InstanceID != actual.ProviderID || config.ModelSelection.Model != actual.Model ||
		config.RuntimeMode != actual.RuntimeMode || config.InteractionMode != actual.InteractionMode ||
		!workspaceImportValue(actual.ProviderID) || !workspaceImportValue(actual.Model) || slices.Index(t3AccessModes, actual.RuntimeMode) <= 0 ||
		!slices.Contains([]string{"default", "plan"}, actual.InteractionMode) {
		return LegacySessionT3Check{Status: "unavailable", Message: "无法确认原线程的执行配置，请重新核验。"}
	}
	actual.Options, err = readModelOptions(config.ModelSelection.Options)
	if err != nil {
		return LegacySessionT3Check{Status: "unavailable", Message: "无法读取原线程的模型选项。"}
	}
	project, err := t.Project(ctx, target, actual.ProjectID)
	if err != nil || !validProject(project) || project.ID != actual.ProjectID {
		return LegacySessionT3Check{Status: "unavailable", Message: "无法核对原线程所属的 T3 项目，请检查项目状态。"}
	}
	actual.ProjectPath = project.Root
	report := LegacySessionT3Check{Actual: &actual}
	report.Differences = actual.differences(target, config.ModelSelection.Provider)
	if len(report.Differences) > 0 {
		report.Status, report.Message = "mismatch", "所选路由与原线程配置不一致。请选择匹配的路由，或保存修订后重新核验。"
		return report
	}
	native, err := page.inspect(target, id)
	if err != nil {
		report.Status, report.Message = "unavailable", "原线程缺少可转接的身份或运行记录，请在 T3 核对线程。"
		return report
	}
	if !native.Quiet {
		report.Status, report.Message = "busy", "原线程仍有运行、排队、审批或后台工作，请等它们结清后重新核验。"
		return report
	}
	if err := t.Preflight(ctx, target); err != nil {
		report.Status, report.Message = "unavailable", "无法确认路由的派发条件，请检查 T3 OAuth、关联权限和模型能力。"
		return report
	}
	report.Status, report.Message = "matched", "原线程配置与所选路由匹配，核验时原线程空闲。"
	return report
}

func (a LegacySessionT3Execution) differences(target Target, driver *string) []LegacySessionDifference {
	actualOptions, _ := json.Marshal(a.Options)
	routeOptions, _ := json.Marshal(target.Options)
	var differences []LegacySessionDifference
	for _, field := range []LegacySessionDifference{
		{"projectId", target.ProjectID, a.ProjectID},
		{"providerId", target.ProviderID, a.ProviderID},
		{"model", target.Model, a.Model},
		{"options", string(routeOptions), string(actualOptions)},
		{"runtimeMode", target.RuntimeMode, a.RuntimeMode},
		{"interactionMode", target.InteractionMode, a.InteractionMode},
	} {
		if field.Route != field.Thread {
			differences = append(differences, field)
		}
	}
	if !sameWorkspacePath(target.ProjectPath, a.ProjectPath) {
		differences = append(differences, LegacySessionDifference{"projectPath", target.ProjectPath, a.ProjectPath})
	}
	if driver != nil && *driver != target.Driver {
		differences = append(differences, LegacySessionDifference{"driver", target.Driver, *driver})
	}
	return differences
}
