package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestConfigModelOptionsUsesExactCatalogAndOnlyWritesDraft(t *testing.T) {
	const environment = "model-options-env"
	const model = "example-model"
	const app = "cli_aux"
	server := mcp.NewServer(&mcp.Implementation{Name: "model-options-fixture", Version: "1"}, nil)
	tools := map[string]map[string]any{
		"t3_environment_read": {"environmentId": environment},
		"t3_project_list":     {"projects": []bridge.Project{}},
		"orchestrator_capabilities": {"runtimeMode": "approval-required", "providers": []bridge.T3Provider{
			{ID: "provider-wrong", Driver: "codex", Models: []bridge.T3Model{{ID: model}}},
			{ID: "provider-right", Driver: "codex", Models: []bridge.T3Model{{
				ID: model, Options: []bridge.T3ModelOption{
					{ID: "enabled", Values: []any{true, false}},
					{ID: "Effort", Values: []any{"low", "high"}},
				},
			}}},
		}},
	}
	for name, result := range tools {
		mcp.AddTool(server, &mcp.Tool{Name: name}, func(_ context.Context, _ *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, map[string]any, error) {
			if name == "orchestrator_capabilities" && (args["environmentId"] != environment || args["search"] != model) {
				t.Error("capabilities did not query the selected route")
			}
			return nil, result, nil
		})
	}
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	defer httpServer.Close()
	dir := t.TempDir()
	token := filepath.Join(dir, "t3.token")
	if err := os.WriteFile(token, []byte("synthetic-oauth"), 0600); err != nil {
		t.Fatal(err)
	}
	template, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	makeAccount := func(id, state string) json.RawMessage {
		t.Helper()
		edits, err := json.Marshal([]configTUIEdit{
			{Path: []string{"appId"}, Value: id},
			{Path: []string{"stateDir"}, Value: filepath.Join(dir, state)},
			{Path: []string{"mcp", "environmentId"}, Value: environment},
			{Path: []string{"mcp", "endpoint"}, Value: httpServer.URL + "/mcp"},
			{Path: []string{"mcp", "tokenFile"}, Value: token},
			{Path: []string{"routes", "work", "environmentId"}, Value: environment},
			{Path: []string{"routes", "work", "projectId"}, Value: "project"},
			{Path: []string{"routes", "work", "providerId"}, Value: "provider-right"},
			{Path: []string{"routes", "work", "model"}, Value: model},
			{Path: []string{"routes", "work", "options"}, Value: map[string]any{"Effort": "low"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		data, err := bridge.EditConfigJSON(template, edits)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	edits, err := json.Marshal([]configTUIEdit{{Path: []string{"additionalAccounts"}, Value: []json.RawMessage{makeAccount(app, "aux")}}})
	if err != nil {
		t.Fatal(err)
	}
	original, err := bridge.EditConfigJSON(makeAccount("cli_main", "main"), edits)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "configuration.json")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"model-options", "-config", path, "-app-id", app, "-route", "work"}
	for _, input := range []string{`{"enabled":"false"}`, `{"effort":"high"}`, `{"enabled":true,"enabled":false}`, `null`} {
		var out bytes.Buffer
		if err := runConfig(args, strings.NewReader(input), &out); err == nil || out.Len() != 0 {
			t.Fatalf("invalid option accepted: %s", input)
		}
	}
	for _, input := range []string{`{"enabled":false,"Effort":"high"}`, `{}`} {
		var out bytes.Buffer
		if err := runConfig(args, strings.NewReader(input), &out); err != nil {
			t.Fatal(err)
		}
		draft, err := bridge.ParseConfig(out.Bytes())
		if err != nil || draft.Routes["work"].Options["Effort"] != "low" || len(draft.AdditionalAccounts) != 1 {
			t.Fatal("wrong CLI draft scope", err)
		}
		options := draft.AdditionalAccounts[0].Routes["work"].Options
		if input == "{}" {
			if len(options) != 0 {
				t.Fatal("empty object did not clear the options")
			}
		} else if len(options) != 2 || options["enabled"] != false || options["Effort"] != "high" {
			t.Fatalf("typed model options did not reach the draft: %+v", options)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("editing options changed the saved file")
	}
}
