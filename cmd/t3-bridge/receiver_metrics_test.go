package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestReceiverMetricLog(t *testing.T) {
	var output bytes.Buffer
	account := receiverProcessAccount{Tenant: "lark", AppID: "cli_synthetic"}
	writeReceiverMetric(&output, account, "run_e2e_ms", 1500, "completed")
	writeReceiverMetric(&output, account, "ws_reconnect", 1, "")
	receiverCommandFailureMetric(&output, account)(bridge.CommandFailureReplyExpired)
	if strings.Count(output.String(), "\n") != 3 {
		t.Fatalf("expected three individual log records: %s", output.String())
	}
	decoder := json.NewDecoder(&output)
	decoder.UseNumber()
	for _, metric := range []struct {
		name, terminal, step string
		value                json.Number
	}{
		{"run_e2e_ms", "completed", "", "1500"},
		{"ws_reconnect", "", "", "1"},
		{"command_fail", "", "reply-expired", "1"},
	} {
		var record map[string]any
		if err := decoder.Decode(&record); err != nil {
			t.Fatal(err)
		}
		fields := 6
		if metric.terminal != "" {
			fields++
		}
		if metric.step != "" {
			fields++
		}
		if len(record) != fields || record["type"] != "receiver-metric" || record["appId"] != account.AppID || record["tenant"] != account.Tenant || record["name"] != metric.name || record["value"] != metric.value {
			t.Fatalf("unexpected metric record: %v", record)
		}
		at, _ := record["at"].(string)
		if _, err := time.Parse(time.RFC3339Nano, at); err != nil {
			t.Fatal(err)
		}
		terminal, _ := record["terminal"].(string)
		if terminal != metric.terminal {
			t.Fatalf("terminal = %q, want %q", terminal, metric.terminal)
		}
		step, _ := record["step"].(string)
		if step != metric.step {
			t.Fatalf("step = %q, want %q", step, metric.step)
		}
	}
}

func TestReceiverEventLogCanBeExported(t *testing.T) {
	var output bytes.Buffer
	account := receiverProcessAccount{Tenant: "lark", AppID: "cli_synthetic"}
	writeReceiverEvent(&output, account, "ws-reconnected")

	var exported bytes.Buffer
	if err := writeServiceTelemetry(&output, &exported, receiverProcessAccount{}); err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(exported.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if len(record) != 5 || record["type"] != "receiver-event" || record["kind"] != "ws-reconnected" ||
		record["tenant"] != account.Tenant || record["appId"] != account.AppID {
		t.Fatalf("unexpected exported event: %v", record)
	}
}

func TestReceiverCapacityMetricsTrackChanges(t *testing.T) {
	var output bytes.Buffer
	account := receiverProcessAccount{Tenant: "feishu", AppID: "cli_synthetic"}
	full := bridge.ReceiverRunCapacity{Limit: 2, Occupied: 2, Waiting: 1}
	ready := bridge.ReceiverRunCapacity{Limit: 2, Occupied: 2}
	writeReceiverCapacityMetrics(&output, account, nil, nil)
	writeReceiverCapacityMetrics(&output, account, nil, &full)
	writeReceiverCapacityMetrics(&output, account, &full, &full)
	writeReceiverCapacityMetrics(&output, account, &full, &ready)
	if strings.Count(output.String(), "\n") != 3 {
		t.Fatalf("expected initial gauges and one changed value: %s", output.String())
	}
	decoder := json.NewDecoder(&output)
	type metric struct {
		Name  string
		Value int
	}
	for _, want := range []metric{{"pool_active", 2}, {"pool_waiting", 1}, {"pool_waiting", 0}} {
		got := metric{Value: -1}
		if err := decoder.Decode(&got); err != nil || got != want {
			t.Fatalf("metric = %+v, want %+v, error = %v", got, want, err)
		}
	}
}
