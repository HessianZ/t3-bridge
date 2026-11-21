package bridge

import (
	"context"
	"fmt"
	"slices"
	"testing"
)

func TestRunCapacityBoundary(t *testing.T) {
	ctx := context.Background()
	e, x, _, _ := testEngine(t)
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	limit := 50
	if view, err := e.LocalRunSettings(ctx, RunSettingsPatch{MaxConcurrent: &limit}); err != nil || view.MaxConcurrent != limit {
		t.Fatalf("setting capacity to %d: got %+v, err = %v", limit, view, err)
	}
	if err := e.Store.update(func(st *State) error {
		for i := range 48 {
			id := fmt.Sprintf("active-%d", i)
			st.Tasks[id] = &Task{ID: id, Phase: "tracking", ThreadID: id, RunID: x.observation.RunID, TextReply: true}
		}
		const unresolved = "unconfirmed-stop"
		task := &Task{ID: unresolved, Phase: "stopped", ThreadID: unresolved, RunID: "unconfirmed-run", StopRequested: true, StopSent: true, NeedsReconcile: true}
		task.NotifyHash = notificationHash(task)
		st.Tasks[unresolved] = task
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var sentRequests []string
	x.onSend = func(task *Task) { sentRequests = append(sentRequests, task.RequestID) }
	fiftieth := message("fiftieth", "/nas 首个边界任务")
	accept(t, e, fiftieth)
	tick(t, e)
	running := e.Store.task(stable(fiftieth.ChatID, fiftieth.TopicID, fiftieth.ID))
	if running == nil || running.Phase != "tracking" || !slices.Equal(sentRequests, []string{running.RequestID}) {
		t.Fatalf("50th task did not dispatch: task = %+v, sent = %v", running, sentRequests)
	}

	fiftyFirst := message("fifty-first", "/nas 排队任务")
	fiftyFirst.UserID = "other"
	accept(t, e, fiftyFirst)
	tick(t, e)
	waiting := e.Store.task(stable(fiftyFirst.ChatID, fiftyFirst.TopicID, fiftyFirst.ID))
	if waiting == nil || waiting.Phase != "waiting_capacity" || waiting.ThreadID != "" || x.opens != 1 || x.sends != 1 {
		t.Fatalf("51st task did not stay queued: task = %+v, opens = %d, sends = %d", waiting, x.opens, x.sends)
	}

	// Confirm only the 50th task's run. The other runs and the unresolved stop still occupy capacity.
	active := x.observation
	x.observation = Observation{FoundRequest: true, RunID: running.RunID, Status: "completed", Summary: "合成结果"}
	if err := e.advance(ctx, running); err != nil {
		t.Fatal(err)
	}
	x.observation = active
	if completed := e.Store.task(running.ID); completed == nil || completed.Phase != "completed" || completed.NeedsReconcile {
		t.Fatalf("matched run did not release capacity: %+v", completed)
	}

	tick(t, e)
	tick(t, e)
	resumed := e.Store.task(waiting.ID)
	if resumed == nil || resumed.Phase != "tracking" || resumed.RequestID != waiting.RequestID ||
		!slices.Equal(sentRequests, []string{running.RequestID, waiting.RequestID}) {
		t.Fatalf("queued task did not dispatch exactly once: task = %+v, sent = %v", resumed, sentRequests)
	}
}
