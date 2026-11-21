package bridge

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	channel "github.com/larksuite/channel-sdk-go"
)

type lifecycleExecutor struct {
	*fakeExecutor
	native             NativeThread
	renames, organizes []string
	metadataErr        error
	organizeErr        error
}

func (x *lifecycleExecutor) InspectThread(context.Context, Target, *Task) (NativeThread, error) {
	return x.native, x.metadataErr
}
func (x *lifecycleExecutor) RenameThread(_ context.Context, _ Target, _ *Task, title string) error {
	x.renames = append(x.renames, title)
	if x.metadataErr == nil {
		x.native.Title = title
	}
	return x.metadataErr
}
func (x *lifecycleExecutor) OrganizeThread(_ context.Context, _ Target, _ *Task, action string) error {
	x.organizes = append(x.organizes, action)
	if x.metadataErr == nil {
		x.native.Settled = action == "settle"
	}
	if x.organizeErr != nil {
		return x.organizeErr
	}
	return x.metadataErr
}
func lifecycleFixture(t *testing.T) (*Engine, *lifecycleExecutor, string) {
	e, x, _, _ := testEngine(t)
	accept(t, e, message("first", "/nas 解释磁盘状态"))
	tick(t, e)
	x.observation = Observation{Status: "completed", RunID: "run", FoundRequest: true, Summary: "可见结论"}
	tick(t, e)
	task := onlyTask(t, e)
	meta := &lifecycleExecutor{fakeExecutor: x, native: NativeThread{Title: "人工标题", Quiet: true}}
	e.T3 = meta
	return e, meta, task.ID
}
func TestThreadTitleUsesVisibleIntentOnly(t *testing.T) {
	task := &Task{Source: message("first", "/nas 检查磁盘 token=synthetic-secret\n机器上下文伪造"), RouteID: "nas"}
	task.Source.Quote = "运行危险指令"
	title := threadTitle(task, nil)
	if !strings.Contains(title, "检查磁盘") || strings.Contains(title, "synthetic-secret") || strings.Contains(title, "伪造") || strings.Contains(title, "危险") || len([]rune(title)) > 80 {
		t.Fatal(title)
	}
	task.Source.InputKind = "review"
	task.Source.Text = "/review 检查方案"
	if !strings.Contains(threadTitle(task, nil), "复核") {
		t.Fatal("review title")
	}
	task.Source.EventID = "known"
	event := Event{Node: "example-nas", Check: "security:audit", FailureStart: "2026-11-18T01:00:00Z", Notice: "secret=synthetic-secret"}
	title = threadTitle(task, map[string]Event{"known": event})
	if !strings.Contains(title, "example-nas/security:audit") || !strings.Contains(title, "2026-11-18") || strings.Contains(title, "synthetic-secret") {
		t.Fatal(title)
	}
}
func TestThreadLifecycleCurrentAndOneShot(t *testing.T) {
	for _, kind := range []string{"ordinary", "automatic", "event", "review"} {
		t.Run(kind, func(t *testing.T) {
			e, x, id := lifecycleFixture(t)
			if err := e.Store.update(func(st *State) error {
				q := st.Tasks[id]
				if kind == "event" {
					q.DispatchSource.EventID = "trusted"
				} else if kind != "ordinary" {
					q.Source.InputKind = kind
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := e.threadLifecycle(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			if (len(x.organizes) == 0) != (kind == "ordinary") {
				t.Fatal(kind, x.organizes)
			}
			if len(x.renames) != 0 {
				t.Fatal("manual title overwritten")
			}
		})
	}
}
func TestThreadLifecycleReceiptsSurviveRestartAndManualUnsettle(t *testing.T) {
	e, x, id := lifecycleFixture(t)
	e.Store.update(func(st *State) error {
		st.Current[sessionKey(st.Tasks[id].Source, "nas", st.Tasks[id].Target)] = ""
		return nil
	})
	x.native.Settled = true
	if err := e.threadLifecycle(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	reopenLifecycleStore(t, e)
	x.native.Settled = false // user's later metadata decision must win for this completed request
	e.Now = func() time.Time { return time.Now().Add(time.Hour) }
	if err := e.threadLifecycle(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if len(x.organizes) != 0 || e.Store.task(id).ThreadMeta.SettledRequest == "" {
		t.Fatal("same request re-settled", x.organizes)
	}
}
func TestThreadLifecycleRequiresQuietAndLocalClosure(t *testing.T) {
	for _, bad := range []string{"native", "send", "stop", "notice", "inbox", "pending", "input", "identity", "legacy-current", "failed-stop", "completed-stop"} {
		t.Run(bad, func(t *testing.T) {
			e, x, id := lifecycleFixture(t)
			e.Store.update(func(st *State) error {
				q := st.Tasks[id]
				st.Current[sessionKey(q.Source, q.RouteID, q.Target)] = ""
				switch bad {
				case "native":
					x.native.Quiet = false
				case "send":
					q.NeedsReconcile = true
				case "stop":
					q.Phase = "stopped"
					q.StopRequested = true
					q.StopSent = false
				case "notice":
					q.NotificationIssue = "未送达"
				case "inbox":
					m := message("queued", "继续")
					m.SessionTask = id
					st.Inbox = append(st.Inbox, m)
				case "pending":
					q.Pending = &Message{ID: "queued"}
				case "input":
					q.InputOpen = true
				case "identity":
					q.Target.Model = "foreign"
				case "failed-stop", "completed-stop":
					q.Phase = strings.TrimSuffix(bad, "-stop")
					q.StopRequested = true
					q.StopSent = false
				case "legacy-current":
					st.Current = nil
				}
				return nil
			})
			if err := e.threadLifecycle(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			if len(x.organizes) != 0 {
				t.Fatal("settled before quiet", bad)
			}
		})
	}
}
func TestThreadLifecycleMetadataFailureNeverResends(t *testing.T) {
	e, x, id := lifecycleFixture(t)
	e.Store.update(func(st *State) error {
		q := st.Tasks[id]
		st.Current[sessionKey(q.Source, q.RouteID, q.Target)] = ""
		return nil
	})
	x.metadataErr = errors.New("secret=synthetic-remote")
	now := time.Now()
	e.Now = func() time.Time { return now }
	for i := 0; i < 8; i++ {
		if err := e.threadLifecycle(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Hour)
		if i == 1 {
			reopenLifecycleStore(t, e)
		}
	}
	q := e.Store.task(id)
	if q.ThreadMeta.Attempts != 3 || strings.Contains(q.ThreadMeta.Issue, "synthetic") || x.sends != 1 || q.Result != "可见结论" {
		t.Fatal(q.ThreadMeta, x.sends)
	}
}

func reopenLifecycleStore(t *testing.T, e *Engine) {
	t.Helper()
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	e.Store = s
	t.Cleanup(func() { s.Close() })
}

func TestThreadTitleSDKWrappers(t *testing.T) {
	m := Normalize(&channel.NormalizedMessage{Content: "@_user_1 /review 复核两轮资料", RawContentType: "text", Mentions: []channel.Mention{{Key: "@_user_1", IsBot: true}}})
	m.InputKind = "review"
	title := threadTitle(&Task{Source: m, RouteID: "nas-review"}, nil)
	if title != "复核 · 复核两轮资料" {
		t.Fatal(title)
	}
	for _, withParts := range []bool{false, true} {
		merged := Message{Text: "[合并输入来源：om_secret_id]\nimg_synthetic_private_key", InputKind: "image", Images: []Image{{Key: "img_synthetic_private_key"}}}
		if withParts {
			merged.Parts = []Message{{Text: "img_synthetic_private_key", InputKind: "image"}, {Text: "file_synthetic_private_key", InputKind: "file"}}
		}
		title = threadTitle(&Task{Source: merged, RouteID: "nas"}, nil)
		if title != "T3 Bridge · 附件资料" {
			t.Fatal(title)
		}
		merged.Parts = append(merged.Parts, Message{Text: "请比较这两份资料", InputKind: "text"})
		if !strings.Contains(threadTitle(&Task{Source: merged}, nil), "请比较这两份资料") {
			t.Fatal("mixed attachment intent lost")
		}
	}
}
func TestThreadLifecycleLegacyRenameAndIdentityUnchanged(t *testing.T) {
	e, x, id := lifecycleFixture(t)
	e.Store.update(func(st *State) error { q := st.Tasks[id]; q.ThreadMeta = &ThreadMetadata{Detached: true}; return nil })
	before := e.Store.task(id)
	x.native.Title = "HomeOps · " + stable(id, "open")[:8]
	if err := e.threadLifecycle(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	after := e.Store.task(id)
	if len(x.renames) != 1 || x.renames[0] == "" || len(x.organizes) != 1 || !after.ThreadMeta.Detached {
		t.Fatal(x.renames, x.organizes, after.ThreadMeta)
	}
	before.ThreadMeta = nil
	after.ThreadMeta = nil
	if !reflect.DeepEqual(before, after) {
		t.Fatal("metadata changed execution/reply/Source identity")
	}
	if err := e.threadLifecycle(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if len(x.renames) != 1 || len(x.organizes) != 1 {
		t.Fatal("metadata receipt replayed")
	}
}
func TestThreadLifecycleNewResumeSameThread(t *testing.T) {
	e, x, id := lifecycleFixture(t)
	original := e.Store.task(id)
	x.native.Settled = true
	if err := e.threadLifecycle(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	accept(t, e, message("reset", "/new"))
	tick(t, e)
	if e.Store.snapshot().Current[sessionKey(original.Source, original.RouteID, original.Target)] != "" {
		t.Fatal("new mapping not reset")
	}
	accept(t, e, message("resume", "/resume "+shortTaskID(id)))
	tick(t, e)
	if x.sends != 1 {
		t.Fatal("resume sent without input")
	}
	x.onSend = func(q *Task) {
		if x.native.Settled {
			t.Error("Send preceded unsettle")
		}
		if q.ThreadID != original.ThreadID {
			t.Error("resumed another thread")
		}
	}
	accept(t, e, message("next", "/nas 再解释一下"))
	tick(t, e)
	q := onlyTask(t, e)
	if x.opens != 1 || x.sends != 2 || q.ThreadID != original.ThreadID || len(q.Replies) != 1 || q.Replies[0].Result != original.Result || len(x.organizes) != 1 || x.organizes[0] != "unsettle" {
		t.Fatal("resume did not retain history/thread", x.organizes)
	}
}
func TestThreadLifecycleNewAndProjectScope(t *testing.T) {
	e, x, _ := lifecycleFixture(t)
	accept(t, e, message("new", "/new"))
	tick(t, e)
	if len(x.organizes) != 1 || x.organizes[0] != "settle" {
		t.Fatal("new did not settle closed old scope", x.organizes)
	}
	// A different project's exact Current key must not cause this project's current to retire.
	e2, y, id2 := lifecycleFixture(t)
	e2.Store.update(func(st *State) error {
		q := st.Tasks[id2]
		other := q.Target
		other.ProjectID = "other-project"
		st.Current[sessionKey(q.Source, q.RouteID, other)] = "another-task"
		return nil
	})
	if err := e2.threadLifecycle(context.Background(), id2); err != nil {
		t.Fatal(err)
	}
	if len(y.organizes) != 0 {
		t.Fatal("other project changed current eligibility")
	}
}
func TestThreadLifecycleStopConfirmation(t *testing.T) {
	for _, phase := range []string{"completed", "failed", "stopped"} {
		t.Run(phase, func(t *testing.T) {
			e, x, id := lifecycleFixture(t)
			e.Store.update(func(st *State) error {
				q := st.Tasks[id]
				q.Phase = phase
				q.StopRequested = true
				q.StopSent = false
				q.NotifyHash = notificationHash(q)
				return nil
			})
			if err := e.threadLifecycle(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			if len(x.organizes) != 0 {
				t.Fatal("unconfirmed stop")
			}
			e.Store.update(func(st *State) error {
				q := st.Tasks[id]
				q.StopSent = true
				st.Current[sessionKey(q.Source, q.RouteID, q.Target)] = ""
				return nil
			})
			if err := e.threadLifecycle(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			if len(x.organizes) != 1 {
				t.Fatal("confirmed quiet stop not settled")
			}
		})
	}
}

func TestThreadEventTitlesDistinguishPurposeAndIncidentTime(t *testing.T) {
	event := Event{Node: "example-nas", Check: "security:audit", FailureStart: "2026-11-18T01:15:00Z"}
	task := &Task{Source: Message{EventID: "trusted"}, RouteID: "nas"}
	titles := map[string]bool{}
	for _, kind := range []string{"automatic", "text", "review"} {
		task.Source.InputKind = kind
		title := threadTitle(task, map[string]Event{"trusted": event})
		if titles[title] || !strings.Contains(title, "2026-11-18 09:15") || len([]rune(title)) > 80 {
			t.Fatal(title)
		}
		titles[title] = true
	}
}

type deadlineLifecycleExecutor struct {
	*lifecycleExecutor
	deadlineChecked bool
}

func (x *deadlineLifecycleExecutor) InspectThread(ctx context.Context, _ Target, _ *Task) (NativeThread, error) {
	deadline, ok := ctx.Deadline()
	x.deadlineChecked = ok && time.Until(deadline) > 0 && time.Until(deadline) <= 2*time.Second
	return NativeThread{}, context.DeadlineExceeded
}
func TestThreadReopenBoundedFailureKeepsSendFence(t *testing.T) {
	e, x, id := lifecycleFixture(t)
	dx := &deadlineLifecycleExecutor{lifecycleExecutor: x}
	e.T3 = dx
	x.sendErr = errors.New("unknown Send ACK")
	accept(t, e, message("next", "/nas 再说明一下"))
	tick(t, e)
	q := e.Store.task(id)
	if !dx.deadlineChecked || x.sends != 2 || q.Phase != "needs_reconcile" || q.ThreadMeta.Issue == "" {
		t.Fatal("metadata changed business fence", q.ThreadMeta, q.Phase, x.sends)
	}
	reopenLifecycleStore(t, e)
	tick(t, e)
	if x.sends != 2 || x.stops != 0 {
		t.Fatal("metadata failure replayed Send/Stop")
	}
}
func TestThreadLifecycleRechecksNewStopBeforeOrganize(t *testing.T) {
	e, x, id := lifecycleFixture(t)
	e.Store.update(func(st *State) error {
		q := st.Tasks[id]
		st.Current[sessionKey(q.Source, q.RouteID, q.Target)] = ""
		return nil
	})
	e.T3 = &changingLifecycleExecutor{lifecycleExecutor: x, change: func() { e.Store.update(func(st *State) error { st.Tasks[id].StopRequested = true; return nil }) }}
	if err := e.threadLifecycle(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if len(x.organizes) != 0 || !e.Store.task(id).StopRequested {
		t.Fatal("metadata ignored newer stop")
	}
}

type changingLifecycleExecutor struct {
	*lifecycleExecutor
	change func()
}

func (x *changingLifecycleExecutor) InspectThread(ctx context.Context, t Target, q *Task) (NativeThread, error) {
	x.change()
	return x.lifecycleExecutor.InspectThread(ctx, t, q)
}

func TestThreadLifecycleLegacyProjectSwitch(t *testing.T) {
	e, x, _, _, _ := workspaceFixture(t)
	accept(t, e, message("first", "/nas 旧项目资料"))
	tick(t, e)
	x.observation = Observation{Status: "completed", RunID: "run", FoundRequest: true, Summary: "旧结论"}
	tick(t, e)
	q := onlyTask(t, e)
	oldTarget := q.Target
	if err := e.Store.update(func(st *State) error { st.Tasks[q.ID].ThreadMeta = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	accept(t, e, message("cd", "/cd /srv/alpha"))
	tick(t, e)
	q = e.Store.task(q.ID)
	if q.ThreadMeta == nil || !q.ThreadMeta.Detached || e.Store.snapshot().Current[sessionKey(q.Source, q.RouteID, oldTarget)] != q.ID {
		t.Fatal("project switch lost old mapping or retirement hint")
	}
	meta := &lifecycleExecutor{fakeExecutor: x, native: NativeThread{Quiet: true, Title: "HomeOps · " + stable(q.ID, "open")[:8]}}
	e.T3 = meta
	if err := e.threadLifecycle(context.Background(), q.ID); err != nil {
		t.Fatal(err)
	}
	if len(meta.renames) != 1 || meta.renames[0] == "" || len(meta.organizes) != 1 {
		t.Fatal("legacy empty-title migration", meta.renames, meta.organizes)
	}
}

func TestThreadLifecycleEventRecoveryReopensOriginalThread(t *testing.T) {
	e, base, l, _ := testEngine(t)
	setupReports(t, e)
	e.Config.EventChats = []string{"chat"}
	e.Config.EventRoutes = map[string]string{"example-nas": "nas"}
	fault := syntheticEvent("example-nas", "security:audit", "fault", e.now())
	recovery := fault
	recovery.Phase = "recovery"
	recovery.ID = eventID(recovery.Node, recovery.Check, recovery.FailureStart, "recovery")
	e.Store.update(func(st *State) error {
		st.EventRecords[fault.ID] = fault
		st.EventRecords[recovery.ID] = recovery
		return nil
	})
	x := &lifecycleExecutor{fakeExecutor: base, native: NativeThread{Title: "自动调查标题", Quiet: true}}
	e.T3 = x
	l.message.Quote = "事件：" + fault.ID
	m := message("fault-input", "请解释这次故障")
	m.ParentID = "alert"
	accept(t, e, m)
	tick(t, e)
	first := onlyTask(t, e)
	base.observation = Observation{RunID: "run", Status: "completed", FoundRequest: true, Summary: "只读调查结论"}
	tick(t, e)
	if len(x.organizes) != 1 || x.organizes[0] != "settle" {
		t.Fatal("event current stayed open", x.organizes)
	}
	old := e.Store.task(first.ID)
	l.message.Quote = "事件：" + recovery.ID
	m = message("recovery-input", "解释恢复证据")
	m.ParentID = "recovery-alert"
	accept(t, e, m)
	tick(t, e)
	q := onlyTask(t, e)
	if base.opens != 1 || base.sends != 2 || q.ThreadID != old.ThreadID || q.RequestID == old.RequestID || q.Source.EventID != fault.ID || q.DispatchSource.EventID != recovery.ID || !strings.Contains(q.ReportInstructions, q.RequestID) || strings.Contains(q.ReportInstructions, old.RequestID) || len(q.Replies) != 1 || q.Replies[0].Source.EventID != fault.ID || len(x.organizes) != 2 || x.organizes[1] != "unsettle" {
		t.Fatal("recovery lost thread/round/report separation", x.organizes)
	}
	tick(t, e)
	q = e.Store.task(first.ID)
	if len(x.organizes) != 3 || q.ThreadMeta.SettledRequest != q.RequestID {
		t.Fatal("recovery did not independently close", x.organizes)
	}
}

// Resolved metadata receipts are checked without cloning retained answer history.
func TestThreadLifecycleReceiptFastPathDoesNotCloneHistory(t *testing.T) {
	e, x, id := lifecycleFixture(t)
	e.Store.update(func(st *State) error {
		q := st.Tasks[id]
		q.ThreadMeta = &ThreadMetadata{TitleChecked: true, SettledRequest: q.RequestID}
		q.Source.InputKind = "automatic"
		q.Result = strings.Repeat("history", 1<<17)
		return nil
	})
	allocations := testing.AllocsPerRun(3, func() {
		if e.lifecycleDue(id) {
			t.Fatal("settled receipt still due")
		}
	})
	if allocations != 0 {
		t.Fatalf("metadata predicate copied retained history: %.0f allocations", allocations)
	}
	if len(x.organizes) != 0 {
		t.Fatal("metadata retry hot loop")
	}
}

type titleLaunchExecutor struct {
	*fakeExecutor
	titles, ids []string
	fail        bool
}

func (x *titleLaunchExecutor) Open(c context.Context, target Target, id, title string) (Execution, error) {
	x.titles = append(x.titles, title)
	x.ids = append(x.ids, id)
	if x.fail {
		return Execution{}, errors.New("unknown idle launch")
	}
	return x.fakeExecutor.Open(c, target, id, title)
}
func TestThreadLaunchTitlePersistedBeforeIdleOpen(t *testing.T) {
	e, base, _, _ := testEngine(t)
	x := &titleLaunchExecutor{fakeExecutor: base, fail: true}
	e.T3 = x
	accept(t, e, message("first", "/nas 解释主机磁盘 token=synthetic-private"))
	tick(t, e)
	q := onlyTask(t, e)
	if q.Phase != "opening" || q.ThreadMeta.Title == "" || strings.Contains(q.ThreadMeta.Title, "synthetic-private") {
		t.Fatal("title not persisted safely")
	}
	reopenLifecycleStore(t, e)
	x.fail = false
	tick(t, e)
	if len(x.titles) != 2 || x.titles[0] != x.titles[1] || x.ids[0] != x.ids[1] || !strings.Contains(x.titles[0], "解释主机磁盘") || base.sends != 1 {
		t.Fatal("launch retry changed title/identity", x.titles)
	}
}

func TestThreadLifecycleLostMetadataAckReadbackAfterRestart(t *testing.T) {
	e, x, id := lifecycleFixture(t)
	e.Store.update(func(st *State) error {
		q := st.Tasks[id]
		st.Current[sessionKey(q.Source, q.RouteID, q.Target)] = ""
		return nil
	})
	x.organizeErr = errors.New("lost metadata ACK")
	if err := e.threadLifecycle(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if len(x.organizes) != 1 || !x.native.Settled || e.Store.task(id).ThreadMeta.Issue == "" {
		t.Fatal("unknown metadata receipt not fenced")
	}
	reopenLifecycleStore(t, e)
	e.Now = func() time.Time { return time.Now().Add(time.Hour) }
	if err := e.threadLifecycle(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if len(x.organizes) != 1 || x.sends != 1 || e.Store.task(id).ThreadMeta.SettledRequest == "" {
		t.Fatal("unknown metadata operation was replayed")
	}
}
