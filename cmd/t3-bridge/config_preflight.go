package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"slices"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

var errWorkspacePreflightApp = errors.New("已保存配置中没有所选应用")

func checkSavedReceiverConfig(ctx context.Context, path, revision string, includeDisabled bool) (bridge.ReceiverPreflight, error) {
	return checkSavedReceiverAppConfig(ctx, path, revision, "", includeDisabled)
}

func checkSavedReceiverAppConfig(ctx context.Context, path, revision, appID string, includeDisabled bool) (bridge.ReceiverPreflight, error) {
	var report bridge.ReceiverPreflight
	raw, err := bridge.ReadPrivateConfigJSON(path)
	if revision != "" && (err != nil || configRevision(raw) != revision) {
		return report, errWorkspaceServiceConfigChanged
	}
	if err != nil {
		return report, err
	}
	revision = configRevision(raw)
	config, err := bridge.ParseConfig(raw)
	if err != nil {
		return report, err
	}
	if appID != "" {
		accounts := config.Accounts()
		index := slices.IndexFunc(accounts, func(account bridge.Config) bool { return account.AppID == appID })
		if index < 0 {
			return report, errWorkspacePreflightApp
		}
		config = accounts[index]
	}
	report = runReceiverPreflight(ctx, config, includeDisabled)
	latest, err := bridge.ReadPrivateConfigJSON(path)
	if err != nil || configRevision(latest) != revision {
		return bridge.ReceiverPreflight{}, errWorkspaceServiceConfigChanged
	}
	return report, nil
}

func runReceiverPreflight(ctx context.Context, config bridge.Config, includeDisabled bool) bridge.ReceiverPreflight {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	report := bridge.CheckReceiverConfig(ctx, config, includeDisabled)
	checkPreflightReceivers(&report, time.Now())
	return report
}

func runConfigPreflight(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("t3-bridge config preflight", flag.ContinueOnError)
	flags.SetOutput(output)
	path := flags.String("config", "", "private "+defaultConfigFlagHelp)
	profile := flags.String("profile", "", "use the named configuration profile")
	appID := flags.String("app-id", "", "check only the App with this exact ID")
	includeDisabled := flags.Bool("include-disabled", false, "also verify disabled accounts without enabling their receivers")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	configPath, err := resolveConfigPath(*path, *profile, defaultConfigPath)
	if err != nil {
		return err
	}
	report, err := checkSavedReceiverAppConfig(context.Background(), configPath, "", *appID, *includeDisabled)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(output).Encode(report); err != nil {
		return err
	}
	if *includeDisabled {
		if !report.AllAccounts.Passed {
			if *appID != "" {
				return errors.New("the selected App did not pass preflight")
			}
			return errors.New("not all configured accounts passed preflight")
		}
		return nil
	}
	if !report.Passed {
		return errors.New("receiver preflight did not pass")
	}
	return nil
}
