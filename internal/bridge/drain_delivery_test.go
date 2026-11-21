package bridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDrainDeliveryAcrossStoreLifecycle(t *testing.T) {
	const appID = "cli_fixture"
	const privateBody = "synthetic private message body"
	const privateIssue = "synthetic raw delivery error"
	const privatePath = "/synthetic/private/process-path"
	completed := func(id string) *Task {
		source := Message{ID: id, Text: privateBody}
		task := &Task{
			ID: id, ReceiverAppID: appID, Source: source, DispatchSource: source,
			Phase: "completed", RequestID: "request-" + id, RunID: "run-" + id,
			CardID: "om_reply_" + id, Summary: privateBody, LegacySessionPath: privatePath,
		}
		task.NotifyHash = notificationHash(task)
		return task
	}
	round := func(id string, delivery ReplyDeliveryState) ReplyRound {
		return ReplyRound{
			InputID: id, Source: Message{ID: id, Text: privateBody}, Phase: "completed",
			RequestID: "request-" + id, CardID: "om_reply_" + id, DeliveryState: delivery,
		}
	}

	stale := completed("om_04_current")
	stale.Phase = "tracking"
	stale.NotifyHash = notificationHash(stale)
	stale.Phase = "completed"
	// A continued session has an older task source and a separate reply destination.
	stale.ReplySource = &stale.DispatchSource
	stale.Source = Message{ID: "om_session_origin"}
	stale.Process = &processMessage{
		ReplyTo: stale.DispatchSource.ID, MessageID: "om_current_process_card", CreateAt: time.Unix(100, 0),
	}
	legacyRound := round("om_01_legacy_round", "")
	legacyRound.NotificationIssue = privateIssue
	pending := round("om_03_pending", ReplyDeliveryPending)
	pending.Process = &processMessage{
		ReplyTo: pending.Source.ID, MessageID: "om_archived_process_card", FinalText: privateBody,
	}
	stale.Replies = []ReplyRound{pending, legacyRound, round("om_02_unconfirmed", ReplyDeliveryUnconfirmed)}
	legacyIssue := completed("om_05_legacy_issue")
	legacyIssue.NotificationIssue = privateIssue

	// The exact terminal NotifyHash predates FinalReplyHash in this fixture.
	legacyDelivered := completed("om_legacy_delivered")
	legacyDelivered.Process = &processMessage{
		ReplyTo: legacyDelivered.Source.ID, MessageID: "om_closed_current_process", Closed: true,
	}
	confirmed := round("om_confirmed", ReplyDeliveryConfirmed)
	confirmed.Process = &processMessage{
		ReplyTo: confirmed.Source.ID, MessageID: "om_closed_archived_process", Closed: true, FinalText: privateBody,
	}
	legacyDelivered.Replies = []ReplyRound{confirmed, round("om_unknown", ReplyDeliveryUnknown), round("om_legacy_unknown", "")}
	waiting := completed("om_waiting")
	waiting.Phase, waiting.NotifyHash = "waiting_capacity", ""

	capped := completed("om_cap_current")
	var cappedSamples []DrainDeliverySample
	for i := 9; i >= 0; i-- {
		id := fmt.Sprintf("om_cap_%02d", i)
		capped.Replies = append(capped.Replies, round(id, ReplyDeliveryPending))
	}
	for i := 0; i < 8; i++ {
		cappedSamples = append(cappedSamples, DrainDeliverySample{MessageID: fmt.Sprintf("om_cap_%02d", i), Kind: "reply"})
	}
	duplicate := round("om_cap_00", ReplyDeliveryUnconfirmed)
	duplicate.RequestID = "retry-request"
	invalid := round("om_/private/process-path", ReplyDeliveryPending)
	invalid.CardID = "om_cap_00_outbound"
	capped.Replies = append(capped.Replies, duplicate, invalid, round("om_", ReplyDeliveryPending))

	for _, tt := range []struct {
		name    string
		tasks   []*Task
		active  int
		pending int
		samples []DrainDeliverySample
	}{
		{
			name: "undelivered current and historical rounds", tasks: []*Task{stale, legacyIssue}, pending: 7,
			samples: []DrainDeliverySample{
				{MessageID: "om_01_legacy_round", Kind: "reply"},
				{MessageID: "om_02_unconfirmed", Kind: "reply"},
				{MessageID: "om_03_pending", Kind: "process"},
				{MessageID: "om_03_pending", Kind: "reply"},
				{MessageID: "om_04_current", Kind: "process"},
				{MessageID: "om_04_current", Kind: "reply"},
				{MessageID: "om_05_legacy_issue", Kind: "reply"},
			},
		},
		{name: "legacy terminal hash and settled history", tasks: []*Task{legacyDelivered}},
		{name: "waiting is active only", tasks: []*Task{waiting}, active: 1},
		{name: "samples are validated, deduplicated and bounded", tasks: []*Task{capped}, pending: 13, samples: cappedSamples},
	} {
		t.Run(tt.name, func(t *testing.T) {
			config := Config{AppID: appID, StateDir: filepath.Join(t.TempDir(), "state")}
			store, err := OpenStore(config.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.update(func(st *State) error {
				st.AccountID = appID
				for _, task := range tt.tasks {
					st.Tasks[task.ID] = task
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}

			path := filepath.Join(config.StateDir, "state.json")
			readState := func() ([]byte, os.FileInfo) {
				t.Helper()
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				return data, info
			}
			before, beforeInfo := readState()
			wantDrained := tt.active == 0 && tt.pending == 0
			checkOnline := func(stage string, store *Store) {
				t.Helper()
				engine := &Engine{Config: config, Store: store}
				got, err := engine.LocalDiagnostics(NewConnections(config, store))
				if err != nil || got.InFlight != tt.active || got.DeliveryPending != tt.pending {
					t.Errorf("%s LocalDiagnostics = %+v, %v, want active=%d delivery=%d", stage, got, err, tt.active, tt.pending)
				}
				if err := store.DrainCheck(appID); (err == nil) != wantDrained {
					t.Errorf("%s DrainCheck = %v, want drained=%v", stage, err, wantDrained)
				}
			}
			checkOnline("live", store)
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			want := DrainAccount{AppID: appID, Status: "drained", DeliverySamples: tt.samples}
			if !wantDrained {
				want.Status = "blocked"
			}
			if tt.active != 0 {
				want.Blockers = append(want.Blockers, DrainBlocker{Code: "active", Count: tt.active})
			}
			if tt.pending != 0 {
				want.Blockers = append(want.Blockers, DrainBlocker{Code: "delivery", Count: tt.pending})
			}
			report := InspectReceiverDrain(config)
			if report.Drained != wantDrained || !reflect.DeepEqual(report.Accounts, []DrainAccount{want}) {
				t.Errorf("InspectReceiverDrain = %+v, want drained=%v account=%+v", report, wantDrained, want)
			}
			raw, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			for _, private := range []string{privateBody, privateIssue, privatePath} {
				if strings.Contains(string(raw), private) {
					t.Error("drain JSON disclosed private fixture data")
				}
			}

			reopened, err := OpenStore(config.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			checkOnline("reopened", reopened)
			if err := reopened.Close(); err != nil {
				t.Fatal(err)
			}
			after, afterInfo := readState()
			if !bytes.Equal(before, after) || !os.SameFile(beforeInfo, afterInfo) || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
				t.Error("diagnostics, drain inspection or reopen modified the persisted state file")
			}
		})
	}
}
