package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

const profileUsage = "usage: t3-bridge profile {list|create NAME [-from CONFIG]|use NAME|archive NAME|restore ARCHIVED_CONFIG|purge NAME [-confirm]|export NAME [-output FILE] [-include-secrets -yes]|import NAME -from BUNDLE [-patch FILE] [-preview|-expected-revision VERSION]}"

type profileRegistry struct {
	root string
}

func localProfiles() (profileRegistry, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return profileRegistry{}, err
	}
	return profileRegistry{root: filepath.Join(dir, "t3-bridge")}, nil
}

func (r profileRegistry) file(name string) (string, error) {
	if !serviceInstanceName.MatchString(name) {
		return "", errors.New("profile name must be 1–48 lowercase letters, numbers or hyphens and begin with a letter or number")
	}
	dir := filepath.Join(r.root, "profiles", name)
	info, err := os.Lstat(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err == nil && !info.IsDir() {
		return "", errors.New("profile directory is not an ordinary directory")
	}
	return filepath.Join(dir, "config.json"), nil
}

func (r profileRegistry) existingFile(name string) (string, error) {
	path, err := r.file(name)
	if err != nil {
		return "", err
	}
	file, err := localfile.OpenPrivate(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("profile %q does not exist", name)
	}
	if err != nil {
		return "", fmt.Errorf("profile %q must contain a private regular configuration file: %w", name, err)
	}
	return path, file.Close()
}

func (r profileRegistry) active() (string, error) {
	path := filepath.Join(r.root, "active-profile")
	file, err := localfile.OpenPrivate(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 50))
	if err != nil {
		return "", err
	}
	if len(raw) > 49 {
		return "", errors.New("invalid active profile file")
	}
	name := strings.TrimSuffix(string(raw), "\n")
	if name != "" && !serviceInstanceName.MatchString(name) {
		return "", errors.New("invalid active profile name")
	}
	return name, nil
}

func (r profileRegistry) names() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(r.root, "profiles"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() && serviceInstanceName.MatchString(entry.Name()) {
			if _, err := r.existingFile(entry.Name()); err == nil {
				names = append(names, entry.Name())
			}
		}
	}
	return names, nil
}

func resolveConfigPath(path, profile, fallback string) (string, error) {
	if path != "" {
		if profile != "" {
			return "", errors.New("choose either -config or -profile")
		}
		return path, nil
	}
	registry, err := localProfiles()
	if err != nil {
		if profile == "" {
			return fallback, nil
		}
		return "", err
	}
	if profile == "" {
		profile, err = registry.active()
		if err != nil {
			return "", err
		}
		if profile == "" {
			if fallback == defaultConfigPath {
				// Preserve a project-local config from earlier installations.
				if _, err := os.Lstat(fallback); errors.Is(err, os.ErrNotExist) {
					return filepath.Join(registry.root, "config.json"), nil
				}
			}
			return fallback, nil
		}
	}
	return registry.existingFile(profile)
}

func runProfile(args []string, output io.Writer) error {
	registry, err := localProfiles()
	if err != nil {
		return err
	}
	return registry.run(args, output)
}

func (r profileRegistry) run(args []string, output io.Writer) error {
	if len(args) == 0 || len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		_, err := fmt.Fprintln(output, profileUsage)
		return err
	}
	if len(args) == 1 && args[0] == "list" {
		return r.list(output)
	}
	if len(args) < 2 {
		return errors.New(profileUsage)
	}
	action, name := args[0], args[1]
	switch {
	case action == "export":
		return r.runExport(name, args[2:], output)
	case action == "import":
		return r.runBundleImport(name, args[2:], output)
	case action == "purge" || action == "remove" && len(args) > 2:
		return r.runPurge(action, name, args[2:], output)
	case action != "create" && len(args) != 2:
		return errors.New(profileUsage)
	}
	if action == "remove" {
		action = "archive"
	}
	if action != "create" && action != "use" && action != "archive" && action != "restore" {
		return errors.New(profileUsage)
	}
	var from string
	if action == "create" {
		flags := flag.NewFlagSet("t3-bridge profile create NAME", flag.ContinueOnError)
		flags.SetOutput(output)
		flags.StringVar(&from, "from", "", "copy settings from a configuration JSON file")
		if err := flags.Parse(args[2:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if flags.NArg() != 0 {
			return errors.New(profileUsage)
		}
	}
	if err := ensureConfigParent(r.root); err != nil {
		return err
	}
	activePath := filepath.Join(r.root, "active-profile")
	lock, err := lockConfigFile(activePath)
	if err != nil {
		return err
	}
	defer lock.Close()

	switch action {
	case "create":
		path, err := r.create(name, from)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, path)
		return err
	case "use":
		path, err := r.existingFile(name)
		if err != nil {
			return err
		}
		if _, err := bridge.LoadConfig(path); err != nil {
			return err
		}
		if _, err := localfile.WritePrivateAtomic(activePath, []byte(name+"\n"), false); err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, path)
		return err
	case "restore":
		path, err := r.restore(name)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, path)
		return err
	default:
		archive, err := r.archive(name, activePath)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, archive)
		return err
	}
}

func (r profileRegistry) list(output io.Writer) error {
	active, err := r.active()
	if err != nil {
		return err
	}
	names, err := r.names()
	if err != nil {
		return err
	}
	for _, name := range names {
		marker := " "
		if name == active {
			marker = "*"
		}
		if _, err := fmt.Fprintf(output, "%s %s\n", marker, name); err != nil {
			return err
		}
	}
	return nil
}

func (r profileRegistry) create(name, from string) (string, error) {
	path, err := r.file(name)
	if err != nil {
		return "", err
	}
	var raw []byte
	if from == "" {
		raw, err = loadConfigTemplate("generic")
	} else {
		raw, err = bridge.ReadConfigJSON(from)
	}
	if err != nil {
		return "", err
	}
	settings, err := bridge.ParseConfig(raw)
	if err != nil {
		return "", err
	}
	candidate, err := r.profileCandidate(name, raw, settings, nil, nil)
	if err != nil {
		return "", err
	}
	if _, err := saveConfigCandidate(path, candidate, "missing"); err != nil {
		return "", err
	}
	return path, nil
}

func (r profileRegistry) profileCandidate(name string, raw []byte, settings bridge.Config, credentialFiles map[string]string, migrationEdits []json.RawMessage) ([]byte, error) {
	state := filepath.Join(r.root, "state", name+"-"+strings.ToLower(rand.Text()))
	type edit struct {
		Path  []string `json:"path"`
		Value any      `json:"value"`
	}
	edits := make([]any, 0, len(migrationEdits)+3*(1+len(settings.AdditionalAccounts)))
	for _, change := range migrationEdits {
		edits = append(edits, change)
	}
	edits = append(edits,
		edit{[]string{"stateDir"}, filepath.Join(state, "primary")},
		edit{[]string{"receiverMachine"}, name},
		edit{[]string{"receiverAuthorized"}, false})
	if file := credentialFiles[settings.AppID]; file != "" {
		edits = append(edits, edit{[]string{"appSecretFile"}, file})
	}
	for i, account := range settings.AdditionalAccounts {
		index := strconv.Itoa(i)
		edits = append(edits,
			edit{[]string{"additionalAccounts", index, "stateDir"}, filepath.Join(state, "accounts", index)},
			edit{[]string{"additionalAccounts", index, "receiverMachine"}, name + "-" + strconv.Itoa(i+2)},
			edit{[]string{"additionalAccounts", index, "receiverAuthorized"}, false})
		if file := credentialFiles[account.AppID]; file != "" {
			edits = append(edits, edit{[]string{"additionalAccounts", index, "appSecretFile"}, file})
		}
	}
	changes, err := json.Marshal(edits)
	if err != nil {
		return nil, err
	}
	return bridge.EditConfigJSON(raw, changes)
}

func (r profileRegistry) archive(name, activePath string) (string, error) {
	path, err := r.existingFile(name)
	if err != nil {
		return "", err
	}
	active, err := r.active()
	if err != nil {
		return "", err
	}
	lock, err := lockConfigFile(path)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	runtimeLock, err := lockProfileRuntime(path)
	if err != nil {
		return "", err
	}
	defer runtimeLock.Close()
	root := filepath.Join(r.root, "profile-archive")
	if err := localfile.MkdirAllPrivate(root); err != nil {
		return "", err
	}
	dest := filepath.Join(root, name+"-"+strings.ToLower(rand.Text()))
	if err := os.Rename(filepath.Dir(path), dest); err != nil {
		return "", err
	}
	if active == name {
		published, err := localfile.WritePrivateAtomic(activePath, nil, false)
		if err != nil && !published {
			return "", errors.Join(err, os.Rename(dest, filepath.Dir(path)))
		}
		if err != nil {
			return "", fmt.Errorf("profile archived at %s but could not confirm durability: %w", dest, err)
		}
	}
	return filepath.Join(dest, "config.json"), nil
}

func (r profileRegistry) restore(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	archiveDir := filepath.Dir(path)
	if filepath.Base(path) != "config.json" || filepath.Dir(archiveDir) != filepath.Join(r.root, "profile-archive") {
		return "", errors.New("choose a configuration inside this user's profile archive")
	}
	name, ok := archivedProfileName(filepath.Base(archiveDir))
	if !ok {
		return "", errors.New("invalid archived profile folder")
	}
	dirInfo, err := os.Lstat(archiveDir)
	if err != nil || !dirInfo.IsDir() {
		return "", errors.New("archived profile must be an ordinary directory")
	}
	raw, err := bridge.ReadPrivateConfigJSON(path)
	if err != nil {
		return "", err
	}
	if _, err := bridge.ParseConfig(raw); err != nil {
		return "", err
	}
	dest, err := r.file(name)
	if err != nil {
		return "", err
	}
	parent := filepath.Dir(dest)
	if _, err := os.Lstat(parent); !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("profile already exists; archive it before restoring")
	}
	return dest, os.Rename(archiveDir, parent)
}

func archivedProfileName(id string) (string, bool) {
	separator := strings.LastIndexByte(id, '-')
	if separator <= 0 || len(id[separator+1:]) < 26 || !serviceInstanceName.MatchString(id[:separator]) {
		return "", false
	}
	for _, ch := range id[separator+1:] {
		if (ch < 'a' || ch > 'z') && (ch < '2' || ch > '7') {
			return "", false
		}
	}
	return id[:separator], true
}
