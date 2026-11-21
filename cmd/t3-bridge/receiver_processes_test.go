package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func (r receiverProcessRegistry) register(accounts []bridge.Config) (*receiverProcessRegistration, error) {
	return r.registerConfig(accounts, "")
}

func TestReceiverProcessCommands(t *testing.T) {
	if command := os.Getenv("T3_BRIDGE_TEST_RECEIVER_PROCESS_COMMAND"); command != "" {
		if err := checkEntry(t, strings.Fields(command)...); err != nil {
			t.Fatal(err)
		}
		return
	}
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("APPDATA", root)
	profiles, err := localProfiles()
	if err != nil {
		t.Fatal(err)
	}
	r := localReceiverProcesses(profiles)
	process, err := r.register([]bridge.Config{{AppID: "cli_test"}, {Tenant: "lark", AppID: "cli_second"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		process.watchStop(ctx, cancel)
		done <- process.close()
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	command := func(args string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReceiverProcessCommands$")
		child.Env = append(os.Environ(), "T3_BRIDGE_TEST_RECEIVER_PROCESS_COMMAND="+args)
		output, err := child.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", args, err, output)
		}
		return string(output)
	}
	id := filepath.Base(process.dir)
	if out := command("ps"); !strings.Contains(out, id) || !strings.Contains(out, "feishu/cli_test, lark/cli_second") || !strings.Contains(out, `version="`+bridgeVersion()+`"`) {
		t.Fatalf("ps omitted receiver, version or accounts: %s", out)
	}
	if out := command("kill " + id); !strings.Contains(out, "T3 work may continue") || !strings.Contains(out, "is no longer active") {
		t.Fatalf("kill omitted shutdown status: %s", out)
	}
	if _, err := os.Stat(process.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("normal shutdown did not remove registration: %v", err)
	}
	if out := command("ps"); !strings.Contains(out, "No active receiver processes") {
		t.Fatalf("ps after shutdown: %s", out)
	}
}

func TestReceiverProcessStaleID(t *testing.T) {
	r := localReceiverProcesses(profileRegistry{root: t.TempDir()})
	accounts := []bridge.Config{{AppID: "cli_test"}}
	old, err := r.register(accounts)
	if err != nil {
		t.Fatal(err)
	}
	oldID := filepath.Base(old.dir)
	if err := r.requestStop(oldID); err != nil {
		t.Fatal(err)
	}
	if err := r.requestStop(oldID); err != nil {
		t.Fatal("repeat stop request:", err)
	}
	// Simulate an abrupt exit, which releases the lock and leaves both files.
	if err := old.lock.Close(); err != nil {
		t.Fatal(err)
	}
	current, err := r.register(accounts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := current.close(); err != nil {
			t.Error(err)
		}
	})
	if err := r.requestStop(oldID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stop accepted stale ID for a reused PID: %v", err)
	}
	if err := r.requestStop("../" + filepath.Base(current.dir)); err == nil {
		t.Fatal("stop accepted a path instead of a process ID")
	}
	processes, err := r.list()
	if err != nil || len(processes) != 1 || processes[0].ID != filepath.Base(current.dir) || processes[0].PID != os.Getpid() {
		t.Fatalf("list after restart: %+v, %v", processes, err)
	}
	if _, err := os.Stat(filepath.Join(current.dir, "stop")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new receiver inherited a stale stop: %v", err)
	}
}
