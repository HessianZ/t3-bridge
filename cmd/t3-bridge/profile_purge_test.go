package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestProfilePurgeRemovesOwnedStateAndReplacesDefault(t *testing.T) {
	r := profileRegistry{root: t.TempDir()}
	path, cfg := createPurgeFixture(t, r, "work", 2)
	invalid, _ := createPurgeFixture(t, r, "broken", 0)
	if _, err := localfile.WritePrivateAtomic(invalid, []byte("{}"), false); err != nil {
		t.Fatal(err)
	}
	createPurgeFixture(t, r, "next", 0)
	createPurgeFixture(t, r, "other", 0)
	if err := r.run([]string{"use", "work"}, io.Discard); err != nil {
		t.Fatal(err)
	}

	custom := filepath.Join(r.root, "custom-state")
	cfg.AdditionalAccounts[1].StateDir = custom
	savePurgeFixture(t, path, cfg)
	owned := []string{cfg.StateDir, cfg.AdditionalAccounts[0].StateDir}
	for _, dir := range append(slices.Clone(owned), custom) {
		if err := localfile.MkdirAllPrivate(dir); err != nil {
			t.Fatal(err)
		}
		writePurgeFixture(t, filepath.Join(dir, "state.json"), "synthetic state")
	}
	writePurgeFixture(t, cfg.AppSecretFile, "synthetic secret")

	plan, err := r.previewPurge("work")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Name != "work" || plan.ProfileDir != filepath.Dir(path) || plan.DefaultAfter != "next" ||
		!slices.Equal(plan.Paths, append([]string{plan.ProfileDir}, owned...)) ||
		!slices.Contains(plan.Retained, custom) || !slices.Contains(plan.Retained, cfg.AppSecretFile) ||
		!slices.Contains(plan.Retained, cfg.MCP.TokenFile) {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	if err := r.applyPurge("work", plan.Revision); err != nil {
		t.Fatal(err)
	}
	for _, dir := range plan.Paths {
		if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("planned path remains at %s: %v", dir, err)
		}
	}
	for _, keep := range []string{custom, cfg.AppSecretFile} {
		if _, err := os.Lstat(keep); err != nil {
			t.Fatalf("retained path %s: %v", keep, err)
		}
	}
	if active, err := r.active(); err != nil || active != "next" {
		t.Fatalf("default = %q, %v", active, err)
	}
}

func TestProfilePurgePreservesSharedStateAndProfileReferences(t *testing.T) {
	r := profileRegistry{root: t.TempDir()}
	target, cfg := createPurgeFixture(t, r, "work", 1)
	other, otherCfg := createPurgeFixture(t, r, "other", 0)
	archived, archivedCfg := createPurgeFixture(t, r, "archived", 0)
	for _, account := range cfg.Accounts() {
		if err := localfile.MkdirAllPrivate(account.StateDir); err != nil {
			t.Fatal(err)
		}
	}
	otherCfg.StateDir = filepath.Join(cfg.StateDir, "child")
	savePurgeFixture(t, other, otherCfg)
	archivedCfg.StateDir = filepath.Dir(cfg.AdditionalAccounts[0].StateDir)
	savePurgeFixture(t, archived, archivedCfg)
	if err := r.run([]string{"archive", "archived"}, io.Discard); err != nil {
		t.Fatal(err)
	}

	plan, err := r.previewPurge("work")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Paths, []string{filepath.Dir(target)}) ||
		!slices.Contains(plan.Retained, cfg.StateDir) || !slices.Contains(plan.Retained, cfg.AdditionalAccounts[0].StateDir) {
		t.Fatalf("shared state was not retained: %+v", plan)
	}

	// The overlapping credential reference was added after the preview.
	otherCfg.AppSecretFile = filepath.Join(filepath.Dir(target), "shared.secret")
	writePurgeFixture(t, otherCfg.AppSecretFile, "synthetic secret")
	savePurgeFixture(t, other, otherCfg)
	if err := r.applyPurge("work", plan.Revision); err == nil || !strings.Contains(err.Error(), otherCfg.AppSecretFile) {
		t.Fatalf("purge must report the referenced profile data: %v", err)
	}
	for _, keep := range []string{target, otherCfg.AppSecretFile, cfg.StateDir, cfg.AdditionalAccounts[0].StateDir} {
		if _, err := os.Lstat(keep); err != nil {
			t.Fatalf("retained path %s: %v", keep, err)
		}
	}
}

func TestProfilePurgeRechecksContentsAndRunningLocks(t *testing.T) {
	r := profileRegistry{root: t.TempDir()}
	path, cfg := createPurgeFixture(t, r, "work", 0)
	if err := r.run([]string{"use", "work"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := localfile.MkdirAllPrivate(cfg.StateDir); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(cfg.StateDir, "state.json")
	writePurgeFixture(t, state, "old")
	storeLock, err := localfile.Lock(filepath.Join(cfg.StateDir, "lock"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer storeLock.Close()
	plan, err := r.previewPurge("work")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.applyPurge("work", plan.Revision); err == nil || !errors.Is(err, localfile.ErrLocked) {
		t.Fatalf("purge acquired an open store: %v", err)
	}
	if err := storeLock.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(state, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := r.applyPurge("work", plan.Revision); err == nil || !strings.Contains(err.Error(), "refresh") {
		t.Fatalf("purge accepted a stale preview: %v", err)
	}
	plan, err = r.previewPurge("work")
	if err != nil {
		t.Fatal(err)
	}
	running, err := lockProfileRuntime(path)
	if err != nil {
		t.Fatal(err)
	}
	defer running.Close()
	if err := r.applyPurge("work", plan.Revision); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("purge accepted a running named receiver: %v", err)
	}
	if err := running.Close(); err != nil {
		t.Fatal(err)
	}
	release, err := acquireReceiverLocks(r, cfg.Accounts())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := r.applyPurge("work", plan.Revision); err == nil || !strings.Contains(err.Error(), "already has a receiver") {
		t.Fatalf("purge accepted a running receiver with an explicit config: %v", err)
	}
	release()
	if err := r.applyPurge("work", plan.Revision); err != nil {
		t.Fatal(err)
	}
	if active, err := r.active(); err != nil || active != "" {
		t.Fatalf("default = %q, %v", active, err)
	}
}

func TestProfilePurgeRejectsSymlinkedStateAncestor(t *testing.T) {
	r := profileRegistry{root: t.TempDir()}
	path, cfg := createPurgeFixture(t, r, "work", 0)
	foreign := t.TempDir()
	if err := os.Symlink(foreign, filepath.Join(r.root, "state")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(foreign, filepath.Base(filepath.Dir(cfg.StateDir)), "primary"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := r.previewPurge("work"); err == nil {
		t.Fatal("purge preview accepted a symbolic link inside the registry")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestProfilePurgeRestoresStagedPathsBeforeFailedPublication(t *testing.T) {
	r := profileRegistry{root: t.TempDir()}
	path, cfg := createPurgeFixture(t, r, "work", 0)
	if err := localfile.MkdirAllPrivate(cfg.StateDir); err != nil {
		t.Fatal(err)
	}
	if err := localfile.MkdirAllPrivate(filepath.Join(r.root, "active-profile")); err != nil {
		t.Fatal(err)
	}
	profileDir := filepath.Dir(path)
	plan := profilePurgePlan{ProfileDir: profileDir, Paths: []string{profileDir, cfg.StateDir}}
	if err := r.commitPurge(plan, true); err == nil {
		t.Fatal("purge replaced a directory at the default profile path")
	}
	for _, keep := range []string{path, cfg.StateDir} {
		if _, err := os.Lstat(keep); err != nil {
			t.Fatalf("staged path was not restored at %s: %v", keep, err)
		}
	}
}

func createPurgeFixture(t *testing.T, r profileRegistry, name string, additional int) (string, bridge.Config) {
	t.Helper()
	raw, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := bridge.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AppID = "cli_" + name
	cfg.AppSecretFile = filepath.Join(r.root, "app.secret")
	cfg.MCP.TokenFile = filepath.Join(r.root, "t3.token")
	for i := 0; i < additional; i++ {
		account := cfg
		account.AdditionalAccounts = nil
		account.AppID += "_" + strconv.Itoa(i)
		account.StateDir = filepath.Join(r.root, "source-state-"+strconv.Itoa(i))
		cfg.AdditionalAccounts = append(cfg.AdditionalAccounts, account)
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "source.json")
	writePurgeFixture(t, source, string(body))
	path, err := r.create(name, source)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err = bridge.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, cfg
}

func savePurgeFixture(t *testing.T, path string, cfg bridge.Config) {
	t.Helper()
	raw, err := bridge.ReadPrivateConfigJSON(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := saveConfigCandidate(path, data, configRevision(raw)); err != nil {
		t.Fatal(err)
	}
}

func writePurgeFixture(t *testing.T, path, body string) {
	t.Helper()
	if _, err := localfile.WritePrivateAtomic(path, []byte(body), true); err != nil {
		t.Fatal(err)
	}
}
