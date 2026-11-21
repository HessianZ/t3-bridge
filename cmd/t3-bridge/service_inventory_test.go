package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestListServiceInstances(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	active := writeInventoryDefinition(t, "linux", "active")
	writeInventoryDefinition(t, "linux", "broken")
	bad := writeInventoryDefinition(t, "linux", "unmanaged")
	if err := os.WriteFile(bad.file, []byte("[Unit]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(active.file), "foreign.service"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	manager := func(command string, args ...string) ([]byte, error) {
		if command != "systemctl" || len(args) != 5 || !slices.Equal(args[:3], []string{"--user", "--no-pager", "show"}) {
			t.Fatalf("unexpected manager call: %s %v", command, args)
		}
		switch args[len(args)-1] {
		case active.id:
			return fmt.Appendf(nil, "LoadState=loaded\nFragmentPath=%s\nActiveState=active\nMainPID=42\n", active.file), nil
		case "t3-bridge-managed-broken.service":
			return nil, errors.New(strings.Repeat("fault\n", 200))
		default:
			t.Fatalf("inspected unrelated definition: %v", args)
			return nil, nil
		}
	}
	var output bytes.Buffer
	if err := listServiceInstances("linux", manager, &output); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if len(lines) != 3 || lines[0] != "active: active (pid 42)" ||
		!strings.HasPrefix(lines[1], "broken: error reading status:") || len(lines[1]) > 240 ||
		!strings.HasPrefix(lines[2], "unmanaged: error checking definition:") {
		t.Fatalf("unexpected inventory: %s", &output)
	}
}

func TestServiceInventoryJournal(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	instance := writeInventoryDefinition(t, "linux", "test")
	manager := func(string, ...string) ([]byte, error) {
		return []byte("LoadState=not-found\nActiveState=inactive\n"), nil
	}
	journal := func(command string, args ...string) ([]byte, error) {
		want := []string{"--user-unit=" + instance.id, "--no-pager", "--quiet", "--lines=2", fmt.Sprintf("_UID=%d", os.Getuid())}
		if command != "journalctl" || !slices.Equal(args, want) {
			t.Fatalf("unexpected journal call: %s %v", command, args)
		}
		var tail serviceJournalTail
		for _, data := range []string{"prefix", strings.Repeat("x", serviceLogMaxBytes), "\nprevious\nlatest\n"} {
			_, _ = tail.Write([]byte(data))
		}
		if len(tail) != serviceLogMaxBytes {
			t.Fatalf("journal byte limit: got %d bytes", len(tail))
		}
		return tail, nil
	}
	var output bytes.Buffer
	if err := instance.readLogs(2, manager, journal, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "previous\nlatest\n" {
		t.Fatalf("unexpected logs: %q", output.String())
	}
}

func TestServiceInventoryTelemetryUsesManagedJournalMessages(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	instance := writeInventoryDefinition(t, "linux", "test")
	account := receiverProcessAccount{Tenant: "feishu", AppID: "cli_test"}
	var metrics bytes.Buffer
	writeReceiverMetric(&metrics, account, "ws_reconnect", 1, "")
	var output bytes.Buffer
	manager := func(string, ...string) ([]byte, error) {
		return []byte("LoadState=not-found\nActiveState=inactive\n"), nil
	}
	journal := func(command string, args ...string) ([]byte, error) {
		want := []string{"--output=cat", "--user-unit=" + instance.id, "--no-pager", "--quiet", "--lines=2", fmt.Sprintf("_UID=%d", os.Getuid())}
		if command != "journalctl" || !slices.Equal(args, want) {
			t.Fatalf("unexpected journal call: %s %v", command, args)
		}
		return append([]byte("private stdout text\n"), metrics.Bytes()...), nil
	}
	if err := instance.readTelemetry(2, manager, journal, &output, receiverProcessAccount{}); err != nil {
		t.Fatal(err)
	}
	if bytes.Count(output.Bytes(), []byte("\n")) != 1 || !strings.Contains(output.String(), `"name":"ws_reconnect"`) || strings.Contains(output.String(), "private") {
		t.Fatalf("unexpected telemetry: %q", output.String())
	}
	output.Reset()
	manager = func(string, ...string) ([]byte, error) {
		return []byte("LoadState=loaded\nFragmentPath=/unmanaged/foreign.service\nActiveState=active\nMainPID=42\n"), nil
	}
	journal = func(string, ...string) ([]byte, error) {
		t.Fatal("unmanaged service reached the telemetry reader")
		return nil, nil
	}
	if err := instance.readTelemetry(2, manager, journal, &output, receiverProcessAccount{}); err == nil || output.Len() != 0 {
		t.Fatalf("unmanaged service exported telemetry: %q, %v", output.String(), err)
	}
}

func TestServiceInventoryLaunchdLogs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	instance := writeInventoryDefinition(t, "darwin", "test")
	if err := os.MkdirAll(instance.logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stdout := filepath.Join(instance.logDir, "stdout.log")
	if err := os.WriteFile(stdout, []byte(strings.Repeat("x", serviceLogMaxBytes)+"\nlatest output"), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr := filepath.Join(instance.logDir, "stderr.log")
	if err := os.WriteFile(stderr, []byte("older error\nlatest error\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := instance.readLogs(1, nil, nil, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "==> stdout.log <==\nlatest output\n==> stderr.log <==\nlatest error\n" {
		t.Fatalf("unexpected logs: %q", output.String())
	}
	if err := os.Remove(stderr); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(stdout, stderr); err != nil {
		t.Fatal(err)
	}
	if err := instance.readLogs(1, nil, nil, &output); err == nil {
		t.Fatal("accepted a symlink in place of stderr.log")
	}
}

func writeInventoryDefinition(t *testing.T, platform, name string) serviceInstance {
	t.Helper()
	instance, err := resolveServiceInstance(platform, name, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(instance.file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(instance.file, []byte(instance.render("/tmp/bridge", "/tmp/config.json")), 0o600); err != nil {
		t.Fatal(err)
	}
	return instance
}
