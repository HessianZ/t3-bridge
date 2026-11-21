package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestConfigReceiverStatusReadsExactAppSnapshot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("APPDATA", root)
	profiles, err := localProfiles()
	if err != nil {
		t.Fatal(err)
	}
	registry := localReceiverProcesses(profiles)
	process, err := registry.register([]bridge.Config{{AppID: "cli_first"}, {Tenant: "lark", AppID: "cli_second"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.close() })
	now := time.Now().UTC()
	process.process.StartedAt = now.Add(-time.Minute)
	raw, err := json.Marshal(process.process)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localfile.WritePrivateAtomic(filepath.Join(process.dir, "process.json"), raw, false); err != nil {
		t.Fatal(err)
	}
	first, second := process.process.Accounts[0], process.process.Accounts[1]
	process.writeDiagnostics(first, receiverDiagnosticSample{
		At: now, Data: bridge.ReceiverDiagnostics{MCPVerified: true, Reconcile: 2},
		Events: []receiverDiagnosticEvent{{At: now, Kind: receiverDiagnosticReconcile, Count: 2}},
	})
	process.writeDiagnostics(second, receiverDiagnosticSample{
		At: now.Add(-31 * time.Second), Data: bridge.ReceiverDiagnostics{FeishuReady: true, Reconcile: 9},
	})
	command := func(id string, account receiverProcessAccount) (string, error) {
		var output bytes.Buffer
		err := runConfig([]string{"receiver-status", "-process", id, "-tenant", account.Tenant, "-app-id", account.AppID}, strings.NewReader(""), &output)
		return output.String(), err
	}
	fresh, err := command(process.process.ID, first)
	if err != nil {
		t.Fatal(err)
	}
	var view receiverStatusView
	if err := json.Unmarshal([]byte(fresh), &view); err != nil {
		t.Fatal(err)
	}
	if view.Process != process.process.ID || view.PID != process.process.PID || view.AppID != first.AppID || view.Tenant != first.Tenant ||
		view.Worker != nil || view.Sample == nil || !view.Sample.At.Equal(now) || !view.Sample.Data.MCPVerified || view.Sample.Data.FeishuReady ||
		view.Sample.Data.Reconcile != 2 || len(view.Sample.Events) != 1 || view.Sample.Events[0].Count != 2 || !strings.Contains(fresh, `"feishuReady":false`) {
		t.Fatalf("fresh result = %s", fresh)
	}
	if stale, err := command(process.process.ID, second); err != nil || !strings.Contains(stale, `"sample":null`) {
		t.Fatalf("expired result = %s, %v", stale, err)
	}
	if out, err := command(process.process.ID, receiverProcessAccount{Tenant: "feishu", AppID: second.AppID}); err == nil || out != "" {
		t.Fatalf("cross-tenant query = %s, %v", out, err)
	}
	if out, err := command(process.process.ID[:8], first); err == nil || out != "" {
		t.Fatalf("partial process ID = %s, %v", out, err)
	}
	if match, err := receiverStatusForAccount(profiles, "", second); err != nil || match.Process != process.process.ID || match.Sample != nil {
		t.Fatalf("unique App lookup = %+v, %v", match, err)
	}
	retrying := receiverWorkerSample{At: now, receiverWorkerStatus: receiverWorkerStatus{
		State: receiverWorkerRetrying, Attempts: 2, Failure: "initial-connect-failed", RetryAt: now.Add(30 * time.Second),
	}}
	process.writeWorkerStatus(second, &retrying)
	workerOutput, err := command(process.process.ID, second)
	view = receiverStatusView{}
	if err != nil || json.Unmarshal([]byte(workerOutput), &view) != nil || view.Sample != nil || view.Worker == nil ||
		*view.Worker != retrying || view.AppID != second.AppID || view.Tenant != second.Tenant {
		t.Fatalf("retrying App result = %s, %v", workerOutput, err)
	}
	duplicate, err := registry.register([]bridge.Config{{AppID: first.AppID}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = duplicate.close() })
	if _, err := receiverStatusForAccount(profiles, "", first); err == nil {
		t.Fatal("ambiguous App lookup succeeded")
	}
	if missing, err := command(duplicate.process.ID, first); err != nil || !strings.Contains(missing, `"sample":null`) {
		t.Fatalf("missing sample on selected process = %s, %v", missing, err)
	}
}
