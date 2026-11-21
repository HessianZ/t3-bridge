package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

const (
	serviceLogMaxLines = 1000
	serviceLogMaxBytes = 128 << 10
)

func (s serviceInstance) readTelemetry(lines int, manager, journal serviceManager, output io.Writer, account receiverProcessAccount) error {
	return exportServiceTelemetry(output, account, func(raw io.Writer) error {
		return s.readLogs(lines, manager, func(command string, args ...string) ([]byte, error) {
			return journal(command, append([]string{"--output=cat"}, args...)...)
		}, raw)
	})
}

func exportServiceTelemetry(output io.Writer, account receiverProcessAccount, readLogs func(io.Writer) error) error {
	var raw bytes.Buffer
	if err := readLogs(&raw); err != nil {
		return err
	}
	return writeServiceTelemetry(&raw, output, account)
}

func (s serviceInstance) readLogs(lines int, manager, journal serviceManager, output io.Writer) error {
	if err := s.checkDefinition(); err != nil {
		return fmt.Errorf("service definition: %w", err)
	}
	if s.platform == "darwin" {
		return s.readLaunchdLogs(lines, output)
	}
	if _, err := s.inspect(manager); err != nil {
		return err
	}
	data, err := journal("journalctl", s.journalArgs(lines)...)
	if err != nil {
		return fmt.Errorf("journalctl for %s: %w", s.id, err)
	}
	if len(data) == 0 {
		_, err := fmt.Fprintln(output, "No logs found.")
		return err
	}
	_, err = output.Write(serviceLogTail(data, lines))
	return err
}

func (s serviceInstance) journalArgs(lines int, options ...string) []string {
	// The unit's output may be in the system journal when per-user storage is unavailable.
	args := []string{"--user-unit=" + s.id, "--no-pager", "--quiet", "--lines=" + strconv.Itoa(lines)}
	args = append(args, options...)
	return append(args, "_UID="+strconv.Itoa(os.Getuid()))
}

func (s serviceInstance) readLaunchdLogs(lines int, output io.Writer) error {
	err := localfile.CheckPrivateDir(s.logDir)
	if errors.Is(err, os.ErrNotExist) {
		_, err := fmt.Fprintln(output, "No logs found.")
		return err
	}
	if err != nil {
		return err
	}
	return writeServiceLogFiles(s.logDir, lines, output)
}

func writeServiceLogFiles(dir string, lines int, output io.Writer) error {
	found := false
	for _, name := range []string{"stdout.log", "stderr.log"} {
		data, err := readServiceLogFile(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if len(data) == 0 {
			continue
		}
		found = true
		if _, err := fmt.Fprintf(output, "==> %s <==\n%s", name, serviceLogTail(data, lines)); err != nil {
			return err
		}
		if data[len(data)-1] != '\n' {
			if _, err := fmt.Fprintln(output); err != nil {
				return err
			}
		}
	}
	if !found {
		_, err := fmt.Fprintln(output, "No logs found.")
		return err
	}
	return nil
}

func readServiceLogFile(path string) ([]byte, error) {
	file, err := localfile.OpenRegular(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("expected a regular log file")
	}
	size := min(info.Size(), serviceLogMaxBytes)
	return io.ReadAll(io.NewSectionReader(file, info.Size()-size, size))
}

func serviceLogTail(data []byte, lines int) []byte {
	end := len(data)
	if end > 0 && data[end-1] == '\n' {
		end--
	}
	for range lines {
		end = bytes.LastIndexByte(data[:end], '\n')
		if end < 0 {
			return data
		}
	}
	return data[end+1:]
}

func runServiceJournal(command string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	tail := make(serviceJournalTail, 0, serviceLogMaxBytes)
	cmd.Stdout = &tail
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return tail, err
}

// A journal entry can contain multiple lines or exceed the output byte limit.
type serviceJournalTail []byte

func (tail *serviceJournalTail) Write(data []byte) (int, error) {
	n := len(data)
	if n >= serviceLogMaxBytes {
		*tail = append((*tail)[:0], data[n-serviceLogMaxBytes:]...)
		return n, nil
	}
	if excess := len(*tail) + n - serviceLogMaxBytes; excess > 0 {
		*tail = (*tail)[:copy(*tail, (*tail)[excess:])]
	}
	*tail = append(*tail, data...)
	return n, nil
}
