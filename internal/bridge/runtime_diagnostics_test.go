package bridge

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestLocalDiagnostics(t *testing.T) {
	config := Config{AppID: "cli_example"}
	message := Message{ID: "om_example", UserID: "ou_example", Text: "synthetic private message"}
	store := &Store{state: State{
		AccountID: config.AppID,
		Inbox:     []Message{message, {ID: "om_other"}},
		Commands:  map[string]*SessionCommand{key(message, message.ID): {}},
		Tasks: map[string]*Task{
			"queued":          {Phase: "waiting_capacity"},
			"sending":         {Phase: "needs_reconcile", NeedsReconcile: true},
			"unseen_request":  {Phase: "needs_reconcile"},
			"stop_unverified": {Phase: "stopped", NeedsReconcile: true, NotifyHash: "sent"},
			"blocked":         {Phase: "blocked"},
			"delivered":       {Phase: "completed", NotifyHash: "sent"},
			"delivery_failed": {Phase: "failed", NotifyHash: "closed", NotificationIssue: "synthetic private error"},
			"historical": {
				Phase: "tracking", RunID: "run-active", Process: &processMessage{MessageID: "om_active_process"},
				Replies: []ReplyRound{{NotificationIssue: "unknown"}, {}, {NotificationIssue: "unknown"}},
			},
		},
	}}
	engine := &Engine{Config: config, Store: store}
	connections := NewConnections(config, store)
	before := store.snapshot()

	for _, tc := range []struct {
		name            string
		verified, ready bool
	}{
		{name: "starting"},
		{name: "connected", verified: true, ready: true},
		{name: "disconnected", verified: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			connections.verified.Store(tc.verified)
			connections.ready.Store(tc.ready)
			got, err := engine.LocalDiagnostics(connections)
			want := ReceiverDiagnostics{MCPVerified: tc.verified, FeishuReady: tc.ready, Inbox: 2, InFlight: 4, Reconcile: 3, DeliveryPending: 4, Capacity: &ReceiverRunCapacity{Occupied: 4, Waiting: 1}}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("LocalDiagnostics = %+v, %v, want %+v", got, err, want)
			}
		})
	}
	if !reflect.DeepEqual(before, store.snapshot()) {
		t.Fatal("LocalDiagnostics changed Store state")
	}
}

func TestLocalDiagnosticsCapacity(t *testing.T) {
	config := Config{AppID: "cli_example"}
	store := &Store{state: State{
		AccountID: config.AppID,
		Settings:  RunSettings{MaxConcurrent: 2},
		Tasks: map[string]*Task{
			"ready":             {Phase: "ready"},
			"waiting_resource":  {Phase: "waiting_resource"},
			"waiting_capacity":  {Phase: "waiting_capacity"},
			"opening":           {Phase: "opening"},
			"dispatching":       {Phase: "dispatching"},
			"needs_reconcile":   {Phase: "needs_reconcile"},
			"tracking":          {Phase: "tracking"},
			"awaiting_children": {Phase: "awaiting_children"},
			"settling":          {Phase: "settling"},
			"stop_unverified":   {Phase: "stopped", NeedsReconcile: true},
			"stopped":           {Phase: "stopped"},
			"completed":         {Phase: "completed"},
			"failed":            {Phase: "failed"},
		},
	}}
	engine := &Engine{Config: config, Store: store}
	got, err := engine.LocalDiagnostics(NewConnections(config, store))
	want := ReceiverRunCapacity{Limit: 2, Occupied: 7, Waiting: 1}
	if err != nil || got.Capacity == nil || *got.Capacity != want {
		t.Fatalf("LocalDiagnostics = %+v, %v, want capacity %+v", got, err, want)
	}
}

func TestLocalDiagnosticsAccountBoundary(t *testing.T) {
	config := Config{AppID: "cli_example"}
	store := &Store{state: State{AccountID: config.AppID}}
	otherStore := &Store{state: State{AccountID: config.AppID}}
	otherConfig := Config{AppID: "cli_other"}
	engine := &Engine{Config: config, Store: store}
	connections := NewConnections(config, store)

	got, err := engine.LocalDiagnostics(connections)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"mcpVerified":false,"feishuReady":false,"inbox":0,"inFlight":0,"reconcile":0,"deliveryPending":0,"capacity":{"limit":0,"occupied":0,"waiting":0}}`
	if string(wire) != want {
		t.Fatalf("diagnostics JSON = %s, want %s", wire, want)
	}

	for _, tc := range []struct {
		name        string
		accountID   string
		connections *Connections
	}{
		{name: "missing connection", accountID: config.AppID},
		{name: "different connection app", accountID: config.AppID, connections: NewConnections(otherConfig, store)},
		{name: "different connection store", accountID: config.AppID, connections: NewConnections(config, otherStore)},
		{name: "different store account", accountID: otherConfig.AppID, connections: connections},
		{name: "unbound store", connections: connections},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store.state.AccountID = tc.accountID
			got, err := engine.LocalDiagnostics(tc.connections)
			if got != (ReceiverDiagnostics{}) || err == nil {
				t.Fatalf("LocalDiagnostics = %+v, %v, want zero diagnostics and an error", got, err)
			}
		})
	}
}
