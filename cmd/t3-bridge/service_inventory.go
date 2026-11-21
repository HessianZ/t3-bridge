package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

const serviceInventoryUsage = "usage: t3-bridge service list | t3-bridge service logs -instance NAME [-lines N] [-telemetry [-follow] [-tenant TENANT -app-id APP_ID]]"

type serviceLogOptions struct {
	lines     int
	telemetry bool
	follow    bool
	account   receiverProcessAccount
	filtered  bool
}

func (options *serviceLogOptions) flags(flags *flag.FlagSet) {
	flags.IntVar(&options.lines, "lines", 100, fmt.Sprintf("recent lines per log stream (1–%d, or 0 with -follow)", serviceLogMaxLines))
	flags.BoolVar(&options.telemetry, "telemetry", false, "export receiver metrics, diagnostics and faults as JSON Lines")
	flags.BoolVar(&options.follow, "follow", false, "continue streaming telemetry until interrupted")
	flags.Func("tenant", "Feishu telemetry tenant (feishu or lark, requires -app-id and -telemetry)", func(value string) error {
		options.account.Tenant, options.filtered = value, true
		return nil
	})
	flags.Func("app-id", "exact Feishu telemetry App ID (requires -tenant and -telemetry)", func(value string) error {
		options.account.AppID, options.filtered = value, true
		return nil
	})
}

func (options serviceLogOptions) validate() error {
	if options.follow && !options.telemetry {
		return errors.New("-follow requires -telemetry")
	}
	if options.filtered {
		if !options.telemetry {
			return errors.New("-tenant and -app-id require -telemetry")
		}
		if !validServiceTelemetryAccount(options.account) {
			return errors.New("provide both -tenant feishu|lark and an exact -app-id when filtering telemetry")
		}
	}
	minimum := 1
	if options.follow {
		minimum = 0
	}
	if options.lines < minimum || options.lines > serviceLogMaxLines {
		return fmt.Errorf("-lines must be between %d and %d", minimum, serviceLogMaxLines)
	}
	return nil
}

// Windows supplies its Task Scheduler inventory in service_windows.go.
var readHostServiceInventory = func() ([]serviceInventoryEntry, error) {
	if (runtime.GOOS != "darwin" && runtime.GOOS != "linux") || os.Geteuid() <= 0 {
		return nil, errors.New("user service inventory unavailable")
	}
	return readServiceInventory(runtime.GOOS, runServiceManager)
}

func runServiceInventory(args []string, output io.Writer) error {
	if len(args) == 0 || (args[0] != "list" && args[0] != "logs") {
		return errors.New(serviceInventoryUsage)
	}
	action := args[0]
	flags := flag.NewFlagSet("t3-bridge service "+action, flag.ContinueOnError)
	flags.SetOutput(output)
	flags.Usage = func() {
		fmt.Fprintln(output, serviceInventoryUsage)
		flags.PrintDefaults()
	}
	var name string
	var logs serviceLogOptions
	if action == "logs" {
		flags.StringVar(&name, "instance", "", "instance name (lowercase letters, digits and hyphens)")
		logs.flags(flags)
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New(serviceInventoryUsage)
	}
	if action == "logs" {
		if !serviceInstanceName.MatchString(name) {
			return errors.New("-instance must contain 1–48 lowercase letters, digits or hyphens and begin with a letter or digit")
		}
		if err := logs.validate(); err != nil {
			return err
		}
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return errors.New("service management supports macOS launchd and Linux systemd user services")
	}
	if os.Geteuid() <= 0 {
		return errors.New("run service management as the user who will run the bridge, without sudo")
	}
	if action == "list" {
		return listServiceInstances(runtime.GOOS, runServiceManager, output)
	}
	instance, err := resolveServiceInstance(runtime.GOOS, name, false)
	if err != nil {
		return err
	}
	if logs.follow {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return instance.followTelemetry(ctx, logs.lines, runServiceManager, followServiceJournal, output, logs.account)
	}
	if logs.telemetry {
		return instance.readTelemetry(logs.lines, runServiceManager, runServiceJournal, output, logs.account)
	}
	return instance.readLogs(logs.lines, runServiceManager, runServiceJournal, output)
}

func listServiceInstances(platform string, manager serviceManager, output io.Writer) error {
	entries, err := readServiceInventory(platform, manager)
	if err != nil {
		return err
	}
	return writeServiceInventory(entries, output)
}

type serviceInventoryEntry struct {
	name   string
	status string
	pid    int
	err    error
	manage func(action string) error
}

func readServiceInventory(platform string, manager serviceManager) ([]serviceInventoryEntry, error) {
	// Resolving the empty name gives the canonical directory and filename pattern.
	template, err := resolveServiceInstance(platform, "", false)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(template.file)
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read service definitions: %w", err)
	}
	suffix := filepath.Ext(template.file)
	prefix := strings.TrimSuffix(filepath.Base(template.file), suffix)
	var inventory []serviceInventoryEntry
	for _, entry := range entries {
		rest, ok := strings.CutPrefix(entry.Name(), prefix)
		if !ok {
			continue
		}
		name, ok := strings.CutSuffix(rest, suffix)
		if !ok || !serviceInstanceName.MatchString(name) {
			continue
		}
		instance, err := resolveServiceInstance(platform, name, false)
		if err != nil {
			return nil, err
		}
		inventory = append(inventory, serviceInventoryStatus(instance, manager))
	}
	return inventory, nil
}

func serviceInventoryStatus(instance serviceInstance, manager serviceManager) serviceInventoryEntry {
	entry := serviceInventoryEntry{name: instance.name}
	if err := instance.checkDefinition(); err != nil {
		entry.err = fmt.Errorf("error checking definition: %.160q", err.Error())
		return entry
	}
	state, err := instance.inspect(manager)
	if err != nil {
		entry.err = fmt.Errorf("error reading status: %.160q", err.Error())
		return entry
	}
	entry.manage = func(action string) error { return instance.manage(action, manager, io.Discard) }
	if !state.loaded {
		entry.status = "installed, stopped"
		return entry
	}
	entry.status, entry.pid = state.state, state.pid
	return entry
}

func writeServiceInventory(entries []serviceInventoryEntry, output io.Writer) error {
	if len(entries) == 0 {
		_, err := fmt.Fprintln(output, "No managed service instances found.")
		return err
	}
	for _, entry := range entries {
		status := entry.status
		if entry.err != nil {
			status = entry.err.Error()
		} else if entry.pid > 0 {
			status = fmt.Sprintf("%s (pid %d)", status, entry.pid)
		}
		if _, err := fmt.Fprintf(output, "%s: %s\n", entry.name, status); err != nil {
			return err
		}
	}
	return nil
}
