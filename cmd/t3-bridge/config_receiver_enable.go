package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"slices"
	"strconv"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

type receiverEnableDraft struct {
	Candidate []byte
	Report    bridge.ReceiverPreflight
}

func prepareReceiverEnable(ctx context.Context, path, revision, appID string) (receiverEnableDraft, error) {
	return prepareReceiverEnableWithCheck(ctx, path, revision, appID, runReceiverPreflight)
}

func prepareReceiverEnableWithCheck(ctx context.Context, path, revision, appID string,
	check func(context.Context, bridge.Config, bool) bridge.ReceiverPreflight,
) (receiverEnableDraft, error) {
	var draft receiverEnableDraft
	raw, err := bridge.ReadPrivateConfigJSON(path)
	if revision != "" && (err != nil || configRevision(raw) != revision) {
		return draft, errWorkspaceServiceConfigChanged
	}
	if err != nil {
		return draft, err
	}
	revision = configRevision(raw)
	config, err := bridge.ParseConfig(raw)
	if err != nil {
		return draft, err
	}
	accounts := config.Accounts()
	index := 0
	if appID != "" {
		index = slices.IndexFunc(accounts, func(account bridge.Config) bool { return account.AppID == appID })
	}
	if index < 0 {
		return draft, errors.New("已保存配置中没有所选应用")
	}
	account := accounts[index]
	if account.ReceiverAuthorized {
		return draft, errors.New("所选应用已启用接收")
	}
	draft.Report = check(ctx, account, true)
	latest, err := bridge.ReadPrivateConfigJSON(path)
	if err != nil || configRevision(latest) != revision {
		return receiverEnableDraft{}, errWorkspaceServiceConfigChanged
	}
	if err := ctx.Err(); err != nil {
		return receiverEnableDraft{}, err
	}
	if draft.Report.AllAccounts == nil || !draft.Report.AllAccounts.Passed {
		return draft, nil
	}
	field := []string{"receiverAuthorized"}
	if index > 0 {
		field = []string{"additionalAccounts", strconv.Itoa(index - 1), "receiverAuthorized"}
	}
	patch, err := json.Marshal([]configTUIEdit{{Path: field, Value: true}})
	if err != nil {
		return receiverEnableDraft{}, err
	}
	draft.Candidate, err = bridge.EditConfigJSON(raw, patch)
	if err != nil {
		return receiverEnableDraft{}, err
	}
	return draft, nil
}

func runConfigPrepareReceiver(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("t3-bridge config prepare-receiver", flag.ContinueOnError)
	flags.SetOutput(output)
	config := flags.String("config", "", "saved private JSON configuration file")
	profile := flags.String("profile", "", "use the named configuration profile")
	app := flags.String("app-id", "", "select the App (defaults to the primary app)")
	revision := flags.String("expected-revision", "", "optional SHA-256 of the saved configuration")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("config prepare-receiver accepts flags only")
	}
	path, err := resolveConfigPath(*config, *profile, defaultConfigPath)
	if err != nil {
		return err
	}
	draft, err := prepareReceiverEnable(context.Background(), path, *revision, *app)
	if err != nil {
		return err
	}
	if draft.Candidate == nil {
		return errors.New("所选应用未通过接收预检。运行 config preflight -include-disabled 查看检查结果")
	}
	_, err = output.Write(draft.Candidate)
	return err
}
