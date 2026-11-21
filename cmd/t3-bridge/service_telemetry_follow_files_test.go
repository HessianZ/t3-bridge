package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServiceTelemetryFileHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdout.log")
	var stream serviceTelemetryFile
	var output bytes.Buffer
	buffer := make([]byte, serviceLogMaxBytes)
	encoder := json.NewEncoder(&output)
	first, last := telemetryFollowLine(1), telemetryFollowLine(2)
	tail := first + strings.Repeat("x", len(buffer)-len(first)-len(last)-2) + "\n" + last
	appendTelemetryFollowLog(t, path, "prefix"+tail)
	if err := stream.poll(path, true, 3, buffer, encoder, receiverProcessAccount{}); err != nil {
		t.Fatal(err)
	}
	if output.String() != last {
		t.Fatalf("cropped history = %q, want %q", output.String(), last)
	}
	output.Reset()
	stream = serviceTelemetryFile{}
	appendTelemetryFollowLog(t, path, "ordinary log\n")
	if err := stream.poll(path, true, 1, buffer, encoder, receiverProcessAccount{}); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("history exceeded raw line limit: %q", output.String())
	}
}

func TestServiceTelemetryFileIncremental(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdout.log")
	var stream serviceTelemetryFile
	var output bytes.Buffer
	buffer := make([]byte, 64)
	encoder := json.NewEncoder(&output)
	poll := func(initial bool) {
		t.Helper()
		if err := stream.poll(path, initial, 0, buffer, encoder, receiverProcessAccount{}); err != nil {
			t.Fatal(err)
		}
	}
	initial, live := telemetryFollowLine(1), telemetryFollowLine(2)
	appendTelemetryFollowLog(t, path, initial[:len(initial)-1])
	poll(true)
	appendTelemetryFollowLog(t, path, "\n"+live[:len(live)-1])
	for range 3 {
		poll(false)
	}
	if output.Len() != 0 {
		t.Fatalf("emitted a startup or incomplete line: %q", output.String())
	}
	appendTelemetryFollowLog(t, path, "\n")
	poll(false)
	poll(false)
	if output.String() != live {
		t.Fatalf("incremental output = %q, want %q", output.String(), live)
	}
	buffer = make([]byte, serviceLogMaxBytes)
	appendTelemetryFollowLog(t, path, strings.Repeat(" ", serviceTelemetryMaxLineBytes)+telemetryFollowLine(3)+telemetryFollowLine(4))
	poll(false)
	if want := live + telemetryFollowLine(4); output.String() != want {
		t.Fatalf("output after oversized line = %q, want %q", output.String(), want)
	}
}

func TestServiceTelemetryFileRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stderr.log")
	var stream serviceTelemetryFile
	var output bytes.Buffer
	buffer := make([]byte, serviceLogMaxBytes)
	encoder := json.NewEncoder(&output)
	poll := func() {
		t.Helper()
		if err := stream.poll(path, false, 0, buffer, encoder, receiverProcessAccount{}); err != nil {
			t.Fatal(err)
		}
	}
	appendTelemetryFollowLog(t, path, telemetryFollowLine(1)+"partial")
	poll()
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	appendTelemetryFollowLog(t, path, telemetryFollowLine(2)+"partial")
	poll()
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	appendTelemetryFollowLog(t, path, telemetryFollowLine(3))
	poll()
	if want := telemetryFollowLine(1) + telemetryFollowLine(2) + telemetryFollowLine(3); output.String() != want {
		t.Fatalf("rotated output = %q, want %q", output.String(), want)
	}
}

func TestFollowServiceTelemetryFiles(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output bytes.Buffer
	var polls int
	checkDir := func(path string) error {
		if path != dir {
			t.Fatalf("checked directory = %q, want %q", path, dir)
		}
		polls++
		switch polls {
		case 1:
			return os.ErrNotExist
		case 2:
			appendTelemetryFollowLog(t, filepath.Join(dir, "stdout.log"), telemetryFollowLine(1))
		case 3:
			appendTelemetryFollowLog(t, filepath.Join(dir, "stderr.log"), telemetryFollowLine(2))
		default:
			cancel()
			return os.ErrNotExist
		}
		return nil
	}
	if err := followServiceTelemetryFiles(ctx, dir, 0, checkDir, &output, receiverProcessAccount{}); err != nil {
		t.Fatal(err)
	}
	if want := telemetryFollowLine(1) + telemetryFollowLine(2); output.String() != want {
		t.Fatalf("follow output = %q, want %q", output.String(), want)
	}

	failure := errors.New("writer failed")
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := followServiceTelemetryFiles(ctx, dir, 1, func(string) error { return nil }, serviceTelemetryErrorWriter{failure}, receiverProcessAccount{})
	if !errors.Is(err, failure) {
		t.Fatalf("follow error = %v, want %v", err, failure)
	}
}

func telemetryFollowLine(value int) string {
	return fmt.Sprintf(`{"type":"receiver-metric","tenant":"feishu","appId":"cli_follow","at":"2026-11-19T02:03:04Z","name":"pool_active","value":%d}`+"\n", value)
}

func appendTelemetryFollowLog(t *testing.T, path, data string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.WriteString(data)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		t.Fatal(err)
	}
}
