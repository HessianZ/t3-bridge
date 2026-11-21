package bridge

import (
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"testing"
)

func completedLegacyReplayFixture(t *testing.T) (*Engine, *legacyReplayTestExecutor, *legacyReplayTestMessenger, Message, string) {
	t.Helper()
	e, x, l, fresh, scope := legacyReplayFixture(t)
	first := fresh
	first.ID, first.Text = "om_imported_turn", "first bridge turn"
	accept(t, e, first)
	tick(t, e)
	assertLegacyReplayDispatch(t, e, x, first, legacyReplayTestThread)
	x.observation = Observation{Status: "completed", Summary: "original answer", RunID: "run", ResultRunID: "run", FoundRequest: true, OriginalRunSettled: true}
	tick(t, e)
	task := onlyTask(t, e)
	st := e.Store.snapshot()
	if task.Phase != "completed" || task.LegacySessionPath == "" || task.RunID == "" || task.FinalReplyHash == "" ||
		st.Current[scope] != task.ID || len(st.LegacySessions) != 0 || x.reads != 1 {
		t.Fatal("fixture did not settle the current imported task with a reply receipt")
	}
	x.sends, l.notifications = 0, 0
	return e, x, l, fresh, scope
}

func reopenLegacyReplayEngine(t *testing.T, e *Engine) *Engine {
	t.Helper()
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return &Engine{Config: e.Config, Store: store, T3: e.T3, Lark: e.Lark, Maintenance: e.Maintenance, Now: e.Now}
}

func TestLegacyRestoreReplaySuppressesRollbackInputAndContinues(t *testing.T) {
	e, x, l, fresh, scope := completedLegacyReplayFixture(t)
	e = reopenLegacyReplayEngine(t, e)
	before := e.Store.snapshot()
	task := before.Tasks[before.Current[scope]]
	old, during, late := fresh, fresh, fresh
	old.ID, during.ID, late.ID = "om_rollback", "om_rollback_during", "om_rollback_late"
	x.ids = []string{old.ID, during.ID, late.ID}
	x.onRead = func() {
		accept(t, e, old)
		accept(t, e, during)
		accept(t, e, during)
	}
	accept(t, e, old)
	tick(t, e)
	x.onRead = nil
	st := e.Store.snapshot()
	if len(st.Inbox) != 0 || len(st.Tasks) != 1 || st.Current[scope] != task.ID ||
		!reflect.DeepEqual(st.Tasks[task.ID], task) || !maps.Equal(st.Bindings, before.Bindings) ||
		x.opens != 0 || x.sends != 0 || l.notifications != 0 || len(l.replies) != 0 || x.reads != 2 {
		t.Fatal("rollback replay changed the current task or receipts, or entered the reply or execution flow")
	}
	for _, id := range x.ids {
		if !st.Seen[key(old, id)] || !st.Seen[inboundSeenKey(old, id)] {
			t.Fatalf("missing replay receipts for %s", id)
		}
	}
	late.TopicID = "omt_changed_delivery_metadata"
	accept(t, e, late)
	if len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("late replay with changed topic metadata entered the inbox")
	}
	accept(t, e, fresh)
	tick(t, e)
	assertLegacyReplayDispatch(t, e, x, fresh, legacyReplayTestThread)
	current := onlyTask(t, e)
	if current.ID != task.ID || x.opens != 0 || x.reads != 2 || len(current.Replies) != 1 ||
		current.Replies[0].RequestID != task.RequestID || current.Replies[0].RunID != task.RunID || current.Replies[0].CardID != task.CardID {
		t.Fatal("new input repeated the proof or lost the prior round on the imported task")
	}
}

func TestLegacyRestoreReplayRequiresEligibleCurrentContext(t *testing.T) {
	for _, scenario := range []string{
		"other topic", "ordinary task", "archived context", "changed project", "active run", "unresolved dispatch", "same engine as import",
	} {
		t.Run(scenario, func(t *testing.T) {
			e, x, _, fresh, scope := completedLegacyReplayFixture(t)
			if err := e.Store.update(func(st *State) error {
				task := st.Tasks[st.Current[scope]]
				switch scenario {
				case "ordinary task":
					task.LegacySessionPath = ""
				case "archived context":
					st.Current[scope] = ""
				case "changed project":
					st.Workspaces[workspaceKey(e.Config, fresh, task.RouteID)] = Workspace{ProjectID: task.Target.ProjectID, Path: t.TempDir()}
				case "active run":
					task.Phase, task.ResultRunID = "tracking", ""
					x.observation = Observation{Status: "running", RunID: task.RunID, FoundRequest: true}
				case "unresolved dispatch":
					task.NeedsReconcile = true
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if scenario != "same engine as import" {
				e = reopenLegacyReplayEngine(t, e)
			}
			if scenario == "other topic" {
				fresh.TopicID = "omt_unrelated"
			}
			const candidate = "om_rollback_candidate"
			x.ids = []string{fresh.ID, candidate}
			accept(t, e, fresh)
			tick(t, e)
			st := e.Store.snapshot()
			_, queued := queuedPresent(&st, fresh)
			if x.reads != 1 || st.Seen[inboundSeenKey(fresh, candidate)] || (!queued && st.Bindings[key(fresh, fresh.ID)] == "") {
				t.Fatal("ineligible current context triggered a replay proof or discarded normal input")
			}
		})
	}
}

func TestLegacyRestoreReplayWriteFailureKeepsPersistedQueueForRetry(t *testing.T) {
	e, x, l, fresh, scope := completedLegacyReplayFixture(t)
	e = reopenLegacyReplayEngine(t, e)
	old := fresh
	old.ID = "om_rollback_queued"
	const unqueued = "om_rollback_unqueued"
	x.ids = []string{old.ID, unqueued}
	accept(t, e, fresh)
	accept(t, e, old)
	before := e.Store.snapshot()
	task := before.Tasks[before.Current[scope]]
	writeErr := errors.New("synthetic restored replay write failure")
	e.Store.writeState = func(_ string, data []byte, _ bool) (bool, error) {
		var attempted State
		if err := json.Unmarshal(data, &attempted); err != nil {
			return false, err
		}
		if len(attempted.Inbox) != 1 || attempted.Inbox[0].ID != fresh.ID || !attempted.Seen[inboundSeenKey(old, unqueued)] ||
			!reflect.DeepEqual(attempted.Tasks[task.ID], task) || !maps.Equal(attempted.Bindings, before.Bindings) {
			t.Fatal("proof write did not publish Seen and queue removal together while preserving receipts")
		}
		return false, writeErr
	}
	if err := e.Tick(t.Context()); !errors.Is(err, writeErr) {
		t.Fatalf("failed replay publication returned %v", err)
	}
	e.Store.writeState = nil
	disk, err := readStateSnapshot(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []State{e.Store.snapshot(), disk} {
		if !reflect.DeepEqual(st.Inbox, before.Inbox) || st.Seen[key(old, unqueued)] ||
			!reflect.DeepEqual(st.Tasks[task.ID], task) || !maps.Equal(st.Bindings, before.Bindings) {
			t.Fatal("failed proof publication changed the inbox, Seen, task, or reply bindings")
		}
	}
	if x.reads != 2 || x.sends != 0 || l.notifications != 0 || len(l.replies) != 0 {
		t.Fatal("failed replay write did not follow proof or reached execution or reply delivery")
	}
	tick(t, e)
	assertLegacyReplayDispatch(t, e, x, fresh, legacyReplayTestThread)
	st := e.Store.snapshot()
	if x.reads != 3 || len(st.Inbox) != 0 || !st.Seen[key(old, unqueued)] || st.Bindings[key(old, old.ID)] != "" {
		t.Fatal("retry did not publish evidence and drop the rollback input before dispatching fresh input")
	}
}
