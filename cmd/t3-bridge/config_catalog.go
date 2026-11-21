package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func runConfigCatalog(args []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("t3-bridge config catalog", flag.ContinueOnError)
	flags.SetOutput(output)
	path := flags.String("config", "", defaultConfigFlagHelp)
	profile := flags.String("profile", "", "use the named configuration profile")
	draftInput := flags.Bool("draft-stdin", false, "read the configuration draft from stdin")
	appID := flags.String("app-id", "", "select a configured app (defaults to the primary app)")
	target := flags.String("environment-id", "", "target environment ID (defaults to the local environment)")
	route := flags.String("route", "", "query the named route's environment and suggest its project")
	search := flags.String("search", "", "search for a model by name or ID")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	account, err := loadT3ConfigAccount(input, *path, *profile, *appID, *draftInput)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connection := account.MCP
	if connection.EnvironmentID == "REPLACE_ENVIRONMENT_ID" {
		connection.EnvironmentID = ""
	}
	var selected bridge.Target
	if *route != "" {
		var ok bool
		selected, ok = account.Routes[*route]
		if !ok || *target != "" {
			return errors.New("-route requires a configured route and cannot be combined with -environment-id")
		}
		*target = selected.EnvironmentID
		if *target == "REPLACE_ENVIRONMENT_ID" {
			*target = ""
		}
	}
	catalog, err := bridge.DiscoverT3(ctx, connection, *target, *search)
	if err != nil {
		return err
	}
	catalog.SuggestProject(selected.ProjectID, selected.ProjectPath)
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(catalog)
}

func loadT3ConfigAccount(input io.Reader, path, profile, appID string, draft bool) (bridge.Config, error) {
	if !draft {
		return loadConfigAccount(path, profile, appID)
	}
	raw, err := readConfigDraft(input, path, profile)
	if err != nil {
		return bridge.Config{}, err
	}
	config, err := bridge.ParseConfig(raw)
	if err != nil {
		return bridge.Config{}, err
	}
	return selectConfigAccount(config, appID)
}

func loadConfigAccount(path, profile, appID string) (bridge.Config, error) {
	resolved, err := resolveConfigPath(path, profile, defaultConfigPath)
	if err != nil {
		return bridge.Config{}, err
	}
	config, err := bridge.LoadConfig(resolved)
	if err != nil {
		return bridge.Config{}, err
	}
	return selectConfigAccount(config, appID)
}

func selectConfigAccount(config bridge.Config, appID string) (bridge.Config, error) {
	if appID == "" {
		return config, nil
	}
	for _, account := range config.Accounts() {
		if account.AppID == appID {
			return account, nil
		}
	}
	return bridge.Config{}, errors.New("app-id is not configured")
}

func configCatalogHandler(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Connection    bridge.MCPConfig `json:"connection"`
		EnvironmentID string           `json:"environmentId"`
		Search        string           `json:"search"`
		ProjectID     string           `json:"projectId"`
		WorkspacePath string           `json:"workspacePath"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decode := json.NewDecoder(r.Body)
	decode.DisallowUnknownFields()
	if err := decode.Decode(&input); err != nil || decode.Decode(new(json.RawMessage)) != io.EOF {
		http.Error(w, "无法读取 T3 查询参数", http.StatusBadRequest)
		return
	}
	input.Search = strings.TrimSpace(input.Search)
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	catalog, err := bridge.DiscoverT3(ctx, input.Connection, input.EnvironmentID, input.Search)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	catalog.SuggestProject(input.ProjectID, input.WorkspacePath)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(catalog)
}
