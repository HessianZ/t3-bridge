package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func runConfigRegisterProject(args []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("t3-bridge config register-project", flag.ContinueOnError)
	flags.SetOutput(output)
	file := flags.String("config", "", "private JSON configuration path (may be new with -draft-stdin)")
	profile := flags.String("profile", "", "use the named configuration profile")
	appID := flags.String("app-id", "", "select the App (defaults to the primary app)")
	route := flags.String("route", "", "select the route (defaults to the selected app's default route)")
	directory := flags.String("directory", "", "absolute path to an existing directory on the target T3 host")
	managed := flags.Bool("managed", false, "prepare a private local workspace for the selected route and register it")
	setupToken := flags.String("setup-token-file", "", "private OAuth JSON for this registration only")
	draftInput := flags.Bool("draft-stdin", false, "read the configuration draft from stdin")
	revision := flags.String("expected-revision", "", "saved configuration SHA-256 or missing (required with -draft-stdin)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || *managed == (*directory != "") {
		return errors.New("config register-project requires exactly one of -directory or -managed and accepts no positional arguments")
	}
	path, err := resolveConfigPath(*file, *profile, defaultConfigPath)
	if err != nil {
		return err
	}
	var candidate []byte
	if *draftInput {
		if *revision == "" || input == nil {
			return errors.New("-draft-stdin requires a draft on stdin and -expected-revision")
		}
		draft, readErr := io.ReadAll(io.LimitReader(input, bridge.MaxConfigBytes+1))
		if readErr != nil {
			return readErr
		}
		candidate, err = registerUnsavedProject(context.Background(), path, *revision, draft, *appID, *route, *directory, *setupToken)
	} else {
		candidate, err = registerSavedProject(context.Background(), path, *revision, *appID, *route, *directory, *setupToken)
	}
	if err != nil {
		return err
	}
	_, err = output.Write(candidate)
	return err
}

// registerSavedProject uses the saved connection and returns a draft for the existing save flow.
func registerSavedProject(ctx context.Context, configPath, revision, appID, routeID, directory, setupTokenFile string) ([]byte, error) {
	return registerProjectDraft(ctx, configPath, revision, appID, routeID, directory, setupTokenFile, bridge.RegisterConfigProject)
}

func registerUnsavedProject(ctx context.Context, configPath, revision string, draft []byte, appID, routeID, directory, setupTokenFile string) ([]byte, error) {
	return registerProjectCandidate(ctx, configPath, revision, draft, appID, routeID, directory, setupTokenFile, bridge.RegisterConfigProject, managedProjectWorkspace)
}

func registerProjectDraft(ctx context.Context, configPath, revision, appID, routeID, directory, setupTokenFile string,
	register func(context.Context, bridge.MCPConfig, string, string) (bridge.Project, error),
) ([]byte, error) {
	return registerProjectDraftWithWorkspace(ctx, configPath, revision, appID, routeID, directory, setupTokenFile, register, managedProjectWorkspace)
}

func registerProjectDraftWithWorkspace(ctx context.Context, configPath, revision, appID, routeID, directory, setupTokenFile string,
	register func(context.Context, bridge.MCPConfig, string, string) (bridge.Project, error),
	workspace func(string, bridge.Config, string) (string, error),
) ([]byte, error) {
	raw, err := bridge.ReadPrivateConfigJSON(configPath)
	if err != nil {
		return nil, err
	}
	baseline := configRevision(raw)
	if revision != "" && revision != baseline {
		return nil, errors.New("配置文件版本已变化，请重新打开后登记项目")
	}
	return registerProjectCandidate(ctx, configPath, baseline, raw, appID, routeID, directory, setupTokenFile, register, workspace)
}

func registerProjectCandidate(ctx context.Context, configPath, baseline string, raw []byte, appID, routeID, directory, setupTokenFile string,
	register func(context.Context, bridge.MCPConfig, string, string) (bridge.Project, error),
	workspace func(string, bridge.Config, string) (string, error),
) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !strings.EqualFold(filepath.Ext(configPath), ".json") || baseline == "" {
		return nil, errors.New("项目登记需要 JSON 配置路径和原配置版本")
	}
	if !projectRegistrationSourceMatches(configPath, baseline) {
		return nil, errors.New("配置文件版本已变化，请重新打开后登记项目")
	}
	config, err := bridge.ParseConfig(raw)
	if err != nil {
		return nil, err
	}
	for index, account := range config.Accounts() {
		if appID != "" && account.AppID != appID {
			continue
		}
		if routeID == "" {
			routeID = account.DefaultRoute
		}
		target, exists := account.Routes[routeID]
		if !exists {
			return nil, errors.New("所选账号中没有该路由")
		}
		connection, err := account.MCP.WithProjectSetupToken(setupTokenFile)
		if err != nil {
			return nil, err
		}
		managed := directory == ""
		if managed {
			directory, err = workspace(configPath, account, routeID)
			if err != nil {
				return nil, err
			}
			lock, err := prepareManagedProjectWorkspace(directory)
			if err != nil {
				return nil, err
			}
			defer lock.Close()
		}
		ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		project, err := register(ctx, connection, target.EnvironmentID, directory)
		if err != nil {
			if managed {
				return nil, fmt.Errorf("专用工作区已保留在 %s。%w", directory, err)
			}
			return nil, err
		}
		if !projectRegistrationSourceMatches(configPath, baseline) {
			return nil, errors.New("T3 项目已核对，配置文件版本已变化。请重新打开配置并选择该项目")
		}
		prefix := []string{"routes", routeID}
		if index > 0 {
			prefix = []string{"additionalAccounts", strconv.Itoa(index - 1), "routes", routeID}
		}
		patch, err := json.Marshal([]configTUIEdit{
			{Path: append(prefix, "projectId"), Value: project.ID},
			{Path: append(prefix, "projectName"), Value: project.Title},
			{Path: append(prefix, "projectPath"), Value: project.Root},
		})
		if err != nil {
			return nil, err
		}
		return bridge.EditConfigJSON(raw, patch)
	}
	return nil, errors.New("所选账号不存在")
}

func projectRegistrationSourceMatches(path, revision string) bool {
	if revision == "missing" {
		_, err := os.Lstat(path)
		return errors.Is(err, os.ErrNotExist)
	}
	raw, err := bridge.ReadPrivateConfigJSON(path)
	return err == nil && configRevision(raw) == revision
}
