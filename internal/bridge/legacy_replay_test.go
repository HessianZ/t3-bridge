package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

const legacyReplayTestThread = "old-t3-thread"

type legacyReplayTestExecutor struct {
	*legacyGroupTestExecutor
	ids    []string
	reads  int
	onRead func()
}

func (f *legacyReplayTestExecutor) readLegacyReplayEvidence(_ context.Context, target Target, threadID string, scope Message, botID string) (legacyGroupEvidence, error) {
	f.reads++
	if f.onRead != nil {
		f.onRead()
	}
	proof, err := f.onEvidence(target, threadID, scope, botID)
	proof.MessageIDs, proof.Revision = f.ids, "replay-revision"
	return proof, err
}

type legacyReplayTestMessenger struct {
	*fakeMessenger
	denied []string
}

func (f *legacyReplayTestMessenger) legacyGroupBotID(context.Context) (string, error) {
	return "ou_replay_bot", nil
}

func (f *legacyReplayTestMessenger) verifyLegacyGroupMessages(_ context.Context, _ Message, ids []string) error {
	for _, id := range ids {
		if slices.Contains(f.denied, id) {
			return errors.New("synthetic Feishu proof refused")
		}
	}
	return nil
}

func legacyReplayFixture(t *testing.T) (*Engine, *legacyReplayTestExecutor, *legacyReplayTestMessenger, Message, string) {
	t.Helper()
	e, executor, messenger, _ := testEngine(t)
	input := Message{ID: "om_new_input", ChatID: "oc_replay", TopicID: "omt_replay", UserID: "ou_replay_user", ChatType: "group", Text: "genuine new input"}
	e.Config.DefaultRoute = "mac"
	e.Config.Chats, e.Config.Topics, e.Config.Users = []string{input.ChatID}, nil, []string{input.UserID}
	target := e.Config.Routes["mac"]
	target.Users, target.ProjectPath = e.Config.Users, t.TempDir()
	e.Config.Routes = map[string]Target{"mac": target}
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	scope := sessionKey(input, "mac", target)
	if err := e.Store.update(func(st *State) error {
		st.LegacySessions = map[string]legacySessionSeed{scope: {ThreadID: legacyReplayTestThread, ProjectPath: target.ProjectPath, ChatType: "group"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	x := &legacyReplayTestExecutor{legacyGroupTestExecutor: &legacyGroupTestExecutor{
		legacyTestExecutor: &legacyTestExecutor{fakeExecutor: executor},
		onEvidence: func(got Target, threadID string, m Message, botID string) (legacyGroupEvidence, error) {
			if targetIdentity(got) != targetIdentity(target) || got.ProjectPath != target.ProjectPath || threadID != legacyReplayTestThread ||
				m.ChatType != input.ChatType || userScope(m) != userScope(input) || botID != "ou_replay_bot" {
				t.Fatalf("legacy proof used the wrong target, thread, message scope, or bot")
			}
			return legacyGroupEvidence{MessageIDs: []string{"om_verified_history"}, Revision: "adoption-revision"}, nil
		},
		onRecheck: func(_, _ string) error { return nil },
	}}
	l := &legacyReplayTestMessenger{fakeMessenger: messenger}
	e.T3, e.Lark = x, l
	return e, x, l, input, scope
}

func assertLegacyReplayDispatch(t *testing.T, e *Engine, x *legacyReplayTestExecutor, input Message, threadID string) {
	t.Helper()
	task := onlyTask(t, e)
	if x.sends != 1 || task.ThreadID != threadID || task.DispatchSource.ID != input.ID || len(task.DispatchSource.Parts) != 0 ||
		!strings.HasPrefix(x.request, input.Text+"\n\n") {
		t.Fatalf("new input did not dispatch on %q as its own turn: thread=%q source=%q parts=%d sends=%d", threadID,
			task.ThreadID, task.DispatchSource.ID, len(task.DispatchSource.Parts), x.sends)
	}
}

func TestLegacyReplaySuppressesOldInputAndSurvivesRestart(t *testing.T) {
	e, x, l, fresh, scope := legacyReplayFixture(t)
	old, late := fresh, fresh
	old.ID, old.Text = "om_old_replay", "previously handled text"
	late.ID = "om_late_replay"
	x.ids = []string{old.ID, late.ID}
	accept(t, e, old)
	tick(t, e)
	st := e.Store.snapshot()
	if len(st.Inbox) != 0 || len(st.Tasks) != 0 || x.opens != 0 || x.sends != 0 || l.notifications != 0 || len(l.replies) != 0 || st.Bindings[key(old, old.ID)] != "" {
		t.Fatal("old input entered the task or reply flow")
	}
	if seed := st.LegacySessions[scope]; seed.ThreadID != legacyReplayTestThread || !seed.ReplayChecked || !st.Seen[key(late, late.ID)] || x.reads != 1 {
		t.Fatal("replay proof did not preserve the pending seed and record late IDs")
	}
	accept(t, e, fresh)
	tick(t, e)
	assertLegacyReplayDispatch(t, e, x, fresh, legacyReplayTestThread)
	if x.opens != 0 || x.reads != 1 {
		t.Fatal("new input opened a thread or repeated the replay proof")
	}
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	restarted := &Engine{Config: e.Config, Store: store, T3: x, Lark: l, Now: e.Now}
	accept(t, restarted, late)
	if len(store.snapshot().Inbox) != 0 {
		t.Fatal("late legacy replay was admitted after reopening the Store")
	}
	tick(t, restarted)
	assertLegacyReplayDispatch(t, restarted, x, fresh, legacyReplayTestThread)
}

func TestLegacyReplayRemovesQueuedAndInFlightReplaysBeforeCoalescing(t *testing.T) {
	e, x, _, fresh, scope := legacyReplayFixture(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e.Now = func() time.Time { return start.Add(3 * time.Second) }
	old, during := fresh, fresh
	fresh.ReceivedAt = start.Format(time.RFC3339Nano)
	old.ID, old.Text, old.ReceivedAt = "om_old_queued", "old queued text", start.Add(100*time.Millisecond).Format(time.RFC3339Nano)
	during.ID, during.Text, during.ReceivedAt = "om_old_during", "old text during proof", start.Add(200*time.Millisecond).Format(time.RFC3339Nano)
	x.ids = []string{old.ID, during.ID, "om_old_never_queued"}
	x.onRead = func() { accept(t, e, during) }
	accept(t, e, fresh)
	accept(t, e, old)
	commits := 0
	e.Store.writeState = func(path string, data []byte, createOnly bool) (bool, error) {
		var st State
		if err := json.Unmarshal(data, &st); err != nil {
			return false, err
		}
		if st.LegacySessions[scope].ReplayChecked {
			commits++
			if len(st.Inbox) != 1 || st.Inbox[0].ID != fresh.ID || !st.Seen[key(during, x.ids[2])] {
				t.Fatal("replay receipts and queue removal were not committed together before coalescing")
			}
		}
		return localfile.WritePrivateAtomic(path, data, createOnly)
	}
	tick(t, e)
	assertLegacyReplayDispatch(t, e, x, fresh, legacyReplayTestThread)
	st := e.Store.snapshot()
	if commits != 1 || len(st.Inbox) != 0 || st.Bindings[key(old, old.ID)] != "" || st.Bindings[key(during, during.ID)] != "" {
		t.Fatal("old inputs were not removed in one commit before the new task")
	}
}

func TestLegacyReplayRequiresMatchingScopeAndPositiveEvidence(t *testing.T) {
	for _, tc := range []string{"other topic", "unverified Feishu ID", "revision drift"} {
		t.Run(tc, func(t *testing.T) {
			e, x, l, fresh, scope := legacyReplayFixture(t)
			const candidate = "om_unqueued_candidate"
			x.ids = []string{fresh.ID, candidate}
			wantThread := legacyReplayTestThread
			switch tc {
			case "other topic":
				fresh.TopicID = "omt_other_topic"
				wantThread = "thread"
			case "unverified Feishu ID":
				x.ids = append(x.ids, "om_confirmed_candidate")
				l.denied = x.ids[:2]
			case "revision drift":
				x.onRecheck = func(_, revision string) error {
					if revision == "replay-revision" {
						return errLegacyGroupEvidence
					}
					return nil
				}
			}
			accept(t, e, fresh)
			tick(t, e)
			assertLegacyReplayDispatch(t, e, x, fresh, wantThread)
			st := e.Store.snapshot()
			if st.Seen[key(fresh, candidate)] {
				t.Fatal("unproven ID entered Seen")
			}
			if tc == "other topic" && (x.reads != 0 || st.LegacySessions[scope].ThreadID != legacyReplayTestThread) {
				t.Fatal("another topic touched the registered seed")
			}
			if tc == "unverified Feishu ID" && !st.Seen[key(fresh, "om_confirmed_candidate")] {
				t.Fatal("independently verified ID was lost with the rejected IDs")
			}
		})
	}
}

func TestLegacyReplayWriteFailureKeepsNewInputForRetry(t *testing.T) {
	e, x, l, fresh, scope := legacyReplayFixture(t)
	old := fresh
	old.ID = "om_old_queued"
	x.ids = []string{old.ID, "om_old_unqueued"}
	accept(t, e, fresh)
	accept(t, e, old)
	writeErr := errors.New("synthetic replay state write failure")
	e.Store.writeState = func(string, []byte, bool) (bool, error) { return false, writeErr }
	if err := e.Tick(t.Context()); !errors.Is(err, writeErr) {
		t.Fatalf("failed replay publication returned %v", err)
	}
	e.Store.writeState = nil
	st := e.Store.snapshot()
	seed := st.LegacySessions[scope]
	if len(st.Inbox) != 2 || st.Inbox[0].ID != fresh.ID || st.Inbox[1].ID != old.ID || len(st.Tasks) != 0 || x.sends != 0 ||
		l.notifications != 0 || len(l.replies) != 0 || seed.ThreadID != legacyReplayTestThread || seed.ReplayChecked || st.Seen[key(old, x.ids[1])] {
		t.Fatal("failed publication changed the seed, inbox, receipts, or task state")
	}
	tick(t, e)
	assertLegacyReplayDispatch(t, e, x, fresh, legacyReplayTestThread)
	if st := e.Store.snapshot(); len(st.Inbox) != 0 || !st.Seen[key(old, x.ids[1])] || x.reads != 2 {
		t.Fatal("replay publication was not retried with the original new input")
	}
}
