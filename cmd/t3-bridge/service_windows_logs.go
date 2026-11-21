//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/HessianZ/t3-bridge/internal/localfile"
	"golang.org/x/sys/windows"
)

const (
	windowsServiceInstanceFlag   = "-windows-service-instance"
	windowsServiceSupervisorFlag = "-windows-service-supervisor"
)

var reopenWindowsLog = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReOpenFile")

func prepareHostReceiver(args []string) ([]string, bool, error) {
	if len(args) == 0 || (args[0] != windowsServiceInstanceFlag && args[0] != windowsServiceSupervisorFlag) {
		return args, false, nil
	}
	if len(args) != 4 || !serviceInstanceName.MatchString(args[1]) || args[2] != "-config" {
		return nil, false, errors.New("invalid managed Windows receiver arguments")
	}
	config, err := windowsServicePath(args[3], ".json")
	if err != nil || config != args[3] {
		return nil, false, errors.New("managed Windows receiver requires an absolute JSON configuration path")
	}
	dir, err := windowsServiceLogDir(args[1])
	if err != nil {
		return nil, false, err
	}
	if err := startWindowsServiceLogCapture(dir); err != nil {
		return nil, false, err
	}
	if args[0] == windowsServiceSupervisorFlag {
		return nil, true, superviseWindowsReceiver(config)
	}
	return args[2:], false, nil
}

func windowsServiceLogDir(name string) (string, error) {
	dir, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)
	if err != nil {
		return "", fmt.Errorf("resolve current user's Local AppData: %w", err)
	}
	return filepath.Join(dir, "t3-bridge-service-logs", name), nil
}

func ensureWindowsServiceLogDir(dir string) error {
	if err := localfile.MkdirAllPrivate(filepath.Dir(dir)); err != nil {
		return err
	}
	return localfile.MkdirAllPrivate(dir)
}

func startWindowsServiceLogCapture(dir string) error {
	if err := ensureWindowsServiceLogDir(dir); err != nil {
		return fmt.Errorf("prepare Windows service logs: %w", err)
	}
	// Keep the handles open for the life of the receiver, including main's final error.
	stderr, err := redirectWindowsServiceLog(dir, "stderr.log", windows.STD_ERROR_HANDLE)
	if err != nil {
		return err
	}
	os.Stderr = stderr
	log.SetOutput(stderr)
	stdout, err := redirectWindowsServiceLog(dir, "stdout.log", windows.STD_OUTPUT_HANDLE)
	if err != nil {
		return err
	}
	os.Stdout = stdout
	return nil
}

func redirectWindowsServiceLog(dir, name string, standardHandle uint32) (*os.File, error) {
	file, err := openWindowsServiceLogAppend(filepath.Join(dir, name))
	if err != nil {
		return nil, fmt.Errorf("open %s for capture: %w", name, err)
	}
	if err := windows.SetStdHandle(standardHandle, windows.Handle(file.Fd())); err != nil {
		file.Close()
		return nil, fmt.Errorf("redirect %s: %w", name, err)
	}
	return file, nil
}

func openWindowsServiceLogAppend(path string) (*os.File, error) {
	file, err := localfile.CreatePrivate(path)
	if errors.Is(err, os.ErrExist) {
		file, err = localfile.OpenPrivate(path)
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	// Reopen the checked file by handle. Append-only access keeps prior output intact.
	handle, _, err := reopenWindowsLog.Call(file.Fd(), windows.FILE_APPEND_DATA|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, 0)
	if windows.Handle(handle) == windows.InvalidHandle {
		return nil, &os.PathError{Op: "reopen for append", Path: path, Err: err}
	}
	return os.NewFile(handle, path), nil
}

func (s windowsService) logs(lines int, scheduler windowsScheduler, output io.Writer) error {
	dir, err := s.managedLogDir(scheduler)
	if err != nil {
		return err
	}
	return readWindowsServiceLogs(dir, lines, output)
}

func (s windowsService) followTelemetry(ctx context.Context, lines int, scheduler windowsScheduler, output io.Writer, account receiverProcessAccount) error {
	dir, err := s.managedLogDir(scheduler)
	if err != nil {
		return err
	}
	return followServiceTelemetryFiles(ctx, dir, lines, checkWindowsServiceLogDir, output, account)
}

func (s windowsService) managedLogDir(scheduler windowsScheduler) (string, error) {
	task, kind, err := s.inspect(scheduler)
	if err != nil {
		return "", err
	}
	if task == nil {
		return "", fmt.Errorf("%s: not installed", s.taskName())
	}
	if kind == windowsTaskLegacy {
		return "", fmt.Errorf("%s: task predates log capture; unregister and install it again with the current bridge to enable logs", s.taskName())
	}
	return windowsServiceLogDir(s.name)
}

func checkWindowsServiceLogDir(dir string) error {
	if err := localfile.CheckPrivateDir(filepath.Dir(dir)); err != nil {
		return err
	}
	return localfile.CheckPrivateDir(dir)
}

func readWindowsServiceLogs(dir string, lines int, output io.Writer) error {
	if err := checkWindowsServiceLogDir(dir); errors.Is(err, os.ErrNotExist) {
		_, err := fmt.Fprintln(output, "No logs found.")
		return err
	} else if err != nil {
		return err
	}
	return writeServiceLogFiles(dir, lines, output)
}
