package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestWriteReceiverDiagnosticLogs(t *testing.T) {
	var output bytes.Buffer
	at := time.Date(2026, 11, 19, 3, 36, 0, 0, time.UTC)
	account := receiverProcessAccount{Tenant: "feishu", AppID: "cli_test"}
	var history receiverDiagnosticHistory
	history.observe(at.Add(-receiverDiagnosticsInterval), bridge.ReceiverDiagnostics{Reconcile: 1})
	changed := bridge.ReceiverDiagnostics{DeliveryPending: 4}
	writeReceiverDiagnosticLogs(&output, account, history.observe(at, changed))
	writeReceiverDiagnosticLogs(&output, account, history.observe(at.Add(receiverDiagnosticsInterval), changed))
	if bytes.Count(output.Bytes(), []byte("\n")) != 2 {
		t.Fatalf("expected one line per observation, got %q", output.Bytes())
	}
	decoder := json.NewDecoder(&output)
	for _, want := range []struct {
		kind  string
		count float64
	}{{receiverDiagnosticReconcile, 0}, {receiverDiagnosticDeliveryPending, 4}} {
		var log map[string]any
		if err := decoder.Decode(&log); err != nil {
			t.Fatal(err)
		}
		if len(log) != 6 || log["type"] != "receiver-diagnostic" || log["tenant"] != account.Tenant ||
			log["appId"] != account.AppID || log["at"] != at.Format(time.RFC3339) || log["kind"] != want.kind || log["count"] != want.count {
			t.Fatalf("unexpected diagnostic log: %v", log)
		}
	}
}

func TestWriteReceiverFailure(t *testing.T) {
	var output bytes.Buffer
	account := receiverProcessAccount{Tenant: "lark", AppID: "cli_test"}
	writeReceiverFailure(&output, account, "worker-tick-failed")
	var log map[string]any
	if err := json.Unmarshal(output.Bytes(), &log); err != nil {
		t.Fatal(err)
	}
	at, _ := log["at"].(string)
	if _, err := time.Parse(time.RFC3339Nano, at); err != nil {
		t.Fatal(err)
	}
	if len(log) != 5 || log["type"] != "receiver-failure" || log["tenant"] != account.Tenant || log["appId"] != account.AppID || log["kind"] != "worker-tick-failed" {
		t.Fatalf("unexpected failure log: %v", log)
	}
}
