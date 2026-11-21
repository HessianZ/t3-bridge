package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestConfigLocalT3Candidate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("T3CODE_HOME", home)
	state := filepath.Join(home, "userdata")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	runtimeFile := filepath.Join(state, "server-runtime.json")
	identity := filepath.Join(state, "environment-id")
	for path, data := range map[string]string{
		runtimeFile: `{"port":3774,"origin":"https://unused.invalid"}`,
		identity:    "env-fixture\n",
	} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}

	template, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	var source map[string]any
	if err := json.Unmarshal(template, &source); err != nil {
		t.Fatal(err)
	}
	var extra map[string]any
	if err := json.Unmarshal(template, &extra); err != nil {
		t.Fatal(err)
	}
	extra["appId"] = "cli_aux"
	extra["stateDir"] = filepath.Join(home, "unused-state")
	extra["defaultRoute"] = "Work.Team"
	work := extra["routes"].(map[string]any)["work"]
	var linked map[string]any
	if err := json.Unmarshal(template, &linked); err != nil {
		t.Fatal(err)
	}
	linkedRoute := linked["routes"].(map[string]any)["work"].(map[string]any)
	linkedRoute["environmentId"] = "already-selected-remote"
	linkedRoute["hostName"] = "remote"
	linkedRoute["aliases"] = []string{}
	extra["routes"] = map[string]any{"Work.Team": work, "Remote": linkedRoute}
	source["additionalAccounts"] = []any{extra}
	sourceJSON, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "bridge.json")
	if err := os.WriteFile(path, sourceJSON, 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runConfig([]string{"local-t3", "-config", path, "-app-id", "cli_aux"}, nil, &output); err != nil {
		t.Fatal(err)
	}
	var draftOutput bytes.Buffer
	if err := runConfig([]string{"local-t3", "-draft-stdin", "-app-id", "cli_aux"}, bytes.NewReader(sourceJSON), &draftOutput); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(draftOutput.Bytes(), output.Bytes()) {
		t.Fatal("draft and saved configuration yielded different candidates")
	}
	draft, err := bridge.ParseConfig(output.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	accounts := draft.Accounts()
	if accounts[0].MCP.EnvironmentID != "REPLACE_ENVIRONMENT_ID" || accounts[1].MCP.RuntimeFile != runtimeFile ||
		accounts[1].MCP.Endpoint != "" || accounts[1].MCP.EnvironmentID != "env-fixture" ||
		accounts[1].Routes["Work.Team"].EnvironmentID != "env-fixture" ||
		accounts[1].Routes["Remote"].EnvironmentID != "already-selected-remote" || accounts[1].ReceiverAuthorized {
		t.Fatal("local discovery changed the wrong connection, route, or receiver authorization")
	}
	if current, err := os.ReadFile(path); err != nil || !bytes.Equal(current, sourceJSON) {
		t.Fatalf("candidate changed its source: %v", err)
	}
	if _, err := os.Stat(extra["stateDir"].(string)); !os.IsNotExist(err) {
		t.Fatal("candidate created receiver state")
	}
	if _, err := useLocalT3(output.Bytes(), 1); err == nil || !strings.Contains(err.Error(), "已填写") {
		t.Fatalf("expected an explicit already-configured error, got %v", err)
	}
	if err := os.WriteFile(identity, []byte("two\nlines\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if candidate, err := useLocalT3(sourceJSON, 1); candidate != nil || err == nil || !strings.Contains(err.Error(), "环境 ID 格式") {
		t.Fatalf("expected invalid identity to be rejected, got %v", err)
	}
	if err := os.WriteFile(runtimeFile, []byte(`{"port":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	if candidate, err := useLocalT3(sourceJSON, 1); candidate != nil || err == nil || !strings.Contains(err.Error(), "有效端口") {
		t.Fatalf("expected invalid port to be rejected, got %v", err)
	}
}
