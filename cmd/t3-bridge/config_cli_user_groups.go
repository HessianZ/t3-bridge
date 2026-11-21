package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"strings"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

const configUserGroupsTimeout = 30 * time.Second

func runConfigUserGroups(args []string, output io.Writer) error {
	const usage = "usage: t3-bridge config my-groups {status|start|finish|cancel|list|invite}"
	if len(args) == 0 {
		return errors.New(usage)
	}
	action := args[0]
	flags := flag.NewFlagSet("t3-bridge config my-groups "+action, flag.ContinueOnError)
	flags.SetOutput(output)
	path := flags.String("config", "", defaultConfigFlagHelp)
	profile := flags.String("profile", "", "use the named configuration profile")
	appID := flags.String("app-id", "", "select a configured app (defaults to the primary app)")
	var purpose, ticket, query, pageToken, chatID string
	var confirm bool
	switch action {
	case "status":
	case "start":
		flags.StringVar(&purpose, "purpose", "", "authorization purpose: list or invite")
	case "finish", "cancel":
		flags.StringVar(&ticket, "ticket", "", "opaque authorization ticket returned by start")
	case "list":
		flags.StringVar(&query, "query", "", "search for a group")
		flags.StringVar(&pageToken, "page-token", "", "token from the previous page of the same query")
	case "invite":
		flags.StringVar(&chatID, "chat-id", "", "ID of the group to invite the configured app to")
		flags.BoolVar(&confirm, "confirm", false, "confirm inviting the configured app to the specified group")
	default:
		return errors.New(usage)
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	switch {
	case flags.NArg() != 0:
		return errors.New("unexpected arguments")
	case action == "start" && purpose != "list" && purpose != "invite":
		return errors.New("-purpose must be list or invite")
	case (action == "finish" || action == "cancel") && (ticket == "" || strings.TrimSpace(ticket) != ticket):
		return errors.New("-ticket requires the opaque ticket returned by start")
	case action == "invite" && (chatID == "" || strings.TrimSpace(chatID) != chatID || !confirm):
		return errors.New("invite requires -chat-id and explicit -confirm")
	}
	account, err := loadConfigAccount(*path, *profile, *appID)
	if err != nil {
		return err
	}
	profiles, err := localProfiles()
	if err != nil {
		return err
	}
	groups, err := bridge.NewUserGroups(account, profiles.root)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), configUserGroupsTimeout)
	defer cancel()
	var result any
	switch action {
	case "status":
		result, err = groups.Status(ctx)
	case "start":
		var challenge bridge.UserGroupsChallenge
		challenge, err = groups.Start(ctx, purpose)
		result = struct {
			Ticket    string    `json:"ticket"`
			URL       string    `json:"url"`
			ExpiresAt time.Time `json:"expiresAt"`
		}{Ticket: challenge.Ticket, URL: challenge.URL, ExpiresAt: challenge.ExpiresAt}
	case "finish":
		result, err = groups.Finish(ctx, ticket)
	case "cancel":
		err = groups.Cancel(ctx, ticket)
	case "list":
		result, err = groups.List(ctx, strings.TrimSpace(query), pageToken)
	case "invite":
		result, err = groups.Invite(ctx, chatID)
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		return err
	}
	return json.NewEncoder(output).Encode(result)
}
