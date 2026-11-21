package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestConfigReceiverReadinessRequiresLoadedRevision(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("APPDATA", home)
	profiles, err := localProfiles()
	if err != nil {
		t.Fatal(err)
	}
	base, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	config, err := bridge.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	config.AppID, config.ReceiverAuthorized = "cli_readiness_a", true
	config.StateDir = filepath.Join(home, "state-a")
	config.AppSecretFile = filepath.Join(home, "app-secret-unused")
	config.MCP.TokenFile = filepath.Join(home, "token-unused")
	extra := config
	extra.AppID, extra.Tenant, extra.ReceiverAuthorized = "cli_readiness_b", "lark", false
	extra.StateDir = filepath.Join(home, "state-b")
	config.AdditionalAccounts = []bridge.Config{extra}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "config.json")
	if _, err := localfile.WritePrivateAtomic(path, raw, true); err != nil {
		t.Fatal(err)
	}
	revision := configRevision(raw)
	registry := localReceiverProcesses(profiles)
	args := []string{"receiver-readiness", "-config", path}
	check := func(state string, wantReady bool) receiverReadinessReport {
		t.Helper()
		var output bytes.Buffer
		err := runConfig(args, nil, &output)
		var report receiverReadinessReport
		if (err == nil) != wantReady || json.Unmarshal(output.Bytes(), &report) != nil || report.Ready != wantReady || len(report.Accounts) != 2 ||
			report.Accounts[0].State != state || report.Accounts[1].State != "disabled" || report.Accounts[1].Tenant != "lark" {
			t.Fatalf("want %q ready=%t, got %s (%v)", state, wantReady, output.String(), err)
		}
		if strings.Contains(output.String(), "secret-unused") || strings.Contains(output.String(), "token-unused") {
			t.Fatal("readiness disclosed a credential reference")
		}
		return report
	}
	check("offline", false)
	if _, err := os.Lstat(registry.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first readiness read created a registry: %v", err)
	}
	account := newReceiverGroupAccount(config.Tenant, config.AppID)
	active, err := registry.registerConfig([]bridge.Config{config}, revision)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if active != nil {
			_ = active.close()
		}
	}()
	check("waiting-sample", false)
	data := bridge.ReceiverDiagnostics{MCPVerified: true, FeishuReady: true}
	active.writeDiagnostics(account, receiverDiagnosticSample{At: time.Now().UTC(), Data: data})
	if got := check("ready", true); got.Revision != revision || got.Accounts[0].Process != active.process.ID || got.Accounts[0].Sample == nil {
		t.Fatalf("ready report lacked provenance: %+v", got)
	}
	data.ReconnectWaiting = true
	active.writeDiagnostics(account, receiverDiagnosticSample{At: time.Now().UTC(), Data: data})
	check("reconnect-wait", false)
	at := time.Now().UTC()
	active.writeWorkerStatus(account, &receiverWorkerSample{At: at, receiverWorkerStatus: receiverWorkerStatus{
		State: receiverWorkerRetrying, Attempts: 1, RetryAt: at.Add(sharedReceiverRetryDelay),
	}})
	check("retrying", false)
	data.ReconnectWaiting = false
	active.writeDiagnostics(account, receiverDiagnosticSample{At: time.Now().UTC(), Data: data})
	newRevision := configRevision(append(raw, '\n'))
	if _, err := localfile.WritePrivateAtomic(path, append(raw, '\n'), false); err != nil {
		t.Fatal(err)
	}
	if got := check("different-revision", false); got.Revision != newRevision || got.Accounts[0].ProcessRevision != revision || got.Accounts[0].Sample != nil {
		t.Fatalf("stale process sample leaked into new revision: %+v", got)
	}
	if _, err := savedReceiverReadiness(profiles, path, revision); !errors.Is(err, errWorkspaceServiceConfigChanged) {
		t.Fatalf("stale requested revision returned %v", err)
	}
	legacy, err := registry.register([]bridge.Config{config})
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.close()
	legacy.writeDiagnostics(account, receiverDiagnosticSample{At: time.Now().UTC(), Data: data})
	check("ambiguous", false)
	if err := active.close(); err != nil {
		t.Fatal(err)
	}
	active = nil
	check("revision-unknown", false)
}
