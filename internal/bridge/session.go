package bridge

import (
	"encoding/json"
	"slices"
	"strings"
)

// Only maps existing Bridge tasks. Empty entries explicitly archive/reset a context.
func userScope(m Message) string { return stable(m.UserID, m.ChatID, m.TopicID) }
func targetIdentity(t Target) string {
	t.ProjectName, t.ProjectPath = "", ""
	t.HostName, t.Aliases = "", nil
	t.CacheFollowupAttachments = false
	if tool := t.ToolContext; tool.guarded() {
		t.ToolContext = &ToolContext{CLIPath: tool.CLIPath, EnvironmentID: tool.EnvironmentID, Identity: tool.Identity, BotBinding: tool.BotBinding, UserBinding: tool.UserBinding}
	} else {
		t.ToolContext = nil
	}
	t.UnavailableReason = ""
	b, _ := json.Marshal(t)
	return stable(string(b))
}
func sessionKey(m Message, route string, t Target) string {
	return stable(userScope(m), route, targetIdentity(t))
}
func scopedTask(c Config, m Message, t *Task, route string) bool {
	configured, ok := c.Routes[route]
	return t != nil && ok && t.Source.InputKind != doctorEchoKind && t.RouteID == route && t.Source.UserID == m.UserID && t.Source.ChatID == m.ChatID && t.Source.TopicID == m.TopicID && taskTargetAllowed(c, t) && c.TargetAllowed(m, configured, route) &&
		(t.Source.Meeting == nil || c.meetingContentScope(m, route))
}
func selectedRoute(c Config, st *State, m Message) string {
	if m.Comment != nil {
		return c.CommentRoute
	}
	if route := requestedRoute(c, routeText(m)); route != "" {
		return route
	}
	if m.RouteAtReceive != "" {
		return m.RouteAtReceive
	}
	if route := st.Selected[userScope(m)]; route != "" {
		return route
	}
	if m.TopicID != "" {
		chat := m
		chat.TopicID = ""
		if route := st.Selected[userScope(chat)]; route != "" {
			return route
		}
	}
	if c.DefaultRoute != "" {
		return c.DefaultRoute
	}
	route := ""
	for id, t := range c.Routes {
		if id != c.ReviewRoute && c.TargetAllowed(m, t, id) {
			if route != "" {
				return ""
			}
			route = id
		}
	}
	return route
}
func currentTask(c Config, st *State, m Message, route string) *Task {
	if route == "" {
		route = selectedRoute(c, st, m)
		if route == "" {
			var found *Task
			for id := range c.Routes {
				if t := currentTask(c, st, m, id); t != nil {
					if found != nil {
						return nil
					}
					found = t
				}
			}
			return found
		}
	}
	target, ok := c.Routes[route]
	if !ok {
		return nil
	}
	target = effectiveTarget(c, st, m, route)
	if id, known := st.Current[sessionKey(m, route, target)]; known {
		t := st.Tasks[id]
		if scopedTask(c, m, t, route) && t.Source.Meeting == nil {
			return t
		}
		return nil
	}
	// Old records have no current mapping. Adopt only one unambiguous owned candidate.
	var found *Task
	for _, t := range st.Tasks {
		if t.Source.InputKind == "automatic" || t.Source.Meeting != nil || !scopedTask(c, m, t, route) || targetIdentity(t.Target) != targetIdentity(target) || (t.ThreadID == "" && terminal(t.Phase)) || t.StopRequested {
			continue
		}
		if found != nil {
			return nil
		}
		found = t
	}
	return found
}
func setCurrent(c Config, st *State, m Message, t *Task) {
	if !scopedTask(c, m, t, t.RouteID) || t.Source.Meeting != nil {
		return
	}
	if st.Current == nil {
		st.Current = map[string]string{}
	}
	if st.Selected == nil {
		st.Selected = map[string]string{}
	}
	st.Current[sessionKey(m, t.RouteID, t.Target)] = t.ID
	if t.ThreadMeta != nil {
		t.ThreadMeta.Detached = false
	}
	st.Selected[userScope(m)] = t.RouteID
	rememberWorkspace(c, st, m, t.RouteID, t.Target)
}
func sessionHistory(c Config, st *State, m Message, route string) []*Task {
	target := effectiveTarget(c, st, m, route)
	tasks := []*Task{}
	for _, task := range st.Tasks {
		if sessionHistoryCandidate(c, st, m, route, target, task) {
			tasks = append(tasks, task)
		}
	}
	slices.SortFunc(tasks, func(a, b *Task) int {
		if n := strings.Compare(b.Updated, a.Updated); n != 0 {
			return n
		}
		return strings.Compare(b.ID, a.ID)
	})
	return tasks
}

func sessionHistoryCandidate(c Config, st *State, m Message, route string, target Target, task *Task) bool {
	if !scopedTask(c, m, task, route) || task.Source.Meeting != nil || task.ThreadID == "" || task.StopRequested || task.Phase == "failed" || task.Phase == "stopped" {
		return false
	}
	if (task.ReceiverAppID != "" && task.ReceiverAppID != c.AppID) || (st.AccountID != "" && st.AccountID != c.AppID) {
		return false
	}
	return targetIdentity(task.Target) == targetIdentity(target) &&
		(task.Target.ProjectPath == "" || target.ProjectPath == "" || task.Target.ProjectPath == target.ProjectPath)
}

func pinInbox(st *State, m Message, task string) {
	for i, v := range st.Inbox {
		if key(v, v.ID) == key(m, m.ID) {
			st.Inbox[i].SessionTask = task
			return
		}
	}
}

func (e *Engine) queue(m Message, t *Task) error {
	if m.Comment != nil && m.SessionTask == t.ID {
		return nil
	}
	return e.Store.update(func(st *State) error { pinInbox(st, m, t.ID); setCurrent(e.Config, st, m, t); return nil })
}
