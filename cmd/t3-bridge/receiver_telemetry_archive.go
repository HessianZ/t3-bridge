package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

const receiverTelemetryArchiveDate = "2006-01-02"

func receiverTelemetryArchiveDir(stateDir string) string {
	return filepath.Join(stateDir, "telemetry")
}

func receiverTelemetryArchiveName(day string) string {
	return "bridge-" + day + ".jsonl"
}

type receiverTelemetryOutput struct {
	mu            sync.Mutex
	dir           string
	account       receiverProcessAccount
	retentionDays int
	clock         func() time.Time
	stdout        io.Writer
	warnings      io.Writer

	day       time.Time
	oldestDay time.Time
	file      *os.File
	fileDay   string
	partial   []byte
	discard   bool
	warned    bool
	closed    bool
}

// Construct only while holding the Store for the bound account.
func newReceiverTelemetryOutput(stateDir string, account receiverProcessAccount, retentionDays int, clock func() time.Time, stdout, warnings io.Writer) io.WriteCloser {
	if clock == nil {
		clock = time.Now
	}
	if stdout == nil {
		stdout = io.Discard
	}
	out := &receiverTelemetryOutput{
		dir:           receiverTelemetryArchiveDir(stateDir),
		account:       account,
		retentionDays: max(1, retentionDays),
		clock:         clock,
		stdout:        stdout,
		warnings:      warnings,
	}
	out.prune(out.today())
	return out
}

func (out *receiverTelemetryOutput) Write(data []byte) (int, error) {
	out.mu.Lock()
	defer out.mu.Unlock()
	if out.closed {
		return 0, io.ErrClosedPipe
	}
	n, err := out.stdout.Write(data)
	if today := out.today(); !today.Equal(out.day) {
		out.prune(today)
	}
	for len(data) > 0 {
		piece, rest, complete := bytes.Cut(data, []byte{'\n'})
		if !out.discard {
			if len(piece) >= serviceTelemetryMaxLineBytes-len(out.partial) {
				out.discard = true
			} else {
				out.partial = append(out.partial, piece...)
			}
		}
		if !complete {
			break
		}
		if !out.discard {
			record, ok := parseServiceTelemetry(out.partial)
			if ok && record.receiverProcessAccount == out.account && !record.At.Before(out.oldestDay) {
				if err := out.append(record); err != nil {
					out.warn()
				}
			}
		}
		out.partial = out.partial[:0]
		out.discard = false
		data = rest
	}
	return n, err
}

func (out *receiverTelemetryOutput) Close() error {
	out.mu.Lock()
	defer out.mu.Unlock()
	out.closed = true
	out.partial = nil
	out.closeFile()
	return nil
}

func (out *receiverTelemetryOutput) today() time.Time {
	return out.clock().UTC().Truncate(24 * time.Hour)
}

func (out *receiverTelemetryOutput) prune(today time.Time) {
	out.closeFile()
	out.day = today
	out.oldestDay = today.AddDate(0, 0, 1-out.retentionDays)
	if err := localfile.MkdirAllPrivate(out.dir); err != nil {
		out.warn()
		return
	}
	entries, err := os.ReadDir(out.dir)
	if err != nil {
		out.warn()
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		date, prefix := strings.CutPrefix(name, "bridge-")
		date, suffix := strings.CutSuffix(date, ".jsonl")
		if !prefix || !suffix || !entry.Type().IsRegular() {
			continue
		}
		day, err := time.Parse(receiverTelemetryArchiveDate, date)
		if err != nil || day.Year() < 1 || receiverTelemetryArchiveName(day.Format(receiverTelemetryArchiveDate)) != name || !day.Before(out.oldestDay) {
			continue
		}
		path := filepath.Join(out.dir, name)
		file, err := localfile.OpenPrivateSingleLink(path)
		if err != nil {
			continue
		}
		_ = file.Close()
		if err := os.Remove(path); err != nil {
			out.warn()
		}
	}
}

func (out *receiverTelemetryOutput) append(record serviceTelemetryRecord) error {
	day := record.At.Format(receiverTelemetryArchiveDate)
	if out.file == nil || out.fileDay != day {
		out.closeFile()
		if err := localfile.MkdirAllPrivate(out.dir); err != nil {
			return err
		}
		file, err := localfile.OpenPrivateAppend(filepath.Join(out.dir, receiverTelemetryArchiveName(day)))
		if err != nil {
			return err
		}
		if err := separateReceiverTelemetryTail(file); err != nil {
			_ = file.Close()
			return err
		}
		out.file, out.fileDay = file, day
	}
	if err := json.NewEncoder(out.file).Encode(record); err != nil {
		out.closeFile()
		return err
	}
	return nil
}

func separateReceiverTelemetryTail(file *os.File) error {
	info, err := file.Stat()
	if err != nil || info.Size() == 0 {
		return err
	}
	reader, err := localfile.OpenPrivateSingleLink(file.Name())
	if err != nil {
		return err
	}
	defer reader.Close()
	var last [1]byte
	if _, err := reader.ReadAt(last[:], info.Size()-1); err != nil {
		return err
	}
	if last[0] != '\n' {
		// A partial write must not absorb the next complete record.
		_, err = file.WriteString("\n")
	}
	return err
}

func (out *receiverTelemetryOutput) closeFile() {
	if out.file != nil {
		if err := out.file.Close(); err != nil {
			out.warn()
		}
		out.file = nil
	}
}

func (out *receiverTelemetryOutput) warn() {
	if !out.warned && out.warnings != nil {
		out.warned = true
		_, _ = io.WriteString(out.warnings, "t3-bridge: receiver telemetry archive unavailable\n")
	}
}
