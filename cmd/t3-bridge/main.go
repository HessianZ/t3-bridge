package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/charmbracelet/x/term"
)

// The macOS app bundle sets this at link time so launching it only opens configuration.
var standaloneConfigApp string

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	args := os.Args[1:]
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version" || args[0] == "-v") {
		_, err := fmt.Fprintln(os.Stdout, bridgeVersion())
		return err
	}
	if standaloneConfigApp == "1" && (len(args) == 0 || args[0] != "run") {
		return runConfigGUI(args)
	}
	if len(args) != 0 {
		switch args[0] {
		case "config":
			return runConfig(args[1:], os.Stdin, os.Stdout)
		case "secrets":
			return runConfigCredentials(args[1:], os.Stdin, os.Stdout)
		case "service":
			return runHostService(args[1:], os.Stdout)
		case "profile":
			return runProfile(args[1:], os.Stdout)
		case "ps", "kill":
			return runReceiverProcessCommand(args, os.Stdout)
		case "run":
			receiverArgs, handled, err := prepareHostReceiver(args[1:])
			if handled || err != nil {
				return err
			}
			args = receiverArgs
		}
	}
	config := flag.String("config", "", defaultConfigFlagHelp)
	profile := flag.String("profile", "", "use the named configuration profile")
	submitReport := flag.Bool("submit-report", false, "queue a bounded task/request/event report from stdin; no model or IM request")
	baseline := flag.Bool("prepare-auto-baseline", false, "offline drained receiver only: prepare fresh automatic-investigation baselines; retain all seen/stop identities")
	adopt := flag.Bool("adopt-legacy-state", false, "offline one-time bind of verified legacy state to the configured app; no receiver/network/secrets")
	drain := flag.Bool("drain-check", false, "after stopping this receiver, verify existing private state is drained; no network or secret reads")
	check := flag.Bool("check", false, "validate config only; no network or secrets read")
	appID := flag.String("app-id", "", "select an app for an offline state command")
	if err := flag.CommandLine.Parse(args); err != nil {
		return err
	}
	if flag.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	path, err := resolveConfigPath(*config, *profile, defaultConfigPath)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			if term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd()) {
				return runConfigTUI([]string{"-config", path})
			}
			return fmt.Errorf("no configuration at %q. Run t3-bridge config tui or t3-bridge config web to set it up", path)
		}
	}
	var profiles profileRegistry
	if !(*check || *submitReport || *baseline || *adopt || *drain) {
		profiles, err = localProfiles()
		if err != nil {
			return err
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		if filepath.Base(absolute) == "config.json" && filepath.Dir(filepath.Dir(absolute)) == filepath.Join(profiles.root, "profiles") {
			lock, err := lockProfileRuntime(absolute)
			if err != nil {
				return err
			}
			defer lock.Close()
		}
	}
	raw, err := bridge.ReadConfigJSON(path)
	if err != nil {
		return err
	}
	c, err := bridge.ParseConfigAtPath(raw, path)
	if err != nil {
		return err
	}
	if *appID != "" {
		if !(*submitReport || *baseline || *adopt || *drain) {
			return errors.New("-app-id requires an offline state command")
		}
		var found bool
		for _, account := range c.Accounts() {
			if account.AppID == *appID {
				c, found = account, true
				break
			}
		}
		if !found {
			return errors.New("app-id is not configured")
		}
	}
	if *check {
		fmt.Println("configuration valid; account/API access remains unverified")
		return nil
	}
	if *submitReport {
		return bridge.SubmitReport(c, os.Stdin, os.Stdout)
	}
	if *baseline {
		if !c.AutoInvestigations.Enabled {
			return errors.New("automatic investigation must be explicitly configured and legacy dispatcher stopped")
		}
		if _, err := os.Stat(filepath.Join(c.StateDir, "state.json")); err != nil {
			return errors.New("existing state required")
		}
		store, err := bridge.OpenStore(c.StateDir)
		if err != nil {
			return err
		}
		defer store.Close()
		if err = store.PrepareAutoBaseline(c.AppID); err != nil {
			return err
		}
		fmt.Println("node baselines prepared; first valid samples are baseline-only; existing event/task/stop identities retained")
		return nil
	}
	if *adopt {
		if _, err := os.Stat(filepath.Join(c.StateDir, "state.json")); err != nil {
			return errors.New("existing legacy state.json required")
		}
		store, err := bridge.OpenStore(c.StateDir)
		if err != nil {
			return err
		}
		defer store.Close()
		if err = store.BindAccount(c.AppID, true); err != nil {
			return err
		}
		fmt.Println("state bound to configured app; retain original account/state backup")
		return nil
	}
	if *drain {
		if _, err := os.Stat(filepath.Join(c.StateDir, "state.json")); err != nil {
			return errors.New("existing state.json required for drain-check")
		}
		store, err := bridge.OpenStore(c.StateDir)
		if err != nil {
			return err
		}
		defer store.Close()
		if err = store.DrainCheck(c.AppID); err != nil {
			return err
		}
		fmt.Println("local state drained; retain original account/state backup and use a separate stateDir for another app")
		return nil
	}
	var accounts []bridge.Config
	for _, account := range c.Accounts() {
		if account.ReceiverAuthorized {
			if fields := account.ReceiverPlaceholderFields(); len(fields) != 0 {
				return fmt.Errorf("Feishu app %q has unresolved template fields: %s", account.AppID, strings.Join(fields, ", "))
			}
			accounts = append(accounts, account)
		}
	}
	if len(accounts) == 0 {
		return errors.New("bot and T3 authorization not accepted; keep receiver disabled until independently authorized")
	}
	release, err := acquireReceiverLocks(profiles, accounts)
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	process, err := localReceiverProcesses(profiles).registerConfig(accounts, configRevision(raw))
	if err != nil {
		return fmt.Errorf("register receiver process: %w", err)
	}
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		process.watchStop(ctx, cancel)
	}()
	defer func() {
		cancel()
		<-watchDone
		if err := process.close(); err != nil {
			fmt.Fprintln(os.Stderr, "remove receiver process registration:", err)
		}
	}()
	receiver := &bridge.ReceiverProcess{
		ID: process.process.ID, PID: process.process.PID, StartedAt: process.process.StartedAt,
		Shared: len(process.process.Accounts) > 1,
		Stop: func() error {
			if err := process.registry.requestStop(process.process.ID); err != nil {
				return err
			}
			cancel()
			return nil
		},
	}
	return runReceiverAccounts(ctx, accounts, sharedReceiverRetryDelay, func(ctx context.Context, account bridge.Config) error {
		return runReceiverAccount(ctx, account, receiver, process)
	}, process)
}

func runReceiverAccount(ctx context.Context, c bridge.Config, receiver *bridge.ReceiverProcess, process *receiverProcessRegistration) error {
	account := newReceiverGroupAccount(c.Tenant, c.AppID)
	defer os.Remove(receiverDiagnosticsPath(process.dir, account))
	store, err := bridge.OpenStore(c.StateDir)
	if err != nil {
		return logReceiverFailure(ctx, os.Stdout, account, "store-open-failed", err)
	}
	defer store.Close()
	if err := store.BindReceiver(c); err != nil {
		return logReceiverFailure(ctx, os.Stdout, account, "account-bind-failed", err)
	}
	output := newReceiverTelemetryOutput(c.StateDir, account, receiverTelemetryRetentionDays(os.Getenv("LARK_CHANNEL_LOG_DAYS")), time.Now, os.Stdout, os.Stderr)
	defer output.Close()
	engine := &bridge.Engine{Version: bridgeVersion(), PollInterval: 2 * time.Second, Config: c, Store: store, Receiver: receiver, Maintenance: bridge.Coordination{Command: c.CoordinationCommand}}
	if c.Classifier.BaseURL != "" {
		engine.Classifier = bridge.Hy4{Config: c.Classifier}
	}
	connections := bridge.NewConnections(c, store)
	engine.OnRunFinished = func(phase string, elapsed time.Duration) {
		writeReceiverMetric(output, account, "run_e2e_ms", elapsed.Milliseconds(), phase)
	}
	engine.OnTaskEvent = func(kind string) {
		writeReceiverEvent(output, account, kind)
	}
	engine.OnCommandFailed = receiverCommandFailureMetric(output, account)
	connections.OnFeishuReconnect = func() {
		writeReceiverMetric(output, account, "ws_reconnect", 1, "")
		writeReceiverEvent(output, account, "ws-reconnected")
	}
	var diagnostics receiverDiagnosticHistory
	process.publishDiagnostics(account, engine, connections, &diagnostics, output)
	if _, err := connections.Reconnect(ctx, engine); err != nil {
		return logReceiverFailure(ctx, output, account, "initial-connect-failed", err)
	}
	defer connections.Close(context.Background())
	engine.Reconnect = func(ctx context.Context) (string, error) { return connections.Reconnect(ctx, engine) }
	engine.ConnectionStatus = connections.Status
	process.publishDiagnostics(account, engine, connections, &diagnostics, output)
	nextDiagnostics := time.Now().Add(receiverDiagnosticsInterval)
	pendingGroups := receiverGroupPendingPath(process.dir, account)
	pendingSettings := receiverRunSettingsIPC.pendingPath(process.dir, account)
	pendingAccessPolicy := receiverAccessPolicyIPC.pendingPath(process.dir, account)
	pendingReconnect := receiverReconnectIPC.pendingPath(process.dir, account)
	pendingMeetings := receiverMeetingIPC.pendingPath(process.dir, account)
	applyReconnect := func(ctx context.Context, action bridge.ReconnectAction) (bridge.LocalReconnectView, error) {
		view, err := engine.ChangeLocalReconnect(ctx, action)
		process.publishDiagnostics(account, engine, connections, &diagnostics, output)
		return view, err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	fmt.Printf("t3-bridge started (%s)\n", c.AppID)
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-connections.Lark.Fatal:
			return logReceiverFailure(ctx, output, account, "inbound-failed", err)
		case stopped := <-connections.Exits:
			if stopped.Generation != connections.Generation() {
				continue
			}
			if ctx.Err() != nil {
				return nil
			}
			return logReceiverFailure(ctx, output, account, "channel-stopped", errors.New("Feishu channel stopped"))
		case now := <-ticker.C:
			if !now.Before(nextDiagnostics) {
				process.publishDiagnostics(account, engine, connections, &diagnostics, output)
				nextDiagnostics = time.Now().Add(receiverDiagnosticsInterval)
			}
			receiverRunSettingsIPC.serve(ctx, process, account, pendingSettings, engine.LocalRunSettings)
			receiverAccessPolicyIPC.serve(ctx, process, account, pendingAccessPolicy, engine.LocalAccessPolicy)
			process.serveReconnect(ctx, account, pendingReconnect, applyReconnect)
			receiverMeetingIPC.serve(ctx, process, account, pendingMeetings, engine.LocalMeeting)
			process.serveGroupAccess(ctx, account, pendingGroups, engine.LocalGroupAccess)
			connections.Lark.RefreshOwner(ctx)
			if err := engine.Tick(ctx); err != nil {
				return logReceiverFailure(ctx, output, account, "worker-tick-failed", errors.New("durable worker failed; inspect private state before restart"))
			}
		}
	}
}

func receiverTelemetryRetentionDays(raw string) int {
	days, err := strconv.Atoi(raw)
	if err != nil || days < 1 || days > 3650 {
		return 30
	}
	return days
}
