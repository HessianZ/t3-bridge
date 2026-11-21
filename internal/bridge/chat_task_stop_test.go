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

func TestStage67ChatTaskStopKeepsOtherScopesAndSurvivesReplay(t *testing.T) {
	e, x, lark := stage67StopFixture(t)
	selected := stage67GroupTask(e, "selected")
	selected.Replies = []ReplyRound{{RequestID: "archived-request", RunID: "archived-run", Phase: "completed", Result: "synthetic archived result"}}
	current := stage67GroupTask(e, "current")
	group, topic, owner := stage67GroupTask(e, "other-group"), stage67GroupTask(e, "other-topic"), stage67GroupTask(e, "other-owner")
	group.Source.ChatID, topic.Source.TopicID, owner.Source.UserID = "oc_stage67_other_group", "omt_stage67_other_topic", "user"
	private := stage66Task(e, "admin-private", false)
	private.Source = stage67AdminMessage(private.Source.ID, private.Source.Text)
	siblings := []*Task{current, group, topic, owner, private}
	tasks := append([]*Task{selected}, siblings...)
	stage67SaveTasks(t, e, tasks...)
	for _, task := range []*Task{current, private} {
		input := task.Source
		input.ID, input.Text = "queued-"+task.ID, "synthetic ordinary follow-up"
		accept(t, e, input)
	}
	before := e.Store.snapshot()

	token := chatTaskToken(selected)
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(token) {
		t.Fatalf("chat task token is not bare 12-digit lowercase hex: %q", token)
	}
	stop := stage67AdminMessage("stop", "/stop task:"+token)
	accept(t, e, stop)
	receipt := stage66Command(t, e, lark.fakeMessenger, stop, tasks...)
	if !receipt.ChatAdminReply || receipt.Kind != "/stop" || receipt.ReplyAt == "" || receipt.Issue != "" ||
		!strings.Contains(receipt.Reply, "已请求停止") || len(lark.destinations) != 1 || !reflect.DeepEqual(lark.destinations[0], stop) {
		t.Fatalf("expected one private stop acknowledgment and a receipt: %+v, destinations=%+v", receipt, lark.destinations)
	}
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	e.Store = s
	durable := s.snapshot()
	marked := durable.Tasks[selected.ID]
	if marked == nil || !marked.StopRequested || marked.StopSent || marked.Phase != selected.Phase || x.stops != 0 ||
		!reflect.DeepEqual(durable.Commands[key(stop, stop.ID)], receipt) {
		t.Fatalf("reopening lost the stop intent or receipt before transport: task=%+v, stops=%d", marked, x.stops)
	}
	if !maps.Equal(before.Current, durable.Current) || !reflect.DeepEqual(before.Inbox, durable.Inbox) {
		t.Fatal("the stop command changed Current or ordinary queued input")
	}
	for _, task := range siblings {
		if !reflect.DeepEqual(durable.Tasks[task.ID], before.Tasks[task.ID]) {
			t.Errorf("the stop command changed unrelated task %s", task.ID)
		}
	}

	executor := &stage66StopExecutor{fakeExecutor: x}
	e.T3 = executor
	for range 2 {
		accept(t, e, stop)
		tick(t, e)
	}
	after := s.snapshot()
	stopped := after.Tasks[selected.ID]
	if stopped == nil || x.stops != 1 || x.sends != 0 || executor.taskID != selected.ID || executor.requestID != selected.RequestID || executor.runID != selected.RunID ||
		stopped.Phase != "stopped" || !stopped.StopSent || stopped.ThreadID != selected.ThreadID || !reflect.DeepEqual(stopped.Replies, selected.Replies) {
		t.Fatalf("expected one Stop for the selected request and preserved archived rounds: calls=%d, stopped=%+v", x.stops, stopped)
	}
	for _, task := range siblings {
		if got := after.Tasks[task.ID]; got == nil || got.StopRequested || got.StopSent || got.RequestID != task.RequestID || got.Phase != "tracking" {
			t.Errorf("transport affected unrelated task %s: %+v", task.ID, got)
		}
	}
	// Tick pins pending ordinary input to its already-current task.
	before.Inbox[0].SessionTask, before.Inbox[1].SessionTask = current.ID, private.ID
	if !maps.Equal(before.Current, after.Current) || !reflect.DeepEqual(before.Inbox, after.Inbox) ||
		len(after.Commands) != 1 || !reflect.DeepEqual(after.Commands[key(stop, stop.ID)], receipt) || len(lark.destinations) != 1 {
		t.Fatal("replay changed Current, pending input, or the retained stop acknowledgment")
	}
}

func TestStage67ChatTaskStopRejectsOutOfScopeCommands(t *testing.T) {
	for _, name := range []string{"private member", "group admin", "stale round", "other app", "missing app", "target changed", "unverified chat", "comment", "meeting", "automation", "doctor echo"} {
		t.Run(name, func(t *testing.T) {
			e, x, lark := stage67StopFixture(t)
			task := stage67GroupTask(e, "selected")
			stop := stage67AdminMessage("stop", "")
			privileged, want := true, "令牌"
			switch name {
			case "private member":
				stop.UserID, stop.ChatID = "other", "oc_stage67_member"
				privileged, want = false, "管理员"
			case "group admin":
				stop.ChatType, stop.ChatID, stop.TopicID = task.Source.ChatType, task.Source.ChatID, task.Source.TopicID
				privileged, want = false, "管理员"
			case "other app":
				localToken := chatTaskToken(task)
				task.ReceiverAppID = "synthetic-foreign-app"
				if chatTaskToken(task) == localToken {
					t.Fatal("changing the receiver app reused the token")
				}
			case "missing app":
				task.ReceiverAppID = ""
			case "target changed":
				changed := e.Config.Routes[task.RouteID]
				changed.ProjectID = "synthetic-replacement-project"
				e.Config.Routes[task.RouteID] = changed
			case "unverified chat":
				task.Source.ChatType = ""
			case "comment":
				task.Source.Comment = &CommentSource{FileType: "docx", FileToken: "synthetic-private-doc"}
			case "meeting":
				task.Source.Meeting = &meetingSource{ID: "synthetic-private-meeting"}
			case "automation":
				task.Source.InputKind = "automatic"
			case "doctor echo":
				task.Source.InputKind = doctorEchoKind
			}
			token := chatTaskToken(task)
			if name == "stale round" {
				task.Replies = []ReplyRound{{RequestID: task.RequestID, RunID: task.RunID, Phase: "completed"}}
				task.RequestID, task.RunID = "synthetic-new-request", "synthetic-new-run"
				if chatTaskToken(task) == token {
					t.Fatal("the old token was reused by the new request")
				}
			}
			stage67SaveTasks(t, e, task)
			before := e.Store.snapshot()
			stop.Text = "/stop task:" + token
			accept(t, e, stop)
			cmd := stage66Command(t, e, lark.fakeMessenger, stop, task)
			after := e.Store.snapshot()
			if cmd.ChatAdminReply != privileged || !strings.Contains(cmd.Reply, want) || strings.Contains(cmd.Reply, token) ||
				!reflect.DeepEqual(before.Tasks, after.Tasks) || !maps.Equal(before.Current, after.Current) || x.stops != 0 || x.sends != 0 {
				t.Fatalf("out-of-scope command exposed metadata or changed execution: %+v", cmd)
			}
		})
	}
}

func TestStage67ChatTaskStopRevocationFencesFailedPrivateReply(t *testing.T) {
	e, x, lark := stage67StopFixture(t)
	task := stage66Task(e, "member-private", false)
	task.Source.UserID, task.Source.ChatID, task.Source.TopicID = "other", "oc_stage67_member", ""
	stage67SaveTasks(t, e, task)
	m := stage67AdminMessage("retry", "/stop task:"+chatTaskToken(task))
	accept(t, e, m)
	lark.replyErr = errors.New("synthetic lost private reply")
	first := stage66Command(t, e, lark.fakeMessenger, m, task)
	before := e.Store.snapshot()
	if !first.ChatAdminReply || first.ReplyAt == "" || first.Issue == "" || len(before.Inbox) != 1 || !before.Tasks[task.ID].StopRequested {
		t.Fatalf("expected durably requested stop and a pending private reply: %+v", first)
	}
	stage66Save(t, e, func(st *State) { st.AccessPolicy.Admins = map[string]bool{m.UserID: false} })
	e.publishPolicy()
	lark.replyErr = nil
	if err := e.consume(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	after := e.Store.snapshot()
	receipt := after.Commands[key(m, m.ID)]
	if len(lark.destinations) != 1 || len(after.Inbox) != 0 || len(after.Commands) != 1 || receipt == nil ||
		receipt.Kind != first.Kind || !reflect.DeepEqual(receipt.Source, m) || receipt.ChatAdminReply || receipt.ReplyAt != first.ReplyAt ||
		receipt.Issue != "" || !strings.Contains(receipt.Reply, "权限已撤销") || !reflect.DeepEqual(before.Tasks, after.Tasks) || !maps.Equal(before.Current, after.Current) {
		t.Fatalf("revoked admin received a retry or lost the retained command history: %+v", receipt)
	}
	tick(t, e)
	if x.stops != 1 || x.sends != 0 || e.Store.task(task.ID).Phase != "stopped" || len(lark.destinations) != 1 {
		t.Fatal("revoking reply access affected the committed stop or retried the reply")
	}
}

type stage67StopMessenger struct {
	*fakeMessenger
	destinations []Message
}

func (l *stage67StopMessenger) ReplyControl(ctx context.Context, m Message, text string, card bool) error {
	l.destinations = append(l.destinations, m)
	return l.fakeMessenger.ReplyControl(ctx, m, text, card)
}

func stage67StopFixture(t *testing.T) (*Engine, *fakeExecutor, *stage67StopMessenger) {
	t.Helper()
	e, x, lark, _ := testEngine(t)
	e.Config.DefaultRoute = "mac"
	e.Config.Admins = []string{"user"}
	e.Config.access = &accessRuntime{owner: "ou_stage67_owner"}
	e.Config.Chats, e.Config.Topics = []string{"oc_stage67_group", "oc_stage67_other_group"}, nil
	e.Config.DirectChats = &[]string{"oc_stage67_admin", "oc_stage67_member"}
	spy := &stage67StopMessenger{fakeMessenger: lark}
	e.Lark = spy
	return e, x, spy
}

func stage67AdminMessage(id, text string) Message {
	return Message{ID: id, ChatID: "oc_stage67_admin", ChatType: "p2p", UserID: "user", Text: text}
}

func stage67GroupTask(e *Engine, id string) *Task {
	task := stage66Task(e, id, false)
	task.Source.ChatID, task.Source.ChatType, task.Source.TopicID, task.Source.UserID = "oc_stage67_group", "group", "omt_stage67_topic", "other"
	return task
}

func stage67SaveTasks(t *testing.T, e *Engine, tasks ...*Task) {
	t.Helper()
	stage66Save(t, e, func(st *State) {
		st.AccountID = e.Config.AppID
		st.Current = make(map[string]string)
		for _, task := range tasks {
			task.BaseTargetID = targetIdentity(task.Target)
			task.DispatchSource = task.Source
			st.Tasks[task.ID] = task
			st.Current[sessionKey(task.Source, task.RouteID, task.Target)] = task.ID
		}
	})
}
