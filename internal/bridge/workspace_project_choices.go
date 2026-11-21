package bridge

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

const workspaceProjectsCommand = "/cd projects"

const (
	workspaceProjectsPageSize = 6
	workspaceProjectsUsage    = "发送 /cd list [页码] 查看项目，或 /cd use <完整项目ID> 切换。"
	workspaceProjectsStale    = "目标、工作区或项目卡片已变化。请重新发送 /cd list。"
	workspaceProjectsBusy     = "当前私聊仍有运行中、待核对或排队的任务。请等任务结清后再切换项目。"
)

func (e *Engine) workspaceProjectsAllowed(st *State, m Message, route string) bool {
	c := e.Config
	base := c.Routes[route]
	if c.AppID == "" || st.AccountID != c.AppID || m.UserID == "" || m.ChatID == "" ||
		m.MeetingInvite != nil || m.MeetingSummary != nil || m.EventID != "" ||
		route == "" || route == c.ReviewRoute || base.EnvironmentID == "" ||
		!e.privateAdminAllowed(m) || !c.TargetAllowed(m, base, route) {
		return false
	}
	// The durable ACL can change before the receiver publishes it in Config.
	if m.UserID != c.ownerID() {
		if allowed, known := st.AccessPolicy.Users[m.UserID]; known && !allowed {
			return false
		}
		if allowed, known := st.AccessPolicy.Admins[m.UserID]; known && !allowed {
			return false
		}
	}
	allowed, known := st.AccessPolicy.Routes[route][m.UserID]
	return !known || allowed
}

func (e *Engine) workspaceProjectControl(ctx context.Context, m Message, id string, cmd *SessionCommand, origin *SessionCommand, button CommandButton) error {
	st := e.Store.snapshot()
	page, projectID := 1, ""
	if m.CommandAction == "" {
		if !directRouteSelection(m) {
			cmd.Reply = "请在已授权私聊直接发送 /cd list 或 /cd use <完整项目ID>。"
			return nil
		}
		words := strings.Fields(routeText(m))
		switch {
		case len(words) == 2 && words[0] == "/cd" && words[1] == "list":
		case len(words) == 3 && words[0] == "/cd" && words[1] == "list":
			var err error
			page, err = strconv.Atoi(words[2])
			if err != nil || page < 1 || strings.HasPrefix(words[2], "+") {
				cmd.Reply = workspaceProjectsUsage
				return nil
			}
		case len(words) == 3 && words[0] == "/cd" && words[1] == "use":
			projectID = words[2]
		default:
			cmd.Reply = workspaceProjectsUsage
			return nil
		}
	} else {
		page = button.ProjectPage
		if button.Workspace != nil {
			projectID = button.Workspace.ProjectID
		}
		if origin == nil || (button.Workspace == nil && page < 1) ||
			(button.Workspace != nil && (page != 0 || projectID == "")) {
			cmd.Reply = workspaceProjectsStale
			return nil
		}
	}

	m = liveRouteSelectionMessage(m)
	route := selectedRoute(e.Config, &st, m)
	if !e.workspaceProjectsAllowed(&st, m, route) {
		cmd.Reply = "只有当前应用的所有者或管理员可在已授权私聊查看或切换当前路由的项目。"
		return nil
	}
	cmd.RouteID, cmd.BaseTargetID = route, targetIdentity(e.Config.Routes[route])
	cmd.Target = effectiveTarget(e.Config, &st, m, route)
	if origin != nil && e.workspaceProjectCardOrigin(&st, m, cmd, origin, button) == nil {
		cmd.Reply = workspaceProjectsStale
		return nil
	}
	if m.CommandAction == "" && projectID != "" && projectID == cmd.Target.ProjectID {
		cmd.Reply = "该项目已是当前项目。"
		return nil
	}
	if projectID != "" && workspaceBusy(e.Config, &st, m, "") {
		cmd.Reply = workspaceProjectsBusy
		return nil
	}
	api, ok := e.T3.(projectAPI)
	if !ok {
		cmd.Reply = "当前 T3 环境的项目资料不可用。"
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if projectID == "" {
		projects, err := api.Projects(ctx, cmd.Target)
		if err != nil {
			e.commandFailed(ctx, CommandFailureWorkspace)
			cmd.Reply = "当前 T3 环境的项目列表不可用。"
			return nil
		}
		live := e.Store.snapshot()
		if !e.workspaceProjectSnapshotMatches(&live, m, cmd) ||
			(origin != nil && e.workspaceProjectCardOrigin(&live, m, cmd, origin, button) == nil) {
			cmd.Reply = workspaceProjectsStale
			return nil
		}
		e.workspaceProjectPage(cmd, projects, page)
		return nil
	}

	project, err := api.Project(ctx, cmd.Target, projectID)
	if err != nil {
		e.commandFailed(ctx, CommandFailureWorkspace)
	}
	if err != nil || project.ID != projectID || !validWorkspaceProjectChoice(project) ||
		(button.Workspace != nil && project.Root != button.Workspace.Path) {
		cmd.Reply = "该项目不可选。请重新发送 /cd list 核对项目 ID。"
		return nil
	}
	return e.Store.update(func(st *State) error {
		if !e.workspaceProjectSnapshotMatches(st, m, cmd) {
			cmd.Reply = workspaceProjectsStale
			return nil
		}
		var liveOrigin *SessionCommand
		if origin != nil {
			liveOrigin = e.workspaceProjectCardOrigin(st, m, cmd, origin, button)
			if liveOrigin == nil {
				cmd.Reply = workspaceProjectsStale
				return nil
			}
		}
		if workspaceBusy(e.Config, st, m, "") {
			cmd.Reply = workspaceProjectsBusy
			return nil
		}
		cmd.Target = effectiveTarget(e.Config, st, m, route)
		e.commitWorkspaceSelection(st, m, id, cmd, project, nil, liveOrigin)
		return nil
	})
}

func (e *Engine) workspaceProjectSnapshotMatches(st *State, m Message, cmd *SessionCommand) bool {
	route := cmd.RouteID
	return e.workspaceProjectsAllowed(st, m, route) && selectedRoute(e.Config, st, m) == route &&
		cmd.BaseTargetID == targetIdentity(e.Config.Routes[route]) &&
		sameResumeProject(effectiveTarget(e.Config, st, m, route), cmd.Target)
}

func (e *Engine) workspaceProjectCardOrigin(st *State, m Message, cmd, origin *SessionCommand, button CommandButton) *SessionCommand {
	live, choice := commandCardAction(e.Config, st, m)
	if live == nil || live.Card.Used || live.Kind != workspaceProjectsCommand ||
		live.Source.ID != origin.Source.ID || live.Source.ChatType != m.ChatType ||
		userScope(live.Source) != userScope(m) || live.RouteID != cmd.RouteID ||
		live.BaseTargetID != cmd.BaseTargetID || !sameResumeProject(live.Target, cmd.Target) ||
		choice.Command != workspaceProjectsCommand || choice.ProjectPage != button.ProjectPage ||
		(choice.Workspace == nil) != (button.Workspace == nil) ||
		(choice.Workspace != nil && *choice.Workspace != *button.Workspace) {
		return nil
	}
	return live
}

func validWorkspaceProjectChoice(project Project) bool {
	return validProject(project) && strings.TrimSpace(project.Title) != ""
}

func (e *Engine) workspaceProjectPage(cmd *SessionCommand, projects []Project, page int) {
	var available []Project
	for _, project := range projects {
		if validWorkspaceProjectChoice(project) {
			available = append(available, project)
		}
	}
	if len(available) == 0 {
		cmd.Reply = "当前 T3 环境没有可选项目。"
		return
	}
	pages := (len(available) + workspaceProjectsPageSize - 1) / workspaceProjectsPageSize
	if page > pages {
		cmd.Reply = fmt.Sprintf("页码超出范围，共 %d 页。%s", pages, workspaceProjectsUsage)
		return
	}
	slices.SortFunc(available, func(a, b Project) int {
		return cmp.Or(cmp.Compare(a.Title, b.Title), cmp.Compare(a.ID, b.ID))
	})
	start := (page - 1) * workspaceProjectsPageSize
	end := min(start+workspaceProjectsPageSize, len(available))
	card := &CommandCard{AppID: e.Config.AppID}
	var reply strings.Builder
	fmt.Fprintf(&reply, "当前路由的 T3 项目 · %d/%d 页 · 共 %d 条", page, pages, len(available))
	for i, project := range available[start:end] {
		name := routeSelectionDisplay(project.Title, 48)
		projectID := routeSelectionDisplay(project.ID, 180)
		fmt.Fprintf(&reply, "\n%d. %s · %s", i+1, name, projectID)
		if project.ID == cmd.Target.ProjectID {
			reply.WriteString(" · 当前")
			continue
		}
		card.Buttons = append(card.Buttons, CommandButton{
			Label:     fmt.Sprintf("选择 %d · %s", i+1, name),
			Command:   workspaceProjectsCommand,
			Workspace: &Workspace{ProjectID: project.ID, Name: project.Title, Path: project.Root},
		})
	}
	if page > 1 {
		card.Buttons = append(card.Buttons, CommandButton{Label: "上一页", Command: workspaceProjectsCommand, ProjectPage: page - 1})
	}
	if page < pages {
		card.Buttons = append(card.Buttons, CommandButton{Label: "下一页", Command: workspaceProjectsCommand, ProjectPage: page + 1})
	}
	reply.WriteString("\n" + workspaceProjectsUsage)
	cmd.Reply = reply.String()
	if len(card.Buttons) > 0 {
		cmd.Card = card
	}
}
