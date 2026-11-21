package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type serviceManager func(command string, args ...string) ([]byte, error)

type serviceState struct {
	loaded bool
	state  string
	pid    int
}

func runServiceManager(command string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return out, err
}

func (s serviceInstance) install(definition string, manager serviceManager) error {
	if _, err := os.Lstat(s.file); err == nil {
		return errors.New("service definition already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	state, err := s.inspect(manager)
	if err != nil {
		return err
	}
	if state.loaded {
		return errors.New("service identifier already registered")
	}
	if s.platform == "darwin" {
		if err := os.MkdirAll(s.logDir, 0o700); err != nil {
			return err
		}
		info, err := os.Lstat(s.logDir)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
			return errors.New("service log directory must be a private directory")
		}
		// LaunchAgents can load the file at login. Disable the unused label before publishing it.
		if err := s.change(manager, "disable", s.target()); err != nil {
			return err
		}
	}
	dir := filepath.Dir(s.file)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".t3-bridge-service-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, writeErr := tmp.WriteString(definition)
	if err := errors.Join(writeErr, tmp.Close()); err != nil {
		return err
	}
	// Publish the complete private file without replacing an existing definition.
	return os.Link(tmp.Name(), s.file)
}

func (s serviceInstance) manage(action string, manager serviceManager, output io.Writer) error {
	if err := s.checkDefinition(); err != nil {
		if action == "status" && errors.Is(err, os.ErrNotExist) {
			_, err := fmt.Fprintf(output, "%s: not installed\n", s.id)
			return err
		}
		return fmt.Errorf("service definition: %w", err)
	}
	state, err := s.inspect(manager)
	if err != nil {
		return err
	}
	if action == "status" {
		if !state.loaded {
			state.state = "installed, stopped"
		}
		if state.pid > 0 {
			_, err := fmt.Fprintf(output, "%s: %s (pid %d)\n", s.id, state.state, state.pid)
			return err
		}
		_, err := fmt.Fprintf(output, "%s: %s\n", s.id, state.state)
		return err
	}
	if s.platform == "linux" {
		if err := s.manageSystemd(action, manager); err != nil {
			return err
		}
	} else if err := s.manageLaunchd(action, manager, state); err != nil {
		return err
	}
	if action == "unregister" {
		if err := s.checkDefinition(); err != nil {
			return err
		}
		if err := os.Remove(s.file); err != nil {
			return err
		}
		if s.platform == "linux" {
			if err := s.change(manager, "daemon-reload"); err != nil {
				return fmt.Errorf("service definition removed; %w", err)
			}
		}
	}
	_, err = fmt.Fprintf(output, "%s: %s requested successfully\n", s.id, action)
	return err
}

func (s serviceInstance) checkDefinition() error {
	info, err := os.Lstat(s.file)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("expected a private regular definition created by t3-bridge service")
	}
	f, err := os.Open(s.file)
	if err != nil {
		return err
	}
	defer f.Close()
	header := s.header()
	got, err := io.ReadAll(io.LimitReader(f, int64(len(header))))
	if err != nil {
		return err
	}
	if string(got) != header {
		return errors.New("definition was not created by t3-bridge service for this instance")
	}
	return nil
}

func (s serviceInstance) target() string {
	return s.domain + "/" + s.id
}

func (s serviceInstance) call(manager serviceManager, args ...string) ([]byte, error) {
	if s.platform == "darwin" {
		return manager("/bin/launchctl", args...)
	}
	return manager("systemctl", append([]string{"--user", "--no-pager"}, args...)...)
}

func (s serviceInstance) change(manager serviceManager, args ...string) error {
	if _, err := s.call(manager, args...); err != nil {
		return fmt.Errorf("%s %s for %s: %w", s.platform, args[0], s.id, err)
	}
	return nil
}

func (s serviceInstance) inspect(manager serviceManager) (serviceState, error) {
	args, delimiter := []string{"print", s.target()}, " = "
	pathKey, stateKey, pidKey := "path", "state", "pid"
	if s.platform == "linux" {
		args = []string{"show", "--property=LoadState,FragmentPath,DropInPaths,ActiveState,MainPID", s.id}
		delimiter = "="
		pathKey, stateKey, pidKey = "FragmentPath", "ActiveState", "MainPID"
	}
	out, err := s.call(manager, args...)
	fields := make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), delimiter)
		if _, seen := fields[key]; ok && !seen {
			fields[key] = value
		}
	}
	if s.platform == "darwin" && err != nil && strings.Contains(string(out), `Could not find service "`+s.id+`"`) {
		return serviceState{}, nil
	}
	if s.platform == "linux" && fields["LoadState"] == "not-found" && fields[stateKey] == "inactive" && fields[pathKey] == "" && fields["DropInPaths"] == "" {
		return serviceState{}, nil
	}
	if err != nil {
		return serviceState{}, fmt.Errorf("cannot inspect %s: %w", s.id, err)
	}
	if fields[pathKey] != s.file || fields["DropInPaths"] != "" {
		return serviceState{}, errors.New("service manager resolves a different definition or additional overrides; refusing to manage it")
	}
	if s.platform == "linux" && fields["LoadState"] != "loaded" {
		return serviceState{}, errors.New("systemd could not load the installed user unit")
	}
	state := fields[stateKey]
	switch state {
	case "active", "inactive", "activating", "deactivating", "reloading", "failed", "maintenance", "running", "not running", "spawn scheduled":
	default:
		state = "unknown state"
	}
	pid, _ := strconv.Atoi(fields[pidKey])
	return serviceState{loaded: true, state: state, pid: pid}, nil
}

func (s serviceInstance) manageSystemd(action string, manager serviceManager) error {
	if err := s.change(manager, "daemon-reload"); err != nil {
		return err
	}
	state, err := s.inspect(manager)
	if err != nil {
		return err
	}
	if !state.loaded {
		return errors.New("systemd did not load the installed user unit")
	}
	if action == "stop" || action == "unregister" {
		return s.change(manager, "disable", "--now", "--no-reload", s.id)
	}
	if err := s.change(manager, "enable", "--no-reload", s.id); err != nil {
		return err
	}
	return s.change(manager, action, s.id)
}

func (s serviceInstance) manageLaunchd(action string, manager serviceManager, state serviceState) error {
	target := s.target()
	if action == "stop" || action == "unregister" || action == "restart" {
		if err := s.change(manager, "disable", target); err != nil {
			return err
		}
		if state.loaded {
			if err := s.change(manager, "bootout", target); err != nil {
				return err
			}
			if err := s.waitForLaunchdStop(manager, state.pid); err != nil {
				return err
			}
			state = serviceState{}
		}
		if action != "restart" {
			return nil
		}
	}
	if err := s.change(manager, "enable", target); err != nil {
		return err
	}
	if !state.loaded {
		return s.change(manager, "bootstrap", s.domain, s.file)
	}
	return s.change(manager, "kickstart", target)
}

func (s serviceInstance) waitForLaunchdStop(manager serviceManager, pid int) error {
	// bootout is asynchronous. Wait for the old process to release its state before bootstrapping again.
	deadline := time.Now().Add(35 * time.Second)
	for time.Now().Before(deadline) {
		state, err := s.inspect(manager)
		if err != nil {
			return err
		}
		if !state.loaded {
			if pid <= 0 {
				return nil
			}
			process, err := os.FindProcess(pid)
			if err == nil {
				err = process.Signal(syscall.Signal(0))
				_ = process.Release()
			}
			if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("check launchd process exit: %w", err)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("launchd service has not stopped after bootout")
}
