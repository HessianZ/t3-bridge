//go:build windows

package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsReceiverJobStopsChild(t *testing.T) {
	const fixture = "T3_BRIDGE_TEST_RECEIVER_JOB"
	const run = "-test.run=^TestWindowsReceiverJobStopsChild$"
	executable := os.Args[0]
	switch os.Getenv(fixture) {
	case "child":
		time.Sleep(20 * time.Second)
		return
	case "supervisor":
		if err := joinWindowsReceiverJob(); err != nil {
			t.Fatal(err)
		}
		child := exec.Command(executable, run)
		child.Env = append(os.Environ(), fixture+"=child")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		fmt.Println(child.Process.Pid)
		if err := child.Wait(); err != nil {
			t.Fatal(err)
		}
		return
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	supervisor := exec.CommandContext(ctx, executable, run)
	supervisor.Env = append(os.Environ(), fixture+"=supervisor")
	output, err := supervisor.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = supervisor.Process.Kill()
		_ = supervisor.Wait()
	}()
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.ParseUint(strings.TrimSpace(line), 10, 32)
	if err != nil || pid == 0 {
		t.Fatalf("job fixture did not start a child: %q", line)
	}
	child, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(child)
	if status, err := windows.WaitForSingleObject(child, 0); err != nil || status != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("child did not stay active before supervisor exit: %d, %v", status, err)
	}
	if err := supervisor.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if status, err := windows.WaitForSingleObject(child, 5000); err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("child did not exit with the supervisor: %d, %v", status, err)
	}
}
