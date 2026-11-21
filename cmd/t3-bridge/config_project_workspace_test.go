package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestRegisterManagedWorkspaceCandidateAndRetry(t *testing.T) {
	registry := profileRegistry{root: filepath.Join(t.TempDir(), "t3-bridge")}
	raw, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	raw, err = bridge.EditConfigJSON(raw, []byte(`[
		{"path":["appId"],"value":"cli_workspace"},
		{"path":["mcp","environmentId"],"value":"env-local"},
		{"path":["routes","work","environmentId"],"value":"env-local"}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "saved", "bridge.json")
	if err := localfile.MkdirAllPrivate(filepath.Dir(configPath)); err != nil {
		t.Fatal(err)
	}
	if _, err := localfile.WritePrivateAtomic(configPath, raw, true); err != nil {
		t.Fatal(err)
	}
	account, err := bridge.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	wantDir, err := registry.managedProjectWorkspace(configPath, account, "work")
	if err != nil || !filepath.IsAbs(wantDir) || filepath.Dir(wantDir) != filepath.Join(registry.root, "workspaces") {
		t.Fatalf("workspace preview: %q, %v", wantDir, err)
	}
	calls := 0
	marker := filepath.Join(wantDir, "keep.md")
	unknown := errors.New("synthetic unknown registration receipt")
	register := func(_ context.Context, c bridge.MCPConfig, environment, directory string) (bridge.Project, error) {
		t.Helper()
		calls++
		if c.EnvironmentID != "env-local" || environment != "env-local" || directory != wantDir {
			t.Fatalf("unexpected target %q or workspace %q", environment, directory)
		}
		if err := localfile.CheckPrivateDir(directory); err != nil {
			t.Fatalf("registration called with no private directory: %v", err)
		}
		if next, err := prepareManagedProjectWorkspace(directory); err == nil {
			next.Close()
			t.Fatal("a second registration acquired the workspace lock")
		}
		if calls == 1 {
			if err := os.WriteFile(marker, []byte("a file written in the workspace"), 0600); err != nil {
				t.Fatal(err)
			}
			return bridge.Project{}, unknown
		}
		return bridge.Project{ID: "project-workspace", Title: "New workspace", Root: directory}, nil
	}
	run := func(revision string) ([]byte, error) {
		return registerProjectDraftWithWorkspace(t.Context(), configPath, revision, "", "", "", "", register, registry.managedProjectWorkspace)
	}
	if candidate, err := run("stale"); err == nil || candidate != nil || calls != 0 {
		t.Fatalf("stale request: calls=%d err=%v", calls, err)
	}
	if _, err := os.Lstat(registry.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preview or stale request touched user workspace directory: %v", err)
	}
	if candidate, err := run(configRevision(raw)); !errors.Is(err, unknown) || candidate != nil || calls != 1 {
		t.Fatalf("unknown receipt: calls=%d err=%v", calls, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("workspace data was lost after failure: %v", err)
	}
	got, err := run(configRevision(raw))
	if err != nil || calls != 2 {
		t.Fatalf("retry: calls=%d err=%v", calls, err)
	}
	candidate, err := bridge.ParseConfig(got)
	if err != nil {
		t.Fatal(err)
	}
	target := candidate.Routes["work"]
	if target.ProjectID != "project-workspace" || target.ProjectPath != wantDir || target.Model != account.Routes["work"].Model ||
		target.RuntimeMode != account.Routes["work"].RuntimeMode || candidate.ReceiverAuthorized {
		t.Fatalf("unexpected candidate route %+v", target)
	}
	if saved, err := os.ReadFile(configPath); err != nil || !bytes.Equal(saved, raw) {
		t.Fatalf("registration modified saved config: %v", err)
	}
}

func TestManagedWorkspaceIsLocalAndBoundToSavedScope(t *testing.T) {
	root := t.TempDir()
	registry := profileRegistry{root: filepath.Join(root, "t3-bridge")}
	configPath := filepath.Join(root, "bridge.json")
	otherConfigPath := filepath.Join(root, "other.json")
	for _, path := range []string{configPath, otherConfigPath} {
		if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	account := bridge.Config{
		AppID: "cli_workspace", Tenant: "feishu",
		MCP: bridge.MCPConfig{EnvironmentID: "env-local", RuntimeFile: filepath.Join(root, "runtime.json")},
		Routes: map[string]bridge.Target{
			"work":   {EnvironmentID: "env-local"},
			"other":  {EnvironmentID: "env-local"},
			"linked": {EnvironmentID: "env-linked"},
		},
	}
	first, err := registry.managedProjectWorkspace(configPath, account, "work")
	if err != nil {
		t.Fatal(err)
	}
	otherApp := account
	otherApp.AppID = "cli_other"
	for _, scope := range []struct {
		configPath, routeID string
		account             bridge.Config
	}{
		{configPath, "work", otherApp},
		{otherConfigPath, "work", account},
		{configPath, "other", account},
	} {
		if got, err := registry.managedProjectWorkspace(scope.configPath, scope.account, scope.routeID); err != nil || got == first {
			t.Fatalf("independent workspace: %q, %v", got, err)
		}
	}
	if _, err := registry.managedProjectWorkspace(configPath, account, "linked"); err == nil {
		t.Fatal("linked target accepted a local workspace")
	}
	account.MCP.RuntimeFile, account.MCP.Endpoint = "", "https://t3.example.invalid/mcp"
	if _, err := registry.managedProjectWorkspace(configPath, account, "work"); err == nil {
		t.Fatal("HTTPS remote target accepted a local workspace")
	}
	if _, err := os.Lstat(registry.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace preview created a directory: %v", err)
	}
}

func TestRegisterProjectRequiresOneDirectoryMode(t *testing.T) {
	for _, args := range [][]string{nil, {"-directory", "/existing", "-managed"}} {
		err := runConfigRegisterProject(args, nil, io.Discard)
		if err == nil || err.Error() != "config register-project requires exactly one of -directory or -managed and accepts no positional arguments" {
			t.Fatalf("ambiguous mode %v: %v", args, err)
		}
	}
}

func TestRegisterManagedProjectBeforeFirstConfigSave(t *testing.T) {
	root := t.TempDir()
	registry := profileRegistry{root: filepath.Join(root, "t3-bridge")}
	path := filepath.Join(root, "new", "subdir", "bridge.json")
	base, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	draft, err := bridge.EditConfigJSON(base, []byte(`[
		{"path":["appId"],"value":"cli_first_project"},
		{"path":["receiverMachine"],"value":"keep-draft"},
		{"path":["mcp","environmentId"],"value":"env-local"},
		{"path":["routes","work","environmentId"],"value":"env-local"}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	account, err := bridge.ParseConfig(draft)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := registry.managedProjectWorkspace(path, account, "work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(registry.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preview created a workspace: %v", err)
	}
	calls := 0
	setupFile := filepath.Join(root, "setup.json")
	register := func(_ context.Context, c bridge.MCPConfig, environment, gotDir string) (bridge.Project, error) {
		calls++
		if gotDir != directory || c.TokenFile != setupFile || environment != "env-local" {
			t.Fatalf("wrong project registration target: %s %s", gotDir, environment)
		}
		return bridge.Project{ID: "new-project", Root: directory, Title: "First workspace"}, nil
	}
	run := func(file string, register func(context.Context, bridge.MCPConfig, string, string) (bridge.Project, error)) ([]byte, error) {
		return registerProjectCandidate(t.Context(), file, "missing", draft, "cli_first_project", "work", "", setupFile, register, registry.managedProjectWorkspace)
	}
	candidate, err := run(path, register)
	if err != nil || calls != 1 {
		t.Fatalf("registration before save: calls=%d, err=%v", calls, err)
	}
	if _, err := os.Lstat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("registration created the config directory: %v", err)
	}
	updated, err := bridge.ParseConfig(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Routes["work"].ProjectID != "new-project" || updated.Routes["work"].ProjectPath != directory ||
		updated.ReceiverMachine != "keep-draft" || updated.MCP != account.MCP || updated.ReceiverAuthorized {
		t.Fatal("project registration changed other draft settings")
	}
	if _, err := saveConfigCandidate(path, candidate, "missing"); err != nil {
		t.Fatal(err)
	}
	if after, err := registry.managedProjectWorkspace(path, updated, "work"); err != nil || after != directory {
		t.Fatalf("workspace identity changed after first save: %s, %v", after, err)
	}
	if stale, err := run(path, register); err == nil || stale != nil || calls != 1 {
		t.Fatal("stale missing revision reached project registration")
	}
	latePath := filepath.Join(root, "appeared.json")
	if stale, err := run(latePath, func(context.Context, bridge.MCPConfig, string, string) (bridge.Project, error) {
		if _, err := saveConfigCandidate(latePath, draft, "missing"); err != nil {
			t.Fatal(err)
		}
		return bridge.Project{ID: "registered-during-save", Root: directory, Title: "Race fixture"}, nil
	}); err == nil || stale != nil {
		t.Fatal("registration displayed a candidate after a competing save")
	}
}
