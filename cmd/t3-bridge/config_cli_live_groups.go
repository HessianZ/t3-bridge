package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"slices"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

type liveGroupOperation func(context.Context, string, string, string, bridge.GroupAccessRequest) (bridge.GroupAccessView, error)

func runConfigLiveGroups(args []string, output io.Writer, operate liveGroupOperation) error {
	flags := flag.NewFlagSet("t3-bridge config live-groups", flag.ContinueOnError)
	flags.SetOutput(output)
	process := flags.String("process", "", "required full receiver process ID from t3-bridge ps")
	appID := flags.String("app-id", "", "required application ID in the receiver process")
	tenant := flags.String("tenant", "feishu", "application tenant: feishu or lark")
	confirm := flags.Bool("confirm", false, "confirm granting access to multiple selected groups")
	var req bridge.GroupAccessRequest
	var chatIDs []string
	flags.Func("chat-id", "exact group ID; repeat for a batch grant with -access allow -confirm", func(id string) error {
		if id == "" || strings.TrimSpace(id) != id {
			return errors.New("expected a non-empty group ID without surrounding whitespace")
		}
		chatIDs = append(chatIDs, id)
		return nil
	})
	flags.StringVar(&req.Access, "access", "", "group access override: allow, deny or inherit")
	flags.StringVar(&req.Mention, "mention", "", "mention rule override: on, off or inherit")
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
	case !slices.Contains([]string{"", "allow", "deny", "inherit"}, req.Access):
		return errors.New("-access must be allow, deny or inherit")
	case !slices.Contains([]string{"", "on", "off", "inherit"}, req.Mention):
		return errors.New("-mention must be on, off or inherit")
	case len(chatIDs) == 0 && req.Access != "":
		return errors.New("-access requires -chat-id")
	}
	switch len(chatIDs) {
	case 0:
	case 1:
		req.ChatID = chatIDs[0]
	default:
		req.ChatIDs = chatIDs
		if err := req.ValidateBatchGrant(); err != nil {
			return err
		}
		if !*confirm {
			return errors.New("-confirm is required to grant access to multiple groups")
		}
	}
	var readID string
	if req.Access == "" && req.Mention == "" {
		readID, req.ChatID = req.ChatID, ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), receiverGroupTimeout)
	defer cancel()
	view, err := operate(ctx, *process, *tenant, *appID, req)
	if err != nil {
		return err
	}
	if readID != "" {
		matches := make([]bridge.GroupAccessEntry, 0, 1)
		for _, group := range view.Groups {
			if group.ID == readID {
				matches = append(matches, group)
			}
		}
		view.Groups = matches
	}
	return json.NewEncoder(output).Encode(view)
}

func localLiveGroupAccess(ctx context.Context, process, tenant, appID string, req bridge.GroupAccessRequest) (bridge.GroupAccessView, error) {
	profiles, err := localProfiles()
	if err != nil {
		return bridge.GroupAccessView{}, err
	}
	return localReceiverProcesses(profiles).groupAccess(ctx, process, tenant, appID, req)
}
