package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestWriteServiceTelemetryPreservesProducerRecords(t *testing.T) {
	var input bytes.Buffer
	account := receiverProcessAccount{Tenant: "lark", AppID: "cli_test-telemetry"}
	writeReceiverMetric(&input, account, "run_e2e_ms", 125, "completed")
	writeReceiverMetric(&input, account, "run_e2e_ms", 0, "failed")
	writeReceiverMetric(&input, account, "ws_reconnect", 1, "")
	writeReceiverMetric(&input, account, "pool_active", 2, "")
	writeReceiverMetric(&input, account, "pool_waiting", 0, "")
	receiverCommandFailureMetric(&input, account)(bridge.CommandFailureReplyFallback)

	at := time.Date(2026, 11, 19, 2, 3, 4, 5, time.UTC)
	writeReceiverDiagnosticLogs(&input, account, []receiverDiagnosticEvent{
		{At: at, Kind: "mcp-verified"},
		{At: at, Kind: "mcp-unverified"},
		{At: at, Kind: "feishu-ready"},
		{At: at, Kind: "feishu-not-ready"},
		{At: at, Kind: "reconcile", Count: 37},
		{At: at, Kind: "delivery-pending", Count: 0},
	})
	for _, kind := range []string{
		"store-open-failed", "account-bind-failed", "initial-connect-failed",
		"inbound-failed", "channel-stopped", "worker-tick-failed",
	} {
		input.WriteString(`{"type":"receiver-failure","tenant":"feishu","appId":"cli_example","at":"2026-11-19T02:03:04Z","kind":"` + kind + "\"}\n")
	}
	for _, kind := range []string{"run-started", "run-completed", "run-failed", "outbound-sent", "ws-reconnected"} {
		input.WriteString(`{"type":"receiver-event","tenant":"feishu","appId":"cli_example","at":"2026-11-19T02:03:04Z","kind":"` + kind + "\"}\n")
	}

	want := input.String()
	var output bytes.Buffer
	if err := writeServiceTelemetry(&input, &output, receiverProcessAccount{}); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != want {
		t.Fatalf("telemetry = %q, want %q", got, want)
	}
}

func TestWriteServiceTelemetryFiltersMixedLogs(t *testing.T) {
	const metricBase = `"type":"receiver-metric","tenant":"feishu","appId":"cli_example","at":"2026-11-19T02:03:04Z",`
	const diagnosticBase = `"type":"receiver-diagnostic","tenant":"feishu","appId":"cli_example","at":"2026-11-19T02:03:04Z",`
	const metric = `{` + metricBase + `"name":"pool_waiting","value":0}`
	const commandFailure = `{` + metricBase + `"name":"command_fail","value":1,"step":"workspace"}`
	const diagnostic = `{` + diagnosticBase + `"kind":"reconcile","count":3}`
	const failure = `{"type":"receiver-failure","tenant":"lark","appId":"cli_other","at":"2026-11-19T02:03:04Z","kind":"inbound-failed"}`
	const event = `{"type":"receiver-event","tenant":"lark","appId":"cli_other","at":"2026-11-19T02:03:04Z","kind":"run-completed"}`

	input := strings.Join([]string{
		"==> stdout.log <==",
		"ordinary log: " + metric,
		metric + " trailing text",
		metric + diagnostic,
		`[` + metric + `]`,
		strings.Repeat("x", 64<<10) + metric,
		strings.Repeat(" ", 4<<10) + metric,
		strings.TrimSuffix(metric, "}") + `,"terminal":{"secret":"private"},"step":"reply","text":"private","path":"/private/log","kind":"reconcile","count":null}` + "\r",
		strings.TrimSuffix(commandFailure, "}") + `,"terminal":"failed","error":"private","kind":"reconcile","count":3}`,
		strings.TrimSuffix(diagnostic, "}") + `,"name":"pool_waiting","value":"private","message":"private"}`,
		"==> stderr.log <==",
		strings.TrimSuffix(failure, "}") + `,"error":"private","count":"private"}`,
		strings.TrimSuffix(event, "}") +
			`,"fields":{"text":"private"},"ctx":{"userId":"private"},"phase":"private","level":"info","ids":["private"],"runId":"private"` +
			`,"name":"pool_active","value":1,"terminal":"completed","step":"reply","count":3}`,
		strings.TrimSuffix(metric, "}"),
	}, "\n")

	var output bytes.Buffer
	if err := writeServiceTelemetry(strings.NewReader(input), &output, receiverProcessAccount{}); err != nil {
		t.Fatal(err)
	}
	if want := metric + "\n" + commandFailure + "\n" + diagnostic + "\n" + failure + "\n" + event + "\n"; output.String() != want {
		t.Fatalf("telemetry = %q, want %q", output.String(), want)
	}
}

func TestWriteServiceTelemetryRejectsInvalidRecords(t *testing.T) {
	const metric = `{"type":"receiver-metric","tenant":"feishu","appId":"cli_example","at":"2026-11-19T02:03:04Z","name":"pool_active","value":1}`
	tests := []struct {
		name, from, to string
	}{
		{"type", `"receiver-metric"`, `"other"`},
		{"tenant", `"feishu"`, `"example"`},
		{"name", `"pool_active"`, `"pool_unknown"`},
		{"unsafe app ID", `"cli_example"`, `"cli_example\n/private"`},
		{"overlong app ID", "cli_example", "cli_" + strings.Repeat("x", 125)},
		{"missing time", `"at":"2026-11-19T02:03:04Z",`, ""},
		{"null time", `"2026-11-19T02:03:04Z"`, "null"},
		{"invalid time", "2026-11-19T02:03:04Z", "yesterday"},
		{"out of range UTC time", "2026-11-19T02:03:04Z", "9999-12-31T23:59:59-01:00"},
		{"mismatched field case", `"appId"`, `"AppID"`},
		{"missing value", `,"value":1`, ""},
		{"null value", `"value":1`, `"value":null`},
		{"negative value", `"value":1`, `"value":-1`},
		{"fractional value", `"value":1`, `"value":1.5`},
		{"string value", `"value":1`, `"value":"1"`},
		{"overflowing value", `"value":1`, `"value":9223372036854775808`},
		{"invalid reconnect value", `"pool_active","value":1`, `"ws_reconnect","value":2`},
		{"missing run terminal", `"pool_active"`, `"run_e2e_ms"`},
		{"unknown run terminal", `"pool_active"`, `"run_e2e_ms","terminal":"private"`},
		{"missing command step", `"pool_active"`, `"command_fail"`},
		{"null command step", `"pool_active"`, `"command_fail","step":null`},
		{"non-string command step", `"pool_active"`, `"command_fail","step":["reply"]`},
		{"unknown command step", `"pool_active"`, `"command_fail","step":"private"`},
		{"mismatched command step case", `"pool_active"`, `"command_fail","Step":"reply"`},
		{"zero command value", `"pool_active","value":1`, `"command_fail","step":"reply","value":0`},
		{"invalid command value", `"pool_active","value":1`, `"command_fail","step":"reply","value":2`},
		{"non-integer command value", `"pool_active","value":1`, `"command_fail","step":"reply","value":1.0`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			line := strings.Replace(metric, tt.from, tt.to, 1)
			if err := writeServiceTelemetry(strings.NewReader(line), &output, receiverProcessAccount{}); err != nil || output.Len() != 0 {
				t.Fatalf("telemetry = %q, error = %v", output.String(), err)
			}
		})
	}

	var input strings.Builder
	for _, record := range []string{
		`"type":"receiver-diagnostic","kind":"unknown","count":0`,
		`"type":"receiver-diagnostic","kind":"mcp-verified","count":1`,
		`"type":"receiver-diagnostic","kind":"reconcile","count":10000`,
		`"type":"receiver-diagnostic","kind":"reconcile","count":null`,
		`"type":"receiver-diagnostic","kind":"delivery-pending"`,
		`"type":"receiver-failure","kind":"private"`,
		`"type":"receiver-event"`,
		`"type":"receiver-event","kind":null`,
		`"type":"receiver-event","kind":["run-started"]`,
		`"type":"receiver-event","kind":"private"`,
		`"type":"receiver-event","kind":"RUN-STARTED"`,
		`"type":"receiver-event","Kind":"run-started"`,
		`"type":"receiver-event","kind":"inbound-failed"`,
		`"type":"receiver-event","kind":"reconcile"`,
	} {
		input.WriteString(`{"tenant":"feishu","appId":"cli_example","at":"2026-11-19T02:03:04Z",` + record + "}\n")
	}
	var output bytes.Buffer
	if err := writeServiceTelemetry(strings.NewReader(input.String()), &output, receiverProcessAccount{}); err != nil || output.Len() != 0 {
		t.Fatalf("telemetry = %q, error = %v", output.String(), err)
	}
}

func TestWriteServiceTelemetryEmptyAndWriterError(t *testing.T) {
	failure := errors.New("writer failed")
	output := serviceTelemetryErrorWriter{failure}
	if err := writeServiceTelemetry(strings.NewReader(""), output, receiverProcessAccount{}); err != nil {
		t.Fatalf("empty source returned %v", err)
	}
	const record = `{"type":"receiver-failure","tenant":"feishu","appId":"cli_example","at":"2026-11-19T02:03:04Z","kind":"inbound-failed"}`
	if err := writeServiceTelemetry(strings.NewReader(record+"\n"), output, receiverProcessAccount{}); !errors.Is(err, failure) {
		t.Fatalf("write error = %v, want %v", err, failure)
	}
}

type serviceTelemetryErrorWriter struct {
	err error
}

func (w serviceTelemetryErrorWriter) Write([]byte) (int, error) { return 0, w.err }
