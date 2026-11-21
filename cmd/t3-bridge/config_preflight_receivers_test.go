package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestPreflightReportsActiveAndStaleReceiverLocks(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("APPDATA", root)
	t.Setenv("LARK_CHANNEL_HOME", filepath.Join(root, "old"))
	profiles, err := localProfiles()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	rows := []bridge.ReceiverPreflightAccount{
		{Tenant: "feishu", AppID: "cli_go", Enabled: true},
		{Tenant: "feishu", AppID: "cli_old"},
		{Tenant: "feishu", AppID: "cli_stale"},
		{Tenant: "lark", AppID: "cli_go"},
	}
	report := bridge.ReceiverPreflight{Passed: true, Accounts: append([]bridge.ReceiverPreflightAccount(nil), rows...)}
	checkPreflightReceivers(&report, now)
	if !report.Passed {
		t.Fatalf("no local receiver reported a conflict: %+v", report)
	}
	if _, err := os.Stat(profiles.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only check created the t3-bridge profile directory: %v", err)
	}

	release, err := acquireReceiverLocks(profiles, []bridge.Config{{AppID: "cli_go"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	createLegacyLock(t, os.Getenv("LARK_CHANNEL_HOME"), "cli_old", now)
	stale := createLegacyLock(t, os.Getenv("LARK_CHANNEL_HOME"), "cli_stale", now.Add(-time.Minute))
	report = bridge.ReceiverPreflight{Passed: true, Accounts: append([]bridge.ReceiverPreflightAccount(nil), rows...)}
	checkPreflightReceivers(&report, now)
	assertReceiverCheckStatuses(t, report, [][2]string{{"failed", "ok"}, {"ok", "failed"}, {"ok", "unknown"}, {"ok", "ok"}})
	if report.Passed {
		t.Fatal("enabled t3-bridge conflict was ignored")
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("preflight changed the old lock: %v", err)
	}

	release()
	report = bridge.ReceiverPreflight{Passed: true, Accounts: append([]bridge.ReceiverPreflightAccount(nil), rows...)}
	checkPreflightReceivers(&report, now)
	assertReceiverCheckStatuses(t, report, [][2]string{{"ok", "ok"}, {"ok", "failed"}, {"ok", "unknown"}, {"ok", "ok"}})
	if !report.Passed {
		t.Fatal("disabled accounts prevented readiness of the enabled account")
	}
	report = bridge.ReceiverPreflight{Passed: true, AllAccounts: &bridge.ReceiverPreflightOutcome{Passed: true}, Accounts: append([]bridge.ReceiverPreflightAccount(nil), rows...)}
	checkPreflightReceivers(&report, now)
	if !report.Passed || report.AllAccounts.Passed {
		t.Fatal("a disabled account's app lock should block expanded verification only")
	}
	rows[1].Enabled = true
	report = bridge.ReceiverPreflight{Passed: true, Accounts: rows}
	checkPreflightReceivers(&report, now)
	if report.Passed {
		t.Fatal("enabled legacy receiver conflict was ignored")
	}
}

func TestConfigPreflightIncludesDisabledAppAndDoesNotReadLegacyConfig(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("APPDATA", root)
	t.Setenv("LARK_CHANNEL_HOME", filepath.Join(root, "legacy"))
	raw, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	values["stateDir"] = stateDir
	raw, err = json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	config, err := bridge.ParseConfig(raw)
	if err != nil || config.ReceiverAuthorized {
		t.Fatalf("expected a disabled template: %v", err)
	}
	legacyLock := createLegacyLock(t, os.Getenv("LARK_CHANNEL_HOME"), config.AppID, time.Now())
	target := strings.TrimSuffix(legacyLock, ".lock")
	holder := bridge.ReceiverLockHolder{Profile: "工作.Codex", PID: 1234}
	data, err := json.Marshal(map[string]any{
		"kind": "app", "target": target, "appId": config.AppID,
		"profile": holder.Profile, "pid": holder.PID, "agentKind": "t3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localfile.WritePrivateAtomic(target+".meta.json", data, true); err != nil {
		t.Fatal(err)
	}
	held, err := localfile.Lock(filepath.Join(stateDir, "lock"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	path := filepath.Join(root, "candidate.json")
	if _, err := localfile.WritePrivateAtomic(path, raw, true); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := runConfigPreflight([]string{"-config", path}, &output); err == nil {
		t.Fatal("disabled configuration reported readiness")
	}
	var report bridge.ReceiverPreflight
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Passed || report.AllAccounts != nil || len(report.Accounts) != 1 || report.Accounts[0].Enabled {
		t.Fatalf("unexpected preflight report: %+v", report)
	}
	checks := report.Accounts[0].Checks
	if len(checks) != 4 || checks[3].Check != "state-lock" || checks[3].Status != "failed" {
		t.Fatalf("the shared state directory lock was not found: %+v", checks)
	}
	if got := checks[1].LockHolder; got == nil || *got != holder {
		t.Fatalf("CLI report did not include the account's old lock holder: %+v", got)
	}
	if checks[2].Check != "setup" || !slices.Contains(checks[2].Fields, `routes["work"].projectId`) {
		t.Fatalf("disabled template did not report its unresolved target: %+v", checks)
	}
	report.Accounts[0].Checks = checks[:2]
	assertReceiverCheckStatuses(t, report, [][2]string{{"ok", "failed"}})
	if _, err := os.Stat(filepath.Join(stateDir, "state.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preflight wrote durable state: %v", err)
	}
	output.Reset()
	if err := runConfigPreflight([]string{"-config", path, "-include-disabled"}, &output); err == nil {
		t.Fatal("incomplete draft passed expanded verification")
	}
	report = bridge.ReceiverPreflight{}
	if err := json.Unmarshal(output.Bytes(), &report); err != nil ||
		report.Passed || report.AllAccounts == nil || report.AllAccounts.Passed || report.Accounts[0].Enabled {
		t.Fatalf("unexpected expanded report or changed authorization: %+v (%v)", report, err)
	}
	saved, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(saved, raw) {
		t.Fatal("preflight changed the saved source")
	}
}

func TestConfigPreflightScopesToSavedApp(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("APPDATA", root)
	t.Setenv("LARK_CHANNEL_HOME", filepath.Join(root, "legacy"))
	raw, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	var config bridge.Config
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	config.AppID, config.StateDir, config.ReceiverAuthorized = "cli_primary", filepath.Join(root, "primary"), true
	extra := config
	extra.AppID, extra.StateDir, extra.ReceiverAuthorized = "cli_other", filepath.Join(root, "other"), false
	config.AdditionalAccounts = []bridge.Config{extra}
	raw, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "candidate.json")
	if _, err := localfile.WritePrivateAtomic(path, raw, true); err != nil {
		t.Fatal(err)
	}
	createLegacyLock(t, os.Getenv("LARK_CHANNEL_HOME"), config.AppID, time.Now())
	for _, app := range config.Accounts() {
		var output bytes.Buffer
		if err := runConfigPreflight([]string{"-config", path, "-app-id", app.AppID, "-include-disabled"}, &output); err == nil {
			t.Fatal("unresolved template passed preflight")
		}
		var report bridge.ReceiverPreflight
		if err := json.Unmarshal(output.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		if report.Passed || report.AllAccounts == nil || report.AllAccounts.Passed || len(report.Accounts) != 1 ||
			report.Accounts[0].AppID != app.AppID || report.Accounts[0].Enabled != app.ReceiverAuthorized {
			t.Fatalf("unexpected preflight scope or outcome: %+v", report)
		}
		checks := report.Accounts[0].Checks
		wantLegacy := "ok"
		if app.ReceiverAuthorized {
			wantLegacy = "failed"
		}
		if len(checks) != 4 || checks[1].Check != "legacy-receiver-lock" || checks[1].Status != wantLegacy || checks[2].Check != "setup" {
			t.Fatalf("unexpected checks for %s: %+v", app.AppID, checks)
		}
	}
	var output bytes.Buffer
	if err := runConfigPreflight([]string{"-config", path, "-app-id", "CLI_OTHER", "-include-disabled"}, &output); !errors.Is(err, errWorkspacePreflightApp) || output.Len() != 0 {
		t.Fatalf("unknown App produced a report: %v, %s", err, output.String())
	}
	if _, err := checkSavedReceiverAppConfig(t.Context(), path, "old-revision", extra.AppID, true); !errors.Is(err, errWorkspaceServiceConfigChanged) {
		t.Fatalf("stale saved revision was accepted: %v", err)
	}
	if saved, err := os.ReadFile(path); err != nil || !bytes.Equal(saved, raw) {
		t.Fatalf("preflight changed the saved configuration: %v", err)
	}
}

func TestLegacyReceiverLockHolderRequiresMatchingMetadata(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	lock := createLegacyLock(t, root, "cli_old", now)
	target := strings.TrimSuffix(lock, ".lock")
	metaFile := target + ".meta.json"
	for _, tc := range []struct {
		name    string
		appID   string
		path    string
		profile string
	}{
		{"other app", "cli_other", target, "codex"},
		{"other target", "cli_old", target + "-other", "codex"},
		{"control in profile", "cli_old", target, "codex\nPID 42"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(map[string]any{
				"kind": "app", "target": tc.path, "appId": tc.appID, "profile": tc.profile, "pid": 42,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := localfile.WritePrivateAtomic(metaFile, data, false); err != nil {
				t.Fatal(err)
			}
			check := checkLegacyReceiverAppLock([]string{root}, nil, "cli_old", now)
			if check.Status != "failed" || check.LockHolder != nil {
				t.Fatalf("invalid metadata changed the lock verdict or was shown: %+v", check)
			}
		})
	}
}

func createLegacyLock(t *testing.T, root, appID string, updated time.Time) string {
	t.Helper()
	path := filepath.Join(root, "registry", "locks", "app", appID+".lock.lock")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, updated, updated); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertReceiverCheckStatuses(t *testing.T, report bridge.ReceiverPreflight, want [][2]string) {
	t.Helper()
	for i, row := range report.Accounts {
		if len(row.Checks) != 2 || row.Checks[0].Check != "receiver-lock" || row.Checks[1].Check != "legacy-receiver-lock" ||
			[2]string{row.Checks[0].Status, row.Checks[1].Status} != want[i] {
			t.Fatalf("unexpected checks for %s: %+v", row.AppID, row.Checks)
		}
	}
}
