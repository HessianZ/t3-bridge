package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func runConfigImportWorkspaces(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("t3-bridge config import-workspaces", flag.ContinueOnError)
	flags.SetOutput(output)
	path := flags.String("config", "", defaultConfigFlagHelp)
	profile := flags.String("profile", "", "use the named destination configuration profile")
	appID := flags.String("app-id", "", "required destination App ID")
	source := flags.String("legacy-workspaces", "", "legacy workspaces.json file (omission matches the installed old configuration to the destination App and tenant)")
	mapping := flags.String("mapping", "", "JSON array with sourceKey, userId, chatId, chatType (p2p/group), routeId, optional topicId and optional projectId for a unique directory match")
	list := flags.Bool("list", false, "list exact source alias keys and configured destination routes without a mapping")
	confirm := flags.String("confirm", "", "apply the reviewed plan to the offline state directory")
	apply := flags.Bool("apply", false, "apply directly with an explicit legacy file (default is read-only preview)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || *appID == "" {
		return errors.New("import-workspaces requires -app-id, with no positional arguments")
	}
	if *list && (*mapping != "" || *apply || *confirm != "") {
		return errors.New("-list cannot be combined with -mapping, -apply or -confirm")
	}
	if !*list && *mapping == "" {
		return errors.New("import-workspaces requires -mapping or -list")
	}
	if *apply && (*source == "" || *confirm != "") {
		return errors.New("-apply requires -legacy-workspaces and cannot be combined with -confirm")
	}
	selected, err := resolveConfigPath(*path, *profile, defaultConfigPath)
	if err != nil {
		return err
	}
	targetPath, err := workspaceImportCLIPath(selected)
	if err != nil {
		return fmt.Errorf("-config: %w", err)
	}
	legacyPath := *source
	if legacyPath != "" {
		legacyPath, err = workspaceImportCLIPath(legacyPath)
		if err != nil {
			return fmt.Errorf("-legacy-workspaces: %w", err)
		}
	}
	target := webSource{Path: targetPath}
	request := workspaceImportRequest{AppID: *appID, LegacyPath: legacyPath}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if *list {
		inspection, err := inspectWorkspaceImport(target, request)
		if err != nil {
			return err
		}
		return encoder.Encode(inspection)
	}
	mappingJSON, err := bridge.ReadConfigJSON(*mapping)
	if err != nil {
		return fmt.Errorf("-mapping: %w", err)
	}
	request.MappingJSON = string(mappingJSON)
	var result workspaceImportResult
	var review string
	switch {
	case *apply:
		input, err := prepareWorkspaceImport(target, request)
		if err != nil {
			return err
		}
		result.LegacyPath = input.legacyPath
		result.WorkspaceImportResult, err = bridge.ImportLegacyWorkspaces(input.account, input.legacy, input.mapping, true)
		if err != nil {
			return err
		}
	case *confirm != "":
		result, err = applyWorkspaceImport(target, request, *confirm)
	default:
		result, review, err = previewWorkspaceImport(target, request)
	}
	if err != nil {
		return err
	}
	return encoder.Encode(struct {
		workspaceImportResult
		Review string `json:"review,omitempty"`
	}{result, review})
}

func workspaceImportCLIPath(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(path)
}
