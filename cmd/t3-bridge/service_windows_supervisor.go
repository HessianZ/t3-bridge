//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func superviseWindowsReceiver(configPath string) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve Windows receiver executable: %w", err)
	}
	if err := joinWindowsReceiverJob(); err != nil {
		return err
	}
	return retryWindowsReceiver(func() (int, error) {
		child := exec.Command(executable, "run", "-config", configPath)
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		err := child.Run()
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode(), nil
		}
		return 0, err
	}, time.Sleep)
}

func retryWindowsReceiver(run func() (int, error), wait func(time.Duration)) error {
	const maxRetries = 3
	for retries := 0; ; retries++ {
		code, err := run()
		if err != nil {
			return fmt.Errorf("run Windows receiver: %w", err)
		}
		if code == 0 {
			return nil
		}
		log.Printf("Windows receiver exited: exit_code=%d retries=%d/%d", code, retries, maxRetries)
		if retries == maxRetries {
			return fmt.Errorf("Windows receiver recovery exhausted after %d retries (last exit code %d)", retries, code)
		}
		wait(time.Minute)
	}
}

func joinWindowsReceiverJob() error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("create Windows receiver job: %w", err)
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("configure Windows receiver job: %w", err)
	}
	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("assign Windows supervisor to receiver job: %w", err)
	}
	// The supervisor owns the only handle. Closing it here would terminate the
	// supervisor too. Windows closes it on exit and terminates remaining children.
	return nil
}
