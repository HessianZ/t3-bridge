package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func exportProfileFixture(t *testing.T) (profileRegistry, string, bridge.Config, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	registry := profileRegistry{root: filepath.Join(dir, "registry")}
	raw, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := bridge.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AppID = "cli_export_primary"
	cfg.AppSecretFile = filepath.Join(dir, "credentials", "primary.json")
	cfg.MCP.TokenFile = filepath.Join(dir, "credentials", "t3.secret")
	secondary := cfg
	secondary.AppID = "cli_export_secondary"
	secondary.StateDir = filepath.Join(dir, "secondary-state")
	secondary.AppSecretFile = filepath.Join(dir, "credentials", "secondary.secret")
	cfg.AdditionalAccounts = []bridge.Config{secondary}
	values := map[string]string{"cli_export_primary": "synthetic-primary-export-value", "cli_export_secondary": "synthetic-secondary-export-value"}
	for _, account := range cfg.Accounts() {
		if err := writeAppSecret(account.AppSecretFile, values[account.AppID]); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeAppSecret(cfg.MCP.TokenFile, "synthetic-t3-token-outside-bundle"); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "source.json")
	raw, err = json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := registry.run([]string{"create", "work", "-from", source}, &out); err != nil {
		t.Fatal(err)
	}
	cfg, err = bridge.LoadConfig(strings.TrimSpace(out.String()))
	if err != nil {
		t.Fatal(err)
	}
	return registry, dir, cfg, values
}

func TestProfilePrivateBundleRoundTrip(t *testing.T) {
	registry, dir, original, values := exportProfileFixture(t)
	file := filepath.Join(dir, "exports", "work.bundle.json")
	plan, err := registry.previewExport("work")
	if err != nil || plan.Credentials != len(values) {
		t.Fatalf("unexpected export preview: %+v, %v", plan, err)
	}
	for _, args := range [][]string{
		{"export", "work", "-include-secrets", "-output", file},
		{"export", "work", "-include-secrets", "-yes"},
	} {
		var out bytes.Buffer
		if err := registry.run(args, &out); err == nil || out.Len() != 0 {
			t.Fatalf("incomplete confirmation produced output: %v", err)
		}
	}
	if _, err := os.Lstat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incomplete confirmation created a bundle: %v", err)
	}
	var out bytes.Buffer
	if err := registry.run([]string{"export", "work", "--include-secrets", "--yes", "--output", file}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != file {
		t.Fatal("export receipt contains more than the local path")
	}
	body, err := os.ReadFile(file)
	if err != nil || bytes.Contains(body, []byte("synthetic-t3-token-outside-bundle")) {
		t.Fatalf("unexpected credentials in bundle: %v", err)
	}
	bundle, _, err := readProfileBundle(file)
	if err != nil || bundle.Profile != "work" || len(bundle.AppSecrets) != len(values) {
		t.Fatalf("invalid export bundle: %v", err)
	}

	preview := func() profileBundleImportPlan {
		t.Helper()
		out.Reset()
		if err := registry.run([]string{"import", "moved", "-from", file, "-preview"}, &out); err != nil {
			t.Fatal(err)
		}
		for _, value := range values {
			if strings.Contains(out.String(), value) {
				t.Fatal("preview exposed an App Secret")
			}
		}
		var plan profileBundleImportPlan
		if err := json.Unmarshal(out.Bytes(), &plan); err != nil || plan.Profile != "work" || plan.Name != "moved" ||
			plan.Source != file || len(plan.Accounts) != len(values) || plan.Revision == "" {
			t.Fatalf("invalid import preview: %+v, %v", plan, err)
		}
		if _, err := os.Lstat(filepath.Dir(plan.Destination)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("import preview created a profile: %v", err)
		}
		return plan
	}
	review := preview()
	if _, err := localfile.WritePrivateAtomic(file, append(body, '\n'), false); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.importReviewedBundle("moved", file, "", review.Revision); !errors.Is(err, errProfileBundleChanged) {
		t.Fatalf("import accepted a stale review: %v", err)
	}
	review = preview()
	out.Reset()
	if err := registry.run([]string{"import", "moved", "-from", file, "-expected-revision", review.Revision}, &out); err != nil {
		t.Fatal(err)
	}
	importedPath := strings.TrimSpace(out.String())
	loaded, err := bridge.LoadConfig(importedPath)
	if err != nil || len(loaded.Accounts()) != len(values) {
		t.Fatalf("invalid imported config: %v", err)
	}
	oldAccounts := original.Accounts()
	for i, account := range loaded.Accounts() {
		if account.StateDir == oldAccounts[i].StateDir || account.AppSecretFile == oldAccounts[i].AppSecretFile ||
			account.ReceiverAuthorized || account.MCP.TokenFile != oldAccounts[i].MCP.TokenFile {
			t.Fatal("import did not isolate new state and App Secret files")
		}
		if value, err := bridge.Secret(account.AppSecretFile); err != nil || value != values[account.AppID] {
			t.Fatalf("App Secret did not round-trip: %v", err)
		}
	}
	raw, err := os.ReadFile(importedPath)
	if err != nil || bytes.Contains(raw, []byte("synthetic-primary-export-value")) || bytes.Contains(raw, []byte("synthetic-secondary-export-value")) {
		t.Fatalf("imported config embedded a credential: %v", err)
	}
	if err := registry.run([]string{"import", "moved", "-from", file}, io.Discard); err == nil {
		t.Fatal("import overwrote an existing profile")
	}

	bundle.AppSecrets[1].AppID = bundle.AppSecrets[0].AppID
	invalid, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	invalidPath := filepath.Join(filepath.Dir(file), "invalid.json")
	if _, err := localfile.WritePrivateAtomic(invalidPath, invalid, true); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.importBundle("invalid", invalidPath, "", ""); err == nil ||
		strings.Contains(err.Error(), "synthetic-primary-export-value") {
		t.Fatalf("invalid credentials were accepted or exposed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(registry.root, "profiles", "invalid")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid bundle published a profile: %v", err)
	}
}

func TestProfileBundleMigratesForeignHost(t *testing.T) {
	registry, dir, foreign, values := exportProfileFixture(t)
	otherHost := `D:\bridge\`
	if runtime.GOOS == "windows" {
		otherHost = "/var/lib/bridge/"
	}
	foreign.StateDir, foreign.AppSecretFile = otherHost+"primary", otherHost+"primary.secret"
	foreign.AdditionalAccounts[0].StateDir, foreign.AdditionalAccounts[0].AppSecretFile = otherHost+"secondary", otherHost+"secondary.secret"
	writeBundle := func(name string, config bridge.Config) string {
		t.Helper()
		body, err := json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		bundle := profileBundle{SchemaVersion: 1, Profile: "work", Config: body}
		for _, account := range config.Accounts() {
			bundle.AppSecrets = append(bundle.AppSecrets, profileBundleEntry{AppID: account.AppID, Secret: values[account.AppID]})
		}
		body, err = json.Marshal(bundle)
		if err != nil {
			t.Fatal(err)
		}
		source := filepath.Join(dir, name+".json")
		if _, err := localfile.WritePrivateAtomic(source, body, true); err != nil {
			t.Fatal(err)
		}
		return source
	}
	managedOnly := writeBundle("managed-foreign", foreign)
	if _, err := registry.previewBundleImport("local-managed", managedOnly, ""); err != nil {
		t.Fatalf("managed fields should migrate without adjustments: %v", err)
	}

	foreign.MCP.Endpoint, foreign.MCP.RuntimeFile = "", otherHost+"runtime.json"
	foreign.MCP.TokenFile = otherHost + "primary-token.json"
	foreign.AdditionalAccounts[0].MCP.TokenFile = otherHost + "secondary-token.json"
	source := writeBundle("foreign", foreign)
	patch := filepath.Join(dir, "local-targets.json")
	if _, err := registry.previewBundleImport("moved", source, ""); err == nil {
		t.Fatal("foreign MCP paths passed target validation")
	}
	writePatch := func(body []byte) {
		t.Helper()
		if _, err := localfile.WritePrivateAtomic(patch, body, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, invalid := range []string{
		`[{"path":["receiverAuthorized"],"value":true}]`,
		`[{"path":["additionalAccounts","0"],"delete":true}]`,
		`[{"path":["additionalAccounts","0","tenant"],"value":"lark"}]`,
		`[{"path":["appId"],"path":["mcp","environmentId"],"value":"any"}]`,
	} {
		writePatch([]byte(invalid))
		if _, err := registry.previewBundleImport("moved", managedOnly, patch); err == nil {
			t.Fatalf("accepted prohibited or ambiguous edit: %s", invalid)
		}
	}

	localToken := filepath.Join(dir, "local-token-reference.json")
	secondaryToken := filepath.Join(dir, "second-token-reference.json")
	changes := []map[string]any{
		{"path": []string{"mcp", "runtimeFile"}, "delete": true},
		{"path": []string{"mcp", "endpoint"}, "value": "http://127.0.0.1:46312/mcp"},
		{"path": []string{"mcp", "tokenFile"}, "value": localToken},
		{"path": []string{"additionalAccounts", "0", "mcp", "tokenFile"}, "value": secondaryToken},
	}
	body, err := json.Marshal(changes)
	if err != nil {
		t.Fatal(err)
	}
	writePatch(body)
	plan, err := registry.previewBundleImport("moved", source, patch)
	fields := []string{"mcp.runtimeFile", "mcp.endpoint", "mcp.tokenFile", "additionalAccounts.0.mcp.tokenFile"}
	if err != nil || plan.Patch != patch || !slices.Equal(plan.PatchedFields, fields) || len(plan.Accounts) != 2 {
		t.Fatalf("patch preview did not identify both accounts and the edits: %+v %v", plan, err)
	}
	writePatch(append(body, '\n'))
	if _, err := registry.importReviewedBundle("moved", source, patch, plan.Revision); !errors.Is(err, errProfileBundleChanged) {
		t.Fatalf("changed adjustment remained confirmable: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(plan.Destination)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preview and stale review wrote a profile: %v", err)
	}
	plan, err = registry.previewBundleImport("moved", source, patch)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := registry.run([]string{"import", "moved", "-from", source, "-patch", patch, "-expected-revision", plan.Revision}, &out); err != nil {
		t.Fatal(err)
	}
	got, err := bridge.LoadConfig(strings.TrimSpace(out.String()))
	if err != nil || got.MCP.RuntimeFile != "" || got.MCP.Endpoint != "http://127.0.0.1:46312/mcp" ||
		got.MCP.TokenFile != localToken || len(got.AdditionalAccounts) != 1 || got.AdditionalAccounts[0].MCP.TokenFile != secondaryToken {
		t.Fatalf("target configuration did not apply local references: %v", err)
	}
	for _, account := range got.Accounts() {
		value, err := bridge.Secret(account.AppSecretFile)
		if err != nil || value != values[account.AppID] || account.ReceiverAuthorized || !filepath.IsAbs(account.StateDir) ||
			!filepath.IsAbs(account.AppSecretFile) || strings.HasPrefix(account.StateDir, otherHost) {
			t.Fatalf("account %q did not keep identity with local private paths: %v", account.AppID, err)
		}
	}
}

func TestProfileExportRejectsStaleReviewAndProtectedTargets(t *testing.T) {
	registry, dir, cfg, _ := exportProfileFixture(t)
	plan, err := registry.previewExport("work")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{plan.Source, cfg.AppSecretFile, filepath.Join(cfg.StateDir, "export.json")} {
		if err := registry.exportFile("work", target, plan.Revision, true, true); err == nil {
			t.Fatalf("export overwrote a protected target %s", target)
		}
	}
	plain := filepath.Join(dir, "exports", "plain.json")
	if err := registry.run([]string{"export", "work", "-output", plain}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.LoadConfig(plain); err != nil {
		t.Fatalf("ordinary export is not usable as a config: %v", err)
	}
	if err := registry.run([]string{"export", "work", "-output", plain}, io.Discard); err == nil {
		t.Fatal("export replaced an existing private file without -force")
	}
	if err := registry.run([]string{"export", "work", "-output", plain, "-force"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(plan.Source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localfile.WritePrivateAtomic(plan.Source, append(raw, '\n'), false); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "exports", "stale.json")
	if err := registry.exportFile("work", file, plan.Revision, true, false); !errors.Is(err, errConfigRevisionConflict) {
		t.Fatalf("export accepted a stale review: %v", err)
	}
	if _, err := os.Lstat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale review produced a bundle: %v", err)
	}
}
