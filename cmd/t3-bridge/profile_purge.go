package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

type profilePurgePlan struct {
	Name, ProfileDir, DefaultAfter, Revision string
	Paths, Retained                          []string
}

func (r profileRegistry) previewPurge(name string) (profilePurgePlan, error) {
	path, err := r.purgeConfigPath(name)
	if err != nil {
		return profilePurgePlan{}, err
	}
	cfg, err := readPurgeConfig(path)
	if err != nil {
		return profilePurgePlan{}, err
	}
	others, err := r.purgeOtherConfigs(path)
	if err != nil {
		return profilePurgePlan{}, err
	}
	plan, _, err := r.purgePlan(name, path, cfg, others)
	return plan, err
}

func (r profileRegistry) applyPurge(name, revision string) error {
	if revision == "" {
		return errors.New("preview profile purge before confirming")
	}
	path, err := r.purgeConfigPath(name)
	if err != nil {
		return err
	}
	activePath := filepath.Join(r.root, "active-profile")
	registryLock, err := lockConfigFile(activePath)
	if err != nil {
		return err
	}
	defer registryLock.Close()
	configLock, err := lockConfigFile(path)
	if err != nil {
		return err
	}
	defer configLock.Close()
	runtimeLock, err := lockProfileRuntime(path)
	if err != nil {
		return err
	}
	defer runtimeLock.Close()

	cfg, err := readPurgeConfig(path)
	if err != nil {
		return err
	}
	if _, err := r.purgeDirectory(filepath.Join(r.root, "receiver-locks")); err != nil {
		return err
	}
	release, err := acquireReceiverLocks(r, cfg.Accounts())
	if err != nil {
		return err
	}
	defer release()

	others, err := r.purgeOtherConfigs(path)
	if err != nil {
		return err
	}
	// Regular saves of active configurations use their own locks rather than the registry lock.
	for _, other := range others {
		if filepath.Dir(filepath.Dir(other)) != filepath.Dir(filepath.Dir(path)) {
			continue
		}
		lock, err := lockConfigFile(other)
		if err != nil {
			return err
		}
		defer lock.Close()
	}
	plan, active, err := r.purgePlan(name, path, cfg, others)
	if err != nil {
		return err
	}
	if plan.Revision != revision {
		return errors.New("profile purge preview changed; refresh before confirming")
	}
	// Offline store commands use the in-directory lock. Check it before staging.
	// Close it before rename, since an open lock file prevents rename on Windows.
	for _, stateDir := range plan.Paths[1:] {
		lockPath := filepath.Join(stateDir, "lock")
		if _, err := os.Lstat(lockPath); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		lock, err := localfile.Lock(lockPath, false)
		if err != nil {
			return fmt.Errorf("state %s must be closed before purging: %w", stateDir, err)
		}
		if err := lock.Close(); err != nil {
			return err
		}
	}
	return r.commitPurge(plan, active == name)
}

func (r profileRegistry) purgeConfigPath(name string) (string, error) {
	if !filepath.IsAbs(r.root) || filepath.Dir(r.root) == r.root {
		return "", errors.New("invalid profile registry directory")
	}
	if _, err := r.purgeDirectory(filepath.Join(r.root, "profiles")); err != nil {
		return "", err
	}
	path, err := r.existingFile(name)
	if err != nil {
		return "", err
	}
	if err := localfile.CheckPrivateDir(filepath.Dir(path)); err != nil {
		return "", err
	}
	return path, nil
}

// Check the registry and each managed ancestor without following links.
func (r profileRegistry) purgeDirectory(path string) (bool, error) {
	relative, err := filepath.Rel(r.root, path)
	if err != nil || !filepath.IsLocal(relative) {
		return false, fmt.Errorf("directory outside the profile registry: %s", path)
	}
	current := r.root
	for _, part := range append([]string{"."}, strings.Split(relative, string(filepath.Separator))...) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !info.IsDir() {
			return false, fmt.Errorf("expected an ordinary directory at %s", current)
		}
	}
	return true, nil
}

func readPurgeConfig(path string) (bridge.Config, error) {
	raw, err := bridge.ReadPrivateConfigJSON(path)
	if err != nil {
		return bridge.Config{}, fmt.Errorf("read purge configuration %s: %w", path, err)
	}
	cfg, err := bridge.ParseConfig(raw)
	if err != nil {
		return bridge.Config{}, fmt.Errorf("invalid purge configuration %s: %w", path, err)
	}
	return cfg, nil
}

func (r profileRegistry) purgeOtherConfigs(target string) ([]string, error) {
	var paths []string
	for _, group := range []string{"profiles", "profile-archive"} {
		parent := filepath.Join(r.root, group)
		exists, err := r.purgeDirectory(parent)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		entries, err := os.ReadDir(parent)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			valid := serviceInstanceName.MatchString(entry.Name())
			if group == "profile-archive" {
				_, valid = archivedProfileName(entry.Name())
			}
			path := filepath.Join(parent, entry.Name(), "config.json")
			if !valid || path == target {
				continue
			}
			if !entry.IsDir() {
				return nil, fmt.Errorf("expected an ordinary profile directory at %s", filepath.Dir(path))
			}
			if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return nil, err
			}
			paths = append(paths, path)
		}
	}
	return paths, nil
}

func (r profileRegistry) purgePlan(name, path string, cfg bridge.Config, others []string) (profilePurgePlan, string, error) {
	active, err := r.active()
	if err != nil {
		return profilePurgePlan{}, "", err
	}
	plan := profilePurgePlan{Name: name, ProfileDir: filepath.Dir(path), DefaultAfter: active}
	plan.Paths = []string{plan.ProfileDir}
	accounts := cfg.Accounts()
	var candidates, protected []string
	for index, account := range accounts {
		state := filepath.Clean(account.StateDir)
		if r.generatedPurgeState(name, account.StateDir) {
			exists, err := r.purgeDirectory(state)
			if err != nil {
				return profilePurgePlan{}, "", err
			}
			if exists {
				if err := localfile.CheckPrivateDir(state); err != nil {
					return profilePurgePlan{}, "", err
				}
				candidates = append(candidates, state)
			}
		} else {
			plan.Retained = append(plan.Retained, state)
		}
		// A bundle import creates these private credentials inside its own profile.
		if account.AppSecretFile == profileCredentialPath(plan.ProfileDir, index) {
			account.AppSecretFile = ""
		}
		plan.Retained = append(plan.Retained, purgeConfigReferences(account)...)
	}
	protected = append(protected, plan.Retained...)
	if active == name {
		plan.DefaultAfter = ""
	}
	for _, other := range others {
		for _, configPath := range []string{other, other + ".backup.json"} {
			if configPath != other {
				if _, err := os.Lstat(configPath); errors.Is(err, os.ErrNotExist) {
					continue
				} else if err != nil {
					return profilePurgePlan{}, "", err
				}
			}
			raw, err := bridge.ReadPrivateConfigJSON(configPath)
			if err != nil {
				return profilePurgePlan{}, "", fmt.Errorf("verify retained profile %s: %w", configPath, err)
			}
			otherCfg, parseErr := bridge.ParseConfig(raw)
			if parseErr != nil {
				// A draft may fail validation yet still contain paths worth protecting.
				_ = json.Unmarshal(raw, &otherCfg)
			}
			if active == name && plan.DefaultAfter == "" && configPath == other && parseErr == nil &&
				filepath.Dir(filepath.Dir(other)) == filepath.Dir(plan.ProfileDir) {
				plan.DefaultAfter = filepath.Base(filepath.Dir(other))
			}
			for _, account := range otherCfg.Accounts() {
				protected = append(protected, account.StateDir)
				protected = append(protected, purgeConfigReferences(account)...)
			}
		}
	}

	for _, ref := range protected {
		if purgePathsOverlap(plan.ProfileDir, ref) {
			return profilePurgePlan{}, "", fmt.Errorf("cannot purge %s while referenced data at %s must be retained", plan.ProfileDir, ref)
		}
	}
	for _, state := range candidates {
		if slices.ContainsFunc(protected, func(ref string) bool { return purgePathsOverlap(state, ref) }) {
			plan.Retained = append(plan.Retained, state)
		} else {
			plan.Paths = append(plan.Paths, state)
		}
	}
	slices.Sort(plan.Retained)
	plan.Retained = slices.Compact(plan.Retained)
	plan.Revision, err = r.purgeRevision(plan, active)
	return plan, active, err
}

func (r profileRegistry) generatedPurgeState(name, path string) bool {
	relative, err := filepath.Rel(filepath.Join(r.root, "state"), path)
	if err != nil || !filepath.IsLocal(relative) || path != filepath.Clean(path) {
		return false
	}
	instance, leaf, ok := strings.Cut(relative, string(filepath.Separator))
	owner, owned := archivedProfileName(instance)
	if !ok || !owned || owner != name {
		return false
	}
	if leaf == "primary" {
		return true
	}
	slot, ok := strings.CutPrefix(leaf, "accounts"+string(filepath.Separator))
	index, err := strconv.Atoi(slot)
	return ok && err == nil && index >= 0 && slot == strconv.Itoa(index)
}

// Include credential and integration paths that can point into a profile or state tree.
func purgeConfigReferences(c bridge.Config) []string {
	refs := []string{c.AppSecretFile, c.MCP.TokenFile, c.MCP.RuntimeFile, c.Classifier.KeyFile,
		c.AgentReportsFile, c.InvestigationDB, c.ReportCommand, c.CoordinationCommand, c.FollowupFlush.Command, c.FollowupFlush.ConfigFile}
	for _, source := range c.Health {
		refs = append(refs, source.KeyFile)
	}
	for _, source := range c.ReportSources {
		refs = append(refs, source.ConfigFile, source.Command)
	}
	for _, path := range c.ReportOutputs {
		refs = append(refs, path)
	}
	for _, path := range c.ReportCopyCommands {
		refs = append(refs, path)
	}
	for _, route := range c.Routes {
		refs = append(refs, route.ProjectPath)
		if tool := route.ToolContext; tool != nil {
			refs = append(refs, tool.CLIPath)
			if tool.BotBinding != nil {
				refs = append(refs, tool.BotBinding.ConfigDir)
			}
			if tool.UserBinding != nil {
				refs = append(refs, tool.UserBinding.ConfigDir)
			}
		}
	}
	return slices.DeleteFunc(refs, func(ref string) bool { return !filepath.IsAbs(ref) })
}

func purgePathsOverlap(path, ref string) bool {
	if !filepath.IsAbs(ref) {
		return false
	}
	overlap := func(a, b string) bool {
		// Default macOS and Windows filesystems can resolve differently cased paths to the same entry.
		if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
			a, b = strings.ToLower(a), strings.ToLower(b)
		}
		fromA, errA := filepath.Rel(a, b)
		fromB, errB := filepath.Rel(b, a)
		return errA == nil && filepath.IsLocal(fromA) || errB == nil && filepath.IsLocal(fromB)
	}
	if overlap(path, ref) {
		return true
	}
	resolvedPath, resolvedRef := credentialPath(path), credentialPath(ref)
	return resolvedPath != "" && resolvedRef != "" && overlap(resolvedPath, resolvedRef)
}

func (r profileRegistry) purgeRevision(plan profilePurgePlan, active string) (string, error) {
	root, err := os.OpenRoot(r.root)
	if err != nil {
		return "", err
	}
	defer root.Close()
	revision := sha256.New()
	fmt.Fprintf(revision, "%q %q %q %q %q\n", plan.Name, active, plan.DefaultAfter, plan.Paths, plan.Retained)
	for _, path := range plan.Paths {
		relative, err := filepath.Rel(r.root, path)
		if err != nil {
			return "", err
		}
		err = fs.WalkDir(root.FS(), filepath.ToSlash(relative), func(entryPath string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			fmt.Fprintf(revision, "%q %d %d %d\n", entryPath, info.Mode(), info.Size(), info.ModTime().UnixNano())
			if info.IsDir() {
				return nil
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("cannot purge a symbolic link or special file at %s", filepath.Join(r.root, filepath.FromSlash(entryPath)))
			}
			file, err := root.Open(filepath.FromSlash(entryPath))
			if err != nil {
				return err
			}
			content := sha256.New()
			_, err = io.Copy(content, file)
			fmt.Fprintf(revision, "%x\n", content.Sum(nil))
			return errors.Join(err, file.Close())
		})
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(revision.Sum(nil)), nil
}

func (r profileRegistry) commitPurge(plan profilePurgePlan, replaceDefault bool) error {
	root, err := os.OpenRoot(r.root)
	if err != nil {
		return err
	}
	defer root.Close()
	type move struct{ original, staged string }
	var moves []move
	suffix := ".purge-" + strings.ToLower(rand.Text())
	rollback := func(cause error) error {
		for i := len(moves) - 1; i >= 0; i-- {
			item := moves[i]
			if err := root.Rename(item.staged, item.original); err != nil {
				cause = errors.Join(cause, fmt.Errorf("restore %s from %s: %w", filepath.Join(r.root, item.original), filepath.Join(r.root, item.staged), err))
			}
		}
		return cause
	}
	for _, path := range plan.Paths {
		relative, err := filepath.Rel(r.root, path)
		if err != nil {
			return rollback(err)
		}
		staged := filepath.Join(filepath.Dir(relative), "."+filepath.Base(relative)+suffix)
		if err := root.Rename(relative, staged); err != nil {
			return rollback(fmt.Errorf("stage %s for purge: %w", path, err))
		}
		moves = append(moves, move{relative, staged})
	}
	if replaceDefault {
		var data []byte
		if plan.DefaultAfter != "" {
			data = []byte(plan.DefaultAfter + "\n")
		}
		published, err := localfile.WritePrivateAtomic(filepath.Join(r.root, "active-profile"), data, false)
		if err != nil && !published {
			return rollback(fmt.Errorf("update the default profile: %w", err))
		}
		if err != nil {
			locations := make([]string, 0, len(moves))
			for _, item := range moves {
				locations = append(locations, filepath.Join(r.root, item.staged))
			}
			return fmt.Errorf("default profile was updated but durability could not be confirmed; staged paths retained at %q: %w", locations, err)
		}
	}
	var cleanupErr error
	for _, item := range moves {
		if err := root.RemoveAll(item.staged); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("profile removed from registry; check remaining files at %s: %w", filepath.Join(r.root, item.staged), err))
		}
	}
	return cleanupErr
}
