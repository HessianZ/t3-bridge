package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestReceiverTelemetryArchiveRotationAndRetention(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	dir := filepath.Join(stateDir, "telemetry")
	if err := localfile.MkdirAllPrivate(dir); err != nil {
		t.Fatal(err)
	}
	expired := filepath.Join(dir, "bridge-2026-11-17.jsonl")
	boundary := filepath.Join(dir, "bridge-2026-11-18.jsonl")
	for _, name := range []string{
		filepath.Base(expired), filepath.Base(boundary),
		"other-2026-11-17.jsonl", "bridge-2026-11-17.jsonl.bak", "bridge-2026-10-32.jsonl",
	} {
		writeReceiverTelemetryFixture(t, filepath.Join(dir, name), scopedTelemetryFeishu)
	}
	otherDir := filepath.Join(dir, "bridge-2026-11-16.jsonl")
	if err := localfile.MkdirAllPrivate(otherDir); err != nil {
		t.Fatal(err)
	}

	// Both writes take place on November 19 in the clock's local zone.
	now := time.Date(2026, 11, 19, 15, 59, 0, 0, time.FixedZone("UTC-08", -8*60*60))
	account := receiverProcessAccount{Tenant: "feishu", AppID: "cli_scope"}
	var stdout, warnings bytes.Buffer
	output := newReceiverTelemetryOutput(stateDir, account, 2, func() time.Time { return now }, &stdout, &warnings)
	defer output.Close()
	requireReceiverTelemetryMissing(t, expired)
	requireReceiverTelemetryArchive(t, boundary, scopedTelemetryFeishu)

	first := strings.Replace(scopedTelemetryFeishu, "2026-11-19T02:03:04Z", "2026-11-19T23:59:00Z", 1)
	writeReceiverTelemetryTestLine(t, output, first)
	requireReceiverTelemetryArchive(t, filepath.Join(dir, "bridge-2026-11-19.jsonl"), first)
	requireReceiverTelemetryArchive(t, boundary, scopedTelemetryFeishu)

	now = now.Add(time.Minute)
	second := strings.Replace(first, "2026-11-19T23:59:00Z", "2026-11-20T00:00:00Z", 1)
	writeReceiverTelemetryTestLine(t, output, second)
	requireReceiverTelemetryMissing(t, boundary)
	requireReceiverTelemetryArchive(t, filepath.Join(dir, "bridge-2026-11-19.jsonl"), first)
	requireReceiverTelemetryArchive(t, filepath.Join(dir, "bridge-2026-11-20.jsonl"), second)
	for _, name := range []string{"other-2026-11-17.jsonl", "bridge-2026-11-17.jsonl.bak", "bridge-2026-10-32.jsonl"} {
		requireReceiverTelemetryArchive(t, filepath.Join(dir, name), scopedTelemetryFeishu)
	}
	if err := localfile.CheckPrivateDir(otherDir); err != nil {
		t.Fatalf("archive-like directory was removed or altered: %v", err)
	}
	if stdout.String() != first+second || warnings.Len() != 0 {
		t.Fatalf("stdout = %q, warnings = %q", &stdout, &warnings)
	}
}

func TestReceiverTelemetryArchiveScopeAndReopen(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := localfile.MkdirAllPrivate(stateDir); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return time.Date(2026, 11, 19, 2, 3, 4, 0, time.UTC) }
	account := receiverProcessAccount{Tenant: "feishu", AppID: "cli_scope"}
	var stdout, warnings bytes.Buffer
	lines := []string{
		"ordinary log\n",
		scopedTelemetryLark,
		scopedTelemetryPrefix,
		strings.Replace(scopedTelemetryFeishu, "cli_scope", "cli_SCOPE", 1),
		strings.Replace(scopedTelemetryFeishu, "run-started", "synthetic-private-kind", 1),
		scopedTelemetrySecret,
	}
	t.Run("first open", func(t *testing.T) {
		output := newReceiverTelemetryOutput(stateDir, account, 2, clock, &stdout, &warnings)
		defer output.Close()
		for _, line := range lines {
			writeReceiverTelemetryTestLine(t, output, line)
		}
	})

	dir := filepath.Join(stateDir, "telemetry")
	if err := localfile.CheckPrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "bridge-2026-11-19.jsonl")
	requireReceiverTelemetryArchive(t, path, scopedTelemetryFeishu)
	broken := `{"type":"receiver-event",`
	file, err := localfile.OpenPrivateAppend(path)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.WriteString(broken)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		t.Fatal(err)
	}

	output := newReceiverTelemetryOutput(stateDir, account, 2, clock, &stdout, &warnings)
	defer output.Close()
	second := strings.Replace(scopedTelemetryFeishu, "run-started", "run-completed", 1)
	writeReceiverTelemetryTestLine(t, output, second)
	data, err := readReceiverTelemetryArchive(stateDir, account, clock(), 10)
	if err != nil || strings.Contains(string(data), broken+"\n") || bytes.Count(data, []byte{'\n'}) != 2 ||
		!strings.Contains(string(data), `"kind":"run-completed"`) {
		t.Fatalf("archive after a partial write = %q, %v", data, err)
	}
	if stdout.String() != strings.Join(lines, "")+second || warnings.Len() != 0 {
		t.Fatalf("stdout = %q, warnings = %q", &stdout, &warnings)
	}
}

func TestReceiverTelemetryArchivePreservesSymlink(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "telemetry")
	if err := localfile.MkdirAllPrivate(dir); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "unmanaged.jsonl")
	writeReceiverTelemetryFixture(t, target, scopedTelemetryFeishu)
	link := filepath.Join(dir, "bridge-2026-11-01.jsonl")
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" || errors.Is(err, os.ErrPermission) || errors.Is(err, errors.ErrUnsupported) {
			t.Skipf("cannot create symlink: %v", err)
		}
		t.Fatal(err)
	}
	clock := func() time.Time { return time.Date(2026, 11, 19, 2, 3, 4, 0, time.UTC) }
	account := receiverProcessAccount{Tenant: "feishu", AppID: "cli_scope"}
	output := newReceiverTelemetryOutput(filepath.Dir(dir), account, 2, clock, io.Discard, io.Discard)
	defer output.Close()
	writeReceiverTelemetryTestLine(t, output, scopedTelemetryFeishu)
	if got, err := os.Readlink(link); err != nil || got != target {
		t.Fatalf("expired-name symlink target = %q, error = %v, want %q", got, err, target)
	}
	requireReceiverTelemetryArchive(t, target, scopedTelemetryFeishu)
}

func TestReceiverTelemetryArchivePreservesHardLinks(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	dir := receiverTelemetryArchiveDir(stateDir)
	if err := localfile.MkdirAllPrivate(dir); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(stateDir, "unmanaged.jsonl")
	writeReceiverTelemetryFixture(t, target, scopedTelemetryFeishu)
	for _, day := range []string{"2026-11-01", "2026-11-19"} {
		if err := os.Link(target, filepath.Join(dir, receiverTelemetryArchiveName(day))); err != nil {
			if errors.Is(err, errors.ErrUnsupported) || errors.Is(err, os.ErrPermission) {
				t.Skipf("cannot create hard link: %v", err)
			}
			t.Fatal(err)
		}
	}
	clock := func() time.Time { return time.Date(2026, 11, 19, 2, 3, 4, 0, time.UTC) }
	account := receiverProcessAccount{Tenant: "feishu", AppID: "cli_scope"}
	var warnings bytes.Buffer
	output := newReceiverTelemetryOutput(stateDir, account, 2, clock, io.Discard, &warnings)
	defer output.Close()
	writeReceiverTelemetryTestLine(t, output, scopedTelemetryFeishu)
	if warnings.Len() == 0 {
		t.Fatal("an unsafe archive should produce a warning")
	}
	requireReceiverTelemetryArchive(t, target, scopedTelemetryFeishu)
	for _, day := range []string{"2026-11-01", "2026-11-19"} {
		requireReceiverTelemetryArchive(t, filepath.Join(dir, receiverTelemetryArchiveName(day)), scopedTelemetryFeishu)
	}
	if _, err := readReceiverTelemetryArchive(stateDir, account, clock(), 1); err == nil {
		t.Fatal("archive reader accepted a hard link")
	}
}

func TestReceiverTelemetryArchiveOutputErrors(t *testing.T) {
	clock := func() time.Time { return time.Date(2026, 11, 19, 2, 3, 4, 0, time.UTC) }
	account := receiverProcessAccount{Tenant: "feishu", AppID: "cli_scope"}
	t.Run("archive error preserves stdout", func(t *testing.T) {
		stateDir := filepath.Join(t.TempDir(), "state")
		if err := localfile.MkdirAllPrivate(stateDir); err != nil {
			t.Fatal(err)
		}
		blockedPath := filepath.Join(stateDir, "telemetry")
		writeReceiverTelemetryFixture(t, blockedPath, scopedTelemetryFeishu)
		var stdout, warnings bytes.Buffer
		output := newReceiverTelemetryOutput(stateDir, account, 2, clock, &stdout, &warnings)
		defer output.Close()
		writeReceiverTelemetryTestLine(t, output, scopedTelemetrySecret)
		if stdout.String() != scopedTelemetrySecret || warnings.Len() == 0 || strings.Contains(warnings.String(), "synthetic secret") {
			t.Fatalf("stdout = %q, warnings = %q", &stdout, &warnings)
		}
		requireReceiverTelemetryArchive(t, blockedPath, scopedTelemetryFeishu)
	})

	t.Run("stdout error is returned", func(t *testing.T) {
		stateDir := filepath.Join(t.TempDir(), "state")
		if err := localfile.MkdirAllPrivate(stateDir); err != nil {
			t.Fatal(err)
		}
		failure := errors.New("synthetic stdout error")
		output := newReceiverTelemetryOutput(stateDir, account, 2, clock, serviceTelemetryErrorWriter{failure}, io.Discard)
		defer output.Close()
		if n, err := io.WriteString(output, scopedTelemetryFeishu); n != 0 || !errors.Is(err, failure) {
			t.Fatalf("write = %d, %v, want 0, %v", n, err, failure)
		}
	})
}

func writeReceiverTelemetryFixture(t *testing.T, path, contents string) {
	t.Helper()
	if _, err := localfile.WritePrivateAtomic(path, []byte(contents), true); err != nil {
		t.Fatal(err)
	}
}

func writeReceiverTelemetryTestLine(t *testing.T, output io.Writer, line string) {
	t.Helper()
	if n, err := io.WriteString(output, line); n != len(line) || err != nil {
		t.Fatalf("write = %d, %v, want %d, nil", n, err, len(line))
	}
}

func requireReceiverTelemetryMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archive %s still exists or cannot be checked: %v", path, err)
	}
}

func requireReceiverTelemetryArchive(t *testing.T, path, want string) {
	t.Helper()
	file, err := localfile.OpenPrivate(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	gotLines := strings.Split(string(data), "\n")
	wantLines := strings.Split(want, "\n")
	if len(gotLines) != len(wantLines) || gotLines[len(gotLines)-1] != "" {
		t.Fatalf("archive %s = %q, want JSONL records %q", path, data, want)
	}
	for i, line := range wantLines[:len(wantLines)-1] {
		var gotRecord, wantRecord map[string]any
		if json.Unmarshal([]byte(gotLines[i]), &gotRecord) != nil || json.Unmarshal([]byte(line), &wantRecord) != nil || !reflect.DeepEqual(gotRecord, wantRecord) {
			t.Fatalf("archive %s line %d = %q, want JSON object %q", path, i+1, gotLines[i], line)
		}
	}
}
