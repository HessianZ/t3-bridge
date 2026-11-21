package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestRegisterSavedProjectCandidateUsesOnlySelectedRoute(t *testing.T) {
	root := t.TempDir()
	template, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := (manualAccountFields{
		AppID: "cli_project_aux", Tenant: "feishu", User: "ou_project", Route: "work",
		AppSecretFile: filepath.Join(root, "aux.secret"), StateDir: filepath.Join(root, "aux-state"),
	}).apply(template)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = bridge.EditConfigJSON(raw, []byte(`[
		{"path":["additionalAccounts","0","mcp","environmentId"],"value":"env-connection"},
		{"path":["additionalAccounts","0","routes","work","environmentId"],"value":"env-route"},
		{"path":["additionalAccounts","0","routes","work","model"],"value":"configured-model"}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "bridge.json")
	if err := os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	const targetDirectory = `C:\work\Bridge Test`
	const title, projectID = "Bridge Test", "project-fixture"
	savedConfig, err := bridge.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	savedConnection := savedConfig.AdditionalAccounts[0].MCP
	setup := filepath.Join(root, "setup-only.json")
	expectedTokenFile := setup
	calls := 0
	register := func(_ context.Context, c bridge.MCPConfig, environmentID, directory string) (bridge.Project, error) {
		t.Helper()
		calls++
		if c.EnvironmentID != "env-connection" || environmentID != "env-route" || directory != targetDirectory ||
			c.TokenFile != expectedTokenFile || c.Endpoint != savedConnection.Endpoint || c.RuntimeFile != savedConnection.RuntimeFile {
			t.Fatalf("wrong saved connection or directory: %+v %q %q", c, environmentID, directory)
		}
		return bridge.Project{ID: projectID, Root: targetDirectory, Title: title}, nil
	}
	if got, err := registerProjectDraft(context.Background(), configPath, "obsolete", "cli_project_aux", "work", targetDirectory, setup, register); got != nil || err == nil || calls != 0 {
		t.Fatal("stale source reached project registration", err)
	}
	if got, err := registerProjectDraft(context.Background(), configPath, configRevision(raw), "cli_project_aux", "work", targetDirectory, "relative.json", register); got != nil || err == nil || calls != 0 {
		t.Fatal("invalid setup path reached project registration", err)
	}
	got, err := registerProjectDraft(context.Background(), configPath, configRevision(raw), "cli_project_aux", "work", targetDirectory, setup, register)
	if err != nil || calls != 1 {
		t.Fatal("failed to produce project candidate", err)
	}
	candidate, err := bridge.ParseConfig(got)
	if err != nil {
		t.Fatal(err)
	}
	target := candidate.AdditionalAccounts[0].Routes["work"]
	if candidate.Routes["work"].ProjectID != "REPLACE_PROJECT_ID" || target.ProjectID != projectID || target.ProjectPath != targetDirectory ||
		target.ProjectName != title || target.Model != "configured-model" || target.EnvironmentID != "env-route" {
		t.Fatalf("candidate changed more than the selected project: %+v", target)
	}
	if candidate.AdditionalAccounts[0].MCP != savedConnection || bytes.Contains(got, []byte("setup-only.json")) ||
		target.RuntimeMode != savedConfig.AdditionalAccounts[0].Routes["work"].RuntimeMode {
		t.Fatal("setup request changed the saved connection or runtime mode")
	}
	if saved, err := os.ReadFile(configPath); err != nil || !bytes.Equal(saved, raw) {
		t.Fatal("registration mutated saved JSON", err)
	}
	expectedTokenFile = savedConnection.TokenFile
	changed := func(ctx context.Context, c bridge.MCPConfig, environmentID, directory string) (bridge.Project, error) {
		if err := os.WriteFile(configPath, append(bytes.Clone(raw), '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		return register(ctx, c, environmentID, directory)
	}
	if got, err := registerProjectDraft(context.Background(), configPath, configRevision(raw), "cli_project_aux", "", targetDirectory, "", changed); got != nil || err == nil ||
		!strings.Contains(err.Error(), "T3 项目已核对") {
		t.Fatal("configuration drift returned a stale project candidate", err)
	}
}
