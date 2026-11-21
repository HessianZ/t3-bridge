package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func checkEntry(t *testing.T, args ...string) error {
	t.Helper()
	priorFlag, priorArgs := flag.CommandLine, os.Args
	t.Cleanup(func() { flag.CommandLine = priorFlag; os.Args = priorArgs })
	flag.CommandLine = flag.NewFlagSet("bridge-check", flag.ContinueOnError)
	os.Args = append([]string{"bridge-check"}, args...)
	return run()
}
func TestConfigurationCheckEntryJSON(t *testing.T) {
	for _, name := range []string{"generic.example.json", "multi-host.example.json", "homeops.example.json"} {
		t.Run(name, func(t *testing.T) {
			if err := checkEntry(t, "-config", filepath.Join("../../config", name), "-check"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestConfigurationDefaultEntryAndInvalidJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	fixture, err := os.ReadFile("../../config/generic.example.json")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "config"), 0700)
	os.WriteFile(filepath.Join(root, "config/local.json"), fixture, 0600)
	t.Chdir(root)
	if err := checkEntry(t, "-check"); err != nil {
		t.Fatal("default JSON entry", err)
	}
	os.WriteFile(filepath.Join(root, "config/local.json"), []byte(`{"appId":["secret-value-DO-NOT-ECHO"]}`), 0600)
	if err := checkEntry(t, "-check"); err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Fatal("bad JSON accepted or leaked value", err)
	}
}

func TestFirstRunUsesSameConfigAsWorkbench(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("APPDATA", root)
	t.Chdir(t.TempDir())
	registry, err := localProfiles()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(registry.root, "config.json")
	checkWorkbenches := func(want string) {
		t.Helper()
		for _, mode := range []string{"web", "gui"} {
			got, _, err := configWindowOptions(mode, nil)
			if err != nil || got != want {
				t.Errorf("%s selected %q, want %q: %v", mode, got, want, err)
			}
		}
	}
	checkWorkbenches(path)
	if _, err := os.Lstat(registry.root); !os.IsNotExist(err) {
		t.Fatalf("choosing the first config created files: %v", err)
	}
	if err := runConfig([]string{"init"}, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := checkEntry(t, "run", "-check"); err != nil {
		t.Fatalf("receiver did not read the saved default: %v", err)
	}
	if err := os.Mkdir("config", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(defaultConfigPath, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	legacy, err := filepath.Abs(defaultConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	checkWorkbenches(legacy)
	if err := checkEntry(t, "run", "-check"); err == nil {
		t.Fatal("receiver skipped an existing invalid legacy path")
	}
}

func TestReceiverStartupRejectsEnabledTemplateBeforeOpeningState(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("APPDATA", root)
	raw, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "receiver-state")
	raw, err = bridge.EditConfigJSON(raw, []byte(fmt.Sprintf(`[
		{"path":["receiverAuthorized"],"value":true},
		{"path":["stateDir"],"value":%q},
		{"path":["appSecretFile"],"value":%q},
		{"path":["mcp","tokenFile"],"value":%q}
	]`, stateDir, filepath.Join(root, "app.secret"), filepath.Join(root, "t3.json"))))
	if err != nil {
		t.Fatal(err)
	}
	c, err := bridge.ParseConfig(raw)
	if err != nil || !c.ReceiverAuthorized {
		t.Fatalf("invalid enabled template fixture: %v", err)
	}
	path := filepath.Join(root, "candidate.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkEntry(t, "-config", path, "-check"); err != nil {
		t.Fatalf("structural draft check failed: %v", err)
	}
	if err := checkEntry(t, "-config", path); err == nil || !strings.Contains(err.Error(), `routes["work"].projectId`) {
		t.Fatalf("startup did not report the remaining template fields: %v", err)
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("startup created state for an incomplete config: %v", err)
	}
}

func TestConfigCLIProducesCandidateOnly(t *testing.T) {
	original, err := os.ReadFile("../../config/generic.example.json")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "source.json")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runConfig([]string{"export", "-config", path}, nil, &out); err != nil {
		t.Fatal(err)
	}
	normalized, err := bridge.ParseConfig(out.Bytes())
	if err != nil || normalized.DefaultRoute != "work" {
		t.Fatal("formatted config is invalid", err)
	}

	out.Reset()
	edits := `[
		{"path":["routes","work","model"],"value":"replacement"},
		{"path":["routes","work","options","Reasoning.Effort"],"value":"high"}
	]`
	if err := runConfig([]string{"patch", "-config", path}, strings.NewReader(edits), &out); err != nil {
		t.Fatal(err)
	}
	candidate, err := bridge.ParseConfig(out.Bytes())
	if err != nil || candidate.Routes["work"].Model != "replacement" ||
		candidate.Routes["work"].Options["Reasoning.Effort"] != "high" ||
		candidate.MCP != normalized.MCP {
		t.Fatal("patch did not preserve and update the expected fields", err)
	}
	saved, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(saved, original) {
		t.Fatal("source config was modified", err)
	}

	out.Reset()
	invalid := `[{"path":["routes","work"],"delete":true}]`
	if err := runConfig([]string{"patch", "-config", path}, strings.NewReader(invalid), &out); err == nil || out.Len() != 0 {
		t.Fatal("invalid batch was exported", err)
	}
}
