package bridge

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadMessageReceiptFromLiveSnapshot(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const appID = "cli_fixture"
	const content = "synthetic private conversation body"
	const threadURL = "https://app.t3.codes/threads/linked-environment/thread-original"
	if err := store.update(func(s *State) error {
		s.AccountID = appID
		s.Tasks["task"] = &Task{
			ID: "task", ReceiverAppID: appID, RouteID: "work", ThreadID: "thread-original",
			Link:           threadURL,
			Target:         Target{EnvironmentID: "linked-environment", ProjectID: "original-project"},
			Source:         Message{ID: "om_first", Text: content},
			DispatchSource: Message{ID: "om_second", Text: content},
			Phase:          "dispatching", RequestID: "request-new", NeedsReconcile: true,
			Replies: []ReplyRound{{InputID: "om_first", Source: Message{ID: "om_first", Text: content, MergedIDs: []string{"om_merged"}},
				RequestID: "request-old", RunID: "run-old", Phase: "completed", CardID: "om_reply", Result: content}},
		}
		legacy := &Task{
			ID: "legacy", ReceiverAppID: appID, Source: Message{ID: "om_legacy_current"},
			Phase: "completed", RequestID: "legacy-request", CardID: "om_legacy_reply",
		}
		legacy.NotifyHash = notificationHash(legacy)
		s.Tasks[legacy.ID] = legacy
		s.Inbox = []Message{{ID: "om_waiting", Text: content}, {ID: "om_cancelled", Cancelled: true}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"om_first", "om_merged"} {
		got, err := ReadMessageReceipt(dir, appID, id)
		if err != nil || got.MessageID != id || got.InputID != "om_first" || got.ThreadID != "thread-original" ||
			got.EnvironmentID != "linked-environment" || got.ProjectID != "original-project" || got.ThreadURL != threadURL ||
			got.RunID != "run-old" || got.RequestID != "request-old" || got.ReplyID != "om_reply" ||
			got.DeliveryState != ReplyDeliveryUnknown || !got.Historical || got.NeedsReconcile {
			t.Fatalf("historical input %s returned %+v, %v", id, got, err)
		}
		raw, err := json.Marshal(got)
		if err != nil || strings.Contains(string(raw), content) {
			t.Fatal("receipt JSON disclosed message content")
		}
	}
	got, err := ReadMessageReceipt(dir, appID, "om_second")
	if err != nil || got.InputID != "om_second" || got.RequestID != "request-new" || got.RunID != "" || got.ThreadURL != threadURL ||
		got.DeliveryState != ReplyDeliveryPending || got.Historical || !got.NeedsReconcile {
		t.Fatalf("current round did not retain the unknown receipt: %+v, %v", got, err)
	}
	for id, phase := range map[string]string{"om_waiting": "queued", "om_cancelled": "cancelled"} {
		got, err := ReadMessageReceipt(dir, appID, id)
		if err != nil || got.Phase != phase || got.InputID != id || got.TaskID != "" || got.RunID != "" || got.ThreadURL != "" ||
			got.DeliveryState != ReplyDeliveryPending {
			t.Fatalf("inbox %s returned %+v, %v", id, got, err)
		}
	}
	if _, err := ReadMessageReceipt(dir, appID, "om_unknown"); !errors.Is(err, ErrMessageReceiptNotFound) {
		t.Fatalf("unknown input returned %v", err)
	}
	if legacy, err := ReadMessageReceipt(dir, appID, "om_legacy_current"); err != nil ||
		legacy.DeliveryState != ReplyDeliveryUnknown || legacy.Historical || legacy.ReplyID != "om_legacy_reply" {
		t.Fatalf("a legacy notification hash was treated as proof of terminal delivery: %+v, %v", legacy, err)
	}
	if _, err := ReadMessageReceipt(dir, "cli_other", "om_first"); err == nil || errors.Is(err, ErrMessageReceiptNotFound) {
		t.Fatalf("wrong account returned %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("inspection modified the receiver snapshot")
	}
	if err := store.update(func(s *State) error {
		s.Tasks["duplicate"] = &Task{ID: "duplicate", ReceiverAppID: appID, DispatchSource: Message{ID: "om_first"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMessageReceipt(dir, appID, "om_first"); err == nil {
		t.Fatal("ambiguous input selected an arbitrary run")
	}
}

func TestMessageReceiptPreservesOrdinaryCardDeliveryAcrossContinuation(t *testing.T) {
	e, t3, lark, _ := testEngine(t)
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	read := func(expected ReplyDeliveryState, historical bool) MessageReceipt {
		t.Helper()
		receipt, err := ReadMessageReceipt(e.Config.StateDir, e.Config.AppID, "om_original")
		if err != nil || receipt.DeliveryState != expected || receipt.Historical != historical {
			t.Fatalf("expected delivery %s, historical %v, got %+v, %v", expected, historical, receipt, err)
		}
		return receipt
	}
	accept(t, e, message("om_original", "/nas first smoke"))
	tick(t, e)
	progress := read(ReplyDeliveryPending, false)
	if progress.ReplyID == "" || progress.Phase != "tracking" || onlyTask(t, e).CardPreviewEnabled {
		t.Fatalf("expected an ordinary progress card: %+v", progress)
	}
	lark.fail = true
	t3.observation.Status, t3.observation.Summary = "completed", "synthetic final result"
	tick(t, e)
	unconfirmed := read(ReplyDeliveryUnconfirmed, false)
	if unconfirmed.Phase != "completed" || unconfirmed.ReplyID != progress.ReplyID || unconfirmed.NeedsReconcile {
		t.Fatalf("delivery failure changed the T3 receipt: %+v", unconfirmed)
	}

	accept(t, e, message("om_continuation", "continue smoke"))
	tick(t, e)
	if t3.sends != 1 || len(e.Store.snapshot().Inbox) != 1 {
		t.Fatal("the second round bypassed the unconfirmed first result")
	}
	lark.fail = false
	tick(t, e)
	confirmed := read(ReplyDeliveryConfirmed, false)
	if confirmed.ReplyID != progress.ReplyID || confirmed.RunID != progress.RunID || t3.sends != 1 ||
		!strings.Contains(lark.sentCards[confirmed.ReplyID], "synthetic final result") {
		t.Fatalf("the original terminal card was not updated: %+v", confirmed)
	}

	tick(t, e)
	archived := read(ReplyDeliveryConfirmed, true)
	if archived.ReplyID != confirmed.ReplyID || archived.RunID != confirmed.RunID || archived.RequestID != confirmed.RequestID ||
		t3.sends != 2 || onlyTask(t, e).CardID == archived.ReplyID {
		t.Fatalf("the continuation moved or lost the old reply receipt: %+v", archived)
	}
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if afterRestart := read(ReplyDeliveryConfirmed, true); afterRestart != archived {
		t.Fatalf("reopening the store changed the original receipt: %+v", afterRestart)
	}
}
