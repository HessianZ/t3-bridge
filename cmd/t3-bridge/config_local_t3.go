package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"strconv"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func runConfigLocalT3(args []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("t3-bridge config local-t3", flag.ContinueOnError)
	flags.SetOutput(output)
	path := flags.String("config", "", defaultConfigFlagHelp)
	profile := flags.String("profile", "", "use the named configuration profile")
	draftInput := flags.Bool("draft-stdin", false, "read the configuration draft from stdin")
	appID := flags.String("app-id", "", "select the App to configure (defaults to the primary app)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	var raw []byte
	var err error
	if *draftInput {
		raw, err = readConfigDraft(input, *path, *profile)
	} else {
		resolved, resolveErr := resolveConfigPath(*path, *profile, defaultConfigPath)
		if resolveErr != nil {
			return resolveErr
		}
		raw, err = bridge.ReadConfigJSON(resolved)
	}
	if err != nil {
		return err
	}
	config, err := bridge.ParseConfig(raw)
	if err != nil {
		return err
	}
	accounts := config.Accounts()
	for index, account := range accounts {
		if *appID == "" || account.AppID == *appID {
			candidate, err := localT3Candidate(raw, index, account)
			if err != nil {
				return err
			}
			_, err = output.Write(candidate)
			return err
		}
	}
	return errors.New("app-id is not configured")
}

func useLocalT3(raw []byte, accountIndex int) ([]byte, error) {
	config, err := bridge.ParseConfig(raw)
	if err != nil {
		return nil, err
	}
	accounts := config.Accounts()
	if accountIndex < 0 || accountIndex >= len(accounts) {
		return nil, errors.New("所选账号不存在")
	}
	return localT3Candidate(raw, accountIndex, accounts[accountIndex])
}

func localT3Candidate(raw []byte, index int, account bridge.Config) ([]byte, error) {
	const placeholder = "REPLACE_ENVIRONMENT_ID"
	if account.MCP.EnvironmentID != placeholder {
		return nil, errors.New("当前账号已填写 T3 环境，请手动编辑连接以切换目标")
	}
	path, environmentID, err := bridge.DiscoverLocalT3()
	if err != nil {
		return nil, err
	}
	edits := []configTUIEdit{
		{Path: []string{"mcp", "endpoint"}, Delete: true},
		{Path: []string{"mcp", "runtimeFile"}, Value: path},
		{Path: []string{"mcp", "environmentId"}, Value: environmentID},
	}
	for route, target := range account.Routes {
		if target.EnvironmentID == placeholder {
			edits = append(edits, configTUIEdit{Path: []string{"routes", route, "environmentId"}, Value: environmentID})
		}
	}
	if index > 0 {
		for i := range edits {
			edits[i].Path = append([]string{"additionalAccounts", strconv.Itoa(index - 1)}, edits[i].Path...)
		}
	}
	patch, err := json.Marshal(edits)
	if err != nil {
		return nil, err
	}
	return bridge.EditConfigJSON(raw, patch)
}
