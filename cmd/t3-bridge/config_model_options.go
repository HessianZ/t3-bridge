package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"strconv"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func runConfigModelOptions(args []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("t3-bridge config model-options", flag.ContinueOnError)
	flags.SetOutput(output)
	path := flags.String("config", "", "saved configuration JSON")
	profile := flags.String("profile", "", "saved named configuration profile")
	appID := flags.String("app-id", "", "select an app (defaults to the primary app)")
	routeID := flags.String("route", "", "configured route (required)")
	flags.Usage = func() {
		io.WriteString(output, "Read a JSON options object from stdin and write a validated configuration draft. {} uses model defaults.\n")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || *routeID == "" {
		return errors.New("config model-options requires -route and a JSON options object on stdin")
	}
	proposal, err := io.ReadAll(io.LimitReader(input, bridge.MaxConfigBytes+1))
	if err != nil {
		return err
	}
	var options map[string]any
	if len(proposal) > bridge.MaxConfigBytes || json.Unmarshal(proposal, &options) != nil || options == nil {
		return errors.New("model options must be a JSON object")
	}
	resolved, err := resolveConfigPath(*path, *profile, defaultConfigPath)
	if err != nil {
		return err
	}
	raw, err := bridge.ReadConfigJSON(resolved)
	if err != nil {
		return err
	}
	config, err := bridge.ParseConfig(raw)
	if err != nil {
		return err
	}
	accounts := config.Accounts()
	index := 0
	if *appID != "" {
		for index < len(accounts) && accounts[index].AppID != *appID {
			index++
		}
		if index == len(accounts) {
			return errors.New("app-id is not configured")
		}
	}
	account := accounts[index]
	target, ok := account.Routes[*routeID]
	if !ok {
		return errors.New("route is not configured for the selected app")
	}
	field := []string{"routes", *routeID, "options"}
	if index != 0 {
		field = append([]string{"additionalAccounts", strconv.Itoa(index - 1)}, field...)
	}
	edits, err := json.Marshal([]configTUIEdit{{Path: field, Value: json.RawMessage(proposal)}})
	if err != nil {
		return err
	}
	candidate, err := bridge.EditConfigJSON(raw, edits)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	catalog, err := bridge.DiscoverT3(ctx, account.MCP, target.EnvironmentID, target.Model)
	if err != nil {
		return err
	}
	for _, provider := range catalog.Providers {
		if provider.ID != target.ProviderID || provider.Driver != target.Driver {
			continue
		}
		for _, model := range provider.Models {
			if model.ID != target.Model {
				continue
			}
			if err := model.ValidateOptions(options); err != nil {
				return err
			}
			current, err := bridge.ReadConfigJSON(resolved)
			if err != nil || configRevision(raw) != configRevision(current) {
				return errors.New("saved configuration changed while reading T3 capabilities; retry from the latest revision")
			}
			_, err = output.Write(candidate)
			return err
		}
	}
	return errors.New("explicit provider/driver/model unavailable")
}
