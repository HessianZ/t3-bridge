package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

const liveReconnectTimeout = receiverIPCTimeout

type liveReconnectOperation func(context.Context, string, string, string, bridge.ReconnectAction) (bridge.LocalReconnectView, error)

func runConfigLiveReconnect(args []string, output io.Writer, operate liveReconnectOperation) error {
	flags := flag.NewFlagSet("t3-bridge config live-reconnect", flag.ContinueOnError)
	flags.SetOutput(output)
	process := flags.String("process", "", "required full receiver process ID from t3-bridge ps")
	appID := flags.String("app-id", "", "required application ID in the receiver process")
	tenant := flags.String("tenant", "feishu", "application tenant: feishu or lark")
	mode := flags.String("action", string(bridge.ReconnectNow), "reconnect now, wait for active runs, or cancel a wait (now|wait|cancel)")
	confirm := flags.Bool("confirm", false, "confirm the reconnect operation for this App")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	action := bridge.ReconnectAction(*mode)
	switch {
	case flags.NArg() != 0:
		return errors.New("unexpected arguments")
	case !receiverProcessID.MatchString(*process):
		return errors.New("-process requires the complete process ID from t3-bridge ps")
	case *appID == "" || strings.TrimSpace(*appID) != *appID:
		return errors.New("-app-id requires a non-empty application ID without surrounding whitespace")
	case *tenant != "feishu" && *tenant != "lark":
		return errors.New("-tenant must be feishu or lark")
	case !action.Valid():
		return errors.New("-action must be now, wait or cancel")
	case !*confirm:
		return errors.New("-confirm is required to reconnect this App")
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveReconnectTimeout)
	defer cancel()
	view, err := operate(ctx, *process, *tenant, *appID, action)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(output).Encode(view); err != nil {
		return err
	}
	switch view.State {
	case bridge.ReconnectStarted, bridge.ReconnectScheduled, bridge.ReconnectCancelled, bridge.ReconnectIdle, bridge.ReconnectRecoveryRequested:
		return nil
	case bridge.ReconnectPending:
		return errors.New("this App already has a reconnect waiting for active runs to settle")
	case bridge.ReconnectWorkerOffline:
		return errors.New("this App's receiver has exited. Use -action now to request recovery")
	default:
		return errReceiverReconnectUnknown
	}
}

func localLiveReconnect(ctx context.Context, process, tenant, appID string, action bridge.ReconnectAction) (bridge.LocalReconnectView, error) {
	profiles, err := localProfiles()
	if err != nil {
		return bridge.LocalReconnectView{}, err
	}
	return localReceiverProcesses(profiles).reconnect(ctx, process, tenant, appID, action)
}
