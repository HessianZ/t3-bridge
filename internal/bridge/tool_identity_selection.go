package bridge

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

const toolIdentityCommand = "/config identity"

func (e *Engine) prepareToolIdentitySelection(st *State, m Message, cmd *SessionCommand, args []string) string {
	if !e.privateAdminAllowed(m) {
		cmd.Reply = "请由所有者或管理员在已授权私聊发送 " + toolIdentityCommand + "。"
		return ""
	}
	cmd.Kind, cmd.RouteID, cmd.Target, cmd.BaseTargetID = toolIdentityCommand, "", Target{}, ""
	if len(args) != 0 && (len(args) != 2 || args[0] != "use") {
		cmd.Reply = "用法：" + toolIdentityCommand + " 或 " + toolIdentityCommand + " use <routeId>"
		return ""
	}
	m = liveToolIdentityMessage(m)
	sourceRoute, source := e.Config.toolIdentitySource(st, m)
	if sourceRoute == "" {
		cmd.Reply = "当前路由需要已授权的本机工具身份绑定和有效工作项目。"
		return ""
	}
	if len(args) == 2 {
		if _, ok := e.Config.toolIdentityDestination(st, m, source, args[1]); ok {
			return args[1]
		}
		cmd.Reply = "该路由不可用于切换工具身份。请发送 " + toolIdentityCommand + " 查看可选路由。"
		return ""
	}

	cmd.Reply = fmt.Sprintf("当前工具身份：%s · %s · %s", display(sourceRoute, 96), source.ToolContext.Identity, display(source.Model, 96))
	card := &CommandCard{AppID: e.Config.AppID}
	for _, route := range slices.Sorted(maps.Keys(e.Config.Routes)) {
		target, ok := e.Config.toolIdentityDestination(st, m, source, route)
		if !ok {
			continue
		}
		id := e.Config.toolIdentitySelectionID(m, sourceRoute, source, route, target)
		if id == "" {
			continue
		}
		label := fmt.Sprintf("%s · %s · %s", display(route, 96), target.ToolContext.Identity, display(target.Model, 96))
		cmd.Reply += "\n" + label + "\n" + toolIdentityCommand + " use " + display(route, 1800)
		card.Buttons = append(card.Buttons, CommandButton{
			Label: "切换至 " + label, Command: toolIdentityCommand, Name: route, ToolIdentityTargetID: id,
		})
	}
	if len(card.Buttons) == 0 {
		cmd.Reply += "\n当前工作项目没有可切换的工具身份路由。"
		return ""
	}
	cmd.Card = card
	return ""
}

// applyToolIdentitySelection runs in the transaction that records the command reply.
func (e *Engine) applyToolIdentitySelection(st *State, m Message, cmd *SessionCommand, route string) {
	cmd.Reply = "工具身份选项已失效。请重新发送 " + toolIdentityCommand + "。"
	if !e.privateAdminAllowed(m) || (st.AccountID != "" && st.AccountID != e.Config.AppID) {
		return
	}
	var origin *SessionCommand
	var button CommandButton
	if route == "" {
		origin, button = commandCardAction(e.Config, st, m)
		if origin == nil || origin.Card.Used || origin.Kind != toolIdentityCommand || button.Command != toolIdentityCommand ||
			userScope(origin.Source) != userScope(m) || !e.privateAdminAllowed(origin.Source) || button.ToolIdentityTargetID == "" {
			return
		}
		route = button.Name
	}
	m = liveToolIdentityMessage(m)
	sourceRoute, source := e.Config.toolIdentitySource(st, m)
	if sourceRoute == "" {
		return
	}
	target, ok := e.Config.toolIdentityDestination(st, m, source, route)
	if !ok || (origin != nil && button.ToolIdentityTargetID != e.Config.toolIdentitySelectionID(m, sourceRoute, source, route, target)) {
		return
	}
	if workspaceBusy(e.Config, st, m, "") {
		cmd.Reply = "当前私聊仍有进行中或排队的任务。请等待任务结束后再切换工具身份。"
		return
	}
	if st.Selected == nil {
		st.Selected = map[string]string{}
	}
	st.Selected[userScope(m)] = route
	if origin != nil {
		origin.Card.Used = true
	}
	cmd.Reply = fmt.Sprintf("已选择工具身份：%s · %s · %s。", display(route, 96), target.ToolContext.Identity, display(target.Model, 96))
}

func liveToolIdentityMessage(m Message) Message {
	m.RouteAtReceive, m.ProjectChoices = "", nil
	m.Text, m.RoutingText = "", ""
	if m.CommandAction != "" {
		m.InputKind = ""
	}
	return m
}

func (c Config) toolIdentitySource(st *State, m Message) (string, Target) {
	route := selectedRoute(c, st, m)
	t := effectiveTarget(c, st, m, route)
	if route == c.ReviewRoute || !t.ToolContext.guarded() || !c.MCP.LocalEnvironment(t.EnvironmentID) || t.ProjectID == "" || !c.TargetAllowed(m, t, route) {
		return "", Target{}
	}
	return route, t
}

func (c Config) toolIdentityDestination(st *State, m Message, source Target, route string) (Target, bool) {
	t := effectiveTarget(c, st, m, route)
	return t, route != c.ReviewRoute && t.ToolContext.guarded() && t.ToolContext.Identity != source.ToolContext.Identity && t.ProviderID != source.ProviderID &&
		t.EnvironmentID == source.EnvironmentID && t.ProjectID == source.ProjectID &&
		(t.ProjectPath == "" || source.ProjectPath == "" || sameWorkspacePath(t.ProjectPath, source.ProjectPath)) &&
		t.RuntimeMode == source.RuntimeMode && t.InteractionMode == source.InteractionMode && c.TargetAllowed(m, t, route)
}

func (c Config) toolIdentitySelectionID(m Message, sourceRoute string, source Target, route string, target Target) string {
	configured, err := json.Marshal([2]Target{c.Routes[sourceRoute], c.Routes[route]})
	if err != nil {
		return ""
	}
	return stable(toolIdentityCommand, c.AppID, userScope(m), sourceRoute, route, string(configured),
		source.ProjectID, source.ProjectName, source.ProjectPath, target.ProjectID, target.ProjectName, target.ProjectPath)
}
