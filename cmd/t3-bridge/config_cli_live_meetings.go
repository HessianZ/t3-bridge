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

func runConfigLiveMeetings(args []string, output io.Writer, operate liveMeetingOperation) error {
	flags := flag.NewFlagSet("t3-bridge config live-meetings", flag.ContinueOnError)
	flags.SetOutput(output)
	process := flags.String("process", "", "required full receiver process ID from t3-bridge ps")
	appID := flags.String("app-id", "", "required application ID in the receiver process")
	tenant := flags.String("tenant", "feishu", "application tenant: feishu or lark")
	action := flags.String("action", "status", "status, join, leave, or confirm-left")
	number := flags.String("number", "", "nine-digit meeting number; required for changes")
	revision := flags.String("revision", "", "current meeting receipt revision; required for leave and confirm-left")
	confirm := flags.Bool("confirm", false, "confirm a meeting change for this App and number")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	switch {
	case flags.NArg() != 0:
		return errors.New("unexpected arguments")
	case !receiverProcessID.MatchString(*process):
		return errors.New("-process requires the complete process ID from t3-bridge ps")
	case *appID == "" || strings.TrimSpace(*appID) != *appID:
		return errors.New("-app-id requires a non-empty application ID without surrounding whitespace")
	case *tenant != "feishu" && *tenant != "lark":
		return errors.New("-tenant must be feishu or lark")
	}
	request := bridge.LocalMeetingRequest{Action: *action, Number: strings.Join(strings.Fields(*number), ""), Revision: *revision}
	if err := request.Validate(); err != nil {
		return err
	}
	if request.Action != "status" && !*confirm {
		return errors.New("-confirm is required for a meeting change")
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveMeetingTimeout)
	defer cancel()
	view, err := operate(ctx, *process, *tenant, *appID, request)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(output).Encode(view); err != nil {
		return err
	}
	switch view.Outcome {
	case "", "joined", "ended", "left", "confirmed":
		return nil
	default:
		return errors.New(meetingOutcomeText(view.Outcome))
	}
}

func localLiveMeeting(ctx context.Context, process, tenant, appID string, request bridge.LocalMeetingRequest) (bridge.LocalMeetingView, error) {
	profiles, err := localProfiles()
	if err != nil {
		return bridge.LocalMeetingView{}, err
	}
	return localReceiverProcesses(profiles).meeting(ctx, process, tenant, appID, request)
}
