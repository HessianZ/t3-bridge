package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestServiceTelemetryFollowOptions(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-follow"}, "-follow requires -telemetry"},
		{[]string{"-telemetry", "-lines", "0"}, "-lines must be between 1 and"},
		{[]string{"-telemetry", "-follow", "-lines", "-1"}, "-lines must be between 0 and"},
	} {
		args := append([]string{"logs", "-instance", "fixture"}, tc.args...)
		if err := runHostService(args, io.Discard); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%v: %v", args, err)
		}
	}
	var help bytes.Buffer
	if err := runHostService([]string{"logs", "-help"}, &help); err != nil || !strings.Contains(help.String(), "-follow") {
		t.Fatalf("logs help: %q, %v", &help, err)
	}
}

func TestServiceTelemetryFollowChecksJournalUnit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix private service definitions")
	}
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	instance := writeInventoryDefinition(t, "linux", "test")
	ctx := context.Background()
	output := new(bytes.Buffer)
	scope := receiverProcessAccount{Tenant: "feishu", AppID: "cli_example"}
	started := 0
	journal := func(gotCtx context.Context, gotOutput io.Writer, gotScope receiverProcessAccount, args ...string) error {
		started++
		want := []string{"--user-unit=" + instance.id, "--no-pager", "--quiet", "--lines=0", "--output=cat", "--follow", fmt.Sprintf("_UID=%d", os.Getuid())}
		if gotCtx != ctx || gotOutput != output || gotScope != scope || !slices.Equal(args, want) {
			t.Fatalf("unexpected journal invocation: %v", args)
		}
		return nil
	}
	manager := func(string, ...string) ([]byte, error) {
		return []byte("LoadState=not-found\nActiveState=inactive\n"), nil
	}
	if err := instance.followTelemetry(ctx, 0, manager, journal, output, scope); err != nil || started != 1 {
		t.Fatalf("managed journal: %d starts, %v", started, err)
	}
	manager = func(string, ...string) ([]byte, error) {
		return []byte("LoadState=loaded\nFragmentPath=/unmanaged/foreign.service\nActiveState=active\n"), nil
	}
	if err := instance.followTelemetry(ctx, 0, manager, journal, output, scope); err == nil || started != 1 {
		t.Fatalf("foreign journal: %d starts, %v", started, err)
	}
}

func TestFollowServiceJournalStreamsAndStops(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell for the synthetic journal command")
	}
	const record = `{"type":"receiver-metric","tenant":"feishu","appId":"cli_example","at":"2026-11-19T02:03:04Z","name":"ws_reconnect","value":1}`
	scope := receiverProcessAccount{Tenant: "feishu", AppID: "cli_example"}
	dir := t.TempDir()
	otherTenant := strings.Replace(record, `"feishu"`, `"lark"`, 1)
	journal := "#!/bin/sh\nprintf '%s\\n' 'ordinary log message' '" + otherTenant + "' '" + record + "'\nexec /bin/sleep 30\n"
	if err := os.WriteFile(filepath.Join(dir, "journalctl"), []byte(journal), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var output bytes.Buffer
	writer := followJournalOutput{Buffer: &output, afterWrite: cancel}
	if err := followServiceJournal(ctx, writer, scope); err != nil || output.String() != record+"\n" || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("stream before exit: %q, context = %v, error = %v", &output, ctx.Err(), err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	failure := errors.New("collector closed output")
	if err := followServiceJournal(ctx, serviceTelemetryErrorWriter{failure}, scope); !errors.Is(err, failure) {
		t.Fatalf("output failure: %v", err)
	}
}

type followJournalOutput struct {
	*bytes.Buffer
	afterWrite func()
}

func (w followJournalOutput) Write(data []byte) (int, error) {
	n, err := w.Buffer.Write(data)
	w.afterWrite()
	return n, err
}
