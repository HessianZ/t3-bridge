package bridge

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

type runTelemetrySample struct {
	phase   string
	elapsed time.Duration
}

func TestRunTelemetryAfterSettling(t *testing.T) {
	e, x, _, _ := testEngine(t)
	now := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	e.Now = func() time.Time { return now }
	x.onPreflight = func() { now = now.Add(time.Minute) }
	x.onSend = func(*Task) {
		if task := onlyTask(t, e); task.Phase != "dispatching" || !task.DispatchStartedAt.Equal(now) {
			t.Fatalf("dispatch fence before Send: phase=%q start=%s", task.Phase, task.DispatchStartedAt)
		}
	}
	var got []runTelemetrySample
	var events []string
	e.OnTaskEvent = func(event string) { events = append(events, event) }
	e.OnRunFinished = func(phase string, elapsed time.Duration) {
		if task := onlyTask(t, e); task.Phase != phase {
			t.Fatalf("callback phase=%q, stored phase=%q", phase, task.Phase)
		}
		got = append(got, runTelemetrySample{phase, elapsed})
	}
	accept(t, e, message("one", "NAS检查磁盘"))
	tick(t, e)
	if !slices.Equal(events, []string{"run-started"}) {
		t.Fatalf("events after dispatch=%v", events)
	}
	x.observation = Observation{Status: "completed", RunID: "run", PendingApprovals: 1, Tasks: []string{"child"}, ChildThreads: []string{"child-thread"}, Unsettled: true}
	for _, phase := range []string{"tracking", "awaiting_children", "settling"} {
		now = now.Add(time.Second)
		tick(t, e)
		if task := onlyTask(t, e); task.Phase != phase || len(got) != 0 {
			t.Fatalf("phase=%q, want %q with no callback, got %v", task.Phase, phase, got)
		}
		if !slices.Equal(events, []string{"run-started"}) {
			t.Fatalf("events while unsettled=%v", events)
		}
		switch phase {
		case "tracking":
			x.observation.PendingApprovals = 0
		case "awaiting_children":
			x.observation.Tasks = nil
		case "settling":
			x.observation.Unsettled = false
		}
	}
	now = now.Add(time.Second)
	tick(t, e)
	tick(t, e)
	if want := []runTelemetrySample{{"completed", 4 * time.Second}}; !slices.Equal(got, want) {
		t.Fatalf("callbacks=%v, want %v", got, want)
	}
	if want := []string{"run-started", "run-completed", "outbound-sent"}; !slices.Equal(events, want) {
		t.Fatalf("events=%v, want %v", events, want)
	}
}

func TestRunTelemetryRestartAndContinuation(t *testing.T) {
	e, x, l, m := testEngine(t)
	now := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	e.Now = clock
	x.sendErr = errors.New("lost send receipt")
	accept(t, e, message("one", "NAS检查磁盘"))
	tick(t, e)
	firstRequest := onlyTask(t, e).RequestID
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	var got []runTelemetrySample
	var events []string
	e = &Engine{Config: e.Config, Store: store, T3: x, Lark: l, Maintenance: m, Now: clock, OnRunFinished: func(phase string, elapsed time.Duration) {
		got = append(got, runTelemetrySample{phase, elapsed})
	}, OnTaskEvent: func(event string) { events = append(events, event) }}
	x.observation = Observation{Status: "completed", RunID: "other-run"}
	now = now.Add(5 * time.Second)
	tick(t, e)
	if task := onlyTask(t, e); task.Phase != "needs_reconcile" || task.RunID != "" || len(got) != 0 {
		t.Fatalf("unknown receipt: phase=%q run=%q callbacks=%v", task.Phase, task.RunID, got)
	}
	if len(events) != 0 {
		t.Fatalf("events for unmatched receipt=%v", events)
	}
	x.observation = Observation{Status: "completed", FoundRequest: true, RunID: "run", Unsettled: true}
	now = now.Add(5 * time.Second)
	tick(t, e)
	if task := onlyTask(t, e); task.Phase != "settling" || task.RunID != "run" || len(got) != 0 {
		t.Fatalf("recovered receipt: phase=%q run=%q callbacks=%v", task.Phase, task.RunID, got)
	}
	x.observation.Unsettled = false
	now = now.Add(5 * time.Second)
	tick(t, e)

	x.sendErr = nil
	x.onPreflight = func() {
		if task := onlyTask(t, e); task.RequestID == firstRequest || !task.DispatchStartedAt.IsZero() || task.RunID != "" {
			t.Fatalf("continuation retained dispatch identity: request=%q start=%s run=%q", task.RequestID, task.DispatchStartedAt, task.RunID)
		}
	}
	now = now.Add(time.Hour)
	reply := message("two", "继续核对")
	reply.ParentID = "card"
	accept(t, e, reply)
	tick(t, e)
	x.observation = Observation{Status: "failed", RunID: "run"}
	now = now.Add(3 * time.Second)
	tick(t, e)
	tick(t, e)
	if want := []runTelemetrySample{{"completed", 15 * time.Second}, {"failed", 3 * time.Second}}; !slices.Equal(got, want) || x.sends != 2 {
		t.Fatalf("callbacks=%v, want %v, sends=%d, want 2", got, want, x.sends)
	}
	want := []string{"run-started", "run-completed", "outbound-sent", "run-started", "run-failed", "outbound-sent"}
	if !slices.Equal(events, want) {
		t.Fatalf("events=%v, want %v", events, want)
	}
}

func TestRunTelemetryFailedSave(t *testing.T) {
	for _, tc := range []struct {
		name      string
		published bool
	}{
		{"before publish", false},
		{"after publish", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, x, _, _ := testEngine(t)
			calls := 0
			e.OnRunFinished = func(string, time.Duration) { calls++ }
			var events []string
			accept(t, e, message("one", "NAS检查磁盘"))
			tick(t, e)
			e.OnTaskEvent = func(event string) { events = append(events, event) }
			writeErr := errors.New("synthetic terminal save failure")
			e.Store.writeState = func(path string, data []byte, createOnly bool) (bool, error) {
				if tc.published {
					if published, err := localfile.WritePrivateAtomic(path, data, createOnly); !published || err != nil {
						t.Fatalf("synthetic publication setup: published=%v, err=%v", published, err)
					}
				}
				return tc.published, writeErr
			}
			x.observation.Status = "completed"
			if err := e.Tick(t.Context()); err != writeErr || calls != 0 || len(events) != 0 {
				t.Fatalf("Tick error=%v, want %v, callbacks=%d, events=%v", err, writeErr, calls, events)
			}
			e.Store.writeState = nil
			tick(t, e)
			tick(t, e)
			want := 1
			if tc.published {
				want = 0
			}
			if calls != want {
				t.Fatalf("callbacks after retry=%d, want %d", calls, want)
			}
			wantEvents := []string{"run-completed", "outbound-sent"}
			if tc.published {
				wantEvents = wantEvents[1:]
			}
			if !slices.Equal(events, wantEvents) {
				t.Fatalf("events=%v, want %v", events, wantEvents)
			}
		})
	}
}

func TestRunTelemetryWaitsForFinalReplyReceipt(t *testing.T) {
	e, x, l, _ := testEngine(t)
	var delivered int
	e.OnTaskEvent = func(event string) {
		if event != "outbound-sent" {
			return
		}
		task := onlyTask(t, e)
		if task.CardID == "" || task.NotifyHash != notificationHash(task) || task.NotificationIssue != "" {
			t.Fatalf("outbound event without a saved final receipt: %+v", task)
		}
		delivered++
	}
	accept(t, e, message("one", "NAS检查磁盘"))
	tick(t, e)
	l.fail = true
	x.observation.Status = "completed"
	tick(t, e)
	if delivered != 0 || onlyTask(t, e).NotificationIssue == "" {
		t.Fatalf("lost reply should still be unconfirmed: delivered=%d", delivered)
	}
	l.fail = false
	tick(t, e)
	tick(t, e)
	if delivered != 1 {
		t.Fatalf("delivered=%d, want 1", delivered)
	}
}
