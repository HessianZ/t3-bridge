package bridge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type Project struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Root       string  `json:"workspaceRoot"`
	DeletedAt  *string `json:"deletedAt"`
	ArchivedAt *string `json:"archivedAt"`
}
type Workspace struct {
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
	Path      string `json:"path"`
}
type projectAPI interface {
	Projects(context.Context, Target) ([]Project, error)
	Project(context.Context, Target, string) (Project, error)
	RegisterProject(context.Context, Target, string) (Project, error)
}

func workspaceKey(c Config, m Message, route string) string {
	return stable(userScope(m), route, targetIdentity(c.Routes[route]))
}
func effectiveTarget(c Config, st *State, m Message, route string) Target {
	t := c.Routes[route]
	w, ok := m.ProjectChoices[workspaceKey(c, m, route)]
	if ok {
		t.ProjectID, t.ProjectName, t.ProjectPath = w.ProjectID, w.Name, w.Path
		return t
	}
	w, ok = st.Workspaces[workspaceKey(c, m, route)]
	if !ok && m.TopicID != "" {
		chat := m
		chat.TopicID = ""
		w, ok = st.Workspaces[workspaceKey(c, chat, route)]
	}
	if ok {
		t.ProjectID, t.ProjectName, t.ProjectPath = w.ProjectID, w.Name, w.Path
	}
	return t
}
func rememberWorkspace(c Config, st *State, m Message, route string, t Target) {
	if st.Workspaces == nil {
		st.Workspaces = map[string]Workspace{}
	}
	st.Workspaces[workspaceKey(c, m, route)] = Workspace{t.ProjectID, t.ProjectName, t.ProjectPath}
}
func executionPolicy(t Target) string { t.ProjectID = ""; return targetIdentity(t) }
func taskTargetAllowed(c Config, t *Task) bool {
	base, ok := c.Routes[t.RouteID]
	if !ok {
		return false
	}
	if t.BaseTargetID == "" {
		return targetIdentity(base) == targetIdentity(t.Target)
	}
	return t.BaseTargetID == targetIdentity(base) && executionPolicy(base) == executionPolicy(t.Target) && t.Target.ProjectID != ""
}
func workspaceBusy(c Config, st *State, m Message, route string) bool {
	for _, t := range st.Tasks {
		if userScope(t.Source) == userScope(m) && (route == "" || t.RouteID == route) &&
			(!terminal(t.Phase) || route == "" && t.NeedsReconcile) {
			return true
		}
	}
	for _, q := range st.Inbox {
		if q.ID == m.ID || userScope(q) != userScope(m) || q.CommandAction != "" || slashWord(c, routeText(q)) != "" || control(routeText(q)) {
			continue
		}
		if route == "" {
			return true
		}
		if q.RouteAtReceive != "" && q.RouteAtReceive != route {
			continue
		}
		if r, _ := explicitRoute(c, routeText(q)); r != "" && r != route {
			continue
		}
		if t := boundTask(st, q); t != nil && t.RouteID != route {
			continue
		}
		return true
	}
	return false
}
func validProject(p Project) bool {
	return p.ID != "" && normalizeWorkspacePath(p.Root) != "" && p.DeletedAt == nil && p.ArchivedAt == nil
}

func workspaceStopOption(kind, args string) (string, bool) {
	prefix := ""
	if kind == "/ws" {
		name, ok := strings.CutPrefix(args, "use ")
		if !ok {
			return args, false
		}
		prefix, args = "use ", strings.TrimSpace(name)
	}
	if args == "--stop" {
		return "", true
	}
	if value, ok := strings.CutPrefix(args, "--stop "); ok {
		return prefix + strings.TrimSpace(value), true
	}
	if value, ok := strings.CutPrefix(args, "-- "); ok {
		return prefix + strings.TrimSpace(value), false
	}
	return prefix + args, false
}

func (e *Engine) workspaceControl(ctx context.Context, m Message, id string, cmd *SessionCommand, args string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	st := e.Store.snapshot()
	if cmd.RouteID == "" || !e.Config.TargetAllowed(m, e.Config.Routes[cmd.RouteID], cmd.RouteID) {
		cmd.Reply = "没有已授权目标，未切换工作区。"
		return nil
	}
	stopCurrent := false
	if m.CommandAction == "" {
		args, stopCurrent = workspaceStopOption(cmd.Kind, args)
	}
	if stopCurrent && args == "" {
		cmd.Reply = "用法：/cd --stop <绝对路径> 或 /ws use --stop <名称>"
		return nil
	}
	if stopCurrent && (m.ParentID != "" || m.ControlTask != "" || m.SessionTask != "" || m.Comment != nil || m.Meeting != nil) {
		cmd.Reply = "请在聊天中直接发送停止并切换命令。"
		return nil
	}
	op, name, _ := strings.Cut(args, " ")
	name = strings.TrimSpace(name)
	if cmd.Kind == "/ws" {
		switch op {
		case "", "list":
			page := 1
			if name != "" {
				var err error
				page, err = strconv.Atoi(name)
				if err != nil || page < 1 {
					cmd.Reply = "用法：/ws list [正整数页码]"
					return nil
				}
			}
			e.workspaceList(&st, m, cmd, page)
			return nil
		case "save", "use", "remove", "rm":
			if name == "" || len([]rune(name)) > 64 || strings.ContainsFunc(name, unicode.IsControl) {
				cmd.Reply = "工作区名称需为1..64个可见字符。"
				return nil
			}
			if op == "remove" || op == "rm" {
				return e.removeWorkspaceAlias(m, id, cmd, name)
			}
		default:
			cmd.Reply = "用法：/ws list [页码] | save <名称> | use <名称> | remove <名称>"
			return nil
		}
	}
	if m.CommandAction != "" {
		origin, button := e.workspaceCardOrigin(&st, m)
		if origin == nil {
			cmd.Reply = staleWorkspaceCard
			return nil
		}
		stopCurrent = button.TaskID != ""
	}
	api, ok := e.T3.(projectAPI)
	if !ok {
		return errors.New("project API unavailable")
	}
	target := cmd.Target
	var selected *Workspace
	if (cmd.Kind == "/cd" && args == "") || (cmd.Kind == "/ws" && strings.HasPrefix(args, "save ")) {
		if target.ProjectPath == "" || target.ProjectName == "" {
			p, err := api.Project(ctx, target, target.ProjectID)
			lookupFailed := err != nil || p.ID != target.ProjectID || normalizeWorkspacePath(p.Root) == ""
			if lookupFailed || !validProject(p) {
				if lookupFailed {
					e.commandFailed(ctx, CommandFailureWorkspace)
				}
				cmd.Reply = "当前T3项目资料不可用，未改变工作区。"
				return nil
			}
			target.ProjectName, target.ProjectPath = p.Title, p.Root
		}
	}
	if cmd.Kind == "/cd" && args == "" {
		cmd.Reply = fmt.Sprintf("目标：%s · 项目：%s\n目录：%s", cmd.RouteID, target.ProjectName, target.ProjectPath)
		if e.workspaceProjectsAllowed(&st, m, cmd.RouteID) {
			cmd.Reply += "\n发送 /cd list 浏览当前路由的已登记项目。"
		}
		return nil
	}
	if cmd.Kind == "/ws" {
		aliases := st.WorkspaceNames[workspaceKey(e.Config, m, cmd.RouteID)]
		w, exists := aliases[name]
		if op == "save" {
			if len(aliases) >= 32 && !exists {
				cmd.Reply = "本范围最多32个工作区别名。"
				return nil
			}
			cmd.Reply = "工作区别名已保存；不改变执行目标或权限。"
			return e.Store.update(func(st *State) error {
				if st.WorkspaceNames == nil {
					st.WorkspaceNames = map[string]map[string]Workspace{}
				}
				k := workspaceKey(e.Config, m, cmd.RouteID)
				if st.WorkspaceNames[k] == nil {
					st.WorkspaceNames[k] = map[string]Workspace{}
				}
				st.WorkspaceNames[k][name] = Workspace{target.ProjectID, target.ProjectName, target.ProjectPath}
				saveCommandState(st, id, cmd)
				return nil
			})
		}
		if !exists {
			cmd.Reply = "当前用户/聊天/话题/目标下没有这个工作区别名。"
			return nil
		}
		selected = &w
	}
	const busyReply = "当前范围仍有运行或排队输入。可等待收口，或用 /cd --stop <绝对路径>、/ws use --stop <名称> 停止当前轮次并切换。"
	if !stopCurrent && workspaceBusy(e.Config, &st, m, cmd.RouteID) {
		cmd.Reply = busyReply
		return nil
	}
	var p Project
	var err error
	if selected != nil {
		p, err = api.Project(ctx, target, selected.ProjectID)
		lookupFailed := err != nil || p.ID != selected.ProjectID || !sameWorkspacePath(p.Root, selected.Path)
		if lookupFailed || !validProject(p) {
			if lookupFailed {
				e.commandFailed(ctx, CommandFailureWorkspace)
			}
			cmd.Reply = "保存的T3项目已不可用或目录变化，原绑定保留。"
			return nil
		}
	} else {
		// A saved registration attempt can re-enter the read-only checks during recovery.
		recoveringRegistration := cmd.ProjectAttempted
		root := normalizeWorkspacePath(args)
		if root == "" {
			cmd.Reply = "请使用目标机器的绝对路径；不展开 ~ 或相对路径，也不创建目录。"
			return nil
		}
		projects, err := api.Projects(ctx, target)
		if err != nil {
			if !recoveringRegistration {
				e.commandFailed(ctx, CommandFailureWorkspace)
			}
			cmd.Reply = "无法完整读取目标机器T3项目目录，原绑定保留。"
			return nil
		}
		matches := []Project{}
		for _, v := range projects {
			if validProject(v) && sameWorkspacePath(v.Root, root) {
				matches = append(matches, v)
			}
		}
		if len(matches) > 1 {
			cmd.Reply = "同一路径对应多个T3项目，未猜测选择；请先核对项目登记。"
			return nil
		}
		if len(matches) == 1 {
			p = matches[0]
		} else {
			if !e.Config.isAdmin(m.UserID) {
				cmd.Reply = "目录尚未登记为T3项目；仅owner/admin可登记已存在目录。"
				return nil
			}
			attempted := cmd.ProjectAttempted
			for _, old := range st.Commands {
				if old.ProjectAttempted && old.RegistrationID == "" && old.Target.EnvironmentID == target.EnvironmentID && sameWorkspacePath(old.WorkspacePath, root) {
					attempted = true
				}
			}
			if attempted {
				cmd.Reply = "此前登记结果仍未知；已只读核对且未找到，未重复创建。请稍后核对T3项目。"
				return nil
			}
			cmd.WorkspacePath = root
			cmd.ProjectAttempted = true
			if err = e.saveCommand(id, cmd); err != nil {
				return err
			}
			p, err = api.RegisterProject(ctx, target, root)
			if err != nil || !validProject(p) || !sameWorkspacePath(p.Root, root) {
				e.commandFailed(ctx, CommandFailureWorkspace)
				cmd.Reply = "项目登记未确认（目录可能不存在）；未创建目录或执行shell。后续仅只读核对，不盲重试登记。"
				return nil
			}
		}
		// Confirm an exact authoritative project, never take provider/model defaults from its metadata.
		pid := p.ID
		p, err = api.Project(ctx, target, pid)
		lookupFailed := err != nil || p.ID != pid || !sameWorkspacePath(p.Root, root)
		if lookupFailed || !validProject(p) {
			if lookupFailed && !recoveringRegistration {
				e.commandFailed(ctx, CommandFailureWorkspace)
			}
			cmd.Reply = "项目读回未确认，原绑定保留；不重复登记。"
			return nil
		}
	}
	return e.Store.update(func(st *State) error {
		if !e.Config.Allowed(m) || !e.Config.TargetAllowed(m, e.Config.Routes[cmd.RouteID], cmd.RouteID) ||
			targetIdentity(e.Config.Routes[cmd.RouteID]) != cmd.BaseTargetID {
			cmd.Reply = "目标或权限已变化，未切换项目。"
			return nil
		}
		var origin *SessionCommand
		if m.CommandAction != "" {
			origin, _ = e.workspaceCardOrigin(st, m)
			if origin == nil || (!stopCurrent && workspaceBusy(e.Config, st, m, cmd.RouteID)) {
				cmd.Reply = "工作区、权限或队列状态已变化。请重新发送 /ws list。"
				return nil
			}
		} else if !stopCurrent && workspaceBusy(e.Config, st, m, cmd.RouteID) {
			cmd.Reply = busyReply
			return nil
		}
		var stopped *Task
		if stopCurrent {
			if selectedRoute(e.Config, st, m) != cmd.RouteID ||
				!sameResumeProject(effectiveTarget(e.Config, st, m, cmd.RouteID), target) {
				cmd.Reply = "当前工作区已变化，请用 /status 核对后重发切换命令。"
				return nil
			}
			current := currentTask(e.Config, st, m, cmd.RouteID)
			if current != nil && (targetIdentity(current.Target) != targetIdentity(target) ||
				(current.ReceiverAppID != "" && current.ReceiverAppID != e.Config.AppID)) {
				cmd.Reply = "当前轮次的项目或接收应用已变化，请用 /status 核对。"
				return nil
			}
			cancelQueued(e.Config, st, m, current)
			if current != nil && !terminal(current.Phase) {
				current.StopRequested = true
				stopped = current
			}
		}
		e.commitWorkspaceSelection(st, m, id, cmd, p, stopped, origin)
		return nil
	})
}

// Call after checking the prior workspace and task state in the store transaction.
func (e *Engine) commitWorkspaceSelection(st *State, m Message, id string, cmd *SessionCommand, p Project, stopped *Task, origin *SessionCommand) {
	prior := cmd.Target
	target := prior
	target.ProjectID, target.ProjectName, target.ProjectPath = p.ID, p.Title, p.Root
	cmd.Target = target
	cmd.RegistrationID = p.ID
	cmd.Reply = "已切换目标 " + cmd.RouteID + " 的项目：" + p.Title + "\n" + p.Root + "\n当前映射已重置；旧任务/队列身份保留，执行模型与权限不变。"
	if stopped != nil {
		cmd.Reply += "\n已请求停止原轮次 " + shortTaskID(stopped.ID) + "。远端状态请在原结果卡查看。"
	}
	if origin != nil {
		origin.Card.Used = true
	}
	if targetIdentity(prior) != targetIdentity(target) {
		if old := st.Tasks[st.Current[sessionKey(m, cmd.RouteID, prior)]]; old != nil {
			if old.ThreadMeta == nil {
				old.ThreadMeta = &ThreadMetadata{}
			}
			old.ThreadMeta.Detached = true
		}
	}
	rememberWorkspace(e.Config, st, m, cmd.RouteID, target)
	if st.Selected == nil {
		st.Selected = map[string]string{}
	}
	st.Selected[userScope(m)] = cmd.RouteID
	if st.Current == nil {
		st.Current = map[string]string{}
	}
	st.Current[sessionKey(m, cmd.RouteID, target)] = ""
	for _, old := range st.Commands {
		if old.Target.EnvironmentID == target.EnvironmentID && sameWorkspacePath(old.WorkspacePath, p.Root) {
			old.RegistrationID = p.ID
		}
	}
	saveCommandState(st, id, cmd)
}

const staleWorkspaceCard = "卡片中的工作区、目标或任务轮次已变化。请重新发送 /ws list。"

func (e *Engine) removeWorkspaceAlias(m Message, id string, cmd *SessionCommand, name string) error {
	return e.Store.update(func(st *State) error {
		if !e.Config.Allowed(m) || !e.Config.TargetAllowed(m, e.Config.Routes[cmd.RouteID], cmd.RouteID) ||
			targetIdentity(e.Config.Routes[cmd.RouteID]) != cmd.BaseTargetID {
			cmd.Reply = "目标或权限已变化。请重新发送 /ws list。"
			return nil
		}
		var origin *SessionCommand
		if m.CommandAction != "" {
			var button CommandButton
			origin, button = e.workspaceCardOrigin(st, m)
			if origin == nil || !button.RemoveWorkspace || button.Name != name || origin.RouteID != cmd.RouteID {
				cmd.Reply = staleWorkspaceCard
				return nil
			}
		}
		aliases := st.WorkspaceNames[workspaceKey(e.Config, m, cmd.RouteID)]
		if _, exists := aliases[name]; !exists {
			cmd.Reply = "当前用户/聊天/话题/目标下没有这个工作区别名。"
			return nil
		}
		delete(aliases, name)
		if origin != nil {
			origin.Card.Used = true
		}
		cmd.Reply = "工作区别名已删除；当前项目不变。"
		saveCommandState(st, id, cmd)
		return nil
	})
}

func (e *Engine) workspaceList(st *State, m Message, cmd *SessionCommand, page int) {
	const pageSize = 8
	aliases := st.WorkspaceNames[workspaceKey(e.Config, m, cmd.RouteID)]
	pages := max(1, (len(aliases)+pageSize-1)/pageSize)
	if page < 1 || page > pages {
		cmd.Reply = fmt.Sprintf("命名工作区共 %d 页。发送 /ws list [页码] 查看。", pages)
		return
	}
	names := make([]string, 0, len(aliases))
	for name := range aliases {
		names = append(names, name)
	}
	slices.Sort(names)
	project := cmd.Target.ProjectPath
	if project == "" {
		project = cmd.Target.ProjectID
	}
	cmd.Reply = fmt.Sprintf("目标：%s · 当前项目：%s\n本范围命名工作区 · %d/%d 页 · 共 %d 条", cmd.RouteID, display(project, 80), page, pages, len(names))
	var buttons []CommandButton
	canSelect := selectedRoute(e.Config, st, m) == cmd.RouteID
	active := activeWorkspaceRound(e.Config, st, m, cmd.RouteID)
	start := (page - 1) * pageSize
	for _, name := range names[start:min(len(names), start+pageSize)] {
		w := aliases[name]
		cmd.Reply += "\n" + name + " · " + display(w.Name, 40) + " · " + display(w.Path, 80)
		current := w.ProjectID == cmd.Target.ProjectID
		if current {
			cmd.Reply += " · 当前"
		}
		if canSelect {
			if !current {
				button := CommandButton{Label: "切换：" + display(name, 45), Command: "/ws", Name: name, Workspace: &w}
				if active != nil {
					button.Label = "停止本轮并切换：" + display(name, 38)
					button.TaskID, button.RequestID = active.ID, active.RequestID
				}
				buttons = append(buttons, button)
			}
			buttons = append(buttons, CommandButton{Label: "删除别名：" + display(name, 45), Command: "/ws", Name: name, Workspace: &w, RemoveWorkspace: true})
		}
	}
	if len(names) == 0 {
		cmd.Reply += "\n无。发送 /ws save <名称> 保存当前工作区。"
	} else {
		cmd.Reply += "\n发送 /ws use <名称> 切换，或 /ws remove <名称> 删除别名。"
		if active != nil {
			cmd.Reply += "\n发送 /ws use --stop <名称> 可请求停止当前轮次并切换。"
		}
	}
	if pages > 1 {
		cmd.Reply += "\n发送 /ws list <页码> 可翻页。"
	}
	if canSelect && page > 1 {
		buttons = append(buttons, CommandButton{Label: "上一页", Command: "/ws", WorkspacePage: page - 1})
	}
	if canSelect && page < pages {
		buttons = append(buttons, CommandButton{Label: "下一页", Command: "/ws", WorkspacePage: page + 1})
	}
	e.prepareCommandCard(cmd, nil, buttons)
}

func activeWorkspaceRound(c Config, st *State, m Message, route string) *Task {
	task := currentTask(c, st, m, route)
	if task != nil && task.RequestID != "" && !terminal(task.Phase) && !task.StopRequested &&
		(task.ReceiverAppID == c.AppID || (task.ReceiverAppID == "" && st.AccountID == c.AppID)) {
		return task
	}
	return nil
}

func (e *Engine) workspaceCardOrigin(st *State, m Message) (*SessionCommand, CommandButton) {
	origin, button := commandCardAction(e.Config, st, m)
	if origin == nil || origin.Card.Used || button.Command != "/ws" || button.Workspace == nil || !e.workspaceCardScopeMatches(st, m, origin) {
		return nil, CommandButton{}
	}
	alias, exists := st.WorkspaceNames[workspaceKey(e.Config, m, origin.RouteID)][button.Name]
	if !exists || alias != *button.Workspace {
		return nil, CommandButton{}
	}
	if button.TaskID != "" {
		task := activeWorkspaceRound(e.Config, st, m, origin.RouteID)
		if task == nil || task.ID != button.TaskID || task.RequestID != button.RequestID {
			return nil, CommandButton{}
		}
	}
	return origin, button
}

func (e *Engine) workspaceCardScopeMatches(st *State, m Message, origin *SessionCommand) bool {
	return origin.Kind == "/ws" && userScope(origin.Source) == userScope(m) && selectedRoute(e.Config, st, m) == origin.RouteID &&
		sameResumeProject(effectiveTarget(e.Config, st, m, origin.RouteID), origin.Target)
}
func (t *T3) Projects(ctx context.Context, target Target) ([]Project, error) {
	out := []Project{}
	cursor := 0
	ids := map[string]bool{}
	for page := 0; page < 10; page++ {
		var p struct {
			Projects []Project `json:"projects"`
			Next     *int      `json:"nextCursor"`
		}
		if err := t.Calls.Call(ctx, "t3_project_list", map[string]any{"environmentId": target.EnvironmentID, "cursor": cursor, "limit": 100}, &p); err != nil {
			return nil, err
		}
		if len(p.Projects) > 100 {
			return nil, errors.New("project page outside bounds")
		}
		for _, v := range p.Projects {
			if ids[v.ID] {
				return nil, errors.New("project pages shifted")
			}
			ids[v.ID] = true
			out = append(out, v)
		}
		if p.Next == nil {
			return out, nil
		}
		if *p.Next <= cursor {
			return nil, errors.New("invalid project cursor")
		}
		cursor = *p.Next
	}
	return nil, errors.New("project list outside bounds")
}
func (t *T3) Project(ctx context.Context, target Target, id string) (Project, error) {
	var p Project
	err := t.Calls.Call(ctx, "t3_project_read", map[string]any{"environmentId": target.EnvironmentID, "projectId": id}, &p)
	return p, err
}
func (t *T3) RegisterProject(ctx context.Context, target Target, root string) (Project, error) {
	var p Project
	err := t.Calls.Call(ctx, "t3_project_create", map[string]any{"environmentId": target.EnvironmentID, "workspaceRoot": root, "title": workspaceDirectoryName(root), "createWorkspaceRootIfMissing": false}, &p)
	return p, err
}
