package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

func TestReceiverPreflightChecksEnabledAccountsAndExactRoutes(t *testing.T) {
	config := testConfig(t)
	target := config.Routes["nas"]
	bad := target
	bad.ProjectID = "missing"
	draft := target
	draft.ProjectID = "REPLACE_PROJECT_ID"
	draft.UnavailableReason = "reserve for later"
	config.Routes = map[string]Target{"z-missing": bad, "a-work": target, "c-offline": draft}
	config.AdditionalAccounts = []Config{{AppID: "disabled", Tenant: "lark", StateDir: t.TempDir()}}
	feishu, connected, closed := 0, 0, 0
	probe := func(_ context.Context, app, _, tenant string) error {
		feishu++
		if app != config.AppID || tenant != "feishu" {
			t.Fatalf("unexpected Feishu identity %s %s", tenant, app)
		}
		return nil
	}
	connect := func(_ context.Context, got MCPConfig) (ToolCaller, func(), error) {
		connected++
		if got != config.MCP {
			t.Fatal("MCP credentials do not correspond to the selected account")
		}
		return scriptedCaller(func(name string, args map[string]any) (any, error) {
			switch name {
			case "t3_environment_read":
				return map[string]string{"environmentId": target.EnvironmentID}, nil
			case "t3_project_read":
				if args["projectId"] == target.ProjectID {
					return map[string]string{"id": target.ProjectID}, nil
				}
				return map[string]string{"id": "unrelated"}, nil
			case "orchestrator_capabilities":
				return T3Capabilities{RuntimeMode: "approval-required", Providers: []T3Provider{{
					ID: target.ProviderID, Driver: target.Driver, Models: []T3Model{{
						ID: target.Model, Options: []T3ModelOption{{ID: "effort", Values: []any{"high"}}},
					}},
				}}}, nil
			default:
				t.Fatalf("unexpected tool %s", name)
				return nil, nil
			}
		}), func() { closed++ }, nil
	}
	report := checkReceiverConfig(context.Background(), config, false, probe, connect)
	if report.Passed || report.AllAccounts != nil || feishu != 1 || connected != 1 || closed != 1 || len(report.Accounts) != 2 {
		t.Fatalf("unexpected result %+v (probe %d, connect %d, close %d)", report, feishu, connected, closed)
	}
	checks := report.Accounts[0].Checks
	if len(checks) != 6 || checks[0].Status != "ok" || checks[1].Status != "ok" ||
		checks[2].Route != "a-work" || checks[2].Status != "ok" ||
		checks[3].Route != "c-offline" || checks[3].Status != "disabled" ||
		checks[4].Route != "z-missing" || checks[4].Status != "failed" || checks[4].Detail != "project unavailable" ||
		checks[5].Check != "state-lock" || checks[5].Status != "ok" ||
		report.Accounts[1].Enabled || len(report.Accounts[1].Checks) != 1 || report.Accounts[1].Checks[0].Check != "state-lock" {
		t.Fatalf("unexpected account checks %+v", report.Accounts)
	}
	delete(config.Routes, "z-missing")
	if report := checkReceiverConfig(context.Background(), config, false, probe, connect); !report.Passed {
		t.Fatalf("valid route failed: %+v", report)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report = checkReceiverConfig(ctx, config, false, probe, connect)
	if report.Passed || feishu != 2 || connected != 2 || report.Accounts[0].Checks[1].Status != "skipped" {
		t.Fatalf("canceled preflight made new requests or claimed success: %+v", report)
	}
}

func TestReceiverPreflightVerifiesSavedDisabledAccounts(t *testing.T) {
	config := testConfig(t)
	target := config.Routes["nas"]
	config.Routes = map[string]Target{"work": target}
	config.ReceiverAuthorized = false
	draft := config
	draft.AppID, draft.Tenant, draft.StateDir, draft.AppSecretFile = "cli_draft", "lark", t.TempDir(), "/private/draft.secret"
	draft.MCP.EnvironmentID, draft.MCP.TokenFile = "draft-env", "/private/draft-mcp.secret"
	draftTarget := target
	draftTarget.EnvironmentID, draftTarget.ProjectID = draft.MCP.EnvironmentID, "draft-project"
	draft.Routes = map[string]Target{"draft": draftTarget}
	config.AdditionalAccounts = []Config{draft}

	var probed, connected []string
	failDraft := false
	probe := func(_ context.Context, app, secretPath, tenant string) error {
		probed = append(probed, app)
		want := config
		if app == draft.AppID {
			want = draft
		}
		if app != want.AppID || secretPath != want.AppSecretFile || (want.Tenant != "" && tenant != want.Tenant) {
			t.Fatalf("Feishu probe used the wrong account: %s %s", tenant, app)
		}
		if failDraft && app == draft.AppID {
			return fmt.Errorf("synthetic identity mismatch")
		}
		return nil
	}
	connect := func(_ context.Context, got MCPConfig) (ToolCaller, func(), error) {
		connected = append(connected, got.EnvironmentID)
		want, project := config.MCP, target
		if got.EnvironmentID == draft.MCP.EnvironmentID {
			want, project = draft.MCP, draftTarget
		}
		if got != want {
			t.Fatal("MCP probe used the wrong account credentials")
		}
		return scriptedCaller(func(name string, args map[string]any) (any, error) {
			switch name {
			case "t3_environment_read":
				return map[string]string{"environmentId": project.EnvironmentID}, nil
			case "t3_project_read":
				if args["projectId"] != project.ProjectID {
					t.Fatalf("unexpected project probe: %v", args)
				}
				return map[string]string{"id": project.ProjectID}, nil
			case "orchestrator_capabilities":
				return T3Capabilities{RuntimeMode: "approval-required", Providers: []T3Provider{{
					ID: project.ProviderID, Driver: project.Driver, Models: []T3Model{{
						ID: project.Model, Options: []T3ModelOption{{ID: "effort", Values: []any{"high"}}},
					}},
				}}}, nil
			default:
				t.Fatalf("unexpected tool %s", name)
				return nil, nil
			}
		}), func() {}, nil
	}

	report := checkReceiverConfig(t.Context(), config, true, probe, connect)
	if report.Passed || report.AllAccounts == nil || !report.AllAccounts.Passed ||
		!slices.Equal(probed, []string{config.AppID, draft.AppID}) ||
		!slices.Equal(connected, []string{config.MCP.EnvironmentID, draft.MCP.EnvironmentID}) {
		t.Fatalf("unexpected disabled account check: %+v", report)
	}
	for _, row := range report.Accounts {
		if row.Enabled || len(row.Checks) != 4 || row.Checks[2].Check != "route" || row.Checks[2].Status != "ok" {
			t.Fatalf("preflight changed receiver authorization or skipped route: %+v", row)
		}
	}

	config.ReceiverAuthorized, failDraft = true, true
	report = checkReceiverConfig(t.Context(), config, true, probe, connect)
	if !report.Passed || report.AllAccounts == nil || report.AllAccounts.Passed {
		t.Fatalf("a disabled account failure must affect only the expanded result: %+v", report)
	}
}

func TestReceiverPreflightReportsTemplateFieldsBeforeProbes(t *testing.T) {
	config := testConfig(t)
	config.ReceiverAuthorized = false
	config.AppID = "REPLACE_APP_ID"
	target := config.Routes["mac"]
	target.ProjectID = "REPLACE_BUILD_PROJECT_ID"
	config.Routes["mac"] = target
	report := checkReceiverConfig(t.Context(), config, true, func(context.Context, string, string, string) error {
		t.Fatal("template triggered a Feishu request")
		return nil
	}, func(context.Context, MCPConfig) (ToolCaller, func(), error) {
		t.Fatal("template opened an MCP connection")
		return nil, nil, nil
	})
	check := report.Accounts[0].Checks[0]
	if report.Passed || report.AllAccounts == nil || report.AllAccounts.Passed || check.Check != "setup" || check.Status != "failed" ||
		!slices.Equal(check.Fields, []string{"appId", `routes["mac"].projectId`}) {
		t.Fatalf("missing startup field diagnostics: %+v", report)
	}
}

func TestFeishuPreflightVerifiesTheCurrentSecret(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case larkcore.TenantAccessTokenInternalUrlPath:
			var req larkcore.SelfBuiltAppAccessTokenReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.AppID != "app" || r.Method != http.MethodPost {
				t.Fatal("unexpected token request")
			}
			if req.AppSecret == "first" {
				fmt.Fprint(w, `{"code":0,"tenant_access_token":"synthetic-token"}`)
			} else {
				fmt.Fprint(w, `{"code":100,"msg":"synthetic private detail"}`)
			}
		case "/open-apis/bot/v3/info":
			if r.Header.Get("Authorization") != "Bearer synthetic-token" || r.Method != http.MethodGet {
				t.Fatal("identity check did not use the fresh token")
			}
			fmt.Fprint(w, `{"code":0,"bot":{"open_id":"synthetic-bot"}}`)
		default:
			t.Fatalf("unexpected Feishu operation %s", r.URL.Path)
		}
		return w.Result(), nil
	})
	client := lark.NewClient("app", "first", lark.WithHttpClient(&http.Client{Transport: transport}),
		lark.WithEnableTokenCache(false), lark.WithLogger(feishuProbeLogger{}))
	if err := checkFeishuAppIdentity(context.Background(), client, "app", "first"); err != nil {
		t.Fatal(err)
	}
	if err := checkFeishuAppIdentity(context.Background(), client, "app", "wrong"); err == nil ||
		err.Error() != "无法取得应用令牌。请检查租户、应用 ID、密钥和网络" {
		t.Fatalf("a stale credential passed or exposed raw details: %v", err)
	}
}
