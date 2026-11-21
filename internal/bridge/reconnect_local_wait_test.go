package bridge

import (
	"context"
	"reflect"
	"testing"
)

func TestLocalReconnectWaitSurvivesReopenAndUnconfirmedReply(t *testing.T) {
	e, executor, messenger, _ := testEngine(t)
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	checkWaiting := func(want bool) {
		t.Helper()
		d, err := e.LocalDiagnostics(NewConnections(e.Config, e.Store))
		if err != nil || d.ReconnectWaiting != want {
			t.Fatalf("ReconnectWaiting = %t, %v, want %t", d.ReconnectWaiting, err, want)
		}
	}
	checkWaiting(false)
	accept(t, e, message("run", "/nas 在途请求"))
	tick(t, e)
	original := onlyTask(t, e)
	if original.Phase != "tracking" || original.RunID == "" || original.RequestID == "" || executor.sends != 1 {
		t.Fatalf("expected one tracked run, phase=%s, run=%q, request=%q, sends=%d", original.Phase, original.RunID, original.RequestID, executor.sends)
	}
	next := message("next", "/nas 排队的请求")
	next.UserID = "other"
	accept(t, e, next)

	calls := 0
	e.Reconnect = func(context.Context) (string, error) {
		calls++
		disk, err := readStateSnapshot(e.Config.StateDir)
		if err != nil {
			t.Fatal(err)
		}
		if disk.ReconnectStatus != reconnectUncertain || disk.PendingReconnect != "" || len(disk.ReconnectRuns) != 0 {
			t.Fatal("reconnect began before persisting the attempt and clearing the wait")
		}
		if task := disk.Tasks[original.ID]; task == nil || task.Phase != "completed" ||
			task.RunID != original.RunID || task.RequestID != original.RequestID || task.NotificationIssue == "" {
			t.Fatalf("expected original run to be quiet with an unconfirmed terminal reply, got %+v", task)
		}
		if _, queued := queuedPresent(&disk, next); !queued || executor.sends != 1 || executor.stops != 0 {
			t.Fatal("queued work or the original run changed before reconnect")
		}
		messenger.fail = false // The connection replacement makes Feishu delivery available again.
		return "synthetic clients replaced", nil
	}
	view, err := e.ChangeLocalReconnect(t.Context(), ReconnectAfterRuns)
	if err != nil || view.State != ReconnectScheduled {
		t.Fatalf("schedule = %+v, %v", view, err)
	}
	checkWaiting(true)
	waiting := e.Store.snapshot()
	queuedBefore, present := queuedPresent(&waiting, next)
	if !present || waiting.PendingReconnect == "" || waiting.ReconnectRuns[original.ID] != original.RequestID {
		t.Fatal("scheduled wait did not preserve the queue and capture the original run")
	}
	view, err = e.ChangeLocalReconnect(t.Context(), ReconnectAfterRuns)
	if err != nil || view.State != ReconnectPending || !reflect.DeepEqual(waiting, e.Store.snapshot()) {
		t.Fatalf("repeated wait changed state: %+v, %v", view, err)
	}
	checkPaused := func(phase string) {
		t.Helper()
		st := e.Store.snapshot()
		queued, present := queuedPresent(&st, next)
		task := st.Tasks[original.ID]
		if calls != 0 || executor.sends != 1 || executor.stops != 0 || st.PendingReconnect != waiting.PendingReconnect ||
			!reflect.DeepEqual(st.ReconnectRuns, waiting.ReconnectRuns) || !present || !reflect.DeepEqual(queued, queuedBefore) ||
			task == nil || task.Phase != phase ||
			task.RunID != original.RunID || task.RequestID != original.RequestID || task.ThreadID != original.ThreadID {
			t.Fatalf("wait changed the queue or original run: reconnects=%d, sends=%d, stops=%d, queued=%t, task=%+v", calls, executor.sends, executor.stops, present, task)
		}
	}
	tick(t, e)
	checkPaused("tracking")
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	e.Store = reopened
	checkWaiting(true)
	executor.observation.Status = "completed"
	executor.observation.Tasks = []string{"child"}
	tick(t, e)
	checkPaused("awaiting_children")

	executor.observation.Tasks = nil
	messenger.fail = true
	tick(t, e)
	checkWaiting(false)
	if calls != 1 || e.Store.snapshot().ReconnectStatus != "synthetic clients replaced" {
		t.Fatalf("quiet run with an unconfirmed reply triggered %d reconnects, want 1", calls)
	}
	tick(t, e)
	tick(t, e)
	if task := e.Store.task(original.ID); task == nil || task.NotificationIssue != "" || task.FinalReplyHash == "" ||
		calls != 1 || executor.sends != 2 {
		t.Fatalf("after reconnect: calls=%d, sends=%d, original task=%+v", calls, executor.sends, task)
	}
}

func TestLocalReconnectCancelWait(t *testing.T) {
	for _, source := range []string{"local", "feishu"} {
		t.Run(source, func(t *testing.T) {
			e, executor, _, _ := testEngine(t)
			e.Config.Admins = []string{"user"}
			if err := e.Store.BindAccount(e.Config.AppID); err != nil {
				t.Fatal(err)
			}
			e.Reconnect = func(context.Context) (string, error) {
				t.Fatal("cancelling a wait attempted to reconnect")
				return "", nil
			}
			accept(t, e, message("run", "/nas 在途请求"))
			tick(t, e)
			accept(t, e, message("next", "/nas 排队的请求"))
			if source == "feishu" {
				accept(t, e, message("wait", "/reconnect --wait"))
				tick(t, e)
			} else {
				view, err := e.ChangeLocalReconnect(t.Context(), ReconnectAfterRuns)
				if err != nil || view.State != ReconnectScheduled {
					t.Fatalf("schedule = %+v, %v", view, err)
				}
			}
			waiting := e.Store.snapshot()
			if waiting.PendingReconnect == "" || executor.sends != 1 {
				t.Fatal("expected one wait and one original dispatch before cancellation")
			}
			view, err := e.ChangeLocalReconnect(t.Context(), ReconnectCancelWait)
			cancelled := e.Store.snapshot()
			if err != nil || view.State != ReconnectCancelled || cancelled.PendingReconnect != "" || len(cancelled.ReconnectRuns) != 0 ||
				!reflect.DeepEqual(waiting.Commands, cancelled.Commands) {
				t.Fatalf("cancel = %+v, %v, pending = %q", view, err, cancelled.PendingReconnect)
			}
			view, err = e.ChangeLocalReconnect(t.Context(), ReconnectCancelWait)
			if err != nil || view.State != ReconnectIdle || !reflect.DeepEqual(cancelled, e.Store.snapshot()) {
				t.Fatalf("cancel with no wait = %+v, %v", view, err)
			}
			executor.observation.Status = "completed"
			tick(t, e)
			tick(t, e)
			if executor.sends != 2 || executor.stops != 0 {
				t.Fatalf("cancellation did not resume the queue: sends=%d, stops=%d", executor.sends, executor.stops)
			}
		})
	}
}

func TestLocalReconnectRejectsMismatchedApp(t *testing.T) {
	e, _, _, _ := testEngine(t)
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	e.Reconnect = func(context.Context) (string, error) {
		t.Fatal("mismatched App attempted to reconnect")
		return "", nil
	}
	view, err := e.ChangeLocalReconnect(t.Context(), ReconnectAfterRuns)
	if err != nil || view.State != ReconnectScheduled {
		t.Fatalf("schedule = %+v, %v", view, err)
	}
	before := e.Store.snapshot()
	e.Config.AppID = "other-app"
	for _, action := range []ReconnectAction{ReconnectNow, ReconnectAfterRuns, ReconnectCancelWait} {
		if view, err := e.ChangeLocalReconnect(t.Context(), action); err == nil || view.State != "" || !reflect.DeepEqual(before, e.Store.snapshot()) {
			t.Fatalf("mismatched App action %q = %+v, %v", action, view, err)
		}
	}
}
