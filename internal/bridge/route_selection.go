package bridge

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

const (
	routeSelectionCommand        = "/config route"
	routeSelectionPageSize       = 6
	routeSelectionIDDisplayLimit = 128
	routeSelectionUsage          = "用法：/config route [页码] 或 /config route use <完整路由ID>。"
	routeSelectionStale          = "路由选择已失效。请重新发送 /config route。"
)

func (e *Engine) routeSelectionAllowed(st *State, m Message) bool {
	c := e.Config
	if c.AppID == "" || st.AccountID != c.AppID || m.ChatType != "p2p" ||
		m.UserID == "" || m.ChatID == "" || m.Comment != nil || m.Meeting != nil ||
		m.MeetingInvite != nil || m.MeetingSummary != nil || m.EventID != "" {
		return false
	}
	// An explicit revocation reaches the store before the receiver publishes it.
	if allowed, overridden := st.AccessPolicy.Users[m.UserID]; overridden && !allowed && m.UserID != c.ownerID() {
		return false
	}
	return c.Allowed(m) && !c.userDenied(m.UserID)
}

func (e *Engine) prepareRouteSelection(st *State, m Message, cmd *SessionCommand, origin *SessionCommand, button CommandButton) string {
	cmd.Kind, cmd.RouteID, cmd.Target, cmd.BaseTargetID = routeSelectionCommand, "", Target{}, ""
	cmd.Name, cmd.Card, cmd.ReplyCard = "", nil, false
	if !e.routeSelectionAllowed(st, m) {
		cmd.Reply = "请在当前应用的已授权私聊直接发送 /config route。"
		return ""
	}

	page := 1
	if m.CommandAction != "" {
		cmd.Reply = routeSelectionStale
		if origin == nil || origin.Card == nil || origin.Kind != routeSelectionCommand ||
			button.Command != routeSelectionCommand || userScope(origin.Source) != userScope(m) ||
			!e.routeSelectionAllowed(st, origin.Source) || button.RouteSelectionPage < 1 ||
			button.RouteSelectionID != "" {
			return ""
		}
		page = button.RouteSelectionPage
	} else {
		if !directRouteSelection(m) {
			cmd.Reply = "请直接发送 /config route 查看私聊路由。"
			return ""
		}
		words := strings.Fields(routeText(m))
		if len(words) < 2 || words[0] != "/config" || words[1] != "route" {
			cmd.Reply = routeSelectionUsage
			return ""
		}
		switch {
		case len(words) == 2:
		case len(words) == 3:
			var err error
			page, err = strconv.Atoi(words[2])
			if err != nil || page < 1 || strings.HasPrefix(words[2], "+") {
				cmd.Reply = routeSelectionUsage
				return ""
			}
		case len(words) == 4 && words[2] == "use":
			if _, ok := e.routeSelectionTarget(st, liveRouteSelectionMessage(m), words[3]); ok {
				return words[3]
			}
			cmd.Reply = "该路由不可选。请发送 /config route 查看已授权路由。"
			return ""
		default:
			cmd.Reply = routeSelectionUsage
			return ""
		}
	}

	m = liveRouteSelectionMessage(m)
	current := selectedRoute(e.Config, st, m)
	var rows []CommandButton
	for _, route := range slices.Sorted(maps.Keys(e.Config.Routes)) {
		target, ok := e.routeSelectionTarget(st, m, route)
		if !ok {
			continue
		}
		id := e.routeSelectionID(st, m, current, route, target)
		if id == "" {
			continue
		}
		label := fmt.Sprintf("%s · %s · %s · %s / %s",
			routeSelectionDisplay(route, routeSelectionIDDisplayLimit), routeSelectionDisplay(cmp.Or(target.ProjectName, target.ProjectID), 36),
			routeSelectionDisplay(target.Model, 42), routeSelectionDisplay(target.RuntimeMode, 22),
			routeSelectionDisplay(target.InteractionMode, 12))
		if route == current {
			label += " · 当前"
		}
		rows = append(rows, CommandButton{Label: label, Command: routeSelectionCommand, Name: route, RouteSelectionID: id})
	}
	if len(rows) == 0 {
		cmd.Reply = "当前没有可选的路由。"
		return ""
	}
	pages := (len(rows) + routeSelectionPageSize - 1) / routeSelectionPageSize
	if page > pages {
		cmd.Reply = fmt.Sprintf("页码超出范围，共%d页。%s", pages, routeSelectionUsage)
		return ""
	}
	first := (page - 1) * routeSelectionPageSize
	last := min(first+routeSelectionPageSize, len(rows))
	card := &CommandCard{AppID: e.Config.AppID, Buttons: rows[first:last:last]}
	var reply strings.Builder
	fmt.Fprintf(&reply, "私聊默认路由 %d/%d", page, pages)
	for i, row := range card.Buttons {
		fmt.Fprintf(&reply, "\n%d. %s", i+1, row.Label)
		card.Buttons[i].Label = fmt.Sprintf("选择 %d · %s", i+1, routeSelectionDisplay(row.Name, 48))
	}
	reply.WriteString("\n" + routeSelectionUsage)
	if page > 1 {
		card.Buttons = append(card.Buttons, CommandButton{Label: "上一页", Command: routeSelectionCommand, RouteSelectionPage: page - 1})
	}
	if page < pages {
		card.Buttons = append(card.Buttons, CommandButton{Label: "下一页", Command: routeSelectionCommand, RouteSelectionPage: page + 1})
	}
	cmd.Reply, cmd.Card = reply.String(), card
	return ""
}

// applyRouteSelection runs in the store transaction that records the command receipt.
func (e *Engine) applyRouteSelection(st *State, m Message, cmd *SessionCommand, route string) {
	cmd.Reply = routeSelectionStale
	if !e.routeSelectionAllowed(st, m) {
		return
	}
	var origin *SessionCommand
	var button CommandButton
	if route == "" {
		origin, button = commandCardAction(e.Config, st, m)
		if origin == nil || origin.Card.Used || origin.Kind != routeSelectionCommand ||
			button.Command != routeSelectionCommand || button.RouteSelectionID == "" ||
			userScope(origin.Source) != userScope(m) || !e.routeSelectionAllowed(st, origin.Source) {
			return
		}
		route = button.Name
	} else if m.CommandAction != "" || !directRouteSelection(m) {
		return
	}

	m = liveRouteSelectionMessage(m)
	target, ok := e.routeSelectionTarget(st, m, route)
	if !ok {
		return
	}
	if origin != nil && button.RouteSelectionID != e.routeSelectionID(st, m, selectedRoute(e.Config, st, m), route, target) {
		return
	}
	if workspaceBusy(e.Config, st, m, "") {
		cmd.Reply = "当前私聊仍有进行中、待核对或排队的任务。请等任务结清后再切换路由。"
		return
	}
	if st.Selected == nil {
		st.Selected = map[string]string{}
	}
	st.Selected[userScope(m)] = route
	if origin != nil {
		origin.Card.Used = true
	}
	cmd.Name = route
	cmd.Reply = "已选择私聊默认路由「" + routeSelectionDisplay(route, routeSelectionIDDisplayLimit) +
		"」。下一条普通消息以此作为默认路由。显式路由命令、引用回复和已启用的分类器仍按现有规则处理。"
}

func (e *Engine) routeSelectionReplyAllowed(st *State, cmd *SessionCommand) bool {
	if !e.routeSelectionAllowed(st, cmd.Source) {
		return false
	}
	m := liveRouteSelectionMessage(cmd.Source)
	if cmd.Name != "" {
		_, ok := e.routeSelectionTarget(st, m, cmd.Name)
		return ok
	}
	if cmd.Card != nil && cmd.Card.ReplyID == "" {
		if cmd.Card.AppID != e.Config.AppID {
			return false
		}
		current := selectedRoute(e.Config, st, m)
		for _, button := range cmd.Card.Buttons {
			if button.RouteSelectionID == "" {
				continue
			}
			target, ok := e.routeSelectionTarget(st, m, button.Name)
			if !ok || button.RouteSelectionID != e.routeSelectionID(st, m, current, button.Name, target) {
				return false
			}
		}
	}
	return true
}

func directRouteSelection(m Message) bool {
	return m.ParentID == "" && m.RootID == "" && m.Quote == "" &&
		m.ControlTask == "" && m.ControlCard == "" && m.SessionTask == ""
}

func liveRouteSelectionMessage(m Message) Message {
	m.RouteAtReceive, m.ProjectChoices = "", nil
	m.Text, m.RoutingText = "", ""
	return m
}

func (e *Engine) routeSelectionTarget(st *State, m Message, route string) (Target, bool) {
	c := e.Config
	target := effectiveTarget(c, st, m, route)
	if route == "" || route == c.ReviewRoute || target.ProjectID == "" || !c.TargetAllowed(m, target, route) {
		return Target{}, false
	}
	if allowed, overridden := st.AccessPolicy.Routes[route][m.UserID]; overridden && !allowed {
		return Target{}, false
	}
	return target, true
}

func (e *Engine) routeSelectionID(st *State, m Message, current, route string, target Target) string {
	snapshot, err := json.Marshal([2]Target{e.Config.Routes[route], target})
	if err != nil {
		return ""
	}
	scope := userScope(m)
	return stable(routeSelectionCommand, st.AccountID, scope, st.Selected[scope], current, route, string(snapshot))
}

func routeSelectionDisplay(value string, limit int) string {
	value = strings.Join(strings.FieldsFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	}), " ")
	if value == "" || IsAbsoluteWorkspacePath(value) {
		return "—"
	}
	return display(value, limit)
}
