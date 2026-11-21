package bridge

import (
	"context"
	"slices"
	"strings"
	"time"
)

const quietWindow = 600 * time.Millisecond

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}
func immediate(c Config, st *State, m Message) bool {
	if m.MeetingInvite != nil || (m.Meeting != nil && m.Meeting.Room) {
		return false
	}
	return m.ControlTask != "" || m.CommandAction != "" || slashWord(c, routeText(m)) != "" || isStop(c, routeText(m), currentTask(c, st, m, ""))
}
func shortInput(m Message) bool {
	// Each comment has its own reply anchor, even when it shares the document's T3 session.
	return m.Comment == nil && m.ReceivedAt != "" && !m.Cancelled && m.AttachmentIssue == "" && len(m.Parts) == 0 && m.InputKind != "merge_forward" && len([]rune(m.Text)) <= 500 && !strings.Contains(m.Text+m.Quote, "eb1_") && (m.EventID == "" || m.EventInherited)
}
func (e *Engine) coalesce(ctx context.Context, m Message, route string, target Target, bound *Task) (Message, bool) {
	if !shortInput(m) {
		return m, false
	}
	first, err := time.Parse(time.RFC3339Nano, m.ReceivedAt)
	if err != nil {
		return m, false
	}
	st := e.Store.snapshot()
	parts := []Message{m}
	latest := first
	found := false
	maxAttachments := attachmentCountLimit(target.CacheFollowupAttachments && bound != nil && bound.ThreadID != "")
	for _, q := range st.Inbox {
		if q.ID == m.ID {
			found = true
			continue
		}
		if !found || userScope(q) != userScope(m) {
			continue
		}
		if q.RouteAtReceive != "" && q.RouteAtReceive != route {
			break
		}
		if !e.Config.Allowed(q) || !shortInput(q) || q.ParentID != m.ParentID || immediate(e.Config, &st, q) {
			break
		}
		if q.EventID != "" && q.EventID != m.EventID {
			break
		}
		at, err := time.Parse(time.RFC3339Nano, q.ReceivedAt)
		if err != nil || at.Before(first) || at.Sub(first) > 2*time.Second {
			break
		}
		if len(parts) >= 8 || len(q.Images)+len(q.Files)+len(m.Images)+len(m.Files) > maxAttachments {
			break
		}
		qb := boundTask(&st, q)
		if q.ParentID == "" && q.SessionTask == "" {
			qb = nil
		}
		if qb != nil && (bound == nil || qb.ID != bound.ID) {
			break
		}
		c := e.Config
		if q.ParentID == "" && q.SessionTask == "" {
			if e.Classifier == nil || c.DefaultRoute == "" {
				qb = bound
			} else {
				c.DefaultRoute = route
			}
		}
		d, err := Route(ctx, c, q, qb, e.Classifier)
		if err != nil || (d.Action != "new_task" && d.Action != "continue") || d.RouteID != route {
			break
		}
		qt := effectiveTarget(c, &st, q, route)
		if qb != nil {
			qt = qb.Target
		}
		if targetIdentity(qt) != targetIdentity(target) {
			break
		}
		latest = at
		parts = append(parts, q)
		// Count combined resources without trusting attachment names as paths.
		m.Images = append(m.Images, q.Images...)
		m.Files = append(m.Files, q.Files...)
	}
	if e.now().Before(latest.Add(quietWindow)) && e.now().Before(first.Add(2*time.Second)) {
		return parts[0], true
	}
	if len(parts) == 1 {
		return m, false
	}
	merged := parts[len(parts)-1]
	merged.EventID, merged.EventNode, merged.EventCheck, merged.EventRoute, merged.EventInherited = parts[0].EventID, parts[0].EventNode, parts[0].EventCheck, parts[0].EventRoute, parts[0].EventInherited
	merged.Quote = parts[0].Quote
	merged.SessionTask = parts[0].SessionTask
	merged.Images = nil
	merged.Files = nil
	merged.Text = ""
	merged.RoutingText = ""
	merged.Parts = parts
	merged.MergedIDs = nil
	for _, p := range parts {
		merged.MergedIDs = append(merged.MergedIDs, p.ID)
		if merged.Text != "" {
			merged.Text += "\n\n"
		}
		merged.Text += "[合并输入来源：" + p.ID + "]\n" + p.Text
		for _, r := range p.Images {
			if r.MessageID == "" {
				r.MessageID = p.ID
			}
			merged.Images = append(merged.Images, r)
		}
		for _, r := range p.Files {
			if r.MessageID == "" {
				r.MessageID = p.ID
			}
			merged.Files = append(merged.Files, r)
		}
	}
	// The caller atomically persists this round and consumes every original Inbox ID.
	return merged, false
}
func messageIDs(m Message) []string {
	if len(m.MergedIDs) > 0 {
		return m.MergedIDs
	}
	return []string{m.ID}
}
func cancelQueued(c Config, st *State, m Message, bound *Task) {
	route := selectedRoute(c, st, m)
	if bound != nil {
		route = bound.RouteID
	}
	stopTarget := effectiveTarget(c, st, m, route)
	if bound != nil {
		stopTarget = bound.Target
	}
	for i, q := range st.Inbox {
		if userScope(q) != userScope(m) || immediate(c, st, q) {
			continue
		}
		if q.RouteAtReceive != "" && q.RouteAtReceive != route {
			continue
		}
		if r := requestedRoute(c, routeText(q)); r != "" && r != route {
			continue
		}
		qb := boundTask(st, q)
		if q.InputKind == "automatic" && (bound == nil || qb == nil || qb.ID != bound.ID) {
			continue
		}
		if q.ParentID == "" && q.ControlTask == "" && q.SessionTask == "" {
			qb = nil
		}
		qt := effectiveTarget(c, st, q, route)
		if qb != nil {
			if qb.RouteID != route || (bound != nil && qb.ID != bound.ID) {
				continue
			}
			qt = qb.Target
		}
		if targetIdentity(qt) != targetIdentity(stopTarget) {
			continue
		}
		st.Inbox[i].Cancelled = true
	}
}
func queuedPresent(st *State, m Message) (Message, bool) {
	for _, q := range st.Inbox {
		if key(q, q.ID) == key(m, m.ID) {
			return q, true
		}
	}
	return Message{}, false
}
func bindInputs(st *State, m Message, task string) {
	for _, id := range messageIDs(m) {
		st.Bindings[key(m, id)] = task
	}
}
func removeInputs(st *State, m Message) {
	st.Inbox = slices.DeleteFunc(st.Inbox, func(v Message) bool {
		return v.ChatID == m.ChatID && v.TopicID == m.TopicID && slices.Contains(messageIDs(m), v.ID)
	})
}
