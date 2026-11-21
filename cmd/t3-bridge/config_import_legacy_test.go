package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestConfigImportLegacyCandidate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)
	t.Setenv("LARK_CHANNEL_HOME", dir)
	base, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	base = []byte(strings.NewReplacer(
		`"work"`, `"Work.Team"`,
		`"options": {}`, `"options": {"Thinking.Level": "high", "thinking.level": "low"}`,
		`"receiverAuthorized": false`, `"receiverAuthorized": true`,
		`"REPLACE_OPEN_ID"`, `"ou_explicit_route_user"`,
	).Replace(string(base)))
	legacy := []byte(`{
		"schemaVersion": 2, "activeProfile": "other",
		"secrets": {"private_provider_marker": {}},
		"profiles": {
			"other": {"schemaVersion": 2, "accounts": {"app": {"id": "cli_other"}}},
			"Team.Work": {
				"schemaVersion": 2, "agentKind": "t3", "mode": "team",
				"accounts": {"app": {"id": "cli_selected", "tenant": "lark", "secret": "private_secret_marker"}},
				"access": {
					"allowedUsers": ["ou_imported"], "admins": ["ou_admin"],
					"allowedChats": ["oc_approved"], "chatRequireMention": {"oc_unlisted": false}
				},
				"t3": {"tokenFile": "/private/old_token_marker"},
				"seen": ["old_execution_marker"]
			}
		}
	}`)
	basePath := filepath.Join(dir, "target.json")
	legacyPath := filepath.Join(dir, "config.json")
	for path, raw := range map[string][]byte{basePath: base, legacyPath: legacy} {
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	state := filepath.Join(dir, "new-state")
	secret := filepath.Join(dir, "unopened.secret")
	args := []string{"-profile", "Team.Work", "-config", basePath, "-state-dir", state}
	var output bytes.Buffer
	if err := runConfigImportLegacy(args, &output); err == nil || !strings.Contains(err.Error(), "-app-secret-file") || output.Len() != 0 {
		t.Fatalf("expected explicit secret reference, got %v", err)
	}
	args = append(args, "-app-secret-file", secret)
	if err := runConfigImportLegacy(args, &output); err != nil {
		t.Fatal(err)
	}
	var explicit bytes.Buffer
	if err := runConfigImportLegacy(append([]string{"-legacy-config", legacyPath}, args...), &explicit); err != nil ||
		!bytes.Equal(explicit.Bytes(), output.Bytes()) {
		t.Fatalf("discovered and explicit sources produced different drafts: %v", err)
	}
	candidate, err := bridge.ParseConfig(output.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if candidate.AppID != "cli_selected" || candidate.Tenant != "lark" || candidate.AppSecretFile != secret || candidate.StateDir != state || candidate.ReceiverAuthorized {
		t.Fatal("candidate identity, state or receiver authorization was not set as requested")
	}
	if !slices.Equal(candidate.Users, []string{"ou_imported"}) || !slices.Equal(candidate.Admins, []string{"ou_admin"}) || !slices.Equal(candidate.Chats, []string{"oc_approved"}) {
		t.Fatal("candidate account ACL differs from the explicit legacy ACL")
	}
	if candidate.DirectChats == nil || len(*candidate.DirectChats) != 0 {
		t.Fatal("legacy private access must stay independent from its group allowlist")
	}
	route := candidate.Routes["Work.Team"]
	if candidate.DefaultRoute != "Work.Team" || !slices.Equal(route.Users, []string{"ou_explicit_route_user"}) || route.Options["Thinking.Level"] != "high" || route.Options["thinking.level"] != "low" {
		t.Fatal("target route, ACL or case-sensitive options changed")
	}
	for _, marker := range []string{"private_provider_marker", "private_secret_marker", "old_token_marker", "old_execution_marker", "oc_unlisted", "cli_other"} {
		if strings.Contains(output.String(), marker) {
			t.Fatalf("candidate contains excluded fixture data %q", marker)
		}
	}
	request := legacyConfigImport{SourcePath: legacyPath, Profile: "Team.Work", StateDir: state, AppSecretFile: secret}
	imported, err := request.apply(base)
	if err != nil || !bytes.Equal(imported, output.Bytes()) {
		t.Fatalf("workbench import differs from CLI import: %v", err)
	}
	if got, err := os.ReadFile(legacyPath); err != nil || !bytes.Equal(got, legacy) {
		t.Fatalf("workbench modified its source: %v", err)
	}
	if _, err := os.Lstat(secret); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workbench touched the credential path: %v", err)
	}
	t.Run("credential review binds the candidate and source", func(t *testing.T) {
		candidate, plan, err := request.preview(base)
		if err != nil || !bytes.Equal(candidate, imported) ||
			plan.Profile != "Team.Work" || plan.AppID != "cli_selected" || plan.Destination != secret {
			t.Fatalf("unexpected credential plan: %+v, %v", plan, err)
		}
		provider := filepath.Join(dir, "nonexistent-provider")
		if err := plan.copy(context.Background(), provider, append(bytes.Clone(candidate), ' ')); err == nil ||
			!strings.Contains(err.Error(), "候选配置已变化") {
			t.Fatalf("changed candidate reached provider discovery: %v", err)
		}
		if err := os.WriteFile(legacyPath, append(bytes.Clone(legacy), ' '), 0600); err != nil {
			t.Fatal(err)
		}
		if err := plan.copy(context.Background(), provider, candidate); err == nil ||
			!strings.Contains(err.Error(), "旧配置内容已变化") {
			t.Fatalf("changed source reached provider discovery: %v", err)
		}
		defaultSource := bytes.Replace(legacy, []byte(`"activeProfile": "other"`),
			[]byte(`"activeProfile": "Team.Work", "ActiveProfile": "other"`), 1)
		defaultRequest := request
		defaultRequest.Profile = ""
		if plan, err := defaultRequest.credentialPlan(defaultSource, candidate); err != nil || plan.Profile != "Team.Work" {
			t.Fatalf("credential copy selected the wrong default profile: %+v, %v", plan, err)
		}
		if err := os.WriteFile(legacyPath, legacy, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(secret); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("credential review touched the destination: %v", err)
		}
	})
	request.SourcePath = filepath.Join(dir, "directory.json")
	if err := os.Mkdir(request.SourcePath, 0700); err != nil {
		t.Fatal(err)
	}
	if imported, err := request.apply(base); err == nil || imported != nil {
		t.Fatalf("expected failure for non-regular import source: %v", err)
	}
	t.Run("copies selected app credential", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("exec-provider fixture uses a POSIX script")
		}
		canonicalDir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("T3_LEGACY_FIXTURE_DIR", canonicalDir)
		t.Setenv("LARK_CHANNEL_HOME", "incorrect-directory")
		t.Setenv("LARK_CHANNEL_PROFILE", "incorrect-profile")
		const fixtureSecret = "synthetic-legacy-provider-secret"
		const provider = `#!/bin/sh
set -eu
test "$*" = 'secrets get'
test "$LARK_CHANNEL_HOME" = "$T3_LEGACY_FIXTURE_DIR"
test "$LARK_CHANNEL_PROFILE" = 'Team.Work'
test "$(cat)" = '{"protocolVersion":1,"provider":"lark-channel","ids":["app-cli_selected"]}'
printf '%s\n' '{"protocolVersion":1,"values":{"app-cli_selected":"` + fixtureSecret + `"}}'
`
		binary := filepath.Join(dir, "legacy-bridge")
		if err := os.WriteFile(binary, []byte(provider), 0700); err != nil {
			t.Fatal(err)
		}
		copied := filepath.Join(dir, "credentials", "imported.secret")
		output.Reset()
		copyArgs := append(slices.Clone(args[:len(args)-2]), "-app-secret-file", copied, "-legacy-bridge", binary)
		copyArgs = append(copyArgs, "-legacy-config", legacyPath)
		if err := runConfigImportLegacy(copyArgs, &output); err != nil {
			t.Fatal(err)
		}
		if value, err := bridge.Secret(copied); err != nil || value != fixtureSecret ||
			!strings.Contains(output.String(), "imported.secret") || strings.Contains(output.String(), fixtureSecret) {
			t.Fatal("expected a private credential file and a candidate containing only its reference")
		}
		if err := os.WriteFile(copied, []byte("existing-private-value\n"), 0600); err != nil {
			t.Fatal(err)
		}
		output.Reset()
		if err := runConfigImportLegacy(copyArgs, &output); err == nil || output.Len() != 0 {
			t.Fatalf("expected existing credential conflict without output: %v", err)
		}
		if value, err := bridge.Secret(copied); err != nil || value != "existing-private-value" {
			t.Fatal("existing credential changed after the conflict")
		}
	})
	if _, err := os.Lstat(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("candidate touched the state path: %v", err)
	}
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := runConfigImportLegacy(args, &output); err == nil || !strings.Contains(err.Error(), "-state-dir") || output.Len() != 0 {
		t.Fatalf("expected rejection of existing state directory, got %v", err)
	}
}

func TestConfigImportLegacyInheritsUsersOnlyForSingleRouteTemplate(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "old.json")
	raw := []byte(`{"schemaVersion":2,"activeProfile":"codex","profiles":{"codex":{
		"schemaVersion":2,"accounts":{"app":{"id":"cli_imported"}},
		"access":{"allowedUsers":["ou_alice","ou_bob"],"admins":["ou_admin_only"]}
	}}}`)
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	request := legacyConfigImport{
		SourcePath: source, StateDir: filepath.Join(dir, "state"), AppSecretFile: filepath.Join(dir, "app.secret"),
	}
	var output bytes.Buffer
	if err := runConfigImportLegacy([]string{
		"-legacy-config", source, "-state-dir", request.StateDir, "-app-secret-file", request.AppSecretFile,
	}, &output); err != nil {
		t.Fatal(err)
	}
	result, err := bridge.ParseConfig(output.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	route := result.Routes[result.DefaultRoute]
	if !slices.Equal(route.Users, []string{"ou_alice", "ou_bob"}) ||
		!slices.Equal(result.Users, route.Users) || !slices.Equal(result.Admins, []string{"ou_admin_only"}) ||
		result.ReceiverAuthorized || route.RuntimeMode != "approval-required" || route.ProjectID != "REPLACE_PROJECT_ID" {
		t.Fatal("template migration changed routing policy or granted users outside the old allowlist")
	}
	generic, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	multi, err := loadConfigTemplate("multi-host")
	if err != nil {
		t.Fatal(err)
	}
	for _, base := range [][]byte{
		multi,
		bytes.Replace(generic, []byte(`"approval-required"`), []byte(`"full-access"`), 1),
		bytes.Replace(generic, []byte(`"REPLACE_APP_ID"`), []byte(`"cli_already_configured"`), 1),
	} {
		if candidate, err := request.apply(base); err == nil || !strings.Contains(err.Error(), "routes[") || candidate != nil {
			t.Fatalf("a configured or elevated route inherited users: %v", err)
		}
	}
}

func TestConfigImportLegacyReusesSelectedT3Connection(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	base, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	const cacheKey = "cacheFollowupAttachments"
	if bytes.Contains(base, []byte(`"`+cacheKey+`"`)) {
		t.Fatal("the template must omit the attachment cache setting")
	}
	base = bytes.Replace(base, []byte(`"receiverAuthorized": false`), []byte(`"receiverAuthorized": true`), 1)
	baseConfig, err := bridge.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	connection := bridge.MCPConfig{
		RuntimeFile: filepath.Join(dir, "server-runtime.json"), TokenFile: filepath.Join(dir, "oauth.json"),
		EnvironmentID: "env_fixture",
	}
	localWorkspace := filepath.Join(dir, "old-workspace")
	for _, tc := range []struct {
		name, endpoint        string
		draftCache, wantCache string
		workspace, wantPath   string
		noReuse, rejected     bool
	}{
		{name: "runtime file", wantCache: "true"},
		{name: "explicit endpoint takes precedence", endpoint: "http://localhost:7777/mcp", wantCache: "true"},
		{name: "explicit false is preserved", draftCache: "false", wantCache: "false"},
		{name: "existing true is preserved", draftCache: "true", wantCache: "true"},
		{name: "remote HTTPS endpoint", endpoint: "https://example.invalid/mcp"},
		{name: "remote drive path", endpoint: "https://example.invalid/mcp",
			workspace: "  d:/Projects/./Bridge  ", wantPath: "d:/Projects/./Bridge"},
		{name: "remote UNC path", endpoint: "https://example.invalid/mcp",
			workspace: "  \\\\server\\share\\Projects\\Bridge  ", wantPath: "\\\\server\\share\\Projects\\Bridge"},
		{name: "drive-relative path is omitted", endpoint: "https://example.invalid/mcp",
			workspace: "D:Projects\\Bridge"},
		{name: "remote HTTP endpoint rejected", endpoint: "http://example.invalid/mcp", rejected: true},
		{name: "without reuse the setting stays absent", noReuse: true},
		{name: "without reuse the setting stays false", noReuse: true, draftCache: "false", wantCache: "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			draft := base
			if tc.draftCache != "" {
				edits := []byte(`[{"path":["routes","` + baseConfig.DefaultRoute + `","` + cacheKey + `"],"value":` + tc.draftCache + `}]`)
				patched, err := bridge.EditConfigJSON(base, edits)
				if err != nil {
					t.Fatal(err)
				}
				draft = patched
			}
			request := legacyConfigImport{
				ReuseT3: !tc.noReuse, StateDir: filepath.Join(dir, "state"), AppSecretFile: filepath.Join(dir, "app.secret"),
			}
			old := connection
			old.Endpoint = tc.endpoint
			workspace, wantProjectPath := tc.workspace, tc.wantPath
			if workspace == "" {
				workspace, wantProjectPath = localWorkspace, localWorkspace
			}
			source, err := json.Marshal(map[string]any{
				"schemaVersion": 2, "activeProfile": "migration", "profiles": map[string]any{
					"migration": map[string]any{
						"schemaVersion": 2, "agentKind": "t3", "t3": map[string]any{
							"runtimeFile": old.RuntimeFile, "endpoint": old.Endpoint, "environmentId": old.EnvironmentID,
							"tokenFile": old.TokenFile, "provider": "old_provider_secret_marker", "model": "old_model_marker",
						},
						"accounts":   map[string]any{"app": map[string]string{"id": "cli_migration"}},
						"access":     map[string][]string{"allowedUsers": {"ou_fixture"}},
						"workspaces": map[string]string{"default": workspace},
					},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			candidate, err := request.candidate(draft, source)
			if tc.rejected {
				if err == nil || !strings.Contains(err.Error(), "loopback") || candidate != nil {
					t.Fatalf("accepted a legacy remote connection: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := bridge.ParseConfig(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if old.Endpoint != "" {
				old.RuntimeFile = ""
			}
			wantMCP := old
			if tc.noReuse {
				wantMCP = baseConfig.MCP
				wantProjectPath = baseConfig.Routes[baseConfig.DefaultRoute].ProjectPath
			}
			route := cfg.Routes[cfg.DefaultRoute]
			if cfg.MCP != wantMCP || route.EnvironmentID != wantMCP.EnvironmentID || route.ProjectID != "REPLACE_PROJECT_ID" ||
				route.ProjectPath != wantProjectPath || cfg.AppSecretFile != request.AppSecretFile || cfg.StateDir != request.StateDir ||
				route.Model != "REPLACE_MODEL_ID" || cfg.ReceiverAuthorized ||
				bytes.Contains(candidate, []byte("old_provider_secret_marker")) || bytes.Contains(candidate, []byte("old_model_marker")) {
				t.Fatal("migration changed the route or copied values outside the connection")
			}
			var raw struct {
				Routes map[string]map[string]json.RawMessage `json:"routes"`
			}
			if err := json.Unmarshal(candidate, &raw); err != nil {
				t.Fatal(err)
			}
			if got := string(raw.Routes[cfg.DefaultRoute][cacheKey]); got != tc.wantCache {
				t.Errorf("default route %s = %q, want %q (empty means omitted)", cacheKey, got, tc.wantCache)
			}
			if request.ReuseT3 {
				configured := bytes.ReplaceAll(base, []byte("REPLACE_ENVIRONMENT_ID"), []byte("env_existing"))
				if _, err := request.candidate(configured, source); err == nil {
					t.Fatal("an already configured environment was replaced")
				}
			}
		})
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("candidate preview must leave the fixture directory empty: %v, %v", entries, err)
	}
}

func testLegacyProfileEdits(raw []byte, name string, draftUsers []string) ([]legacyImportEdit, error) {
	source, err := selectLegacyImportSource(raw, name)
	if err != nil {
		return nil, err
	}
	return source.profileEdits(draftUsers, "")
}

func TestConfigImportLegacyRequiresExplicitUsers(t *testing.T) {
	legacy := []byte(`{
		"schemaVersion": 2, "activeProfile": "team",
		"profiles": {"team": {
			"schemaVersion": 2, "mode": "team",
			"accounts": {"app": {"id": "cli_example"}},
			"access": {"admins": ["ou_admin_only"], "allowedChats": ["oc_approved"]}
		}}
	}`)
	edits, err := testLegacyProfileEdits(legacy, "", nil)
	if err == nil || !strings.Contains(err.Error(), "access.allowedUsers") || strings.Contains(err.Error(), "ou_admin_only") || edits != nil {
		t.Fatalf("expected field error without fallback grants or source identifiers, got %v", err)
	}
}

func TestConfigImportLegacySingleAccount(t *testing.T) {
	const literal = "synthetic-single-account-secret"
	const primary = `{"id":"cli_single","tenant":"lark","secret":"` + literal + `"}`
	const preferences = `,"preferences":{"access":{"allowedUsers":["ou_imported"],"admins":["ou_admin"],"allowedChats":["oc_allowed"]}}`
	for _, tc := range []struct {
		name, source         string
		users, admins, chats []string
	}{
		{"root app only", `{"app":` + primary + `}`, []string{"ou_target"}, nil, nil},
		{"nested app with access", `{"schemaVersion":1,"app":{"id":"cli_stale","secret":"ignored-placeholder"},"accounts":{"app":` + primary + `}` + preferences + `}`,
			[]string{"ou_imported"}, []string{"ou_admin"}, []string{"oc_allowed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			base, err := loadConfigTemplate("generic")
			if err != nil {
				t.Fatal(err)
			}
			base = bytes.ReplaceAll(base, []byte("REPLACE_OPEN_ID"), []byte("ou_target"))
			basePath, sourcePath := filepath.Join(dir, "target.json"), filepath.Join(dir, "old.json")
			for p, data := range map[string][]byte{basePath: base, sourcePath: []byte(tc.source)} {
				if err := os.WriteFile(p, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			state, secret := filepath.Join(dir, "state"), filepath.Join(dir, "private", "app.secret")
			args := []string{"-legacy-config", sourcePath, "-config", basePath, "-state-dir", state, "-app-secret-file", secret}
			var output bytes.Buffer
			if err := runConfigImportLegacy(args, &output); err != nil {
				t.Fatal(err)
			}
			candidate := bytes.Clone(output.Bytes())
			config, err := bridge.ParseConfig(candidate)
			if err != nil || config.AppID != "cli_single" || config.Tenant != "lark" || config.ReceiverAuthorized || config.AppSecretFile != secret ||
				!slices.Equal(config.Users, tc.users) || !slices.Equal(config.Admins, tc.admins) ||
				!slices.Equal(config.Chats, tc.chats) || !slices.Equal(config.Routes["work"].Users, []string{"ou_target"}) ||
				strings.Contains(output.String(), literal) || strings.Contains(output.String(), "cli_stale") {
				t.Fatalf("unexpected single-account migration result: %v", err)
			}
			for _, p := range []string{state, filepath.Dir(secret)} {
				if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("preview touched a destination: %v", err)
				}
			}
			request := legacyConfigImport{SourcePath: sourcePath, StateDir: state, AppSecretFile: secret}
			previewed, plan, err := request.preview(base)
			if err != nil || !plan.Inline || plan.Profile != "" || !bytes.Equal(previewed, candidate) {
				t.Fatalf("unexpected single-account copy preview: %v", err)
			}
			if err := os.WriteFile(sourcePath, []byte(tc.source+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := plan.copy(context.Background(), "", candidate); err == nil || !strings.Contains(err.Error(), "旧配置内容已变化") {
				t.Fatalf("changed source was not detected: %v", err)
			}
			if err := os.WriteFile(sourcePath, []byte(tc.source), 0600); err != nil {
				t.Fatal(err)
			}
			output.Reset()
			if err := runConfigImportLegacy(append(slices.Clone(args), "-copy-app-secret"), &output); err != nil || !bytes.Equal(output.Bytes(), candidate) {
				t.Fatalf("explicit copy failed or changed the candidate: %v", err)
			}
			if got, err := bridge.Secret(secret); err != nil || got != literal {
				t.Fatal("explicit copy did not save the expected private credential")
			}
			if got, err := os.ReadFile(sourcePath); err != nil || string(got) != tc.source {
				t.Fatal("copy modified the legacy file")
			}
		})
	}
}

func TestConfigImportLegacySingleAccountRejectsAmbiguousSources(t *testing.T) {
	const legacy = `{"app":{"id":"cli_single","tenant":"feishu","secret":{"source":"env","id":"PRIVATE_FIXTURE"}},"preferences":{"access":{"allowedUsers":["ou_user"]}}}`
	if _, err := testLegacyProfileEdits([]byte(legacy), "profile", nil); err == nil {
		t.Fatal("accepted a profile selector for a single account")
	}
	for _, changed := range []string{
		strings.Replace(legacy, `{"app"`, `{"profiles":{},"app"`, 1),
		strings.Replace(legacy, `{"app"`, `{"schemaVersion":3,"app"`, 1),
		strings.Replace(legacy, `"tenant":"feishu",`, "", 1),
		strings.Replace(legacy, `"ou_user"`, `"invalid"`, 1),
		strings.Replace(legacy, `["ou_user"]`, "[]", 1),
	} {
		if _, err := testLegacyProfileEdits([]byte(changed), "", []string{"ou_target"}); err == nil {
			t.Fatal("accepted an invalid single-account source")
		}
	}
	if _, err := testLegacyProfileEdits([]byte(`{"app":{"id":"cli_single","tenant":"feishu"}}`), "", []string{"REPLACE_OPEN_ID"}); err == nil {
		t.Fatal("retained a template placeholder when the source omitted its ACL")
	}
	if _, err := testLegacyProfileEdits([]byte(legacy), "", nil); err != nil {
		t.Fatalf("referenced credential prevented config-only import: %v", err)
	}
	for _, raw := range []string{legacy, strings.Replace(legacy, `{"source":"env","id":"PRIVATE_FIXTURE"}`, `"${PRIVATE_FIXTURE}"`, 1)} {
		if _, err := (legacyConfigImport{}).credentialPlan([]byte(raw), []byte(`{"appId":"cli_single"}`)); err == nil ||
			!strings.Contains(err.Error(), "已有私有凭据文件") || strings.Contains(err.Error(), "PRIVATE_FIXTURE") {
			t.Fatalf("unexpected copy decision for a referenced credential: %v", err)
		}
	}
}
