package main

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
	"github.com/larksuite/oapi-sdk-go/v3/scene/registration"
)

func TestBindConfigExistingApp(t *testing.T) {
	dir := t.TempDir()
	raw := existingBindConfigFixture(t, dir)
	secretPath := filepath.Join(dir, "scanned.secret")
	statePath := filepath.Join(dir, "scanned-state")
	for _, tc := range []struct {
		name string
		app  string
		user *registration.UserInfo
		path []string
	}{
		{"primary with implicit Feishu tenant", "cli_mainfixture", &registration.UserInfo{TenantBrand: "feishu"}, []string{"appSecretFile"}},
		{"additional Lark with matching brand", "cli_larkfixture", &registration.UserInfo{TenantBrand: "lark", OpenID: "ou_scanner"}, []string{"additionalAccounts", "1", "appSecretFile"}},
		{"additional Lark without brand", "cli_larkfixture", &registration.UserInfo{OpenID: "ou_scanner"}, []string{"additionalAccounts", "1", "appSecretFile"}},
		{"additional Lark without user info", "cli_larkfixture", nil, []string{"additionalAccounts", "1", "appSecretFile"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := &registration.RegisterAppResult{ClientID: tc.app, UserInfo: tc.user}
			candidate, err := bindConfig(raw, result, secretPath, statePath)
			if err != nil {
				t.Fatal(err)
			}
			assertExistingBindCandidate(t, raw, candidate, secretPath, tc.path...)
		})
	}
}

func TestQRCodeApplyExistingAppCredential(t *testing.T) {
	const scanned = "synthetic-scanned-secret"
	for _, tc := range []struct {
		name, app, brand, stored string
		wantError                bool
	}{
		{"implicit Feishu rejects Lark", "cli_mainfixture", "lark", "", true},
		{"Lark rejects Feishu", "cli_larkfixture", "feishu", "", true},
		{"identical credential accepted", "cli_larkfixture", "lark", scanned, false},
		{"different credential refused", "cli_larkfixture", "lark", "synthetic-stored-secret", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			raw := existingBindConfigFixture(t, dir)
			secretPath := filepath.Join(dir, "credentials", tc.app+".secret")
			if tc.stored != "" {
				if err := localfile.MkdirAllPrivate(filepath.Dir(secretPath)); err != nil {
					t.Fatal(err)
				}
				if _, err := localfile.WritePrivateAtomic(secretPath, []byte(tc.stored+"\n"), true); err != nil {
					t.Fatal(err)
				}
			}
			binding := existingScanFixture(t, dir, &registration.RegisterAppResult{
				ClientID: tc.app, ClientSecret: scanned, UserInfo: &registration.UserInfo{TenantBrand: tc.brand},
			})
			candidate, err := binding.apply(raw, binding.id)
			if (err != nil) != tc.wantError {
				t.Fatalf("apply error = %v, want error %t", err, tc.wantError)
			}
			if tc.wantError {
				if !bytes.Equal(candidate, raw) {
					t.Fatal("rejected scan changed the draft")
				}
			} else {
				assertExistingBindCandidate(t, raw, candidate, secretPath, "additionalAccounts", "1", "appSecretFile")
			}
			if tc.stored == "" {
				if _, err := os.Stat(filepath.Dir(secretPath)); !os.IsNotExist(err) {
					t.Fatalf("mismatched scan reached credential storage: %v", err)
				}
			} else if value, err := bridge.Secret(secretPath); err != nil || value != tc.stored {
				t.Fatalf("stored credential changed or could not be read: %v", err)
			}
		})
	}
}

func TestConfigWebQRCodeRebindExistingApp(t *testing.T) {
	const app = "cli_larkfixture"
	const scanned = "synthetic-web-rescan-secret"
	dir := t.TempDir()
	raw := existingBindConfigFixture(t, dir)
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	binding := existingScanFixture(t, dir, &registration.RegisterAppResult{ClientID: app, ClientSecret: scanned})
	handler := configWebHandler(webTestHost, []webSource{{ID: "file", Path: path}}, binding)
	page := submitWebConfig(handler, url.Values{
		"config": {string(raw)}, "revision": {configRevision(raw)}, "action": {"bind"}, "bindingId": {binding.id},
	})
	assertWebPage(t, page, http.StatusOK, "与来源的差异", `name="reviewed"`)
	draft := regexp.MustCompile(`(?s)id="config" name="config"[^>]*>(.*?)</textarea>`).FindStringSubmatch(page.Body.String())
	if draft == nil {
		t.Fatal("missing bound draft")
	}
	if strings.Contains(page.Body.String(), scanned) {
		t.Fatal("bind response exposed the scanned secret")
	}
	candidate := []byte(strings.TrimPrefix(html.UnescapeString(draft[1]), "\n"))
	secretPath := filepath.Join(dir, "credentials", app+".secret")
	assertExistingBindCandidate(t, raw, candidate, secretPath, "additionalAccounts", "1", "appSecretFile")
	if value, err := bridge.Secret(secretPath); err != nil || value != scanned {
		t.Fatalf("could not read scanned credential from its canonical private path: %v", err)
	}
	if saved, err := os.ReadFile(path); err != nil || !bytes.Equal(saved, raw) {
		t.Fatalf("binding changed the saved configuration before review: %v", err)
	}
}

func existingBindConfigFixture(t *testing.T, dir string) []byte {
	t.Helper()
	account := func(name, tenant string) bridge.Config {
		user := "ou_" + name
		return bridge.Config{
			AppID: "cli_" + name, Tenant: tenant, ReceiverMachine: name, ReceiverAuthorized: true,
			AppSecretFile: filepath.Join(dir, name+".secret"), StateDir: filepath.Join(dir, name+"-state"),
			Users: []string{user, "ou_collaborator"}, Admins: []string{user}, Chats: []string{"oc_" + name},
			MCP:          bridge.MCPConfig{Endpoint: "http://127.0.0.1:3773/mcp", TokenFile: filepath.Join(dir, "t3.secret"), EnvironmentID: "fixture-env"},
			DefaultRoute: "work",
			Routes: map[string]bridge.Target{"work": {
				EnvironmentID: "fixture-env", ProjectID: "project-" + name, Driver: "codex", ProviderID: "fixture-provider", Model: "fixture-model",
				Options: map[string]any{"MixedCase": json.Number("9007199254740993")}, RuntimeMode: "auto-accept-edits",
				InteractionMode: "default", Users: []string{user},
			}},
		}
	}
	config := account("mainfixture", "")
	config.AdditionalAccounts = []bridge.Config{account("otherfixture", "feishu"), account("larkfixture", "lark")}
	config.AdditionalAccounts[0].ReceiverAuthorized = false
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func existingScanFixture(t *testing.T, dir string, result *registration.RegisterAppResult) *qrRegistration {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	return &qrRegistration{configDir: dir, ctx: ctx, cancel: cancel, id: "fixture-scan", credentials: result,
		view: qrBindingView{State: "scanned", AppID: result.ClientID}}
}

func assertExistingBindCandidate(t *testing.T, raw, candidate []byte, secretPath string, path ...string) {
	t.Helper()
	edits, err := json.Marshal([]map[string]any{{"path": path, "value": secretPath}})
	if err != nil {
		t.Fatal(err)
	}
	want, err := bridge.EditConfigJSON(raw, edits)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(candidate, want) {
		t.Fatalf("want only %v updated, candidate differs from expected: %+v", path, configChanges(string(want), string(candidate)))
	}
}
