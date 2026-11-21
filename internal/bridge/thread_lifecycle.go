package bridge

import (
	"context"
	"strings"
	"time"
	"unicode"
)

// UI metadata receipts are independent of execution, notification and report fences.
// A receipt belongs to a request, so a manual unsettle is not undone on every poll.
type ThreadMetadata struct {
	Title          string `json:"title,omitempty"`
	TitleChecked   bool   `json:"titleChecked,omitempty"`
	SettledRequest string `json:"settledRequest,omitempty"`
	Detached       bool   `json:"detached,omitempty"`
	Operation      string `json:"operation,omitempty"`
	Attempts       int    `json:"attempts,omitempty"`
	NextAt         string `json:"nextAt,omitempty"`
	Issue          string `json:"issue,omitempty"`
}
type NativeThread struct {
	Title          string
	Settled, Quiet bool
}
type threadMetadataAPI interface {
	InspectThread(context.Context, Target, *Task) (NativeThread, error)
	RenameThread(context.Context, Target, *Task, string) error
	OrganizeThread(context.Context, Target, *Task, string) error
}

func threadTitle(t *Task, events map[string]Event) string {
	if t.Source.Meeting != nil {
		return "T3 Bridge · 会议问答"
	}
	id := t.Source.EventID
	if id == "" {
		id = t.DispatchSource.EventID
	}
	if event, ok := events[id]; ok {
		date := ""
		if at, err := time.Parse(time.RFC3339Nano, event.FailureStart); err == nil {
			date = at.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02 15:04")
		}
		// Identity comes from validated probes, never quoted instructions or raw evidence.
		prefix := "告警解读 · "
		if t.Source.InputKind == "automatic" {
			prefix = "自动调查 · "
		}
		if t.Source.InputKind == "review" {
			prefix = "复核 · "
		}
		return prefix + display(event.Node+"/"+event.Check, 46) + " · " + date
	}
	prefix := "T3 Bridge · "
	if t.Source.InputKind == "review" {
		prefix = "复核 · "
	}
	input := t.Source
	if len(input.Parts) > 0 {
		input = Message{Text: "附件资料"}
		for _, part := range t.Source.Parts {
			if part.InputKind != "image" && part.InputKind != "file" && strings.TrimSpace(routeText(part)) != "" {
				input = part
				break
			}
		}
	}
	text := strings.TrimSpace(routeText(input))
	if input.InputKind == "image" || input.InputKind == "file" || strings.HasPrefix(text, "[合并输入来源：") {
		text = "附件资料"
	}
	for _, command := range []string{"/" + t.RouteID, "/review"} {
		if text == command || strings.HasPrefix(text, command+" ") {
			text = strings.TrimSpace(strings.TrimPrefix(text, command))
		}
	}
	text, _, _ = strings.Cut(text, "\n")
	if strings.Contains(strings.ToUpper(text), "PRIVATE KEY") {
		text = ""
	}
	text = reportURL.ReplaceAllString(text, "[URL已省略]")
	text = display(text, 60)
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, text)
	if text == "" {
		text = "会话"
	}
	return prefix + text
}
func (e *Engine) initialThreadTitle(t *Task) string {
	e.Store.mu.Lock()
	defer e.Store.mu.Unlock()
	return threadTitle(t, e.Store.state.EventRecords)
}

// Called under the Store lock. Scope keys include route, execution policy AND project.
func (e *Engine) lifecycleEligible(st *State, t *Task) bool {
	if t.ThreadID == "" || t.RequestID == "" || t.NeedsReconcile || t.Pending != nil || t.InputOpen ||
		(t.Phase != "completed" && t.Phase != "failed" && t.Phase != "stopped") ||
		(t.StopRequested && !t.StopSent) || t.NotificationIssue != "" || t.NotifyHash == "" || len(t.TaskIDs) > 0 {
		return false
	}
	if !e.lifecycleOwned(st, t) {
		return false
	}
	for _, m := range st.Inbox {
		if m.SessionTask == t.ID || boundTask(st, m) == t ||
			(userScope(m) == userScope(t.Source) && selectedRoute(e.Config, st, m) == t.RouteID && targetIdentity(effectiveTarget(e.Config, st, m, t.RouteID)) == targetIdentity(t.Target)) {
			return false
		}
	}
	return true
}

// Durable launch binding and receiving identity, never a scan of the T3 thread list.
func (e *Engine) lifecycleOwned(st *State, t *Task) bool {
	if t.LegacySessionPath != "" || t.ID != stable(t.Source.ChatID, t.Source.TopicID, t.Source.ID) ||
		(t.ReceiverAppID != e.Config.AppID && !(t.ReceiverAppID == "" && st.AccountID == e.Config.AppID)) ||
		!taskTargetAllowed(e.Config, t) || !e.Config.TargetAllowed(t.Source, t.Target, t.RouteID) {
		return false
	}
	return true
}
func lifecycleSettle(st *State, t *Task) bool {
	// One-shot work stays one-shot even if an explicit reference made it current.
	if t.Source.InputKind == "automatic" || t.Source.InputKind == "review" || t.Source.InputKind == doctorEchoKind || t.DispatchSource.EventID != "" || t.Source.EventID != "" || t.Phase == "failed" || t.Phase == "stopped" {
		return true
	}
	if t.ThreadMeta != nil && t.ThreadMeta.Detached {
		return true
	}
	if t.Source.Meeting != nil {
		if t.Source.Meeting.Room {
			return st.Meetings[t.Source.Meeting.Number].ID != t.Source.Meeting.ID || st.Current[meetingRoomKey(t.Source)] != t.ID
		}
		return st.Meetings[t.Source.Meeting.Number].TaskID != t.ID
	}
	current, known := st.Current[sessionKey(t.Source, t.RouteID, t.Target)]
	// Legacy ambiguous/no-map conversations remain open until an explicit transition.
	return known && current != t.ID
}
func (e *Engine) lifecycleDue(id string) bool {
	e.Store.mu.Lock()
	defer e.Store.mu.Unlock()
	t := e.Store.state.Tasks[id]
	if t != nil && t.ThreadMeta != nil && t.ThreadMeta.TitleChecked && t.ThreadMeta.SettledRequest == t.RequestID {
		return false
	}
	if t == nil || !e.lifecycleEligible(&e.Store.state, t) {
		return false
	}
	m := t.ThreadMeta
	if m == nil {
		return true
	}
	if at, err := time.Parse(time.RFC3339Nano, m.NextAt); err == nil && e.now().Before(at) {
		return false
	}
	if m.Operation != "" && strings.HasPrefix(m.Operation, t.RequestID+":") && m.Attempts >= 3 {
		return false
	}
	return !m.TitleChecked || (lifecycleSettle(&e.Store.state, t) && m.SettledRequest != t.RequestID)
}
func (e *Engine) saveThreadMetadata(t *Task) error {
	return e.Store.update(func(st *State) error {
		if live := st.Tasks[t.ID]; live != nil && live.RequestID == t.RequestID && live.ThreadID == t.ThreadID {
			copy := *t.ThreadMeta
			live.ThreadMeta = &copy
		}
		return nil
	})
}
func (e *Engine) threadLifecycles(ctx context.Context, ids []string) error {
	if _, ok := e.T3.(threadMetadataAPI); !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	count := 0
	for _, id := range ids {
		if count == 2 || ctx.Err() != nil {
			break
		}
		if !e.lifecycleDue(id) {
			continue
		}
		count++
		if err := e.threadLifecycle(ctx, id); err != nil {
			return err
		}
	}
	return nil
}
func (e *Engine) metadataAttempt(t *Task, action string) error {
	m := t.ThreadMeta
	operation := t.RequestID + ":" + action
	if !strings.HasPrefix(m.Operation, t.RequestID+":") {
		m.Attempts = 0
	}
	m.Operation = operation
	if action == "inspect" || action == "unsettle" {
		m.Attempts++
	}
	m.NextAt = e.now().Add(30 * time.Second).UTC().Format(time.RFC3339Nano)
	m.Issue = "线程列表元数据待只读核对；不影响业务结果，不重发任务"
	return e.saveThreadMetadata(t)
}
func (e *Engine) threadLifecycle(ctx context.Context, id string) error {
	api, ok := e.T3.(threadMetadataAPI)
	if !ok || !e.lifecycleDue(id) {
		return nil
	}
	t := e.Store.task(id)
	if t.NotifyHash != notificationHash(t) {
		return nil
	}
	if t.ThreadMeta == nil {
		t.ThreadMeta = &ThreadMetadata{Title: e.initialThreadTitle(t)}
	}
	m := t.ThreadMeta
	if m.Title == "" {
		m.Title = e.initialThreadTitle(t)
	}
	// A crash or lost metadata ACK always reads back before another metadata mutation.
	if err := e.metadataAttempt(t, "inspect"); err != nil {
		return err
	}
	native, err := api.InspectThread(ctx, t.Target, t)
	if err != nil {
		return nil
	}
	if !native.Quiet {
		m.Attempts = 0
		m.Operation = ""
		m.Issue = ""
		return e.saveThreadMetadata(t)
	}
	if !m.TitleChecked {
		// Recognize the placeholder written by the original HomeOps receiver.
		if native.Title == "HomeOps · "+stable(t.ID, "open")[:8] {
			if err := e.metadataAttempt(t, "rename"); err != nil {
				return err
			}
			if err := api.RenameThread(ctx, t.Target, t, m.Title); err != nil {
				return nil
			}
			native, err = api.InspectThread(ctx, t.Target, t)
			if err != nil || native.Title != m.Title {
				return nil
			}
		}
		// Any other existing title is human-owned, including root's one-time cleanup.
		m.TitleChecked = true
	}
	e.Store.mu.Lock()
	live := e.Store.state.Tasks[id]
	eligible := live != nil && live.RequestID == t.RequestID && e.lifecycleEligible(&e.Store.state, live) && lifecycleSettle(&e.Store.state, live)
	e.Store.mu.Unlock()
	if native.Settled {
		m.SettledRequest = t.RequestID
	} else if eligible && native.Quiet {
		if err := e.metadataAttempt(t, "settle"); err != nil {
			return err
		}
		if err := api.OrganizeThread(ctx, t.Target, t, "settle"); err != nil {
			return nil
		}
		native, err = api.InspectThread(ctx, t.Target, t)
		if err != nil || !native.Settled {
			return nil
		}
		m.SettledRequest = t.RequestID
	}
	m.Attempts = 0
	m.Operation = ""
	m.Issue = ""
	return e.saveThreadMetadata(t)
}

// Unsettle is UI metadata, not a retry or permission to revive a stopped task.
// Business Send still happens once under its original durable fence if metadata fails.
func (e *Engine) reopenThread(ctx context.Context, t *Task) error {
	api, ok := e.T3.(threadMetadataAPI)
	if !ok || (t.RunID == "" && len(t.Replies) == 0) {
		return nil
	}
	e.Store.mu.Lock()
	owned := e.lifecycleOwned(&e.Store.state, t)
	e.Store.mu.Unlock()
	if !owned {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if t.ThreadMeta == nil {
		t.ThreadMeta = &ThreadMetadata{Title: e.initialThreadTitle(t)}
	}
	if err := e.metadataAttempt(t, "unsettle"); err != nil {
		return err
	}
	native, err := api.InspectThread(ctx, t.Target, t)
	if err == nil && native.Settled {
		err = api.OrganizeThread(ctx, t.Target, t, "unsettle")
		if err == nil {
			native, err = api.InspectThread(ctx, t.Target, t)
		}
	}
	if err == nil && !native.Settled {
		t.ThreadMeta.Issue = ""
		t.ThreadMeta.Operation = ""
		t.ThreadMeta.Attempts = 0
		t.ThreadMeta.NextAt = ""
	}
	return e.saveThreadMetadata(t)
}
