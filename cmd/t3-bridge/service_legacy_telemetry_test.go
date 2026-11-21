package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLegacyTelemetryRelayConsumesServiceExport(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("optional Node runtime unavailable")
	}
	adapter := filepath.Join(t.TempDir(), "old adapter #1.mjs")
	if err := os.WriteFile(adapter, []byte(`
export default (meta) => ({
    emit(event) { console.log(JSON.stringify({ meta, event })); },
    recordMetric(name, value, tags) { console.log(JSON.stringify({ meta, name, value, tags })); },
    recordError(error, context) { console.log(JSON.stringify({ meta, error, context })); },
    flush() { console.log(JSON.stringify({ meta, lifecycle: "flush" })); },
    close() { console.log(JSON.stringify({ meta, lifecycle: "close" })); },
});
`), 0600); err != nil {
		t.Fatal(err)
	}
	account := receiverProcessAccount{Tenant: "feishu", AppID: "cli_export"}
	var logs bytes.Buffer
	writeReceiverMetric(&logs, account, "run_e2e_ms", 35, "completed")
	writeReceiverDiagnosticLogs(&logs, account, []receiverDiagnosticEvent{{
		At: time.Now().UTC(), Kind: receiverDiagnosticDeliveryPending, Count: 2,
	}})
	writeReceiverFailure(&logs, account, "channel-stopped")
	logs.WriteString("unstructured log with synthetic-secret\n")
	var feed bytes.Buffer
	if err := writeServiceTelemetry(&logs, &feed, receiverProcessAccount{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "../../deploy/lark-telemetry-relay.mjs", "--bridge-version", "v1.2.3", "--module", adapter)
	cmd.Stdin = &feed
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("optional relay failed: %v, %s", err, out)
	}
	for _, want := range []string{
		`"version":"1.2.3"`, `"appId":"cli_export"`, `"tenant":"feishu"`,
		`"name":"run_e2e_ms","value":35,"tags":{"terminal":"done"}`,
		`"event":"delivery-pending","fields":{"count":2}`, `"error":"receiver.channel-stopped"`,
		`"lifecycle":"flush"`, `"lifecycle":"close"`,
	} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("collector did not receive %s: %s", want, out)
		}
	}
	lines := bytes.Split(bytes.TrimSpace(out), []byte{'\n'})
	if len(lines) != 6 || bytes.Contains(out, []byte("synthetic-secret")) {
		t.Fatalf("unexpected collector records: %s", out)
	}
	for _, line := range lines {
		if !json.Valid(line) {
			t.Fatalf("unexpected relay output: %s", line)
		}
	}
}
