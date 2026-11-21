package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestLegacyBridgeExecutableExplicit(t *testing.T) {
	dir := t.TempDir()
	name := "explicit provider"
	if runtime.GOOS == "windows" {
		name += ".mjs"
	}
	provider := writeLegacyExecutableFixture(t, filepath.Join(dir, name), 0o700)
	wantArgs := []string{provider, "secrets", "get"}
	if runtime.GOOS == "windows" {
		node := writeLegacyExecutableFixture(t, filepath.Join(dir, "node.exe"), 0o700)
		wantArgs = []string{node, provider, "secrets", "get"}
	}
	t.Setenv("PATH", "")
	if got, err := legacyBridgeExecutable(provider); err != nil || got != provider {
		t.Fatalf("legacyBridgeExecutable = %q, %v, want %q", got, err, provider)
	}
	cmd, err := legacyCredentialCommand(context.Background(), provider)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Err != nil || cmd.Path != wantArgs[0] || !slices.Equal(cmd.Args, wantArgs) {
		t.Fatalf("command path %q, args %q, error %v, want %q", cmd.Path, cmd.Args, cmd.Err, wantArgs)
	}

	invalid := []string{filepath.Base(provider), filepath.Join(dir, "missing"), dir}
	if runtime.GOOS != "windows" {
		invalid = append(invalid, writeLegacyExecutableFixture(t, filepath.Join(dir, "non-executable"), 0o600))
	}
	for _, path := range invalid {
		if got, err := legacyBridgeExecutable(path); err == nil || got != "" {
			t.Errorf("legacyBridgeExecutable(%q) = %q, %v, want error", path, got, err)
		}
	}
}

func TestLegacyBridgeExecutablePath(t *testing.T) {
	cwd := t.TempDir()
	pathDir := t.TempDir()
	t.Chdir(cwd)
	name := legacyBridgeName
	if runtime.GOOS == "windows" {
		name += ".cmd"
		for _, dir := range []string{filepath.Join(cwd, "relative"), pathDir} {
			writeLegacyExecutableFixture(t, filepath.Join(dir, "node.exe"), 0o700)
			writeLegacyExecutableFixture(t, filepath.Join(dir, "node_modules", legacyBridgeName, "bin", legacyBridgeName+".mjs"), 0o600)
		}
	}
	writeLegacyExecutableFixture(t, filepath.Join(cwd, "relative", name), 0o700)
	want := writeLegacyExecutableFixture(t, filepath.Join(pathDir, name), 0o700)
	t.Setenv("PATH", strings.Join([]string{"relative", pathDir}, string(os.PathListSeparator)))
	if got, err := legacyBridgeExecutable(""); err != nil || got != want {
		t.Fatalf("legacyBridgeExecutable = %q, %v, want %q", got, err, want)
	}
	t.Setenv("PATH", "relative")
	if got, err := legacyBridgeExecutable(""); err != nil || got != "" {
		t.Fatalf("legacyBridgeExecutable with relative PATH = %q, %v, want empty result", got, err)
	}
}

func TestLegacyCredentialArgsWindowsNPM(t *testing.T) {
	for _, local := range []bool{false, true} {
		name := "global"
		if local {
			name = "local"
		}
		t.Run(name, func(t *testing.T) {
			prefix := filepath.Join(t.TempDir(), "prefix with spaces & ampersand")
			modules := filepath.Join(prefix, "node_modules")
			shimDir := prefix
			if local {
				shimDir = filepath.Join(modules, ".bin")
			}
			shim := writeLegacyExecutableFixture(t, filepath.Join(shimDir, legacyBridgeName+".cmd"), 0o600)
			pathDir := t.TempDir()
			t.Setenv("PATH", pathDir)
			pathNode := writeLegacyExecutableFixture(t, filepath.Join(pathDir, "node.exe"), 0o700)
			if _, err := legacyCredentialArgs(shim, true); err == nil {
				t.Fatal("accepted a shim without its package entry")
			}
			entry := writeLegacyExecutableFixture(t, filepath.Join(modules, legacyBridgeName, "bin", legacyBridgeName+".mjs"), 0o600)
			want := []string{pathNode, entry, "secrets", "get"}
			if got, err := legacyCredentialArgs(shim, true); err != nil || !slices.Equal(got, want) {
				t.Fatalf("args = %q, %v, want %q", got, err, want)
			}
			want[0] = writeLegacyExecutableFixture(t, filepath.Join(shimDir, "node.exe"), 0o700)
			if got, err := legacyCredentialArgs(shim, true); err != nil || !slices.Equal(got, want) {
				t.Fatalf("args with adjacent Node = %q, %v, want %q", got, err, want)
			}
		})
	}
}

func TestLegacyCredentialArgsWindowsRejectsUnavailableLaunchers(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", "")
	for _, name := range []string{"entry.mjs", "custom-wrapper.cmd"} {
		path := writeLegacyExecutableFixture(t, filepath.Join(dir, name), 0o600)
		if got, err := legacyCredentialArgs(path, true); err == nil || got != nil {
			t.Errorf("legacyCredentialArgs(%q) = %q, %v, want error", path, got, err)
		}
	}
}

func TestLegacyCredentialArgsWindowsExecutable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", "")
	path := writeLegacyExecutableFixture(t, filepath.Join(dir, "bridge.exe"), 0o700)
	want := []string{path, "secrets", "get"}
	if got, err := legacyCredentialArgs(path, true); err != nil || !slices.Equal(got, want) {
		t.Fatalf("args = %q, %v, want %q", got, err, want)
	}
}

func writeLegacyExecutableFixture(t *testing.T, path string, mode os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture\n"), mode); err != nil {
		t.Fatal(err)
	}
	return path
}
