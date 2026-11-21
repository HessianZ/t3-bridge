//go:build windows

package main

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsServiceLogCapture(t *testing.T) {
	const helperDir = "T3_BRIDGE_TEST_WINDOWS_SERVICE_LOG_DIR"
	if dir := os.Getenv(helperDir); dir != "" {
		if err := startWindowsServiceLogCapture(dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Print(strings.Repeat("x", serviceLogMaxBytes), "\nstdout fixture\n")
		log.SetFlags(0)
		log.Print("logger fixture")
		println("runtime stderr fixture")
		os.Exit(0)
	}

	dir := filepath.Join(t.TempDir(), "logs", "work")
	for range 2 {
		command := exec.Command(os.Args[0], "-test.run=^TestWindowsServiceLogCapture$")
		command.Env = append(os.Environ(), helperDir+"="+dir)
		if output, err := command.CombinedOutput(); err != nil || len(output) != 0 {
			diagnostic, _ := os.ReadFile(filepath.Join(dir, "stderr.log"))
			t.Fatalf("capture helper: %v, inherited output %q, stderr %q", err, output, diagnostic)
		}
	}
	stderr, err := readServiceLogFile(filepath.Join(dir, "stderr.log"))
	if string(stderr) != strings.Repeat("logger fixture\nruntime stderr fixture\n", 2) || err != nil {
		t.Fatalf("appended stderr = %q, %v", stderr, err)
	}
	stdout, err := readServiceLogFile(filepath.Join(dir, "stdout.log"))
	if len(stdout) != serviceLogMaxBytes || err != nil {
		t.Fatalf("stdout tail = %d bytes, %v", len(stdout), err)
	}
	var output bytes.Buffer
	if err := readWindowsServiceLogs(dir, 1, &output); err != nil {
		t.Fatal(err)
	}
	want := "==> stdout.log <==\nstdout fixture\n==> stderr.log <==\nruntime stderr fixture\n"
	if output.String() != want {
		t.Fatalf("logs = %q, want %q", output.String(), want)
	}
}
