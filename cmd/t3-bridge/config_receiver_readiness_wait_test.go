package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestConfigReceiverReadinessWait(t *testing.T) {
	// Start the process-wide signal watcher outside the virtual clock.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	signal.Stop(signals)

	t.Run("becomes ready", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			path, raw, active := receiverReadinessWaitFixture(t)
			args := []string{"receiver-readiness", "-config", path}
			revision := configRevision(raw)
			var output bytes.Buffer
			start := time.Now()
			err := runConfig(args, nil, &output)
			if elapsed := time.Since(start); err == nil || elapsed != 0 {
				t.Fatalf("readiness without -wait returned %v after %s, want immediate failure", err, elapsed)
			}
			report := decodeReceiverReadinessWaitReport(t, &output)
			if report.Ready || report.Revision != revision || report.Accounts[0].State != "waiting-sample" {
				t.Fatalf("readiness without -wait = %+v", report)
			}

			output.Reset()
			done := make(chan error, 1)
			const wait = 10 * time.Second
			start = time.Now()
			go func() {
				done <- runConfig(append(args, "-wait", wait.String(), "-expected-revision", revision), nil, &output)
			}()
			synctest.Wait()
			if len(done) != 0 || output.Len() != 0 {
				t.Fatalf("readiness returned or wrote before the sample was available: %q", output.String())
			}
			active.writeDiagnostics(active.process.Accounts[0], receiverDiagnosticSample{
				At: time.Now().UTC(), Data: bridge.ReceiverDiagnostics{MCPVerified: true, FeishuReady: true},
			})
			if err := <-done; err != nil || time.Since(start) >= wait {
				t.Fatalf("readiness returned %v after %s, want success before the deadline", err, time.Since(start))
			}
			report = decodeReceiverReadinessWaitReport(t, &output)
			row := report.Accounts[0]
			if !report.Ready || report.Revision != revision || row.State != "ready" ||
				row.Process != active.process.ID || row.ProcessRevision != revision || row.Sample == nil ||
				!row.Sample.Data.MCPVerified || !row.Sample.Data.FeishuReady {
				t.Fatalf("readiness with a matching sample = %+v", report)
			}
		})
	})

	t.Run("times out", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			path, raw, _ := receiverReadinessWaitFixture(t)
			var output bytes.Buffer
			const wait = time.Millisecond
			start := time.Now()
			err := runConfig([]string{"receiver-readiness", "-config", path, "-wait", wait.String()}, nil, &output)
			if elapsed := time.Since(start); err == nil || !strings.Contains(err.Error(), "timed out") || elapsed < wait {
				t.Fatalf("readiness returned %v after %s, want wait timeout", err, elapsed)
			}
			report := decodeReceiverReadinessWaitReport(t, &output)
			if report.Ready || report.Revision != configRevision(raw) || report.Accounts[0].State != "waiting-sample" {
				t.Fatalf("readiness at timeout = %+v", report)
			}
		})
	})

	t.Run("saved revision changes", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			path, raw, _ := receiverReadinessWaitFixture(t)
			var output bytes.Buffer
			done := make(chan error, 1)
			go func() {
				done <- runConfig([]string{"receiver-readiness", "-config", path, "-wait", "10s"}, nil, &output)
			}()
			synctest.Wait()
			if len(done) != 0 || output.Len() != 0 {
				t.Fatalf("readiness returned or wrote before the revision changed: %q", output.String())
			}
			if _, err := localfile.WritePrivateAtomic(path, append(raw, '\n'), false); err != nil {
				t.Fatal(err)
			}
			if err := <-done; !errors.Is(err, errWorkspaceServiceConfigChanged) || output.Len() != 0 {
				t.Fatalf("readiness after the revision changed returned %v and %q", err, output.String())
			}
		})
	})

	t.Run("rejects nonpositive waits", func(t *testing.T) {
		path, _, _ := receiverReadinessWaitFixture(t)
		for _, value := range []string{"-1s", "0"} {
			var output bytes.Buffer
			err := runConfig([]string{"receiver-readiness", "-config", path, "-wait", value}, nil, &output)
			if err == nil || !strings.Contains(err.Error(), "wait") || json.Valid(output.Bytes()) {
				t.Errorf("-wait %s returned %v and %q, want a flag error", value, err, output.String())
			}
		}
	})
}

func receiverReadinessWaitFixture(t *testing.T) (string, []byte, *receiverProcessRegistration) {
	t.Helper()
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
	config.AppID, config.ReceiverAuthorized = "cli_readiness_wait", true
	config.StateDir = filepath.Join(home, "state")
	config.AppSecretFile = filepath.Join(home, "app-secret-unused")
	config.MCP.TokenFile = filepath.Join(home, "token-unused")
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "config.json")
	if _, err := localfile.WritePrivateAtomic(path, raw, true); err != nil {
		t.Fatal(err)
	}
	active, err := localReceiverProcesses(profiles).registerConfig([]bridge.Config{config}, configRevision(raw))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = active.close() })
	return path, raw, active
}

func decodeReceiverReadinessWaitReport(t *testing.T, output *bytes.Buffer) receiverReadinessReport {
	t.Helper()
	var report receiverReadinessReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil || len(report.Accounts) != 1 {
		t.Fatalf("want exactly one readiness JSON report with one account, got %q (%v)", output.String(), err)
	}
	return report
}
