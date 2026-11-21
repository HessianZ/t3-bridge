package bridge

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestInspectReceiverDrainMixedAccounts(t *testing.T) {
	root := t.TempDir()
	primary := Config{AppID: "cli_fixture_primary", StateDir: filepath.Join(root, "primary")}
	extra := Config{AppID: "cli_fixture_extra", StateDir: filepath.Join(root, "extra")}
	missing := Config{AppID: "cli_fixture_missing", StateDir: filepath.Join(root, "missing")}
	noLock := Config{AppID: "cli_fixture_no_lock", StateDir: filepath.Join(root, "no-lock")}
	primary.AdditionalAccounts = []Config{extra, missing, noLock}
	open := func(account Config) *Store {
		t.Helper()
		store, err := OpenStore(account.StateDir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		if err := store.BindAccount(account.AppID); err != nil {
			t.Fatal(err)
		}
		return store
	}
	held, stopped := open(primary), open(extra)
	if err := stopped.Close(); err != nil {
		t.Fatal(err)
	}
	if err := localfile.MkdirAllPrivate(noLock.StateDir); err != nil {
		t.Fatal(err)
	}
	if _, err := localfile.WritePrivateAtomic(filepath.Join(noLock.StateDir, "state.json"), []byte("{}"), true); err != nil {
		t.Fatal(err)
	}

	got := InspectReceiverDrain(primary)
	want := DrainReport{Accounts: []DrainAccount{
		{AppID: primary.AppID, Status: "busy"},
		{AppID: extra.AppID, Status: "drained"},
		{AppID: missing.AppID, Status: "missing"},
		{AppID: noLock.AppID, Status: "unavailable"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("report = %#v, want %#v", got, want)
	}
	for _, path := range []string{missing.StateDir, filepath.Join(noLock.StateDir, "lock")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inspection created an entry: %s: %v", path, err)
		}
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	primary.AdditionalAccounts = []Config{extra}
	got = InspectReceiverDrain(primary)
	want = DrainReport{Drained: true, Accounts: []DrainAccount{
		{AppID: primary.AppID, Status: "drained"}, {AppID: extra.AppID, Status: "drained"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("report after releasing lock = %#v, want %#v", got, want)
	}
}

func TestInspectReceiverDrainBlockers(t *testing.T) {
	c := Config{AppID: "cli_fixture", StateDir: filepath.Join(t.TempDir(), "state")}
	store, err := OpenStore(c.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	err = store.update(func(state *State) error {
		state.AccountID = c.AppID
		msg := Message{ID: "message-inbox", ChatID: "chat"}
		state.Inbox = []Message{msg}
		state.PendingReconnect = "pending-reconnect"
		state.Commands = map[string]*SessionCommand{
			key(msg, msg.ID): {ProjectAttempted: true},
		}
		state.Meetings = map[string]meetingRecord{
			"joined": {Phase: "joined"}, "rejected": {Phase: meetingJoinRejected},
		}
		state.Tasks["working"] = &Task{Phase: "running"}
		state.Tasks["failed"] = &Task{Phase: "failed", NeedsReconcile: true}
		state.Tasks["card"] = &Task{
			Phase: "completed", CardPreviewEnabled: true, CardID: "card", NotifyHash: "earlier-preview",
			Process: &processMessage{CreateAt: time.Unix(100, 0)},
			Replies: []ReplyRound{
				{NotificationIssue: "old reply requires review", Process: &processMessage{Closed: true, Issue: "unknown"}},
				{Process: &processMessage{FinalText: "completion not sent"}},
				{Process: &processMessage{Closed: true}},
			},
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	got := InspectReceiverDrain(c)
	want := DrainReport{Accounts: []DrainAccount{{AppID: c.AppID, Status: "blocked", Blockers: []DrainBlocker{
		{"active", 1}, {"inbox", 1}, {"controls", 2}, {"meetings", 1},
		{"command-outcome", 1}, {"run-outcome", 1}, {"delivery", 6},
	}}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("report = %+v, want %+v", got, want)
	}
}

func TestDrainStoppedOriginalReceipt(t *testing.T) {
	const requestID, runID = "request-current", "run-current"
	const runError = "remote stop/send outcome"
	for _, tt := range []struct {
		name, request, run, settledRequest string
		needsReconcile, deliveryPending    bool
		wantBlockers                       []DrainBlocker
		wantStoreError                     string
	}{
		{
			name: "legacy receipt", request: requestID, run: runID,
			wantBlockers: []DrainBlocker{{"run-outcome", 1}}, wantStoreError: runError,
		},
		{
			name: "missing request ID", run: runID,
			wantBlockers: []DrainBlocker{{"run-outcome", 1}}, wantStoreError: runError,
		},
		{
			name: "different request", request: requestID, run: runID, settledRequest: "request-previous",
			wantBlockers: []DrainBlocker{{"run-outcome", 1}}, wantStoreError: runError,
		},
		{
			name: "missing run ID", request: requestID, settledRequest: requestID,
			wantBlockers: []DrainBlocker{{"run-outcome", 1}}, wantStoreError: runError,
		},
		{
			name: "settled", request: requestID, run: runID, settledRequest: requestID,
		},
		{
			name: "settled with pending delivery", request: requestID, run: runID, settledRequest: requestID,
			deliveryPending: true,
			wantBlockers:    []DrainBlocker{{"delivery", 1}}, wantStoreError: "notification identity/result unresolved",
		},
		{
			name: "settled with reconciliation required", request: requestID, run: runID, settledRequest: requestID,
			needsReconcile: true,
			wantBlockers:   []DrainBlocker{{"run-outcome", 1}}, wantStoreError: runError,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := Config{AppID: "cli_fixture", StateDir: filepath.Join(t.TempDir(), "state")}
			store, err := OpenStore(c.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			task := &Task{
				ID: "original", ReceiverAppID: c.AppID, Phase: "stopped", ThreadID: "thread",
				RequestID: tt.request, RunID: tt.run, StopRequested: true,
				StopSettledRequest: tt.settledRequest, NeedsReconcile: tt.needsReconcile,
			}
			if !tt.deliveryPending {
				task.NotifyHash = notificationHash(task)
			}
			err = store.update(func(state *State) error {
				state.AccountID = c.AppID
				state.Tasks[task.ID] = task
				return nil
			})
			if err := errors.Join(err, store.Close()); err != nil {
				t.Fatal(err)
			}

			want := DrainReport{Drained: len(tt.wantBlockers) == 0, Accounts: []DrainAccount{
				{AppID: c.AppID, Status: "blocked", Blockers: tt.wantBlockers},
			}}
			if want.Drained {
				want.Accounts[0].Status = "drained"
			}
			if got := InspectReceiverDrain(c); !reflect.DeepEqual(got, want) {
				t.Errorf("InspectReceiverDrain = %+v, want %+v", got, want)
			}

			reopened, err := OpenStore(c.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			err = reopened.DrainCheck(c.AppID)
			if tt.wantStoreError == "" {
				if err != nil {
					t.Errorf("DrainCheck = %v, want nil", err)
				}
			} else if err == nil || !strings.HasPrefix(err.Error(), tt.wantStoreError) {
				t.Errorf("DrainCheck = %v, want error starting with %q", err, tt.wantStoreError)
			}
		})
	}
}

func TestInspectReceiverDrainUntrustedState(t *testing.T) {
	for _, tt := range []struct {
		name, account, tasks, commands, status string
	}{
		{"unbound", "", "{}", "{}", "unbound"},
		{"foreign", "cli_other", "{}", "{}", "foreign"},
		{"foreign-task", "cli_fixture", `{"foreign":{"receiverAppId":"cli_other"}}`, "{}", "foreign"},
		{"null-task", "cli_fixture", `{"broken":null}`, "{}", "invalid"},
		{"null-command", "cli_fixture", "{}", `{"broken":null}`, "invalid"},
		{"truncated-json", "cli_fixture", "{", "{}", "invalid"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := Config{AppID: "cli_fixture", StateDir: filepath.Join(t.TempDir(), "state")}
			if err := localfile.MkdirAllPrivate(c.StateDir); err != nil {
				t.Fatal(err)
			}
			lock, err := localfile.Lock(filepath.Join(c.StateDir, "lock"), false)
			if err != nil {
				t.Fatal(err)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			data := []byte(`{"version":1,"seen":{},"bindings":{},"events":{},"accountId":"` +
				tt.account + `","tasks":` + tt.tasks + `,"commands":` + tt.commands + `}`)
			if _, err := localfile.WritePrivateAtomic(filepath.Join(c.StateDir, "state.json"), data, true); err != nil {
				t.Fatal(err)
			}
			got := InspectReceiverDrain(c)
			if got.Drained || !reflect.DeepEqual(got.Accounts, []DrainAccount{{AppID: c.AppID, Status: tt.status}}) {
				t.Fatalf("report = %+v, want %s", got, tt.status)
			}
		})
	}
}
