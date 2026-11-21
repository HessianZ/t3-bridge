package bridge

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestDocTaskControlRejectsOutOfScopeCommands(t *testing.T) {
	for _, name := range []string{"non-admin", "group admin", "unauthorized private scope", "stale round", "other app", "target changed", "ordinary chat"} {
		t.Run(name, func(t *testing.T) {
			e, x, lark, _ := testEngine(t)
			e.Config.CommentRoute, e.Config.DefaultRoute = "mac", "mac"
			e.Config.Admins = []string{"user"}
			task := stage66Task(e, "selected", true)
			admin := stage66PrivateMessage("control", "")
			want := "管理员"
			switch name {
			case "non-admin":
				admin.UserID = "other"
			case "group admin":
				admin.ChatType = "group"
			case "unauthorized private scope":
				admin.TopicID = "outside"
			default:
				want = "令牌"
			}
			if name == "other app" {
				task.ReceiverAppID = "synthetic-other-app"
			}
			if name == "ordinary chat" {
				task = stage66Task(e, "ordinary", false)
			}
			token := docTaskToken(task)
			if name == "stale round" {
				task.RequestID = "synthetic-replacement-request"
				if docTaskToken(task) == token {
					t.Fatal("changing only the request ID reused the previous token")
				}
			}
			stage66Save(t, e, func(st *State) {
				st.AccountID = e.Config.AppID
				st.Tasks[task.ID] = task
				st.Current = map[string]string{sessionKey(task.Source, task.RouteID, task.Target): task.ID}
			})
			if name == "target changed" {
				target := e.Config.Routes["mac"]
				target.ProjectID = "synthetic-replacement-project"
				e.Config.Routes["mac"] = target
			}
			e.idleRuns = map[string]idleRunStreak{task.ID: {RequestID: task.RequestID}}
			before := e.Store.snapshot()
			for _, args := range []struct{ word, suffix string }{{"/stop", ""}, {"/timeout", " 120"}} {
				admin.ID, admin.Text = args.word, args.word+" comment:"+token+args.suffix
				cmd := stage66Command(t, e, lark, admin, task)
				if !strings.Contains(cmd.Reply, want) {
					t.Errorf("%s refusal = %q, want %q", args.word, cmd.Reply, want)
				}
			}
			after := e.Store.snapshot()
			if !reflect.DeepEqual(after.Tasks, before.Tasks) || !maps.Equal(after.Current, before.Current) ||
				!maps.Equal(after.IdleTimeouts, before.IdleTimeouts) || !maps.Equal(after.Timeouts, before.Timeouts) ||
				e.idleRuns[task.ID].RequestID != task.RequestID || x.stops != 0 || x.sends != 0 {
				t.Fatal("a refused command changed a task, session, timeout, sample, or execution")
			}
		})
	}
}

func TestDocTaskControlStopsOnlySelectedRound(t *testing.T) {
	e, x, lark, _ := testEngine(t)
	e.Config.CommentRoute, e.Config.DefaultRoute = "mac", "mac"
	e.Config.access = &accessRuntime{owner: "user"}
	selected := stage66Task(e, "older-round", true)
	current := stage66Task(e, "current-round", true)
	ordinary := stage66Task(e, "ordinary", false)
	scope := sessionKey(current.Source, current.RouteID, current.Target)
	stage66Save(t, e, func(st *State) {
		for _, task := range []*Task{selected, current, ordinary} {
			st.Tasks[task.ID] = task
		}
		st.Current = map[string]string{scope: current.ID, sessionKey(ordinary.Source, ordinary.RouteID, ordinary.Target): ordinary.ID}
	})
	queuedChat := stage66PrivateMessage("queued-chat", "synthetic private follow-up")
	queuedComment := current.Source
	queuedComment.ID = "queued-comment"
	accept(t, e, queuedChat)
	accept(t, e, queuedComment)
	xStop := &stage66StopExecutor{fakeExecutor: x}
	e.T3 = xStop

	token := docTaskToken(selected)
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(token) {
		t.Fatalf("expected a bare 12-character hexadecimal token, got %q", token)
	}
	stop := stage66PrivateMessage("stop", "/stop comment:"+token)
	accept(t, e, stop)
	cmd := stage66Command(t, e, lark, stop, selected, current, ordinary)
	if !strings.Contains(cmd.Reply, "已请求停止") {
		t.Fatalf("expected stop acknowledgment, got %q", cmd.Reply)
	}
	st := e.Store.snapshot()
	if task := st.Tasks[selected.ID]; !task.StopRequested || task.StopSent || task.Phase != "tracking" || x.stops != 0 {
		t.Fatalf("command should mark the exact round for the engine to interrupt: %+v", task)
	}
	if st.Tasks[current.ID].StopRequested || st.Tasks[ordinary.ID].StopRequested || st.Current[scope] != current.ID {
		t.Fatal("stopping a detached round affected a current session")
	}
	for _, m := range []Message{queuedChat, queuedComment} {
		if q, ok := queuedPresent(&st, m); !ok || q.Cancelled {
			t.Errorf("stop removed or cancelled unrelated input %s", m.ID)
		}
	}
	tick(t, e)
	if x.stops != 1 || x.sends != 0 || xStop.taskID != selected.ID || xStop.requestID != selected.RequestID || xStop.runID != selected.RunID ||
		e.Store.task(selected.ID).Phase != "stopped" || e.Store.task(current.ID).StopRequested || e.Store.task(ordinary.ID).StopRequested {
		t.Fatalf("engine interruption did not preserve the selected round: stops=%d task=%q request=%q run=%q", x.stops, xStop.taskID, xStop.requestID, xStop.runID)
	}
}

func TestDocTaskControlPersistsTimeoutForSelectedCommentScope(t *testing.T) {
	e, x, lark, _ := testEngine(t)
	e.Config.CommentRoute, e.Config.DefaultRoute = "mac", "mac"
	e.Config.Admins = []string{"user"}
	task := stage66Task(e, "selected", true)
	active := task.Source
	active.Comment = &CommentSource{FileType: "docx", FileToken: task.Source.Comment.FileToken, CommentID: "synthetic-active-comment", ReplyID: "synthetic-active-reply"}
	task.ReplySource, task.DispatchSource = &active, active
	ordinary := stage66Task(e, "ordinary", false)
	sibling := stage66Task(e, "sibling-commenter", true)
	sibling.Source.UserID = "user"
	sibling.Source.Comment.CommentID = active.Comment.CommentID
	sibling.DispatchSource = sibling.Source
	docScope := sessionKey(task.Source, task.RouteID, task.Target)
	scope := idleTimeoutScope(active, task.RouteID, task.Target)
	originalScope := idleTimeoutScope(task.Source, task.RouteID, task.Target)
	otherScope := idleTimeoutScope(sibling.Source, sibling.RouteID, sibling.Target)
	ordinaryScope := sessionKey(ordinary.Source, ordinary.RouteID, ordinary.Target)
	stage66Save(t, e, func(st *State) {
		st.Settings.IdleTimeoutMinutes = 7
		st.Current = map[string]string{docScope: task.ID, ordinaryScope: ordinary.ID, sessionKey(sibling.Source, sibling.RouteID, sibling.Target): sibling.ID}
		st.IdleTimeouts = map[string]int{originalScope: 11, otherScope: 13, ordinaryScope: 17}
		st.Timeouts = map[string]int{docScope: task.TimeoutSeconds, ordinaryScope: ordinary.TimeoutSeconds}
		for _, entry := range []*Task{task, sibling, ordinary} {
			st.Tasks[entry.ID] = entry
		}
	})
	before := e.Store.snapshot()
	run := func(id, suffix string) *SessionCommand {
		t.Helper()
		return stage66Command(t, e, lark, stage66PrivateMessage(id, "/timeout comment:"+docTaskToken(task)+suffix), task, sibling, ordinary)
	}
	for _, tc := range []struct {
		id, args string
		minutes  int
		override bool
		mutates  bool
	}{
		{"query", "", 4, false, false},
		{"set", " 120", 120, true, true},
		{"off", " off", 0, true, true},
		{"default", " default", 7, false, true},
		{"switched", " 1", 7, false, false},
	} {
		if tc.id == "switched" {
			replacement := stage66Task(e, "replacement", true)
			stage66Save(t, e, func(st *State) {
				st.Tasks[replacement.ID] = replacement
				st.Current[docScope] = replacement.ID
			})
		}
		sample := idleRunStreak{RequestID: task.RequestID, RunID: task.RunID}
		e.idleRuns = map[string]idleRunStreak{task.ID: sample, ordinary.ID: {RequestID: ordinary.RequestID}}
		cmd := run(tc.id, tc.args)
		if tc.id == "query" && (!strings.Contains(cmd.Reply, "7分钟") || !strings.Contains(cmd.Reply, "当前轮次：4分钟")) {
			t.Errorf("query did not report both inherited and active timeout: %q", cmd.Reply)
		}
		if tc.id == "switched" && !strings.Contains(cmd.Reply, "已切换") {
			t.Errorf("expected switched session refusal, got %q", cmd.Reply)
		}
		st, err := readStateSnapshot(e.Config.StateDir)
		if err != nil {
			t.Fatal(err)
		}
		wantOverrides := maps.Clone(before.IdleTimeouts)
		if tc.override {
			wantOverrides[scope] = tc.minutes
		}
		got := st.Tasks[task.ID]
		if !maps.Equal(st.IdleTimeouts, wantOverrides) || got.IdleTimeoutMinutes != tc.minutes ||
			got.TimeoutSeconds != task.TimeoutSeconds || got.DeadlineAt != task.DeadlineAt ||
			!maps.Equal(st.Timeouts, before.Timeouts) || st.Settings.IdleTimeoutMinutes != 7 ||
			!reflect.DeepEqual(st.Tasks[ordinary.ID], before.Tasks[ordinary.ID]) || !reflect.DeepEqual(st.Tasks[sibling.ID], before.Tasks[sibling.ID]) ||
			(tc.id == "switched" && st.Tasks[st.Current[docScope]].IdleTimeoutMinutes != 4) {
			t.Errorf("%s affected the wrong durable scope: active=%d, overrides=%v", tc.id, got.IdleTimeoutMinutes, st.IdleTimeouts)
		}
		if observed, ok := e.idleRuns[task.ID]; ok == tc.mutates || (!tc.mutates && observed != sample) ||
			e.idleRuns[ordinary.ID].RequestID != ordinary.RequestID {
			t.Errorf("%s changed the wrong idle sampling streak: %v", tc.id, e.idleRuns)
		}
		if tc.id == "set" {
			next := active
			next.ID = "next-comment-input"
			e.startNextTurn(&st, got, next, got.RouteID, got.Target, true)
			if got.IdleTimeoutMinutes != 120 || got.RequestID == task.RequestID {
				t.Errorf("continuation did not inherit the persisted override with a new request: minutes=%d request=%q", got.IdleTimeoutMinutes, got.RequestID)
			}
		}
	}
	if x.stops != 0 || x.sends != 0 {
		t.Fatal("timeout controls should perform no T3 operations")
	}
}

func TestDocTaskControlRevocationFencesFailedReplyRetry(t *testing.T) {
	for _, args := range []struct{ word, suffix string }{{"/stop", ""}, {"/timeout", " 19"}} {
		t.Run(args.word[1:], func(t *testing.T) {
			e, x, lark, _ := testEngine(t)
			e.Config.CommentRoute, e.Config.DefaultRoute = "mac", "mac"
			e.Config.Admins = []string{"user"}
			task := stage66Task(e, "selected", true)
			stage66Save(t, e, func(st *State) {
				st.Tasks[task.ID] = task
				st.Current = map[string]string{sessionKey(task.Source, task.RouteID, task.Target): task.ID}
			})
			m := stage66PrivateMessage("retry", args.word+" comment:"+docTaskToken(task)+args.suffix)
			accept(t, e, m)
			lark.replyErr = errors.New("synthetic reply failed")
			first := stage66Command(t, e, lark, m, task)
			before := e.Store.snapshot()
			if !first.DocAdminReply || first.Issue == "" || first.ReplyAt == "" || len(before.Inbox) != 1 ||
				(args.word == "/stop" && !before.Tasks[task.ID].StopRequested) ||
				(args.word == "/timeout" && before.Tasks[task.ID].IdleTimeoutMinutes != 19) {
				t.Fatalf("expected a committed action with an unconfirmed reply: %+v", first)
			}
			stage66Save(t, e, func(st *State) { st.AccessPolicy.Admins = map[string]bool{"user": false} })
			e.publishPolicy()
			lark.replyErr = nil
			if handled, err := e.sessionControl(context.Background(), m); err != nil || !handled {
				t.Fatalf("handling retry: handled=%v, err=%v", handled, err)
			}
			after := e.Store.snapshot()
			retry := after.Commands[key(m, m.ID)]
			if len(lark.replies) != 1 || len(after.Inbox) != 0 || len(after.Commands) != 1 || retry == nil ||
				retry.DocAdminReply || retry.Issue != "" || retry.ReplyAt != first.ReplyAt || !strings.Contains(retry.Reply, "权限已撤销") ||
				!reflect.DeepEqual(after.Tasks, before.Tasks) || !maps.Equal(after.Current, before.Current) ||
				!maps.Equal(after.IdleTimeouts, before.IdleTimeouts) || x.stops != 0 || x.sends != 0 {
				t.Fatalf("revoked retry sent or reapplied the original command: %+v, replies=%q", retry, lark.replies)
			}
		})
	}
}

type stage66StopExecutor struct {
	*fakeExecutor
	taskID, requestID, runID string
}

func (x *stage66StopExecutor) Stop(ctx context.Context, target Target, t *Task) error {
	x.taskID, x.requestID, x.runID = t.ID, t.RequestID, t.RunID
	return x.fakeExecutor.Stop(ctx, target, t)
}

func stage66PrivateMessage(id, text string) Message {
	m := message(id, text)
	m.ChatType = "p2p"
	return m
}

func stage66Task(e *Engine, id string, comment bool) *Task {
	source := stage66PrivateMessage("message-"+id, "synthetic-private-text-"+id)
	source.Quote = "synthetic-private-quote-" + id
	if comment {
		source.UserID, source.ChatID, source.ChatType, source.TopicID = "other", "document:synthetic", "", ""
		source.Comment = &CommentSource{FileType: "docx", FileToken: "synthetic-private-doc", CommentID: "synthetic-comment", ReplyID: "synthetic-reply-" + id}
	}
	return &Task{
		ID: id, ReceiverAppID: e.Config.AppID, RequestID: "synthetic-request-" + id,
		Source: source, DispatchSource: source, Phase: "tracking", RouteID: "mac", Target: e.Config.Routes["mac"],
		ThreadID: "synthetic-thread-" + id, RunID: "synthetic-run-" + id,
		RequestMessage: "synthetic-private-prompt-" + id, Result: "synthetic-private-result-" + id,
		IdleTimeoutMinutes: 4, TimeoutSeconds: 300, DeadlineAt: "2099-01-01T00:00:00Z",
	}
}

func stage66Save(t *testing.T, e *Engine, update func(*State)) {
	t.Helper()
	if err := e.Store.update(func(st *State) error { update(st); return nil }); err != nil {
		t.Fatal(err)
	}
}

func stage66Command(t *testing.T, e *Engine, lark *fakeMessenger, m Message, tasks ...*Task) *SessionCommand {
	t.Helper()
	n := len(lark.replies)
	if handled, err := e.sessionControl(context.Background(), m); err != nil || !handled {
		t.Fatalf("%s: handled=%v, err=%v", m.Text, handled, err)
	}
	cmd := e.Store.snapshot().Commands[key(m, m.ID)]
	if cmd == nil || cmd.Reply == "" || cmd.Card != nil || cmd.ReplyCard || len(lark.replies) != n+1 || lark.replies[n] != cmd.Reply {
		t.Fatalf("%s expected one plain metadata reply, got %+v and %q", m.Text, cmd, lark.replies[n:])
	}
	for _, task := range tasks {
		hidden := []string{task.Source.Text, task.Source.Quote, task.RequestMessage, task.Result, task.RequestID,
			task.ThreadID, task.RunID, task.Target.ProjectID, task.Target.EnvironmentID}
		for _, source := range []Message{task.Source, task.DispatchSource} {
			if c := source.Comment; c != nil {
				hidden = append(hidden, c.FileToken, c.CommentID, c.ReplyID)
			}
		}
		for _, value := range hidden {
			if value != "" && strings.Contains(cmd.Reply, value) {
				t.Errorf("%s disclosed task content or identifiers %q in %q", m.Text, value, cmd.Reply)
			}
		}
	}
	return cmd
}
