package bridge

import (
	"context"
	"slices"
	"testing"
)

type commentReactionCall struct {
	ref    CommentSource
	active bool
}

type commentReactionRecorder struct {
	*fakeMessenger
	record func(CommentSource, bool)
}

func (r commentReactionRecorder) updateCommentWorkingReaction(_ context.Context, ref CommentSource, active bool) {
	r.record(ref, active)
}

func TestCommentReactionRecoversAttemptAfterRestart(t *testing.T) {
	e, executor, messenger, _ := testEngine(t)
	e.Config.CommentRoute = "mac"
	ref := CommentSource{FileType: "docx", FileToken: "document", CommentID: "thread", ReplyID: "reply"}
	var calls []commentReactionCall
	e.Lark = commentReactionRecorder{messenger, func(got CommentSource, active bool) {
		persisted := onlyTask(t, e).CommentReaction
		if persisted == nil || *persisted != got {
			t.Fatalf("reaction was not preceded by its durable target: %+v", persisted)
		}
		calls = append(calls, commentReactionCall{got, active})
	}}
	accept(t, e, Message{ID: "comment:input", ChatID: "document:scope", UserID: "user", Text: "问题", Comment: &ref})
	tick(t, e)
	if !slices.Equal(calls, []commentReactionCall{{ref, true}}) || messenger.notifications != 0 {
		t.Fatalf("unexpected delivery while running: %+v", calls)
	}
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restored.Close() })
	e.Store = restored
	executor.observation = Observation{Status: "completed", RunID: "run", Tasks: []string{"in-flight"}}
	tick(t, e)
	if len(calls) != 1 || messenger.notifications != 0 {
		t.Fatal("restart repeated reaction or cleared it before all work settled")
	}
	executor.observation = Observation{Status: "completed", RunID: "run", Summary: "合成答复"}
	tick(t, e)
	tick(t, e)
	if !slices.Equal(calls, []commentReactionCall{{ref, true}, {ref, false}}) || messenger.notifications != 1 || onlyTask(t, e).CommentReaction != nil {
		t.Fatalf("unexpected reaction or final delivery after restart: %+v", calls)
	}
}
