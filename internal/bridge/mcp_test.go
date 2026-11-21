package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type scriptedCaller func(string, map[string]any) (any, error)

func (f scriptedCaller) Call(_ context.Context, name string, args, out any) error {
	b, _ := json.Marshal(args)
	var a map[string]any
	json.Unmarshal(b, &a)
	v, err := f(name, a)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	b, _ = json.Marshal(v)
	return json.Unmarshal(b, out)
}
func pageFor(target Target) map[string]any {
	return map[string]any{"thread": map[string]any{"threadId": "thread", "projectId": target.ProjectID, "providerInstanceId": target.ProviderID, "model": target.Model, "runtimeMode": target.RuntimeMode, "interactionMode": target.InteractionMode, "pendingRequestCount": 0, "pendingBackgroundTasks": []any{}}, "items": []any{}, "recentRuns": []any{map[string]any{"runId": "run", "status": "completed", "model": target.Model, "providerInstanceId": target.ProviderID}}, "hasMore": false, "nextPosition": nil}
}

func resultActivityFixture(target Target, original, wake string) map[string]any {
	p := pageFor(target)
	initial := p["recentRuns"].([]any)[0].(map[string]any)
	initial["runId"], initial["ordinal"] = original, 1
	initial["executedModel"] = target.Model
	p["recentRuns"] = append(p["recentRuns"].([]any), map[string]any{
		"runId": wake, "ordinal": 2, "status": "completed", "model": target.Model, "providerInstanceId": target.ProviderID, "executedModel": target.Model,
	})
	p["items"] = []any{
		map[string]any{"runId": original, "position": 2, "type": "subagent", "subagentId": "sample-child", "itemId": "child-item", "title": "sample", "status": "completed", "sourceThreadId": "thread", "visibility": "local"},
		map[string]any{"runId": wake, "position": 4, "type": "notification", "itemId": "turn-item:message:completed", "text": "Delegated task \"sample\" finished", "status": "completed", "sourceThreadId": "thread", "visibility": "local"},
	}
	return p
}

func TestObserveStopSettlementRequiresOriginalRun(t *testing.T) {
	for _, tt := range []struct {
		name, waitedRun, waitedStatus, readStatus string
		timedOut                                  any
		want                                      bool
	}{
		{"original interrupted", "run", "interrupted", "interrupted", false, true},
		{"other wait run", "result", "completed", "interrupted", false, false},
		{"original wait still running", "run", "running", "interrupted", false, false},
		{"original read still running", "run", "interrupted", "running", false, false},
		{"original absent from read", "run", "interrupted", "", false, false},
		{"timed out", "run", "interrupted", "interrupted", true, false},
		{"timeout flag unknown", "run", "interrupted", "interrupted", nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			target := Target{ProjectID: "project", ProviderID: "provider", Model: "model"}
			caller := scriptedCaller(func(name string, args map[string]any) (any, error) {
				switch name {
				case "t3_thread_wait":
					if args["runId"] != "run" || args["untilDelegatedWorkSettled"] != true {
						t.Fatal("wait must target the original run and its delegated work")
					}
					return map[string]any{
						"threadId": "thread", "runId": tt.waitedRun, "status": tt.waitedStatus, "timedOut": tt.timedOut,
						"resultRun": map[string]any{"runId": "result", "status": "completed"},
					}, nil
				case "t3_thread_read":
					if args["view"] == "activity" {
						return resultActivityFixture(target, "run", "result"), nil
					}
					page := pageFor(target)
					runs := []any{map[string]any{"runId": "result", "status": "completed", "model": target.Model, "providerInstanceId": target.ProviderID}}
					if tt.readStatus != "" {
						runs = append(runs, map[string]any{"runId": "run", "status": tt.readStatus, "model": target.Model, "providerInstanceId": target.ProviderID})
					}
					page["recentRuns"] = runs
					page["items"] = []any{map[string]any{"position": 1, "type": "user_message", "runId": "run", "createdBy": "agent", "creationSource": "mcp",
						"sourceThreadId": "thread", "visibility": "local", "text": "original"}}
					return page, nil
				default:
					t.Fatalf("unexpected tool %s", name)
					return nil, nil
				}
			})
			o, err := (&T3{Calls: caller}).Observe(t.Context(), target, &Task{ThreadID: "thread", RunID: "run", RequestMessage: "original"})
			if err != nil || o.OriginalRunSettled != tt.want || (o.ResultRunID == "result") != tt.want {
				t.Fatalf("status=%s original settled=%t error=%v, want original settled=%t", o.Status, o.OriginalRunSettled, err, tt.want)
			}
		})
	}
}

func TestOfficialMCPTransportStructuredAndText(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-t3", Version: "test"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "structured"}, func(_ context.Context, _ *mcp.CallToolRequest, in struct {
		Value string `json:"value"`
	}) (*mcp.CallToolResult, map[string]string, error) {
		return nil, map[string]string{"value": in.Value}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "text"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"value":"text"}`}}}, nil, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "error"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return nil, nil, errors.New("private failure text")
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	requests := 0
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Error("missing token reference")
		}
		handler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()
	token := filepath.Join(t.TempDir(), "mcp.secret")
	os.WriteFile(token, []byte("synthetic-token"), 0600)
	c, err := ConnectMCP(context.Background(), MCPConfig{Endpoint: httpServer.URL + "/mcp", TokenFile: token})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Session.Close()
	for _, name := range []string{"structured", "text"} {
		var out map[string]string
		var args any = struct{}{}
		if name == "structured" {
			args = map[string]string{"value": "ok"}
		}
		if err = c.Call(context.Background(), name, args, &out); err != nil || out["value"] == "" {
			t.Fatalf("%s %v", name, err)
		}
	}
	if err = c.Call(context.Background(), "error", struct{}{}, nil); err == nil || strings.Contains(err.Error(), "private failure") {
		t.Fatal("tool failure leaked details")
	}
	if requests < 4 {
		t.Fatal("SDK not exercised")
	}
}

func TestMCPPreflightAndCatalogOperateAccess(t *testing.T) {
	target := Target{
		EnvironmentID: "fixture-environment", ProjectID: "fixture-project",
		ProviderID: "fixture-provider", Driver: "codex", Model: "fixture-model",
		RuntimeMode: "approval-required", InteractionMode: "default",
		Options: map[string]any{},
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-t3", Version: "test"}, nil)
	var toolCalls atomic.Int32
	for name, response := range map[string]map[string]any{
		"t3_environment_read": {"environmentId": target.EnvironmentID},
		"t3_project_read":     {"id": target.ProjectID},
		"t3_project_list":     {"projects": []Project{{ID: target.ProjectID}}},
		"t3_thread_read":      metadataPageFor(target),
		"t3_thread_configure": configurationFor(target),
		"orchestrator_capabilities": {
			"runtimeMode": target.RuntimeMode,
			"providers": []T3Provider{
				{ID: target.ProviderID, Driver: target.Driver, Models: []T3Model{{ID: target.Model}}},
			},
		},
	} {
		mcp.AddTool(server, &mcp.Tool{Name: name}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, map[string]any, error) {
			toolCalls.Add(1)
			return nil, response, nil
		})
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Error("incorrect MCP endpoint or bearer token")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()

	const (
		readOnly = `{"access_token":"synthetic-token","scope":"orchestration:read"}`
		operate  = `{"access_token":"synthetic-token","scope":"orchestration:read orchestration:operate"}`
	)
	for _, tc := range []struct {
		name, file, credential, access string
	}{
		{"read-only", "oauth.json", readOnly, "missing"},
		{"similar scope", "oauth.json", `{"access_token":"synthetic-token","scope":"orchestration:read orchestration:operate-extra"}`, "missing"},
		{"operate", "oauth.json", operate, "granted"},
		{"legacy OAuth", "oauth.json", `{"access_token":"synthetic-token"}`, "unknown"},
		{"raw token", "mcp.secret", "synthetic-token", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			cfg := MCPConfig{Endpoint: httpServer.URL + "/mcp", EnvironmentID: target.EnvironmentID, TokenFile: filepath.Join(t.TempDir(), tc.file)}
			if err := os.WriteFile(cfg.TokenFile, []byte(tc.credential), 0600); err != nil {
				t.Fatal(err)
			}
			client, err := ConnectMCP(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Session.Close()

			catalog, err := DiscoverT3(ctx, cfg, target.EnvironmentID, target.Model)
			if err != nil {
				t.Fatal(err)
			}
			if catalog.OperateAccess != tc.access {
				t.Fatalf("catalog operate access = %q, want %q", catalog.OperateAccess, tc.access)
			}
			if len(catalog.Projects) != 1 ||
				catalog.Projects[0].ID != target.ProjectID || len(catalog.Providers) != 1 ||
				len(catalog.Providers[0].Models) != 1 || catalog.Providers[0].Models[0].ID != target.Model {
				t.Fatal("catalog omitted the project or model")
			}
			payload, err := json.Marshal(catalog)
			if err != nil {
				t.Fatal(err)
			}
			publicJSON := string(payload)
			if !strings.Contains(publicJSON, `"operateAccess":"`+tc.access+`"`) {
				t.Fatal("catalog JSON omitted the operate access status")
			}

			if tc.name == "read-only" {
				// Editing the file must not upgrade the already connected session.
				if err := os.WriteFile(cfg.TokenFile, []byte(operate), 0600); err != nil {
					t.Fatal(err)
				}
			}
			api := T3{Calls: client, Config: cfg}
			before := toolCalls.Load()
			preflightErr := api.Preflight(ctx, target)
			for _, private := range []string{"synthetic-token", "orchestration:read", "orchestration:operate"} {
				if strings.Contains(publicJSON, private) || preflightErr != nil && strings.Contains(preflightErr.Error(), private) {
					t.Fatal("catalog or preflight exposed credential contents")
				}
			}
			if tc.access == "missing" {
				if !errors.Is(preflightErr, ErrMCPOperationNotGranted) || toolCalls.Load() != before {
					t.Fatalf("preflight should fail before calling MCP tools: %v", preflightErr)
				}
				task := &Task{ThreadID: "thread", RunID: "run"}
				if native, err := api.InspectThread(ctx, target, task); err != nil || !native.Quiet {
					t.Fatalf("read-only credential could not inspect an existing quiet run: %v", err)
				}
				before = toolCalls.Load()
				if err := api.OrganizeThread(ctx, target, task, "settle"); !errors.Is(err, ErrMCPOperationNotGranted) {
					t.Fatalf("expected metadata write to be blocked: %v", err)
				}
				if err := api.RenameThread(ctx, target, task, "other"); !errors.Is(err, ErrMCPOperationNotGranted) || toolCalls.Load() != before {
					t.Fatalf("metadata write called MCP with read-only scope: %v", err)
				}
			} else if preflightErr != nil || toolCalls.Load() == before {
				t.Fatalf("preflight should validate the target through MCP: %v", preflightErr)
			}
		})
	}
}

func TestT3PreflightAndIdleLaunch(t *testing.T) {
	target := testConfig(t).Routes["nas"]
	for _, bad := range []string{"", "omitted-provider-modes", "environment", "project", "provider", "driver", "model", "permissions", "provider-mode", "options"} {
		t.Run("preflight/"+bad, func(t *testing.T) {
			api := &T3{Calls: scriptedCaller(func(name string, a map[string]any) (any, error) {
				switch name {
				case "t3_environment_read":
					id := target.EnvironmentID
					if bad == "environment" {
						id = "other"
					}
					return map[string]string{"environmentId": id}, nil
				case "t3_project_read":
					id := target.ProjectID
					if bad == "project" {
						id = "other"
					}
					return map[string]any{"id": id, "deletedAt": nil, "archivedAt": nil}, nil
				case "orchestrator_capabilities":
					provider, model, mode, value := target.ProviderID, target.Model, "approval-required", "high"
					driver := "opencode"
					if bad == "driver" {
						driver = "codex"
					}
					if bad == "provider" {
						provider = "other"
					}
					if bad == "model" {
						model = "other"
					}
					if bad == "permissions" {
						mode = "read-only"
					}
					if bad == "options" {
						value = "low"
					}
					candidate := map[string]any{"providerInstanceId": provider, "driverKind": driver, "runtimeModes": []string{"approval-required"},
						"models": []any{map[string]any{"id": model, "options": []any{map[string]any{"id": "effort", "values": []string{value}}}}}}
					if bad == "omitted-provider-modes" {
						delete(candidate, "runtimeModes")
					}
					if bad == "provider-mode" {
						candidate["runtimeModes"] = []string{"auto"}
					}
					return map[string]any{"runtimeMode": mode, "providers": []any{candidate}}, nil
				}
				return nil, errors.New("unexpected tool")
			})}
			err := api.Preflight(context.Background(), target)
			if (err != nil) != (bad != "" && bad != "omitted-provider-modes") {
				t.Fatalf("unexpected error %v", err)
			}
		})
	}
	api := &T3{Calls: scriptedCaller(func(name string, a map[string]any) (any, error) {
		if name != "t3_thread_launch" {
			t.Fatal(name)
		}
		if _, ok := a["message"]; ok {
			t.Fatal("production prompt in unverified launch")
		}
		selection := a["modelSelection"].(map[string]any)
		if selection["provider"] != "opencode" || selection["instanceId"] != target.ProviderID || selection["model"] != target.Model || a["clientRequestId"] != "stable-id" {
			t.Fatal("implicit executor or request ID")
		}
		return map[string]any{"threadId": "thread", "runId": nil, "projectId": target.ProjectID}, nil
	})}
	if _, err := api.Open(context.Background(), target, "stable-id", "检查磁盘状态"); err != nil {
		t.Fatal(err)
	}
}
func TestT3PaginationReconciliationTextAndChildren(t *testing.T) {
	target := testConfig(t).Routes["nas"]
	task := &Task{ThreadID: "thread", RequestID: "request", RequestMessage: "用户原文\nrequestId: request", DispatchSource: message("one", "用户原文")}
	reads := 0
	api := &T3{Calls: scriptedCaller(func(name string, a map[string]any) (any, error) {
		if name == "t3_thread_wait" {
			return map[string]any{"threadId": "thread", "resultRun": map[string]any{"runId": "run", "status": "completed"}}, nil
		}
		p := pageFor(target)
		if a["threadId"] == "child" {
			p["thread"].(map[string]any)["threadId"] = "child"
			p["recentRuns"].([]any)[0].(map[string]any)["status"] = "running"
			return p, nil
		}
		if a["itemId"] == "request-item" {
			p["items"] = []any{map[string]any{"itemId": "request-item", "text": "requestId: request", "textTruncated": false}}
			return p, nil
		}
		reads++
		if a["afterPosition"].(float64) == -1 {
			items := []any{}
			for i := 0; i < 100; i++ {
				items = append(items, map[string]any{"position": i, "createdBy": "system", "text": "older"})
			}
			p["items"] = items
			p["hasMore"] = true
			p["nextPosition"] = 99
		} else {
			p["items"] = []any{map[string]any{"position": 100, "itemId": "request-item", "createdBy": "user", "runId": "run", "text": "用户原文\n", "textTruncated": true, "nextTextOffset": 5}, map[string]any{"position": 101, "createdBy": "agent", "runId": "run", "text": "最终结论"}}
			p["thread"].(map[string]any)["pendingBackgroundTasks"] = []any{map[string]any{"taskId": "task-child", "childThreadId": "child"}}
		}
		return p, nil
	})}
	o, err := api.Observe(context.Background(), target, task)
	if err != nil || !o.FoundRequest || o.RunID != "run" || o.Summary != "最终结论" || len(o.Tasks) == 0 || len(o.ChildThreads) != 1 || reads != 2 {
		t.Fatalf("observation=%+v err=%v reads=%d", o, err, reads)
	}
	// Parent no longer lists the child: durable identity still prevents premature completion.
	task.RunID = "run"
	task.ChildThreads = o.ChildThreads
	api.Calls = scriptedCaller(func(name string, a map[string]any) (any, error) {
		if name == "t3_thread_wait" {
			return map[string]any{"threadId": "thread", "resultRun": map[string]any{"runId": "run", "status": "completed"}}, nil
		}
		p := pageFor(target)
		if a["threadId"] == "child" {
			p["thread"].(map[string]any)["threadId"] = "child"
			p["recentRuns"].([]any)[0].(map[string]any)["status"] = "failed"
		}
		return p, nil
	})
	o, err = api.Observe(context.Background(), target, task)
	if err != nil || o.Status != "failed" {
		t.Fatal("failed child lost after parent removed pending entry")
	}
}
func TestStopQueuedRunAndNoProviderFallback(t *testing.T) {
	target := testConfig(t).Routes["nas"]
	task := &Task{ID: "task", ThreadID: "thread", RunID: "run"}
	cancelled := false
	api := &T3{Calls: scriptedCaller(func(name string, a map[string]any) (any, error) {
		switch name {
		case "t3_thread_wait":
			return map[string]any{"threadId": "thread", "resultRun": map[string]any{"runId": "run", "status": "completed"}}, nil
		case "t3_thread_configure":
			return configurationFor(target), nil
		case "t3_thread_read":
			p := pageFor(target)
			p["recentRuns"].([]any)[0].(map[string]any)["status"] = "queued"
			return p, nil
		case "t3_queue":
			if a["action"] != "cancel" || a["queuedRunId"] != "run" {
				t.Fatal("wrong queue cancellation")
			}
			cancelled = true
			return map[string]any{}, nil
		default:
			t.Fatal("unexpected interrupt/fallback " + name)
			return nil, nil
		}
	})}
	if err := api.Stop(context.Background(), target, task); err != nil || !cancelled {
		t.Fatal(err)
	}
}

func configurationFor(target Target) map[string]any {
	return map[string]any{"threadId": "thread", "modelSelection": map[string]any{"provider": target.Driver, "instanceId": target.ProviderID, "model": target.Model, "options": target.Options}, "runtimeMode": target.RuntimeMode, "interactionMode": target.InteractionMode}
}

func TestSettledResultRunReplacesEarlyParentReply(t *testing.T) {
	target := testConfig(t).Routes["nas"]
	task := &Task{ThreadID: "thread", RunID: "run", RequestMessage: "原始输入"}
	api := &T3{Calls: scriptedCaller(func(name string, a map[string]any) (any, error) {
		if name == "t3_thread_wait" {
			// The latest T3 run is independent. Recover the prior, attributable wake.
			return map[string]any{"threadId": "thread", "runId": "run", "status": "completed", "timedOut": false,
				"resultRun": map[string]string{"runId": "independent-run", "status": "completed"}}, nil
		}
		p := resultActivityFixture(target, "run", "settled-run")
		p["recentRuns"] = append(p["recentRuns"].([]any), map[string]any{
			"runId": "independent-run", "ordinal": 3, "status": "completed", "model": target.Model, "providerInstanceId": target.ProviderID,
		})
		independent := map[string]any{"position": 5, "type": "user_message", "createdBy": "user", "runId": "independent-run", "text": "其他输入",
			"sourceThreadId": "thread", "visibility": "local"}
		if a["view"] == "activity" {
			p["items"] = append(p["items"].([]any), independent)
			return p, nil
		}
		p["items"] = []any{
			map[string]any{"position": 1, "type": "user_message", "runId": "run", "createdBy": "agent", "creationSource": "mcp",
				"sourceThreadId": "thread", "visibility": "local", "text": "原始输入"},
			map[string]any{"position": 3, "createdBy": "agent", "runId": "run", "text": "早期进度"},
			map[string]any{"position": 4, "createdBy": "agent", "runId": "settled-run", "type": "assistant_message", "status": "completed",
				"sourceThreadId": "thread", "visibility": "local", "creationSource": "provider", "text": "异步收口后的最终结论"},
			independent,
			map[string]any{"position": 6, "createdBy": "agent", "runId": "independent-run", "text": "其他答复"},
		}
		return p, nil
	})}
	o, err := api.Observe(context.Background(), target, task)
	if err != nil || o.Summary != "异步收口后的最终结论" || o.ResultRunID != "settled-run" || o.RunID != task.RunID || o.Unsettled || len(o.Tasks) != 0 {
		t.Fatalf("%+v %v", o, err)
	}
}
func TestVerifyReadOnlyConfigurationAndDrift(t *testing.T) {
	target := testConfig(t).Routes["nas"]
	for _, drift := range []bool{false, true} {
		api := &T3{Calls: scriptedCaller(func(name string, a map[string]any) (any, error) {
			if name == "t3_thread_read" {
				return pageFor(target), nil
			}
			if name != "t3_thread_configure" {
				t.Fatal(name)
			}
			if _, ok := a["modelSelection"]; ok {
				t.Fatal("verification mutated thread")
			}
			r := configurationFor(target)
			if drift {
				r["modelSelection"].(map[string]any)["options"] = map[string]string{"effort": "low"}
			}
			return r, nil
		})}
		if err := api.Verify(context.Background(), target, "thread"); (err != nil) != drift {
			t.Fatal("configuration drift accepted", err)
		}
	}
}
func TestParentCompletedWithoutSettledResultStaysPending(t *testing.T) {
	target := testConfig(t).Routes["nas"]
	task := &Task{ThreadID: "thread", RunID: "run"}
	api := &T3{Calls: scriptedCaller(func(name string, a map[string]any) (any, error) {
		if name == "t3_thread_wait" {
			return map[string]any{"threadId": "thread", "timedOut": true}, nil
		}
		return pageFor(target), nil
	})}
	o, err := api.Observe(context.Background(), target, task)
	if err != nil || !o.Unsettled || len(o.Tasks) != 0 {
		t.Fatal("missing settlement completed early or became a fake child", err)
	}
}

func TestVerifyOfficialNASConfigurationShape(t *testing.T) {
	target := testConfig(t).Routes["nas"]
	target.ProviderID = "nas-admin"
	target.Model = "codebuddy-direct/deepseek-v4.1-flash"
	target.Options = map[string]any{}
	target.RuntimeMode = "full-access"
	api := &T3{Calls: scriptedCaller(func(name string, a map[string]any) (any, error) {
		if name == "t3_thread_read" {
			return pageFor(target), nil
		}
		if name != "t3_thread_configure" {
			t.Fatal("unexpected tool", name)
		}
		if _, ok := a["modelSelection"]; ok {
			t.Fatal("read-only verify wrote configuration")
		}
		return map[string]any{"threadId": "thread", "modelSelection": map[string]any{"instanceId": "nas-admin", "model": "codebuddy-direct/deepseek-v4.1-flash", "options": []any{}}, "runtimeMode": "full-access", "interactionMode": "default"}, nil
	})}
	if err := api.Verify(context.Background(), target, "thread"); err != nil {
		t.Fatal("official NAS read-only shape rejected", err)
	}
}

func TestVerifyOptionsShapesAndIdentityBoundaries(t *testing.T) {
	for _, kind := range []string{"array", "map", "missing_provider_map", "provider_conflict", "option_conflict", "duplicate_option", "missing_value", "invalid_options", "instance_conflict", "model_conflict", "runtime_conflict", "interaction_conflict", "thread_conflict", "project_conflict", "read_instance_conflict"} {
		t.Run(kind, func(t *testing.T) {
			target := testConfig(t).Routes["nas"]
			target.Options = map[string]any{"effort": "high", "thinking": false}
			api := &T3{Calls: scriptedCaller(func(name string, a map[string]any) (any, error) {
				if name == "t3_thread_read" {
					p := pageFor(target)
					if kind == "project_conflict" {
						p["thread"].(map[string]any)["projectId"] = "other"
					}
					if kind == "read_instance_conflict" {
						p["thread"].(map[string]any)["providerInstanceId"] = "other"
					}
					return p, nil
				}
				if name != "t3_thread_configure" {
					t.Fatal(name)
				}
				r := configurationFor(target)
				selection := r["modelSelection"].(map[string]any)
				delete(selection, "provider")
				selection["options"] = []any{map[string]any{"id": "thinking", "value": false}, map[string]any{"id": "effort", "value": "high"}}
				switch kind {
				case "map":
					selection["options"] = target.Options
					selection["provider"] = target.Driver
				case "missing_provider_map":
					selection["options"] = target.Options
				case "provider_conflict":
					selection["provider"] = "codex"
				case "option_conflict":
					selection["options"] = []any{map[string]any{"id": "effort", "value": "low"}, map[string]any{"id": "thinking", "value": false}}
				case "duplicate_option":
					selection["options"] = []any{map[string]any{"id": "effort", "value": "low"}, map[string]any{"id": "effort", "value": "high"}, map[string]any{"id": "thinking", "value": false}}
				case "missing_value":
					selection["options"] = []any{map[string]any{"id": "effort"}}
				case "invalid_options":
					selection["options"] = "invalid"
				case "instance_conflict":
					selection["instanceId"] = "other"
				case "model_conflict":
					selection["model"] = "other"
				case "runtime_conflict":
					r["runtimeMode"] = "full-access"
				case "interaction_conflict":
					r["interactionMode"] = "plan"
				case "thread_conflict":
					r["threadId"] = "other"
				}
				return r, nil
			})}
			err := api.Verify(context.Background(), target, "thread")
			valid := kind == "array" || kind == "map" || kind == "missing_provider_map"
			if (err == nil) != valid {
				t.Fatalf("configuration verification mismatch: %s %v", kind, err)
			}
		})
	}
}

func TestStopOnlyKnownOwnedRun(t *testing.T) {
	target := testConfig(t).Routes["nas"]
	for _, mode := range []string{"missing_id", "not_found", "other_running", "other_queued", "owned_queued_with_manual", "wrong_receipt", "no_active_run", "background"} {
		t.Run(mode, func(t *testing.T) {
			task := &Task{ID: "bridge", ThreadID: "thread", RunID: "run"}
			if mode == "missing_id" {
				task.RunID = ""
			}
			mutations := 0
			api := &T3{Calls: scriptedCaller(func(name string, a map[string]any) (any, error) {
				switch name {
				case "t3_thread_configure":
					return configurationFor(target), nil
				case "t3_thread_read":
					p := pageFor(target)
					runs := p["recentRuns"].([]any)
					if mode == "not_found" {
						runs[0].(map[string]any)["runId"] = "manual"
					}
					if mode == "other_running" || mode == "other_queued" || mode == "owned_queued_with_manual" {
						state := "running"
						if mode == "other_queued" {
							state = "queued"
						}
						runs = append(runs, map[string]any{"runId": "manual", "status": state})
					}
					if mode == "owned_queued_with_manual" {
						runs[0].(map[string]any)["status"] = "queued"
					}
					if mode == "background" {
						p["thread"].(map[string]any)["pendingBackgroundTasks"] = []map[string]string{{"taskId": "child"}}
					}
					p["recentRuns"] = runs
					return p, nil
				case "t3_queue":
					mutations++
					if a["queuedRunId"] != "run" || mode != "owned_queued_with_manual" {
						t.Fatal("cancelled other/unknown run")
					}
					return map[string]any{}, nil
				case "t3_thread_interrupt":
					mutations++
					if _, whole := a["stop"]; whole || a["threadId"] != "thread" || a["runId"] != "run" {
						t.Fatal("interrupt was not restricted to the original run")
					}
					switch mode {
					case "wrong_receipt":
						return map[string]any{"threadId": "thread", "runId": "manual", "status": "interrupt_requested"}, nil
					case "no_active_run":
						return map[string]any{"threadId": "thread", "runId": nil, "status": "no_active_run"}, nil
					default:
						return map[string]any{"threadId": "thread", "runId": "run", "status": "completed"}, nil
					}
				default:
					mutations++
					t.Fatal("thread-wide interrupt could affect manual work", name)
					return nil, nil
				}
			})}
			err := api.Stop(context.Background(), target, task)
			switch mode {
			case "owned_queued_with_manual", "other_running", "other_queued":
				if err != nil || mutations != 1 {
					t.Fatal("owned run not safely addressed", err)
				}
			case "wrong_receipt", "no_active_run":
				if err == nil || mutations != 1 {
					t.Fatal("unbound acknowledgement was accepted", err)
				}
			case "background":
				if !errors.Is(err, errStopBackgroundPending) || mutations != 1 {
					t.Fatal("pending work was lost after interrupt", err)
				}
			default:
				if err == nil || mutations != 0 {
					t.Fatal("unknown run was stopped", err)
				}
			}
		})
	}
}

func mcpAgentInputPage(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile("../../testdata/t3-mcp-agent-input.json")
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	if err = json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestT3MCPRequestInputIdentity(t *testing.T) {
	target := testConfig(t).Routes["nas"]
	for _, tc := range []struct {
		name, creator, kind, channel, visibility, source, run, knownRun string
		want                                                            bool
	}{
		{"mcp-unknown-ack", "agent", "user_message", "", "local", "thread", "run", "", true},
		{"mcp-known-run", "agent", "user_message", "", "local", "thread", "run", "run", true},
		{"legacy-user", "user", "", "", "", "", "run", "", true},
		{"legacy-user-message", "user", "message", "", "", "", "run", "", true},
		{"user-message", "user", "user_message", "", "local", "thread", "run", "", true},
		{"assistant-echo", "agent", "assistant_message", "", "local", "thread", "run", "", false},
		{"legacy-agent-echo", "agent", "", "", "local", "thread", "run", "", false},
		{"reasoning-echo", "agent", "reasoning", "", "local", "thread", "run", "", false},
		{"tool-echo", "agent", "tool_result", "", "local", "thread", "run", "", false},
		{"user-assistant-echo", "user", "assistant_message", "", "local", "thread", "run", "", false},
		{"user-reasoning-echo", "user", "reasoning", "", "local", "thread", "run", "", false},
		{"user-tool-echo", "user", "tool_result", "", "local", "thread", "run", "", false},
		{"hidden-channel", "agent", "user_message", "analysis", "local", "thread", "run", "", false},
		{"missing-locality", "agent", "user_message", "", "", "thread", "run", "", false},
		{"missing-source", "agent", "user_message", "", "local", "", "run", "", false},
		{"foreign-source", "agent", "user_message", "", "local", "foreign", "run", "", false},
		{"inherited", "agent", "user_message", "", "inherited", "thread", "run", "", false},
		{"user-foreign-source", "user", "user_message", "", "local", "foreign", "run", "", false},
		{"user-inherited", "user", "user_message", "", "inherited", "thread", "run", "", false},
		{"known-other-mcp-run", "agent", "user_message", "", "local", "thread", "other", "run", false},
		{"known-other-user-run", "user", "", "", "", "", "other", "run", false},
		{"missing-run", "agent", "user_message", "", "local", "thread", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := mcpAgentInputPage(t)
			item := p["items"].([]any)[0].(map[string]any)
			item["createdBy"], item["type"], item["channel"] = tc.creator, tc.kind, tc.channel
			item["visibility"], item["sourceThreadId"], item["runId"] = tc.visibility, tc.source, tc.run
			api := &T3{Calls: scriptedCaller(func(name string, a map[string]any) (any, error) {
				switch name {
				case "t3_thread_wait":
					return map[string]any{"threadId": "thread", "resultRun": map[string]string{"runId": "run", "status": "completed"}}, nil
				case "t3_thread_read":
					if a["itemId"] != nil {
						t.Fatal("non-input expanded")
					}
					return p, nil
				default:
					t.Fatal("reconciliation attempted a mutation", name)
					return nil, nil
				}
			})}
			o, err := api.Observe(context.Background(), target, &Task{ThreadID: "thread", RunID: tc.knownRun, RequestMessage: "用户原文\nrequestId: request\n"})
			if err != nil || o.FoundRequest != tc.want || (tc.knownRun != "" && o.RunID != tc.knownRun) || (!tc.want && tc.knownRun == "" && o.RunID != "") {
				t.Fatalf("request identity: observation=%+v err=%v", o, err)
			}
			if tc.want && (o.RunID != "run" || o.Summary != "" || o.Progress != "") {
				t.Fatalf("input was lost or displayed as assistant output: %+v", o)
			}
		})
	}
}

func TestT3MCPRequestInputPaginationAndTruncation(t *testing.T) {
	target := testConfig(t).Routes["nas"]
	for _, tc := range []struct {
		name, creator, kind, visibility, source, run, knownRun string
		want                                                   bool
	}{
		{"mcp-input", "agent", "user_message", "local", "thread", "run", "", true},
		{"legacy-input", "user", "", "", "", "run", "", true},
		{"assistant-echo", "agent", "assistant_message", "local", "thread", "run", "", false},
		{"foreign-input", "agent", "user_message", "local", "foreign", "run", "", false},
		{"inherited-input", "agent", "user_message", "inherited", "thread", "run", "", false},
		{"other-run", "user", "", "", "", "other", "run", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads, tails := 0, 0
			api := &T3{Calls: scriptedCaller(func(name string, a map[string]any) (any, error) {
				if name == "t3_thread_wait" {
					return map[string]any{"threadId": "thread", "timedOut": true}, nil
				}
				if name != "t3_thread_read" {
					t.Fatal("reconciliation attempted a mutation", name)
				}
				p := mcpAgentInputPage(t)
				if a["itemId"] != nil {
					tails++
					if a["itemId"] != "mcp-input" || a["textOffset"] != float64(5) {
						t.Fatal("wrong text continuation")
					}
					p["items"] = []any{map[string]any{"itemId": "mcp-input", "text": "requestId: request", "textTruncated": false}}
					return p, nil
				}
				reads++
				if a["afterPosition"] == float64(-1) {
					p["items"], p["hasMore"], p["nextPosition"] = []any{}, true, 71
					return p, nil
				}
				if a["afterPosition"] != float64(71) {
					t.Fatal("wrong history cursor")
				}
				item := p["items"].([]any)[0].(map[string]any)
				item["createdBy"], item["type"], item["visibility"], item["sourceThreadId"], item["runId"] = tc.creator, tc.kind, tc.visibility, tc.source, tc.run
				item["text"], item["textTruncated"], item["nextTextOffset"] = "用户原文\n", true, 5
				return p, nil
			})}
			o, err := api.Observe(context.Background(), target, &Task{ThreadID: "thread", RunID: tc.knownRun, RequestMessage: "用户原文\nrequestId: request\n"})
			if err != nil || o.FoundRequest != tc.want || reads != 2 || (tails == 1) != tc.want || (!tc.want && tails != 0) {
				t.Fatalf("pagination request identity: %+v err=%v reads=%d tails=%d", o, err, reads, tails)
			}
		})
	}
}

func TestT3MCPUnknownAckRecoveryAfterRestartDoesNotResend(t *testing.T) {
	e, x, _, _ := testEngine(t)
	x.sendErr = errors.New("lost ACK")
	accept(t, e, message("one", "/nas 只读核对"))
	tick(t, e)
	before := onlyTask(t, e)
	if before.Phase != "needs_reconcile" || before.RunID != "" || x.sends != 1 || before.RequestMessage != prompt(before) || !strings.HasSuffix(before.RequestMessage, "\n") || x.request != before.RequestMessage {
		t.Fatal("lost ACK fixture not fenced")
	}
	e.Store.Close()
	s, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e.Store = s
	e.T3 = &T3{Calls: scriptedCaller(func(name string, a map[string]any) (any, error) {
		if name == "t3_thread_wait" {
			if a["runId"] != "run" {
				t.Fatal("wait rebound to another run")
			}
			return map[string]any{"threadId": "thread", "resultRun": map[string]string{"runId": "run", "status": "completed"}}, nil
		}
		if name != "t3_thread_read" {
			t.Fatal("reconciliation attempted mutation", name)
		}
		p := mcpAgentInputPage(t)
		p["items"].([]any)[0].(map[string]any)["text"] = strings.TrimSpace(before.RequestMessage)
		p["items"] = append(p["items"].([]any), map[string]any{"position": 73, "createdBy": "agent", "type": "assistant_message", "runId": "run", "sourceThreadId": "thread", "visibility": "local", "text": "只读核对完成"})
		return p, nil
	})}
	tick(t, e)
	recovered := onlyTask(t, e)
	if recovered.RunID != "run" || recovered.NeedsReconcile || recovered.Phase != "settling" {
		t.Fatalf("accepted request not recovered read-only: %s %s", recovered.Phase, recovered.RunID)
	}
	tick(t, e)
	after := onlyTask(t, e)
	if after.Phase != "completed" || after.Result != "只读核对完成" || after.RequestID != before.RequestID || after.RequestMessage != before.RequestMessage || after.CardID != before.CardID || after.ThreadID != before.ThreadID || after.Source.Text != before.Source.Text || x.sends != 1 {
		t.Fatal("request replay, identity change or premature result")
	}
}

func TestT3MCPRequestWireTrimPreservesInternalContent(t *testing.T) {
	target := testConfig(t).Routes["nas"]
	task := &Task{ThreadID: "thread", RequestID: "request", RouteID: "nas", Target: target,
		DispatchSource: message("one", "## 标题\n**保留  双空格**\n`inline`\n\n```sh\nprintf 'hello'\n```\n| A | B |\n|---|---|\n| 1 | 2 |")}
	raw := prompt(task)
	originalText := task.DispatchSource.Text
	if !strings.HasSuffix(raw, "\n") {
		t.Fatal("product prompt no longer reproduces the wire boundary")
	}
	canonical := strings.TrimSpace(raw)
	outer := " \t\n" + raw + " \t\n"
	for _, tc := range []struct {
		name, expected, received string
		want                     bool
	}{
		{"product-original", raw, raw, true},
		{"product-trailing-newline-trimmed", raw, canonical, true},
		{"outer-original", outer, outer, true},
		{"outer-trimmed", outer, canonical, true},
		{"already-canonical", canonical, canonical, true},
		{"internal-space-changed", raw, strings.Replace(canonical, "保留  双空格", "保留 双空格", 1), false},
		{"internal-newline-changed", raw, strings.Replace(canonical, "\n\n", "\n", 1), false},
		{"markdown-changed", raw, strings.Replace(canonical, "**保留  双空格**", "保留  双空格", 1), false},
		{"request-id-changed", raw, strings.Replace(canonical, "requestId: request", "requestId: other", 1), false},
		{"body-changed", raw, strings.Replace(canonical, "printf 'hello'", "printf 'other'", 1), false},
		{"extra-prefix", raw, "额外正文\n" + canonical, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task.RequestMessage = tc.expected
			api := &T3{Calls: scriptedCaller(func(name string, a map[string]any) (any, error) {
				if name != "t3_thread_read" {
					t.Fatal("wire reconciliation attempted mutation", name)
				}
				p := mcpAgentInputPage(t)
				p["items"].([]any)[0].(map[string]any)["text"] = tc.received
				return p, nil
			})}
			o, err := api.Observe(context.Background(), target, task)
			if err != nil || o.FoundRequest != tc.want || (o.RunID != "") != tc.want || task.RequestMessage != tc.expected || task.DispatchSource.Text != originalText {
				t.Fatalf("wire trim mismatch: found=%v run=%q err=%v", o.FoundRequest, o.RunID, err)
			}
		})
	}
}
