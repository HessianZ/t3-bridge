package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/larksuite/oapi-sdk-go/v3/scene/registration"
)

func TestQRCodeApplyReplaceApp(t *testing.T) {
	const (
		newApp  = "cli_replacementfixture"
		secret  = "synthetic-replacement-secret"
		scanner = "ou_replacement_scanner"
	)
	for _, tc := range []struct {
		name, oldApp, newTenant string
		index                   int
	}{
		{"primary Feishu to Lark", "cli_mainfixture", "lark", 0},
		{"additional Lark to Feishu", "cli_larkfixture", "feishu", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			raw, old := replaceBindConfigFixture(t, dir, tc.index)
			oldState := filepath.Join(old.StateDir, "state.json")
			if err := os.Mkdir(old.StateDir, 0700); err != nil {
				t.Fatal(err)
			}
			oldFiles := map[string]string{
				old.AppSecretFile: "synthetic-old-app-secret\r\n",
				oldState:          "{\n  \"appId\": \"" + tc.oldApp + "\"\n}\n",
			}
			for path, data := range oldFiles {
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}

			q := existingScanFixture(t, dir, &registration.RegisterAppResult{
				ClientID: newApp, ClientSecret: secret,
				UserInfo: &registration.UserInfo{OpenID: scanner, TenantBrand: tc.newTenant},
			})
			q.replaceAppID = tc.oldApp
			candidate, err := q.apply(raw, q.id)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := bridge.ParseConfig(candidate); err != nil {
				t.Fatalf("invalid replacement draft: %v", err)
			}
			var config bridge.Config
			decodeReplaceBindJSON(t, candidate, &config)
			accounts := config.Accounts()
			if len(accounts) != 3 || accounts[tc.index].AppID != newApp {
				t.Fatalf("expected replacement in account slot %d, got %d accounts", tc.index, len(accounts))
			}
			before, after := replaceBindJSONAccounts(t, raw), replaceBindJSONAccounts(t, candidate)
			for i, account := range accounts {
				if i != tc.index && !reflect.DeepEqual(before[i], after[i]) {
					t.Errorf("unchanged account %q differs from its original JSON", account.AppID)
				}
			}
			got := accounts[tc.index]
			secretPath := filepath.Join(dir, "credentials", newApp+".secret")
			if got.Tenant != tc.newTenant || got.AppSecretFile != secretPath || got.ReceiverAuthorized {
				t.Errorf("wrong replacement identity, credential path or receiver authorization for %q", newApp)
			}
			if filepath.Dir(got.StateDir) != filepath.Join(dir, "state") || got.StateDir == old.StateDir {
				t.Errorf("replacement has no independent state directory: %q", got.StateDir)
			}
			if !slices.Equal(got.Users, []string{scanner}) || len(got.Admins) != 0 || len(got.Chats) != 0 || got.DirectChats != nil || len(got.Topics) != 0 {
				t.Error("replacement should grant only the scanner access and clear inherited ACLs")
			}
			if got.CommentRoute != "" || got.MeetingEnabled || got.MeetingRoute != "" || got.MeetingSummaryTarget != "" ||
				got.MeetingRoomReplies || len(got.MeetingAutoJoinUsers) != 0 || got.ReviewRoute != "" ||
				len(got.EventRoutes) != 0 || len(got.EventChats) != 0 || got.AutoInvestigations != (bridge.AutoInvestigations{}) || got.CoordinationCommand != "" {
				t.Error("replacement retained account-specific automation or route settings")
			}
			if got.MCP != old.MCP || got.DefaultRoute != old.DefaultRoute || len(got.Routes) != 1 {
				t.Fatal("replacement should carry only the selected account's MCP connection and default route")
			}
			route := got.Routes[got.DefaultRoute]
			if len(route.Checks) != 0 || route.ToolContext != nil {
				t.Error("replacement route retained checks or tool context")
			}
			route.Checks = nil
			wantRoute := old.Routes[old.DefaultRoute]
			wantRoute.RuntimeMode = "approval-required"
			wantRoute.Users = []string{scanner}
			wantRoute.Checks, wantRoute.ToolContext = nil, nil
			if !reflect.DeepEqual(route, wantRoute) {
				t.Errorf("replacement route differs from the approved transfer: got %+v, want %+v", route, wantRoute)
			}

			if bytes.Contains(candidate, []byte(secret)) {
				t.Error("draft exposed the scanned secret")
			}
			if value, err := bridge.Secret(secretPath); err != nil || value != secret {
				t.Errorf("replacement credential was not written privately: %v", err)
			}
			if _, err := os.Lstat(got.StateDir); !os.IsNotExist(err) {
				t.Errorf("replacement state directory was created: %v", err)
			}
			for path, want := range oldFiles {
				if data, err := os.ReadFile(path); err != nil || string(data) != want {
					t.Errorf("old file %s changed: %v", filepath.Base(path), err)
				}
			}
		})
	}
}

func TestQRCodeApplyReplaceAppRejectsBeforeWritingCredential(t *testing.T) {
	const newApp = "cli_replacementfixture"
	scanner := &registration.UserInfo{OpenID: "ou_scanner", TenantBrand: "feishu"}
	for _, tc := range []struct {
		name, oldApp, scannedApp string
		user                     *registration.UserInfo
		noDefaultRoute           bool
	}{
		{"new app already configured", "cli_mainfixture", "cli_otherfixture", scanner, false},
		{"same app scanned", "cli_mainfixture", "cli_mainfixture", scanner, false},
		{"no scanner user", "cli_mainfixture", newApp, nil, false},
		{"no scanner OpenID", "cli_mainfixture", newApp, &registration.UserInfo{TenantBrand: "feishu"}, false},
		{"partial old app ID", "cli_main", newApp, scanner, false},
		{"selected app has no default route", "cli_larkfixture", newApp, scanner, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			raw := existingBindConfigFixture(t, dir)
			if tc.noDefaultRoute {
				var err error
				raw, err = bridge.EditConfigJSON(raw, []byte(`[{"path":["additionalAccounts","1","defaultRoute"],"delete":true}]`))
				if err != nil {
					t.Fatal(err)
				}
			}
			q := existingScanFixture(t, dir, &registration.RegisterAppResult{
				ClientID: tc.scannedApp, ClientSecret: "synthetic-rejected-secret", UserInfo: tc.user,
			})
			q.replaceAppID = tc.oldApp
			candidate, err := q.apply(raw, q.id)
			if err == nil || !bytes.Equal(candidate, raw) {
				t.Errorf("expected rejection with the original draft, got %v", err)
			}
			if _, err := os.Lstat(filepath.Join(dir, "credentials")); !os.IsNotExist(err) {
				t.Errorf("rejected scan reached credential storage: %v", err)
			}
		})
	}
}

func TestQRCodeReplaceAppRequiresNewModelForDedicatedToolProvider(t *testing.T) {
	dir := t.TempDir()
	raw, old := replaceBindConfigFixture(t, dir, 0)
	raw, err := bridge.EditConfigJSON(raw, []byte(`[
		{"path":["routes","selected","toolContext","verified"],"value":true},
		{"path":["routes","selected","toolContext","version"],"value":"fixture"}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	q := existingScanFixture(t, dir, &registration.RegisterAppResult{
		ClientID: "cli_replacementfixture", ClientSecret: "synthetic-secret",
		UserInfo: &registration.UserInfo{OpenID: "ou_scanner", TenantBrand: "feishu"},
	})
	q.replaceAppID = old.AppID
	candidate, err := q.apply(raw, q.id)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := bridge.ParseConfig(candidate)
	if err != nil {
		t.Fatal(err)
	}
	route := cfg.Routes[cfg.DefaultRoute]
	if route.ToolContext != nil || route.ProviderID != "REPLACE_PROVIDER_INSTANCE_ID" ||
		route.Model != "REPLACE_MODEL_ID" || len(route.Options) != 0 ||
		route.ProjectID != old.Routes[old.DefaultRoute].ProjectID ||
		cfg.ReceiverAuthorized || len(cfg.ReceiverPlaceholderFields()) == 0 {
		t.Fatal("replacement kept a dedicated provider or lost its original T3 project")
	}
}

func TestQRCodeStartRejectsRepeatedReplaceAppID(t *testing.T) {
	q := existingScanFixture(t, t.TempDir(), &registration.RegisterAppResult{ClientID: "cli_replacementfixture"})
	q.register = func(context.Context, *registration.Options) (*registration.RegisterAppResult, error) {
		return nil, nil
	}
	id := q.id
	mux := http.NewServeMux()
	q.routes(mux)
	req := httptest.NewRequest(http.MethodPost, "/bind/start?replaceAppId=cli_mainfixture&replaceAppId=cli_larkfixture", nil)
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Errorf("duplicate replacement target status = %d, want %d", resp.Code, http.StatusBadRequest)
	}
	if view, ok := q.status(id); !ok || view.State != "scanned" {
		t.Error("rejecting an ambiguous replacement target reset the current scan")
	}
}

func replaceBindConfigFixture(t *testing.T, dir string, index int) ([]byte, bridge.Config) {
	t.Helper()
	var config bridge.Config
	decodeReplaceBindJSON(t, existingBindConfigFixture(t, dir), &config)
	old := &config
	if index > 0 {
		old = &config.AdditionalAccounts[index-1]
	}
	old.MCP.Endpoint = "http://127.0.0.1:4884/mcp"
	old.MCP.TokenFile = filepath.Join(dir, "selected-t3.secret")
	old.MCP.EnvironmentID = "selected-env"
	old.DirectChats = &[]string{"oc_fixture_direct"}
	old.Topics = []string{"omt_fixture"}
	old.CoordinationCommand = filepath.Join(dir, "coordinate")
	old.AutoInvestigations.LegacyDispatcherStopped = true
	old.EventRoutes = map[string]string{"fixture-node": "selected"}
	old.EventChats = old.Chats
	old.CommentRoute, old.MeetingRoute, old.ReviewRoute = "brief", "brief", "review"
	old.MeetingEnabled, old.MeetingRoomReplies = true, true
	old.MeetingSummaryTarget, old.MeetingAutoJoinUsers = "origin", old.Admins
	selected := old.Routes[old.DefaultRoute]
	selected.EnvironmentID, selected.Kind = old.MCP.EnvironmentID, "nas"
	selected.Checks = []string{"fixture-check"}
	selected.ToolContext = &bridge.ToolContext{
		CLIPath: filepath.Join(dir, "lark-cli"), EnvironmentID: selected.EnvironmentID, Identity: "bot",
	}
	brief := old.Routes[old.DefaultRoute]
	brief.RuntimeMode, brief.InteractionMode = "approval-required", "plan"
	old.Routes = map[string]bridge.Target{"selected": selected, "brief": brief, "review": brief}
	old.DefaultRoute = "selected"
	if err := config.Validate(); err != nil {
		t.Fatalf("invalid fixture: %v", err)
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	return raw, *old
}

func decodeReplaceBindJSON(t *testing.T, raw []byte, value any) {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(value); err != nil {
		t.Fatal(err)
	}
}

func replaceBindJSONAccounts(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var main map[string]any
	decodeReplaceBindJSON(t, raw, &main)
	additional := main["additionalAccounts"].([]any)
	delete(main, "additionalAccounts")
	accounts := []map[string]any{main}
	for _, account := range additional {
		accounts = append(accounts, account.(map[string]any))
	}
	return accounts
}
