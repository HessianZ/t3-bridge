package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

const (
	defaultConfigPath     = "config/local.json"
	defaultConfigFlagHelp = "JSON configuration file (defaults to active profile, existing config/local.json, or the user's config.json)"
)

func runConfig(args []string, input io.Reader, output io.Writer) error {
	const usage = "usage: t3-bridge config {init|check|preflight|prepare-receiver|drain|check-app|check-meeting|check-tool-identity|tool-binding|list-groups|live-groups|live-settings|live-access|live-reconnect|live-meetings|receiver-status|receiver-readiness|telemetry-archive|my-groups|receipt|show|export|backup|patch|diff|version|save|catalog|model-options|local-t3|register-project|authorize-t3|bind-feishu|replace-app|credentials|tui|gui|web|import-legacy|import-workspaces|import-session}"
	if len(args) == 0 {
		return errors.New(usage)
	}
	command := args[0]
	switch command {
	case "web":
		return runConfigWeb(args[1:])
	case "gui":
		return runConfigGUI(args[1:])
	case "tui":
		return runConfigTUI(args[1:])
	case "preflight":
		return runConfigPreflight(args[1:], output)
	case "prepare-receiver":
		return runConfigPrepareReceiver(args[1:], output)
	case "drain":
		return runConfigDrain(args[1:], output)
	case "receipt":
		return runConfigReceipt(args[1:], output)
	case "telemetry-archive":
		return runConfigTelemetryArchive(args[1:], output)
	case "catalog":
		return runConfigCatalog(args[1:], input, output)
	case "model-options":
		return runConfigModelOptions(args[1:], input, output)
	case "local-t3":
		return runConfigLocalT3(args[1:], input, output)
	case "register-project":
		return runConfigRegisterProject(args[1:], input, output)
	case "authorize-t3":
		return runConfigAuthorizeT3(args[1:], input, output)
	case "bind-feishu":
		return runConfigBindFeishu(args[1:], input, os.Stderr)
	case "replace-app":
		return runConfigReplaceApp(args[1:], input, output)
	case "credentials":
		return runConfigCredentials(args[1:], input, output)
	case "check-app", "check-meeting", "list-groups":
		return runConfigFeishu(command, args[1:], output)
	case "check-tool-identity":
		return runConfigToolIdentity(args[1:], output)
	case "tool-binding":
		return runConfigToolBinding(args[1:], output)
	case "live-groups":
		return runConfigLiveGroups(args[1:], output, localLiveGroupAccess)
	case "live-settings":
		return runConfigLiveSettings(args[1:], output, localLiveSettings)
	case "live-access":
		return runConfigLiveAccess(args[1:], output, localLiveAccess)
	case "live-reconnect":
		return runConfigLiveReconnect(args[1:], output, localLiveReconnect)
	case "live-meetings":
		return runConfigLiveMeetings(args[1:], output, localLiveMeeting)
	case "receiver-status":
		return runConfigReceiverStatus(args[1:], output, localReceiverStatus)
	case "receiver-readiness":
		return runConfigReceiverReadiness(args[1:], output)
	case "my-groups":
		return runConfigUserGroups(args[1:], output)
	case "import-legacy":
		return runConfigImportLegacy(args[1:], output)
	case "import-workspaces":
		return runConfigImportWorkspaces(args[1:], output)
	case "import-session":
		return runConfigImportSession(args[1:], output)
	case "init", "check", "show", "export", "backup", "patch", "diff", "version", "save":
	default:
		return errors.New(usage)
	}
	flags := flag.NewFlagSet("t3-bridge config "+command, flag.ContinueOnError)
	configPath := flags.String("config", "", defaultConfigFlagHelp)
	profile := flags.String("profile", "", "use the named configuration profile")
	var template, expectedRevision string
	var printDraft, draftInput bool
	switch command {
	case "init":
		flags.StringVar(&template, "template", "generic", "built-in template: generic, multi-host, or homeops")
		flags.BoolVar(&printDraft, "draft", false, "write a template draft to stdout without creating a configuration file")
	case "check":
		flags.BoolVar(&draftInput, "draft-stdin", false, "validate a configuration draft from stdin")
	case "save":
		flags.StringVar(&expectedRevision, "expected-revision", "", "required SHA-256 of the source file, or missing for first creation; reads candidate JSON from stdin")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	if printDraft && (*configPath != "" || *profile != "") {
		return errors.New("-draft cannot be combined with -config or -profile")
	}
	var path string
	var err error
	if !printDraft && !draftInput {
		path, err = resolveConfigPath(*configPath, *profile, defaultConfigPath)
		if err != nil {
			return err
		}
	}

	switch command {
	case "init":
		switch template {
		case "generic", "multi-host", "homeops":
		default:
			return errors.New("config template must be generic, multi-host, or homeops")
		}
		candidate, err := loadConfigTemplate(template)
		if err != nil {
			return err
		}
		if printDraft {
			candidate, err = bridge.EditConfigJSON(candidate, nil)
			if err != nil {
				return err
			}
			_, err = output.Write(candidate)
			return err
		}
		return printConfigSave(output, path, candidate, "missing")
	case "save":
		if expectedRevision == "" {
			return errors.New("config save requires -expected-revision")
		}
		candidate, err := io.ReadAll(io.LimitReader(input, bridge.MaxConfigBytes+1))
		if err != nil {
			return err
		}
		return printConfigSave(output, path, candidate, expectedRevision)
	}

	var raw []byte
	if draftInput {
		raw, err = readConfigDraft(input, *configPath, *profile)
	} else if command == "backup" {
		raw, err = readConfigBackup(path)
	} else {
		raw, err = bridge.ReadConfigJSON(path)
	}
	if err != nil {
		return err
	}
	switch command {
	case "check":
		if _, err := bridge.ParseConfig(raw); err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, "configuration valid")
		return err
	case "version":
		_, err = fmt.Fprintln(output, configRevision(raw))
		return err
	case "diff":
		candidate, err := io.ReadAll(io.LimitReader(input, bridge.MaxConfigBytes+1))
		if err != nil {
			return err
		}
		return printConfigDiff(output, path, raw, candidate)
	}

	var edits []byte
	if command == "patch" {
		edits, err = io.ReadAll(io.LimitReader(input, bridge.MaxConfigBytes+1))
		if err != nil {
			return err
		}
		if len(edits) == 0 {
			return errors.New("provide a JSON array of edits on stdin")
		}
	}
	candidate, err := bridge.EditConfigJSON(raw, edits)
	if err != nil {
		return err
	}
	_, err = output.Write(candidate)
	return err
}

func readConfigDraft(input io.Reader, path, profile string) ([]byte, error) {
	if path != "" || profile != "" {
		return nil, errors.New("-draft-stdin cannot be combined with -config or -profile")
	}
	if input == nil {
		return nil, errors.New("-draft-stdin requires a JSON configuration draft on stdin")
	}
	raw, err := io.ReadAll(io.LimitReader(input, bridge.MaxConfigBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > bridge.MaxConfigBytes {
		return nil, errors.New("configuration draft exceeds 1 MiB")
	}
	return raw, nil
}

func printConfigSave(output io.Writer, path string, candidate []byte, expectedRevision string) error {
	revision, err := saveConfigCandidate(path, candidate, expectedRevision)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, revision)
	return err
}

func printConfigDiff(output io.Writer, path string, raw, candidate []byte) error {
	before, err := bridge.EditConfigJSON(raw, nil)
	if err != nil {
		return fmt.Errorf("source config: %w", err)
	}
	after, err := bridge.EditConfigJSON(candidate, nil)
	if err != nil {
		return fmt.Errorf("candidate config: %w", err)
	}
	changes := configChanges(string(before), string(after))
	if len(changes) == 0 {
		return nil
	}
	if _, err := fmt.Fprintf(output, "--- %s\n+++ candidate (stdin)\n", path); err != nil {
		return err
	}
	for _, change := range changes {
		if _, err := fmt.Fprintf(output, "%s\n%s%s", change.Header, change.Removed, change.Added); err != nil {
			return err
		}
	}
	return nil
}
