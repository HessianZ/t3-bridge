package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/config"
	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestNamedProfileLifecycle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	t.Chdir(t.TempDir())
	registry, err := localProfiles()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := config.Examples.ReadFile("generic.example.json")
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "source.json")
	raw = []byte(strings.NewReplacer(`"receiverAuthorized": false`, `"receiverAuthorized": true`, `"options": {}`, `"options": {"Custom.Name": true}`).Replace(string(raw)))
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := runProfile(args, &out); err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out.String())
	}
	first := run("create", "dev", "-from", source)
	second := run("create", "other", "-from", source)
	a, err := bridge.LoadConfig(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := bridge.LoadConfig(second)
	if err != nil {
		t.Fatal(err)
	}
	if a.ReceiverAuthorized || b.ReceiverAuthorized || a.StateDir == b.StateDir || a.ReceiverMachine != "dev" || a.Routes["work"].Options["Custom.Name"] != true {
		t.Fatal("profile creation did not isolate receivers or preserve settings")
	}
	run("use", "dev")
	if selected, err := resolveConfigPath("", "", defaultConfigPath); err != nil || selected != first {
		t.Fatalf("default profile selection failed: %q, %v", selected, err)
	}
	if selected, err := resolveConfigPath(source, "", defaultConfigPath); err != nil || selected != source {
		t.Fatal("an explicit path should take precedence")
	}
	if err := runConfig([]string{"check", "-profile", "other"}, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	if exported, err := bridge.ParseConfig([]byte(run("export", "dev"))); err != nil || exported.StateDir != a.StateDir {
		t.Fatalf("profile export changed settings: %v", err)
	}
	running, err := lockProfileRuntime(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := runProfile([]string{"archive", "dev"}, io.Discard); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("a running profile must stay in place: %v", err)
	}
	if err := running.Close(); err != nil {
		t.Fatal(err)
	}
	archived := run("archive", "dev")
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("profile was not archived: %v", err)
	}
	if selected, err := resolveConfigPath("", "", defaultConfigPath); err != nil || selected != filepath.Join(registry.root, "config.json") {
		t.Fatalf("archive did not clear the active profile: %q, %v", selected, err)
	}
	if dst := run("restore", archived); dst != first {
		t.Fatalf("unexpected restore path: %q", dst)
	}
	restored, err := bridge.LoadConfig(first)
	if err != nil || restored.StateDir != a.StateDir {
		t.Fatalf("restore did not preserve original settings: %v", err)
	}
}
