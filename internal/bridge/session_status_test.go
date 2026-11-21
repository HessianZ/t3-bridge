package bridge

import (
	"errors"
	"strings"
	"testing"
)

func TestSessionStatusDistinguishesApprovalAndDelivery(t *testing.T) {
	e, x, lark, _ := testEngine(t)
	accept(t, e, message("approval-run", "/nas 核对"))
	tick(t, e)
	x.observation.PendingApprovals = 2
	tick(t, e)
	task := onlyTask(t, e)
	e.Store.Close()
	store, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e.Store = store
	runStatus := func(id string) string {
		t.Helper()
		return runSessionStatusCommand(t, e, message(id, "/status")).Reply
	}
	if reply := runStatus("after-restart"); !strings.Contains(reply, "等待 T3 审批或问题（2 项）") || strings.Contains(reply, "待送达") {
		t.Fatalf("approval status after restart: %s", reply)
	}

	x.observation.PendingApprovals = 0
	x.observation.Status, x.observation.Summary = "completed", "SYNTHETIC_RESULT_BODY"
	if err := e.advance(t.Context(), store.task(task.ID)); err != nil {
		t.Fatal(err)
	}
	if reply := runStatus("before-delivery"); !strings.Contains(reply, "已完成") || !strings.Contains(reply, "终态回复：待送达") ||
		strings.Contains(reply, "审批") || strings.Contains(reply, x.observation.Summary) {
		t.Fatalf("terminal status before final card delivery: %s", reply)
	}
	lark.fail = true
	if err := e.notifyResult(t.Context(), store.task(task.ID)); err != nil {
		t.Fatal(err)
	}
	if reply := runStatus("failed-delivery"); !strings.Contains(reply, "回复待核对") {
		t.Fatalf("failed delivery status: %s", reply)
	}
	lark.fail = false
	if err := e.notifyResult(t.Context(), store.task(task.ID)); err != nil {
		t.Fatal(err)
	}
	if reply := runStatus("after-delivery"); strings.Contains(reply, "待送达") || strings.Contains(reply, "回复待核对") || x.sends != 1 {
		t.Fatalf("confirmed delivery still pending: %s", reply)
	}
}

func TestSessionStatusQueueCountsOnlyOwnedPendingInput(t *testing.T) {
	c := testConfig(t)
	e := Engine{Config: c}
	m := message("status", "/status")
	task := &Task{ID: "current", Source: m, Phase: "waiting_capacity", RouteID: "nas", Target: c.Routes["nas"]}
	pinned := message("pinned", "已关联的续聊")
	pinned.SessionTask = task.ID
	referenced := message("referenced", "原消息续聊")
	referenced.ParentID = "current-reply"
	rootOnly := message("fresh", "尚未分流")
	rootOnly.RootID = referenced.ParentID
	cancelled := pinned
	cancelled.ID, cancelled.Cancelled = "cancelled", true
	foreign := pinned
	foreign.ID, foreign.UserID = "foreign", "other"
	otherTopic := pinned
	otherTopic.ID, otherTopic.TopicID = "outside", "another-topic"
	control := pinned
	control.ID, control.Text = "control", "/status"
	st := State{
		Inbox:    []Message{m, pinned, referenced, rootOnly, cancelled, foreign, otherTopic, control},
		Tasks:    map[string]*Task{task.ID: task},
		Bindings: map[string]string{key(m, referenced.ParentID): task.ID},
	}
	reply := e.sessionStatus(&st, m, task, "nas")
	if !strings.Contains(reply, "状态：等待并发额度 · 排队：2 · 待分流：1") || strings.Contains(reply, "本应用并发") {
		t.Fatalf("incorrect scoped status: %s", reply)
	}
}

func TestSessionStatusAppCapacityAndRevocation(t *testing.T) {
	e, _, lark, _ := testEngine(t)
	e.Config.DefaultRoute, e.Config.Admins = "nas", []string{"user"}
	if err := e.Store.update(func(st *State) error {
		st.AccountID = e.Config.AppID
		st.Tasks["held"] = &Task{Phase: "stopped", NeedsReconcile: true, Summary: "SYNTHETIC_PRIVATE"}
		st.Tasks["waiting"] = &Task{Phase: "waiting_capacity"}
		st.Tasks["resource"] = &Task{Phase: "waiting_resource"}
		st.Settings.MaxConcurrent = 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	admin := message("admin-status", "/status")
	admin.ChatType = "p2p"
	ownerReply := runSessionStatusCommand(t, e, admin)
	if !ownerReply.ChatAdminReply || !strings.Contains(ownerReply.Reply, "占用 1 · 上限 1 · 等额度 1") ||
		strings.Contains(ownerReply.Reply, "SYNTHETIC_PRIVATE") {
		t.Fatalf("wrong application aggregate: %s", ownerReply.Reply)
	}
	group, member := admin, admin
	group.ID, group.ChatType = "group-status", "group"
	member.ID, member.UserID = "member-status", "other"
	for _, m := range []Message{group, member} {
		if cmd := runSessionStatusCommand(t, e, m); cmd.ChatAdminReply || strings.Contains(cmd.Reply, "本应用并发") {
			t.Fatalf("application aggregate escaped private admin scope: %q", cmd.Reply)
		}
	}

	lark.replyErr = errors.New("synthetic reply lost")
	admin.ID = "retry-status"
	if cmd := runSessionStatusCommand(t, e, admin); !cmd.ChatAdminReply || cmd.Issue == "" {
		t.Fatal("missing pending private status receipt")
	}
	afterSendAttempt := len(lark.replies)
	e.Config.Admins = nil
	lark.replyErr = nil
	if err := e.consume(t.Context(), admin); err != nil {
		t.Fatal(err)
	}
	st := e.Store.snapshot()
	if cmd := st.Commands[key(admin, admin.ID)]; cmd.ChatAdminReply || strings.Contains(cmd.Reply, "本应用并发") ||
		len(lark.replies) != afterSendAttempt || len(st.Inbox) != 0 {
		t.Fatal("revoked admin status was replayed")
	}
}

func runSessionStatusCommand(t *testing.T, e *Engine, m Message) *SessionCommand {
	t.Helper()
	accept(t, e, m)
	st := e.Store.snapshot()
	queued, ok := queuedPresent(&st, m)
	if !ok {
		t.Fatal("missing status input")
	}
	if err := e.consume(t.Context(), queued); err != nil {
		t.Fatal(err)
	}
	return e.Store.snapshot().Commands[key(m, m.ID)]
}
