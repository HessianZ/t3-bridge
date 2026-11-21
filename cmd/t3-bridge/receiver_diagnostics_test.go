package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func (r receiverProcessRegistry) diagnostics(process receiverProcess, account receiverProcessAccount) (receiverDiagnosticSample, bool) {
	sample, _ := r.receiverSnapshot(process, account)
	if sample == nil {
		return receiverDiagnosticSample{}, false
	}
	return *sample, true
}

func TestReceiverDiagnosticsHistoryObservations(t *testing.T) {
	var history receiverDiagnosticHistory
	var want []receiverDiagnosticEvent
	start := time.Date(2026, time.November, 19, 0, 0, 0, 0, time.UTC)
	for i, tc := range []struct {
		name               string
		mcp, feishu        bool
		reconcile, pending int
		events             []receiverDiagnosticEvent
	}{
		{"baseline", true, true, 2, 3, nil},
		{"same flags and counters", true, true, 2, 3, nil},
		{"connection changes and increases", false, false, 4, 5, []receiverDiagnosticEvent{
			{Kind: "mcp-unverified"}, {Kind: "feishu-not-ready"},
			{Kind: "reconcile", Count: 4}, {Kind: "delivery-pending", Count: 5},
		}},
		{"partial decrease", false, false, 1, 2, nil},
		{"increase from latest sample", false, false, 2, 3, []receiverDiagnosticEvent{
			{Kind: "reconcile", Count: 2}, {Kind: "delivery-pending", Count: 3},
		}},
		{"recovery", true, true, 0, 0, []receiverDiagnosticEvent{
			{Kind: "mcp-verified"}, {Kind: "feishu-ready"},
			{Kind: "reconcile", Count: 0}, {Kind: "delivery-pending", Count: 0},
		}},
		{"still recovered", true, true, 0, 0, nil},
	} {
		at := start.Add(time.Duration(i) * receiverDiagnosticsInterval)
		history.observe(at, bridge.ReceiverDiagnostics{
			MCPVerified: tc.mcp, FeishuReady: tc.feishu, Reconcile: tc.reconcile, DeliveryPending: tc.pending,
			Inbox: i, InFlight: i,
		})
		for _, event := range tc.events {
			event.At = at
			want = append(want, event)
		}
		if !slices.Equal(history.events, want) {
			t.Fatalf("%s: events = %+v, want %+v", tc.name, history.events, want)
		}
	}
}

func TestReceiverDiagnosticsHistoryLimits(t *testing.T) {
	var history receiverDiagnosticHistory
	start := time.Date(2026, time.November, 19, 0, 0, 0, 0, time.UTC)
	history.observe(start, bridge.ReceiverDiagnostics{})
	for i := 1; i <= 14; i++ {
		history.observe(start.Add(time.Duration(i)*receiverDiagnosticsInterval), bridge.ReceiverDiagnostics{
			Reconcile: receiverDiagnosticEventCountLimit + i,
		})
	}
	if len(history.events) != 12 {
		t.Fatalf("retained %d events, want 12", len(history.events))
	}
	for i, event := range history.events {
		want := receiverDiagnosticEvent{At: start.Add(time.Duration(i+3) * receiverDiagnosticsInterval), Kind: "reconcile", Count: 9_999}
		if event != want {
			t.Fatalf("event %d = %+v, want %+v", i, event, want)
		}
	}
}

func TestFailedReceiverWorkerClearsOnlyItsDiagnostics(t *testing.T) {
	r := localReceiverProcesses(profileRegistry{root: t.TempDir()})
	blockedState := filepath.Join(t.TempDir(), "state-file")
	if err := os.WriteFile(blockedState, []byte("not a state directory"), 0600); err != nil {
		t.Fatal(err)
	}
	failed := bridge.Config{AppID: "cli_failed", StateDir: blockedState}
	healthy := bridge.Config{Tenant: "lark", AppID: "cli_healthy"}
	process, err := r.register([]bridge.Config{failed, healthy})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.close() })
	for _, account := range process.process.Accounts {
		process.writeDiagnostics(account, receiverDiagnosticSample{
			At:   time.Now().UTC(),
			Data: bridge.ReceiverDiagnostics{MCPVerified: true, FeishuReady: true},
		})
		if _, ok := r.diagnostics(process.process, account); !ok {
			t.Fatal("fixture did not publish a ready sample")
		}
	}
	if err := runReceiverAccount(t.Context(), failed, &bridge.ReceiverProcess{Shared: true}, process); err == nil {
		t.Fatal("receiver accepted a state path occupied by a file")
	}
	if _, ok := r.diagnostics(process.process, process.process.Accounts[0]); ok {
		t.Fatal("failed App still reported its ready sample")
	}
	if sample, ok := r.diagnostics(process.process, process.process.Accounts[1]); !ok || !sample.Data.FeishuReady {
		t.Fatal("healthy App lost its ready sample")
	}
}

func TestReceiverDiagnosticsLifecycle(t *testing.T) {
	r := localReceiverProcesses(profileRegistry{root: t.TempDir()})
	configs := []bridge.Config{{AppID: "cli_first"}, {Tenant: "lark", AppID: "cli_second"}}
	p, err := r.register(configs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.lock.Close() })
	first, second := p.process.Accounts[0], p.process.Accounts[1]
	if _, ok := r.diagnostics(p.process, first); ok {
		t.Fatal("account without a sample reported known diagnostics")
	}
	startup := receiverDiagnosticSample{At: time.Now().UTC()}
	p.writeDiagnostics(first, startup)
	oldSnapshot, err := os.ReadFile(receiverDiagnosticsPath(p.dir, first))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(oldSnapshot, []byte(`"events"`)) {
		t.Fatal("baseline snapshot did not use the old format")
	}
	if got, ok := r.diagnostics(p.process, first); !ok || !reflect.DeepEqual(got, startup) {
		t.Fatalf("startup diagnostics = %+v, %t", got, ok)
	}
	if _, ok := r.diagnostics(p.process, second); ok {
		t.Fatal("unpublished account inherited another account's diagnostics")
	}
	connected := receiverDiagnosticSample{At: time.Now().UTC(), Data: bridge.ReceiverDiagnostics{
		MCPVerified: true, FeishuReady: true, Inbox: 1, InFlight: 2, Reconcile: 3, DeliveryPending: 4,
	}}
	connected.Events = []receiverDiagnosticEvent{
		{At: connected.At, Kind: "mcp-verified"},
		{At: connected.At, Kind: "delivery-pending", Count: 4},
	}
	p.writeDiagnostics(first, connected)
	p.writeDiagnostics(second, startup)
	if got, ok := r.diagnostics(p.process, first); !ok || !reflect.DeepEqual(got, connected) {
		t.Fatalf("connected diagnostics = %+v, %t", got, ok)
	}
	if got, ok := r.diagnostics(p.process, second); !ok || !reflect.DeepEqual(got, startup) {
		t.Fatalf("second account diagnostics = %+v, %t", got, ok)
	}
	unknown := newReceiverGroupAccount("lark", first.AppID)
	p.writeDiagnostics(unknown, startup)
	if _, ok := r.diagnostics(p.process, unknown); ok {
		t.Fatal("accepted a sample for an unregistered account")
	}
	disconnected := connected
	disconnected.At = time.Now().UTC()
	disconnected.Data.FeishuReady = false
	p.writeDiagnostics(first, disconnected)
	if got, ok := r.diagnostics(p.process, first); !ok || !reflect.DeepEqual(got, disconnected) {
		t.Fatalf("disconnected diagnostics = %+v, %t", got, ok)
	}
	if err := p.lock.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.diagnostics(p.process, first); ok {
		t.Fatal("exited receiver reported known diagnostics with its files still present")
	}
	restarted, err := r.register(configs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.close() })
	if _, ok := r.diagnostics(restarted.process, first); ok {
		t.Fatal("restarted receiver inherited old diagnostics")
	}
}

func TestReceiverDiagnosticsRejectInvalidSamples(t *testing.T) {
	r := localReceiverProcesses(profileRegistry{root: t.TempDir()})
	p, err := r.register([]bridge.Config{{AppID: "cli_first"}, {AppID: "cli_second"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.close() })
	now := time.Now().UTC()
	p.process.StartedAt = now.Add(-time.Minute)
	registration, err := json.Marshal(p.process)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localfile.WritePrivateAtomic(filepath.Join(p.dir, "process.json"), registration, false); err != nil {
		t.Fatal(err)
	}
	account := p.process.Accounts[0]
	path := receiverDiagnosticsPath(p.dir, account)
	for _, tc := range []struct {
		name string
		at   time.Time
	}{
		{"expired", now.Add(-31 * time.Second)},
		{"future", now.Add(time.Minute)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p.writeDiagnostics(account, receiverDiagnosticSample{At: tc.at})
			if _, ok := r.diagnostics(p.process, account); ok {
				t.Fatal("accepted invalid timestamp")
			}
		})
	}
	p.writeDiagnostics(account, receiverDiagnosticSample{
		At: now, Data: bridge.ReceiverDiagnostics{Capacity: &bridge.ReceiverRunCapacity{Limit: 2, Occupied: 1}},
		Events: []receiverDiagnosticEvent{{At: now, Kind: "reconcile", Count: 1}},
	})
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		old  string
		new  string
	}{
		{"wrong process", p.process.ID, "another-process"},
		{"wrong app", account.AppID, p.process.Accounts[1].AppID},
		{"missing counter", `,"inbox":0`, ""},
		{"negative counter", `"reconcile":0`, `"reconcile":-1`},
		{"null readiness", `"feishuReady":false`, `"feishuReady":null`},
		{"duplicate readiness", `"feishuReady":false`, `"feishuReady":true,"feishuReady":false`},
		{"missing capacity count", `,"waiting":0`, ""},
		{"negative capacity count", `"occupied":1`, `"occupied":-1`},
		{"unknown event code", `"kind":"reconcile"`, `"kind":"fixture-secret"`},
		{"negative event count", `"count":1`, `"count":-1`},
		{"excessive event count", `"count":1`, `"count":10000`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := bytes.Replace(raw, []byte(tc.old), []byte(tc.new), 1)
			if bytes.Equal(raw, changed) {
				t.Fatal("fixture did not modify sample")
			}
			if _, err := localfile.WritePrivateAtomic(path, changed, false); err != nil {
				t.Fatal(err)
			}
			if _, ok := r.diagnostics(p.process, account); ok {
				t.Fatal("accepted invalid sample")
			}
		})
	}

	// An exhausted worker remains actionable even after a normal sample would expire.
	exhausted := receiverWorkerSample{At: now.Add(-45 * time.Second), receiverWorkerStatus: receiverWorkerStatus{
		State: receiverWorkerExhausted, Attempts: 4, Failure: "store-open-failed",
	}}
	p.writeWorkerStatus(account, &exhausted)
	if sample, worker := r.receiverSnapshot(p.process, account); sample != nil || worker == nil || *worker != exhausted {
		t.Fatalf("exhausted worker returned %+v, %+v", sample, worker)
	}
	for _, tc := range []struct {
		name   string
		status receiverWorkerStatus
		data   receiverDiagnosticData
	}{
		{name: "unknown state", status: receiverWorkerStatus{State: "other", Attempts: 1}},
		{name: "out of range attempts", status: receiverWorkerStatus{State: receiverWorkerExhausted, Attempts: 5}},
		{name: "old retry deadline", status: receiverWorkerStatus{State: receiverWorkerRetrying, Attempts: 1, RetryAt: now.Add(-time.Second)}},
		{name: "far retry deadline", status: receiverWorkerStatus{State: receiverWorkerRetrying, Attempts: 1, RetryAt: now.Add(time.Hour)}},
		{name: "arbitrary failure text", status: receiverWorkerStatus{State: receiverWorkerExhausted, Attempts: 4, Failure: "fixture-secret"}},
		{name: "conflicting diagnostics", status: exhausted.receiverWorkerStatus, data: receiverDiagnosticData{FeishuReady: new(bool)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p.writeDiagnosticFile(receiverDiagnosticFile{
				Instance: p.process.ID, Account: account, At: now, Data: tc.data, Worker: &tc.status,
			})
			if sample, worker := r.receiverSnapshot(p.process, account); sample != nil || worker != nil {
				t.Fatalf("invalid worker returned %+v, %+v", sample, worker)
			}
		})
	}
}
