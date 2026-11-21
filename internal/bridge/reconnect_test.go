package bridge

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestLocalReconnectPreservesAccountWorkAndChatWait(t *testing.T) {
	e, executor, _, _ := testEngine(t)
	e.Config.Admins = []string{"user"}
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	accept(t, e, message("run", "/nas 在途请求"))
	tick(t, e)
	before := e.Store.snapshot()
	var calls int
	e.Reconnect = func(context.Context) (string, error) {
		calls++
		if e.Store.snapshot().ReconnectStatus != reconnectUncertain {
			t.Fatal("connection replacement began before persisting its status")
		}
		return "synthetic clients replaced", nil
	}
	view, err := e.LocalReconnect(t.Context())
	if err != nil || view.State != ReconnectStarted || calls != 1 || executor.sends != 1 || executor.stops != 0 {
		t.Fatalf("unexpected reconnect result: %+v, %v, calls %d, sends %d, stops %d", view, err, calls, executor.sends, executor.stops)
	}
	after := e.Store.snapshot()
	if !reflect.DeepEqual(after.Tasks, before.Tasks) || !reflect.DeepEqual(after.Commands, before.Commands) || after.ReconnectStatus != "synthetic clients replaced" {
		t.Fatal("local reconnect changed execution or command receipts")
	}
	accept(t, e, message("wait", "/reconnect --wait"))
	tick(t, e)
	waiting := e.Store.snapshot()
	view, err = e.LocalReconnect(t.Context())
	if err != nil || view.State != ReconnectPending || calls != 1 || waiting.PendingReconnect == "" || !reflect.DeepEqual(waiting, e.Store.snapshot()) {
		t.Fatalf("local reconnect displaced a chat reconnect: %+v, %v", view, err)
	}
}

func TestReconnectWaitPersistsUntilRunAndReplySettle(t *testing.T) {
	e, executor, messenger, _ := testEngine(t)
	e.Config.Admins = []string{"user"}
	wait := message("wait", "/reconnect --wait")
	waitID := key(wait, wait.ID)
	next := message("next", "/nas 排队等待的请求")
	calls := 0
	e.Reconnect = func(context.Context) (string, error) {
		calls++
		state := e.Store.snapshot()
		if state.PendingReconnect != "" || !state.Commands[waitID].ReconnectAttempted {
			t.Fatal("reconnect called before writing the durable attempt")
		}
		if _, queued := queuedPresent(&state, next); !queued || executor.sends != 1 {
			t.Fatal("pending work was dispatched before reconnect")
		}
		return "synthetic clients replaced", nil
	}
	accept(t, e, message("run", "/nas 在途请求"))
	tick(t, e)
	if executor.sends != 1 {
		t.Fatalf("started runs = %d, want 1", executor.sends)
	}
	messenger.replyErr = errors.New("synthetic acknowledgement unavailable")
	accept(t, e, wait)
	accept(t, e, next)
	tick(t, e)
	if e.Store.snapshot().PendingReconnect != waitID || calls != 0 || executor.sends != 1 {
		t.Fatal("wait request was lost or a run was dispatched early")
	}
	executor.observation.Status = "completed"
	tick(t, e)
	if calls != 0 || executor.sends != 1 {
		t.Fatal("reconnected before confirming the acknowledgement")
	}

	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	e.Store = reopened
	messenger.replyErr = nil
	tick(t, e)
	if calls != 1 || e.Store.snapshot().ReconnectStatus != "synthetic clients replaced" {
		t.Fatal("restart did not complete the durable pending reconnect")
	}
	tick(t, e)
	if calls != 1 || executor.sends != 2 {
		t.Fatalf("reconnects = %d, dispatches = %d after resuming", calls, executor.sends)
	}
}

func TestReconnectWaitCancellationResumesDispatch(t *testing.T) {
	e, executor, messenger, _ := testEngine(t)
	e.Config.Admins = []string{"user"}
	e.Reconnect = func(context.Context) (string, error) {
		t.Fatal("cancelled wait attempted to reconnect")
		return "", nil
	}
	accept(t, e, message("run", "/nas 在途请求"))
	tick(t, e)
	accept(t, e, message("wait", "/reconnect --wait"))
	accept(t, e, message("queued", "/nas 排队的请求"))
	tick(t, e)
	accept(t, e, message("cancel", "/reconnect --cancel"))
	executor.observation.Status = "completed"
	tick(t, e)
	tick(t, e)
	if e.Store.snapshot().PendingReconnect != "" || executor.sends != 2 ||
		!strings.Contains(strings.Join(messenger.replies, "\n"), "已取消等待重连") {
		t.Fatal("cancel did not release the queue")
	}
}

func TestReconnectCommandFailureReportsOneSample(t *testing.T) {
	for _, command := range []string{"/reconnect", "/reconnect --wait"} {
		t.Run(command, func(t *testing.T) {
			e, _, _, _ := testEngine(t)
			e.Config.Admins = []string{"user"}
			var calls int
			var failures []CommandFailureStep
			e.OnCommandFailed = func(step CommandFailureStep) { failures = append(failures, step) }
			e.Reconnect = func(context.Context) (string, error) {
				calls++
				return "", errors.New("synthetic private cause")
			}
			denied := message("denied", command)
			denied.UserID = "other"
			accept(t, e, denied)
			tick(t, e)
			if calls != 0 || len(failures) != 0 {
				t.Fatal("authorization decline attempted or reported reconnect")
			}
			m := message("failed", command)
			accept(t, e, m)
			tick(t, e)
			accept(t, e, m)
			tick(t, e)
			if calls != 1 || len(failures) != 1 || failures[0] != CommandFailureReconnect {
				t.Fatalf("calls=%d, failure samples=%v", calls, failures)
			}
		})
	}
}

func TestReconnectWaitChecksStoppedRunBeforeReplacingClients(t *testing.T) {
	e, executor, _, _ := testEngine(t)
	e.Config.Admins = []string{"user"}
	metadata := &lifecycleExecutor{fakeExecutor: executor, native: NativeThread{Title: "synthetic task"}}
	e.T3 = metadata
	calls := 0
	e.Reconnect = func(context.Context) (string, error) { calls++; return "reconnected", nil }
	accept(t, e, message("run", "/nas 在途请求"))
	tick(t, e)
	accept(t, e, message("wait", "/reconnect --wait"))
	tick(t, e)
	active := onlyTask(t, e)
	if err := e.Store.update(func(st *State) error {
		st.Tasks[active.ID].StopRequested = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	tick(t, e)
	if executor.stops != 1 || calls != 0 || e.Store.snapshot().PendingReconnect == "" {
		t.Fatal("stop acknowledgement was mistaken for T3 quiescence")
	}
	metadata.native.Quiet = true
	tick(t, e)
	if calls != 1 || e.Store.snapshot().PendingReconnect != "" {
		t.Fatal("quiet readback did not release the pending reconnect")
	}
}
