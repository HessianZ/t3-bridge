package bridge

import (
	"context"
	"testing"
)

func TestCommentIdleScopeFollowsCurrentReplyAnchor(t *testing.T) {
	e, _, _, _ := testEngine(t)
	e.Config.CommentRoute = "mac"
	route := e.Config.CommentRoute
	target := e.Config.Routes[route]
	first := Message{ID: "first", ChatID: "document:synthetic", UserID: "user",
		Comment: &CommentSource{FileToken: "fixture", CommentID: "anchor-a", ReplyID: "first"}}
	second := first
	second.ID = "second"
	second.Comment = &CommentSource{FileToken: "fixture", CommentID: "anchor-b", ReplyID: "second"}
	followup := second
	followup.ID = "followup"
	followup.Comment = &CommentSource{FileToken: "fixture", CommentID: "anchor-b", ReplyID: "followup"}

	if sessionKey(first, route, target) != sessionKey(second, route, target) ||
		idleTimeoutScope(first, route, target) == idleTimeoutScope(second, route, target) ||
		idleTimeoutScope(second, route, target) != idleTimeoutScope(followup, route, target) {
		t.Fatal("idle timeouts must distinguish comment anchors within a document session")
	}
	if err := e.Store.update(func(st *State) error {
		st.Settings.IdleTimeoutMinutes = 5
		task := e.createTask(st, first, Decision{Action: "new_task", RouteID: route}, "")
		e.startNextTurn(st, task, second, route, target, true)
		cmd := &SessionCommand{}
		if !e.docTaskTimeout(st, cmd, task, []string{"12"}) {
			t.Fatalf("current comment anchor timeout rejected: %s", cmd.Reply)
		}
		if st.idleTimeout(idleTimeoutScope(first, route, target)) != 5 || task.IdleTimeoutMinutes != 12 {
			t.Fatal("timeout did not affect only the selected comment anchor")
		}
		e.startNextTurn(st, task, followup, route, target, true)
		if task.IdleTimeoutMinutes != 12 {
			t.Fatal("follow-up lost comment anchor override")
		}
		e.startNextTurn(st, task, first, route, target, true)
		if task.IdleTimeoutMinutes != 5 {
			t.Fatal("another comment anchor inherited the override")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCommentAnchorsKeepIndependentReplyReceipts(t *testing.T) {
	e, _, _, _ := testEngine(t)
	e.Config.CommentRoute = "mac"
	first := Message{ID: "first", ChatID: "document:synthetic", UserID: "user", Text: "Question A",
		Comment: &CommentSource{FileToken: "fixture", CommentID: "anchor-a"}}
	second := first
	second.ID, second.Text = "second", "Question B"
	second.Comment = &CommentSource{FileToken: "fixture", CommentID: "anchor-b"}
	accept(t, e, first)
	accept(t, e, second)
	queued := e.Store.snapshot().Inbox
	if len(queued) != 2 {
		t.Fatalf("expected both comment deliveries to be queued, got %d", len(queued))
	}
	got, hold := e.coalesce(context.Background(), queued[0], "mac", e.Config.Routes["mac"], nil)
	if hold || got.ID != first.ID || got.Comment.CommentID != first.Comment.CommentID || len(got.MergedIDs) > 0 {
		t.Fatal("a comment must keep its original reply anchor and inbound receipt")
	}
}

func TestCommentStopLeavesNextCommentRunnable(t *testing.T) {
	e, executor, _, _ := testEngine(t)
	e.Config.CommentRoute = "mac"
	e.Config.Admins = []string{"other"}
	first := Message{ID: "first", ChatID: "document:synthetic", UserID: "user", Text: "First comment",
		Comment: &CommentSource{FileToken: "fixture", CommentID: "anchor-a"}}
	accept(t, e, first)
	tick(t, e)
	original := onlyTask(t, e)
	if original.Phase != "tracking" || executor.sends != 1 {
		t.Fatalf("first comment did not reach its run: %+v", original)
	}
	second := first
	second.ID, second.Text = "second", "Second comment"
	second.Comment = &CommentSource{FileToken: "fixture", CommentID: "anchor-b"}
	accept(t, e, second)
	stop := message("stop-comment", "/stop comment:"+docTaskToken(original))
	stop.ChatType, stop.UserID = "p2p", "other"
	accept(t, e, stop)
	tick(t, e)
	if stopped := e.Store.task(original.ID); stopped.Phase != "stopped" || executor.stops != 1 {
		t.Fatalf("original run did not stop: %+v", stopped)
	}
	tick(t, e)
	nextID := stable(second.ChatID, second.TopicID, second.ID)
	next := e.Store.task(nextID)
	if next == nil || next.Phase != "tracking" || executor.opens != 2 || executor.sends != 2 {
		t.Fatalf("next comment did not start independently: %+v", next)
	}
}
