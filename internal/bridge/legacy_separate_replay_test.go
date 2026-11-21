package bridge

import (
	"context"
	"maps"
	"reflect"
	"strings"
	"testing"
)

func completedSeparateLegacyReplayFixture(t *testing.T) (*Engine, *legacyReplayTestExecutor, *legacyReplayTestMessenger, Message, string, string) {
	t.Helper()
	e, x, l, fresh, scope := completedLegacyReplayFixture(t)
	oldID := e.Store.snapshot().Current[scope]
	reset, native := fresh, fresh
	reset.ID, reset.Text = "om_separate_reset", "/new"
	native.ID, native.Text = "om_separate_native", "start independent native thread"
	accept(t, e, reset)
	tick(t, e)
	if id, known := e.Store.snapshot().Current[scope]; !known || id != "" {
		t.Fatalf("/new did not clear the current mapping: %q", id)
	}
	accept(t, e, native)
	tick(t, e)
	x.observation.Summary = "native answer"
	tick(t, e)
	st := e.Store.snapshot()
	current := st.Tasks[st.Current[scope]]
	if old := st.Tasks[oldID]; old == nil || old.ThreadID != legacyReplayTestThread || old.Phase != "completed" || old.FinalReplyHash == "" {
		t.Fatal("fixture lost the settled imported task")
	}
	if current == nil || current.ID == oldID || current.ThreadID == "" || current.ThreadID == legacyReplayTestThread ||
		current.LegacySessionPath != "" || current.Phase != "completed" || current.FinalReplyHash == "" ||
		current.Source.ID != native.ID || len(st.Tasks) != 2 || x.opens != 1 || x.sends != 1 {
		t.Fatal("fixture did not complete a separate native current thread")
	}
	x.opens, x.sends, l.notifications, l.replies = 0, 0, 0, nil
	return e, x, l, fresh, scope, oldID
}

type separateLegacyReplayMessenger struct {
	*legacyReplayTestMessenger
	onVerify func(Message, []string)
}

func (f *separateLegacyReplayMessenger) verifyLegacyGroupMessages(ctx context.Context, m Message, ids []string) error {
	f.onVerify(m, ids)
	return f.legacyReplayTestMessenger.verifyLegacyGroupMessages(ctx, m, ids)
}

func TestLegacySeparateReplaySuppressesOldInputAndKeepsNativeCurrent(t *testing.T) {
	e, x, l, fresh, scope, oldID := completedSeparateLegacyReplayFixture(t)
	old, late := fresh, fresh
	old.ID, old.Text = "om_separate_rollback", "old bridge input on the imported thread"
	late.ID = "om_separate_late"
	const rejected = "om_separate_unverified"
	x.ids, l.denied = []string{old.ID, late.ID, rejected}, []string{rejected}
	verified := map[string]bool{}
	e.Lark = &separateLegacyReplayMessenger{legacyReplayTestMessenger: l, onVerify: func(m Message, ids []string) {
		if m.ChatType != old.ChatType || userScope(m) != userScope(old) {
			t.Fatal("Feishu proof did not use the rollback message scope")
		}
		for _, id := range ids {
			verified[id] = true
		}
	}}
	rechecks := 0
	x.onRecheck = func(thread, revision string) error {
		if thread != legacyReplayTestThread || revision != "replay-revision" {
			t.Fatalf("unexpected proof recheck: %q, %q", thread, revision)
		}
		rechecks++
		return nil
	}
	e = reopenLegacyReplayEngine(t, e)
	before := e.Store.snapshot()
	native := before.Tasks[before.Current[scope]]
	reads := x.reads
	accept(t, e, old)
	queuedState := e.Store.snapshot()
	if _, queued := queuedPresent(&queuedState, old); !queued {
		t.Fatal("rollback input did not enter the queue before proof")
	}
	tick(t, e)
	after := e.Store.snapshot()
	if x.reads != reads+1 || rechecks != 1 || !maps.Equal(verified, map[string]bool{old.ID: true, late.ID: true, rejected: true}) {
		t.Fatal("rollback proof did not read and recheck the imported T3 thread and verify its Feishu messages")
	}
	if len(after.Inbox) != 0 || x.opens != 0 || x.sends != 0 || l.notifications != 0 || len(l.replies) != 0 {
		t.Fatal("rollback input remained queued or reached execution or reply delivery")
	}
	if !reflect.DeepEqual(after.Tasks, before.Tasks) || !maps.Equal(after.Current, before.Current) ||
		!maps.Equal(after.Bindings, before.Bindings) || !reflect.DeepEqual(after.Commands, before.Commands) {
		t.Fatal("rollback proof changed current mapping, tasks or existing receipts")
	}
	wantSeen := maps.Clone(before.Seen)
	for _, id := range []string{old.ID, late.ID} {
		wantSeen[key(old, id)] = true
		wantSeen[inboundSeenKey(old, id)] = true
	}
	if !maps.Equal(after.Seen, wantSeen) {
		t.Fatal("Seen did not retain old receipts and add only the verified replay IDs")
	}

	x.onSend = func(task *Task) {
		if task.ID != native.ID || task.ThreadID != native.ThreadID {
			t.Fatalf("fresh input dispatched on %q instead of %q", task.ThreadID, native.ThreadID)
		}
	}
	accept(t, e, fresh)
	tick(t, e)
	after = e.Store.snapshot()
	continued := after.Tasks[after.Current[scope]]
	if continued == nil || continued.ID != native.ID || len(after.Tasks) != 2 || x.opens != 0 || x.sends != 1 ||
		continued.DispatchSource.ID != fresh.ID || len(continued.DispatchSource.Parts) != 0 || !strings.HasPrefix(x.request, fresh.Text+"\n\n") ||
		!reflect.DeepEqual(after.Tasks[oldID], before.Tasks[oldID]) {
		t.Fatal("fresh input did not continue the native thread as its own turn")
	}
	if len(continued.Replies) != 1 || continued.Replies[0].RequestID != native.RequestID ||
		continued.Replies[0].RunID != native.RunID || continued.Replies[0].CardID != native.CardID {
		t.Fatal("native continuation lost the previous turn receipt")
	}
	e = reopenLegacyReplayEngine(t, e)
	late.TopicID = "omt_separate_changed_delivery_metadata"
	accept(t, e, late)
	if len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("late replay entered the reopened Store with changed delivery metadata")
	}
}

func TestLegacySeparateReplayRequiresUnambiguousOwnedHistory(t *testing.T) {
	for _, scenario := range []string{
		"other receiver", "other topic", "other target", "missing native thread", "missing current mapping", "empty current mapping",
		"multiple original threads", "current imported thread wins",
	} {
		t.Run(scenario, func(t *testing.T) {
			e, x, l, input, scope, oldID := completedSeparateLegacyReplayFixture(t)
			if err := e.Store.update(func(st *State) error {
				old, current := st.Tasks[oldID], st.Tasks[st.Current[scope]]
				switch scenario {
				case "other receiver":
					old.ReceiverAppID = "other-app"
				case "other topic":
					old.Source.TopicID = "omt_other_scope"
				case "other target":
					old.Target.ProjectID = "other-project"
				case "missing native thread":
					current.ThreadID, current.Link = "", ""
				case "missing current mapping":
					delete(st.Current, scope)
				case "empty current mapping":
					st.Current[scope] = ""
				case "multiple original threads", "current imported thread wins":
					other := *old
					other.Source.ID = "om_other_imported_turn"
					other.ID = stable(other.Source.ChatID, other.Source.TopicID, other.Source.ID)
					other.ThreadID = "other-imported-thread"
					other.RunID, other.ResultRunID = "other-imported-run", "other-imported-run"
					other.RequestID = stable(other.ID, other.Source.ID, e.Config.AppID)
					other.CardRequestID = stable(other.ID, other.Source.ID)
					other.CardID = "other-imported-card"
					other.DispatchSource, other.ReplySource = other.Source, &other.Source
					other.NotifyHash, other.FinalReplyHash = notificationHash(&other), finalReplyContentHash(&other)
					st.Tasks[other.ID] = &other
					bindInputs(st, other.Source, other.ID)
					st.Bindings[key(other.Source, other.CardID)] = other.ID
					if scenario == "current imported thread wins" {
						st.Current[scope] = oldID
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			e = reopenLegacyReplayEngine(t, e)
			const unqueued = "om_separate_unqueued_candidate"
			x.ids = []string{input.ID, unqueued}
			reads := x.reads
			accept(t, e, input)
			tick(t, e)
			st := e.Store.snapshot()
			_, queued := queuedPresent(&st, input)
			bound := st.Bindings[key(input, input.ID)] != ""
			if scenario == "current imported thread wins" {
				if x.reads != reads+1 || queued || bound || st.Current[scope] != oldID || x.sends != 0 || l.notifications != 0 ||
					!st.Seen[inboundSeenKey(input, unqueued)] {
					t.Fatal("Stage 173 did not prioritize the current imported thread over another candidate")
				}
				return
			}
			if x.reads != reads || st.Seen[key(input, unqueued)] || st.Seen[inboundSeenKey(input, unqueued)] || (!queued && !bound) {
				t.Fatal("ineligible history triggered replay proof or discarded the input")
			}
		})
	}
}
