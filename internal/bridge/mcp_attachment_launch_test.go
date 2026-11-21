package bridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const attachmentLaunchFixtureID = "mcp:launch:40ebb23f6b0432d971a120e4246ff525461158b4a0c10f7f63c0fb396bb87239"

func attachmentLaunchFixture() (Target, *Task) {
	target := Target{
		EnvironmentID: "local", ProjectID: "project", Driver: "codex", ProviderID: "codex-instance", Model: "test-model",
		Options: map[string]any{"Effort": "high", "Thinking": false}, RuntimeMode: "approval-required", InteractionMode: "default",
	}
	return target, &Task{
		AttachmentLaunch: &attachmentLaunchReceipt{ExpectedThreadID: attachmentLaunchFixtureID},
		ThreadMeta:       &ThreadMetadata{Title: "Synthetic upload"},
		RequestID:        "request-1", RequestMessage: "  Read this attachment.\n",
		Attachments: []Attachment{{ID: "pending-synthetic", Type: "file", Name: "fixture.txt", MIME: "text/plain", Size: 4}},
	}
}

func attachmentLaunchConfiguration(target Target, threadID string) map[string]any {
	r := configurationFor(target)
	r["threadId"] = threadID
	r["modelSelection"] = map[string]any{
		"instanceId": target.ProviderID, "model": target.Model,
		"options": []any{map[string]any{"id": "Thinking", "value": false}, map[string]any{"id": "Effort", "value": "high"}},
	}
	return r
}

func attachmentLaunchPage(target Target, id string) (map[string]any, map[string]any) {
	p := pageFor(target)
	thread := p["thread"].(map[string]any)
	thread["threadId"], thread["link"] = id, "https://example.invalid/thread"
	thread["createdBy"], thread["creationSource"] = "agent", "mcp"
	thread["parentThreadId"], thread["relationshipToParent"] = nil, nil
	p["recentRuns"].([]any)[0].(map[string]any)["ordinal"] = 1
	item := map[string]any{
		"position": 1, "type": "user_message", "visibility": "local", "sourceThreadId": id,
		"createdBy": "agent", "creationSource": "mcp", "itemId": "timeline-item", "messageId": id,
		"runId": "run", "text": "Read this attachment.",
	}
	p["items"] = []any{item}
	return p, item
}

func TestAttachmentLaunchReceipt(t *testing.T) {
	for _, kind := range []string{
		"success", "unknown-namespace", "remote", "lost", "project", "selection", "thread", "run",
		"read-project", "read-options", "read-runtime", "read-unavailable",
	} {
		t.Run(kind, func(t *testing.T) {
			target, task := attachmentLaunchFixture()
			// A valid explicit receipt may use a different ID algorithm.
			id := "thread-from-other-version"
			if kind == "read-unavailable" {
				id = task.AttachmentLaunch.ExpectedThreadID
			}
			launches, reads, configurations := 0, 0, 0
			caller := scriptedCaller(func(name string, args map[string]any) (any, error) {
				switch name {
				case attachmentLaunchTool:
					launches++
					want := map[string]any{
						"environmentId": target.EnvironmentID, "projectId": target.ProjectID, "title": task.ThreadMeta.Title,
						"runtimeMode": target.RuntimeMode, "interactionMode": target.InteractionMode,
						"clientRequestId": task.RequestID, "message": task.RequestMessage,
						"workspaceStrategy": map[string]any{"type": "root"},
						"modelSelection": map[string]any{
							"provider": target.Driver, "instanceId": target.ProviderID, "model": target.Model,
							"options": []any{map[string]any{"id": "Effort", "value": "high"}, map[string]any{"id": "Thinking", "value": false}},
						},
						"attachments": []any{map[string]any{"id": "pending-synthetic", "type": "file", "name": "fixture.txt", "mimeType": "text/plain", "sizeBytes": float64(4)}},
					}
					if !reflect.DeepEqual(args, want) {
						t.Fatalf("unexpected launch payload: %#v", args)
					}
					r := attachmentLaunchConfiguration(target, id)
					r["runId"], r["projectId"], r["link"] = "run", target.ProjectID, "https://example.invalid/thread"
					switch kind {
					case "lost":
						return nil, errors.New("synthetic lost reply")
					case "thread":
						r["threadId"] = " "
					case "run":
						r["runId"] = nil
					case "project":
						r["projectId"] = "another-project"
					case "selection":
						r["modelSelection"].(map[string]any)["model"] = "another-model"
					}
					return r, nil
				case "t3_thread_read":
					reads++
					if args["threadId"] != id {
						t.Fatalf("read used %v instead of the explicit receipt %s", args["threadId"], id)
					}
					if kind == "read-unavailable" {
						return nil, errors.New("synthetic read failure")
					}
					r, _ := attachmentLaunchPage(target, id)
					if kind == "read-project" {
						r["thread"].(map[string]any)["projectId"] = "another-project"
					}
					return r, nil
				case "t3_thread_configure":
					configurations++
					if len(args) != 2 || args["threadId"] != id || args["environmentId"] != target.EnvironmentID {
						t.Fatalf("unexpected configure args: %#v", args)
					}
					r := attachmentLaunchConfiguration(target, id)
					switch kind {
					case "read-options":
						r["modelSelection"].(map[string]any)["options"] = []any{}
					case "read-runtime":
						r["runtimeMode"] = "full-access"
					}
					return r, nil
				default:
					t.Fatal("unexpected tool", name)
					return nil, nil
				}
			})
			api := &T3{Calls: caller, Config: MCPConfig{EnvironmentID: target.EnvironmentID}}
			if kind == "remote" {
				target.EnvironmentID = "linked"
			}
			if kind == "unknown-namespace" {
				task.AttachmentLaunch.ExpectedThreadID = api.attachmentLaunchThreadID(task.RequestID)
				if task.AttachmentLaunch.ExpectedThreadID != "" {
					t.Fatal("unrecognized connection provided a lookup hint")
				}
			}
			ex, err := api.launchWithAttachments(context.Background(), target, task)
			if kind == "success" || kind == "unknown-namespace" {
				if err != nil || ex != (Execution{id, "run", "https://example.invalid/thread"}) || reads != 1 || configurations != 1 {
					t.Fatalf("unverified success: %+v reads=%d configure=%d err=%v", ex, reads, configurations, err)
				}
			} else if err == nil || ex != (Execution{}) {
				t.Fatalf("invalid launch confirmed: %+v %v", ex, err)
			}
			retainHint := kind == "success" || kind == "remote" || kind == "lost" || kind == "read-unavailable"
			if task.AttachmentLaunch == nil || (task.AttachmentLaunch.ExpectedThreadID == attachmentLaunchFixtureID) != retainHint {
				t.Fatalf("unexpected pending receipt: %+v", task.AttachmentLaunch)
			}
			if (kind != "remote" && launches != 1) || (kind == "remote" && launches != 0) {
				t.Fatalf("launches=%d", launches)
			}
		})
	}
}

func TestRecoverAttachmentLaunchEvidence(t *testing.T) {
	for _, kind := range []string{
		"success", "exact", "paged", "text-continuation", "text-offset", "wrong-continuation", "no-hint", "remote",
		"wrong-thread", "wrong-project", "parent", "relationship", "missing-lineage", "delegated",
		"origin", "message-id", "visibility", "source-thread", "wrong-body", "earlier-message",
		"wrong-run", "wrong-ordinal", "run-model", "executed-drift", "configuration",
	} {
		t.Run(kind, func(t *testing.T) {
			target, task := attachmentLaunchFixture()
			id := task.AttachmentLaunch.ExpectedThreadID
			p, item := attachmentLaunchPage(target, id)
			thread := p["thread"].(map[string]any)
			run := p["recentRuns"].([]any)[0].(map[string]any)
			switch kind {
			case "exact":
				item["text"] = task.RequestMessage
			case "no-hint":
				task.AttachmentLaunch.ExpectedThreadID = ""
			case "wrong-thread":
				thread["threadId"] = "different"
			case "wrong-project":
				thread["projectId"] = "different"
			case "parent":
				thread["parentThreadId"] = "parent"
			case "relationship":
				thread["relationshipToParent"] = "fork"
			case "missing-lineage":
				delete(thread, "parentThreadId")
			case "delegated":
				thread["delegatedFrom"] = map[string]any{"threadId": "parent"}
			case "origin":
				thread["creationSource"] = "desktop"
			case "message-id":
				item["messageId"] = "different"
			case "visibility":
				item["visibility"] = "inherited"
			case "source-thread":
				item["sourceThreadId"] = "different"
			case "wrong-body":
				item["text"] = "Read that attachment."
			case "earlier-message":
				p["items"] = append([]any{map[string]any{"position": 0, "type": "user_message"}}, item)
			case "wrong-run":
				run["runId"] = "different"
			case "wrong-ordinal":
				run["ordinal"] = 2
			case "run-model":
				run["model"] = "different"
			case "executed-drift":
				run["executedModel"] = "different"
			}
			after, textReads := float64(-1), 0
			api := &T3{Config: MCPConfig{EnvironmentID: target.EnvironmentID}, Calls: scriptedCaller(func(name string, args map[string]any) (any, error) {
				if args["threadId"] != id || args["environmentId"] != target.EnvironmentID {
					t.Fatalf("lookup escaped its durable identity: %+v", args)
				}
				switch name {
				case "t3_thread_read":
					if args["view"] != "messages" || args["maxCharsPerItem"] != float64(20000) {
						t.Fatalf("unexpected thread read: %#v", args)
					}
					if args["itemId"] != nil {
						textReads++
						if args["itemId"] != "timeline-item" || args["textOffset"] != float64(5) {
							t.Fatalf("unexpected text cursor: %+v", args)
						}
						p, tail := attachmentLaunchPage(target, id)
						tail["text"] = "this attachment."
						if kind == "wrong-continuation" {
							tail["messageId"] = "different"
						}
						return p, nil
					}
					if args["afterPosition"] != after || args["runLimit"] != float64(50) {
						t.Fatalf("unexpected page cursor: %+v", args)
					}
					if kind == "paged" && after == -1 {
						after = 0
						first, _ := attachmentLaunchPage(target, id)
						first["items"] = []any{map[string]any{"position": 0, "type": "notification"}}
						first["hasMore"], first["nextPosition"] = true, 0
						return first, nil
					}
					if kind == "text-continuation" || kind == "wrong-continuation" || kind == "text-offset" {
						item["text"], item["textTruncated"], item["nextTextOffset"] = "Read ", true, 5
						if kind == "text-offset" {
							item["nextTextOffset"] = 0
						}
					}
					return p, nil
				case "t3_thread_configure":
					if len(args) != 2 {
						t.Fatalf("configuration lookup was not read-only: %+v", args)
					}
					cfg := attachmentLaunchConfiguration(target, id)
					if kind == "configuration" {
						cfg["interactionMode"] = "plan"
					}
					return cfg, nil
				default:
					t.Fatal("unexpected tool", name)
					return nil, nil
				}
			})}
			if kind == "remote" {
				target.EnvironmentID = "linked"
			}
			ex, err := api.recoverAttachmentLaunch(context.Background(), target, task)
			switch kind {
			case "success", "exact", "paged", "text-continuation":
				if err != nil || ex != (Execution{id, "run", "https://example.invalid/thread"}) {
					t.Fatalf("failed recovery: %+v %v", ex, err)
				}
			default:
				if err == nil || ex != (Execution{}) {
					t.Fatalf("invalid evidence confirmed: %+v %v", ex, err)
				}
			}
			if kind == "text-continuation" && textReads != 1 {
				t.Fatalf("text continuation calls=%d", textReads)
			}
			if kind != "no-hint" && task.AttachmentLaunch.ExpectedThreadID != id {
				t.Fatal("read-only recovery changed the pending receipt")
			}
		})
	}
}

func TestAttachmentLaunchConnectedBearerAndLostHTTPReceipt(t *testing.T) {
	t.Run("http", func(t *testing.T) { testAttachmentLaunchWithLostReceipt(t, false) })
	t.Run("https", func(t *testing.T) { testAttachmentLaunchWithLostReceipt(t, true) })
}

func testAttachmentLaunchWithLostReceipt(t *testing.T, useTLS bool) {
	t.Helper()
	target, task := attachmentLaunchFixture()
	id := task.AttachmentLaunch.ExpectedThreadID
	payload := `{"v":1,"kind":"session","sid":"synthetic-session-1","sub":"mcp-client","method":"bearer-access-token"}`
	bearer := base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".c3ludGhldGlj"
	path := filepath.Join(t.TempDir(), "synthetic.oauth.json")
	var resource string
	writeBearer := func(token string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"access_token": token, "resource": resource})
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "synthetic-t3", Version: "test"}, nil)
	var launches atomic.Int32
	mcp.AddTool(server, &mcp.Tool{Name: attachmentLaunchTool}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, map[string]any, error) {
		launches.Add(1)
		reply := attachmentLaunchConfiguration(target, id)
		reply["projectId"], reply["runId"] = target.ProjectID, "run"
		return nil, reply, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "t3_thread_read"}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, map[string]any, error) {
		p, _ := attachmentLaunchPage(target, id)
		return nil, p, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "t3_thread_configure"}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, map[string]any, error) {
		return nil, attachmentLaunchConfiguration(target, id), nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true})
	listen := httptest.NewServer
	if useTLS {
		listen = httptest.NewTLSServer
	}
	httpServer := listen(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer "+bearer {
			t.Error("request did not use the connected synthetic bearer")
		}
		if req.Method == http.MethodPost {
			rec := httptest.NewRecorder()
			before := launches.Load()
			handler.ServeHTTP(rec, req)
			if launches.Load() > before {
				// The launch has been accepted; only its response is unavailable.
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			for key, values := range rec.Header() {
				w.Header()[key] = values
			}
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}
		handler.ServeHTTP(w, req)
	}))
	defer httpServer.Close()
	ctx := context.Background()
	cfg := MCPConfig{EnvironmentID: target.EnvironmentID, Endpoint: httpServer.URL + "/mcp", TokenFile: path}
	transport := http.DefaultTransport
	if useTLS {
		resource = cfg.Endpoint
		transport = httpServer.Client().Transport
	}
	writeBearer(bearer)
	client, err := connectMCP(ctx, cfg, transport)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Session.Close()
	writeBearer("rotated-synthetic-token")
	api := &T3{Calls: client, Config: cfg}
	if got := api.attachmentLaunchThreadID(task.RequestID); got != id {
		t.Fatalf("lookup did not derive from connection: %q", got)
	}
	if got := api.attachmentLaunchThreadID("request-<>&\u2028\u2029\\u2028"); got != "mcp:launch:bdbe0abe7ad82bf905c5a1cc9ecfb6717c61538e08c310f6d2fdf8a5ead4aec3" {
		t.Fatalf("lookup did not match JSON.stringify: %q", got)
	}
	if ex, err := api.launchWithAttachments(ctx, target, task); err == nil || ex != (Execution{}) {
		t.Fatalf("lost response reported as success: %+v %v", ex, err)
	}
	if ex, err := api.recoverAttachmentLaunch(ctx, target, task); err != nil || ex.ThreadID != id || ex.RunID != "run" || launches.Load() != 1 {
		t.Fatalf("lost response recovery failed: %+v %v launches=%d", ex, err, launches.Load())
	}
}
