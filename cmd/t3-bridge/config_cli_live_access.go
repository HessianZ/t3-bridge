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

type liveAccessOperation func(context.Context, string, string, string, bridge.LocalAccessPolicyRequest) (bridge.LocalAccessPolicyView, error)

func runConfigLiveAccess(args []string, output io.Writer, operate liveAccessOperation) error {
	flags := flag.NewFlagSet("t3-bridge config live-access", flag.ContinueOnError)
	flags.SetOutput(output)
	process := flags.String("process", "", "required full receiver process ID from t3-bridge ps")
	appID := flags.String("app-id", "", "required application ID in the receiver process")
	tenant := flags.String("tenant", "feishu", "application tenant: feishu or lark")
	var req bridge.LocalAccessPolicyRequest
	flags.StringVar(&req.Kind, "kind", "", "permission to change: user, admin or route")
	flags.StringVar(&req.UserID, "user-id", "", "application Open ID to change")
	flags.StringVar(&req.RouteID, "route-id", "", "required with -kind route")
	flags.StringVar(&req.Mode, "mode", "", "permission override: allow, deny or inherit; omit all change flags to read")
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
	var changing, routeSpecified bool
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "route-id":
			changing, routeSpecified = true, true
		case "kind", "user-id", "mode":
			changing = true
		}
	})
	if changing {
		if routeSpecified && req.Kind != "route" {
			return errors.New("-route-id requires -kind route")
		}
		if err := req.ValidateChange(); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), receiverIPCTimeout)
	defer cancel()
	view, err := operate(ctx, *process, *tenant, *appID, req)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(view)
}

func localLiveAccess(ctx context.Context, process, tenant, appID string, req bridge.LocalAccessPolicyRequest) (bridge.LocalAccessPolicyView, error) {
	profiles, err := localProfiles()
	if err != nil {
		return bridge.LocalAccessPolicyView{}, err
	}
	return localReceiverProcesses(profiles).accessPolicy(ctx, process, tenant, appID, req)
}
