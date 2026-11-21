package main

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestServiceRenderOffline(t *testing.T) {
	binary := filepath.Join(t.TempDir(), `T3 & tools $name%/t3-bridge`)
	config := filepath.Join(t.TempDir(), `config "$name%".json`)
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			var output strings.Builder
			err := runService([]string{"render", "-instance", "test--bot", "-platform", platform, "-binary", binary, "-config", config}, &output)
			if err != nil {
				t.Fatal(err)
			}
			if platform == "linux" {
				for _, want := range []string{`ExecStart=":`, `T3 & tools $name%%/t3-bridge"`, `-config "`, `config \"$name%%\".json"`, "UMask=0077"} {
					if !strings.Contains(output.String(), want) {
						t.Fatalf("missing %q in unit:\n%s", want, &output)
					}
				}
				return
			}
			var plist struct {
				Arguments []string `xml:"dict>array>string"`
			}
			if err := xml.Unmarshal([]byte(output.String()), &plist); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(plist.Arguments, []string{binary, "run", "-config", config}) {
				t.Fatalf("unexpected plist arguments: %q", plist.Arguments)
			}
		})
	}
}

func TestServiceLinuxLifecycle(t *testing.T) {
	id := "t3-bridge-managed-test.service"
	s := serviceInstance{platform: "linux", id: id, name: "test", file: filepath.Join(t.TempDir(), "systemd", "user", id)}
	var commands []string
	manager := func(command string, args ...string) ([]byte, error) {
		if command != "systemctl" || !slices.Equal(args[:2], []string{"--user", "--no-pager"}) {
			t.Fatalf("unexpected manager: %s %q", command, args)
		}
		commands = append(commands, args[2])
		if args[2] != "show" {
			return nil, nil
		}
		if _, err := os.Stat(s.file); errors.Is(err, os.ErrNotExist) {
			return []byte("LoadState=not-found\nActiveState=inactive\n"), nil
		}
		return fmt.Appendf(nil, "LoadState=loaded\nFragmentPath=%s\nActiveState=active\nMainPID=123\nunrelated secret text\n", s.file), nil
	}
	if err := s.install(s.render("/tmp/binary", "/tmp/config.json"), manager); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("expected private unit file, got %v: %v", info, err)
	}
	var output strings.Builder
	if err := s.manage("status", manager, &output); err != nil || output.String() != id+": active (pid 123)\n" {
		t.Fatalf("status output %q: %v", &output, err)
	}
	for _, action := range []string{"start", "stop", "restart", "unregister"} {
		if err := s.manage(action, manager, io.Discard); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	want := []string{"show", "show", "show", "daemon-reload", "show", "enable", "start", "show", "daemon-reload", "show", "disable", "show", "daemon-reload", "show", "enable", "restart", "show", "daemon-reload", "show", "disable", "daemon-reload"}
	if !slices.Equal(commands, want) {
		t.Fatalf("unexpected manager sequence: %q", commands)
	}
	if _, err := os.Stat(s.file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("definition still installed: %v", err)
	}
}

func TestServiceLaunchdLifecycle(t *testing.T) {
	root := t.TempDir()
	s := serviceInstance{platform: "darwin", name: "test", id: "ai.t3-bridge.managed.test", file: filepath.Join(root, "LaunchAgents", "test.plist"), logDir: filepath.Join(root, "logs"), domain: "gui/501"}
	var commands []string
	loaded := false
	manager := func(command string, args ...string) ([]byte, error) {
		if command != "/bin/launchctl" {
			t.Fatalf("unexpected manager: %s", command)
		}
		commands = append(commands, strings.Join(args, " "))
		switch args[0] {
		case "print":
			if !loaded {
				return fmt.Appendf(nil, "Could not find service %q in domain for user gui: 501", s.id), errors.New("exit status 113")
			}
			return fmt.Appendf(nil, "\tpath = %s\n\tstate = running\n\tpid = 2147483647\n", s.file), nil
		case "bootout":
			loaded = false
		case "bootstrap":
			loaded = true
		}
		return nil, nil
	}
	config := filepath.Join(root, "config.json")
	if err := s.install(s.render("/tmp/binary", config), manager); err != nil {
		t.Fatal(err)
	}
	preserved := map[string]string{
		config:                                "fixture configuration",
		filepath.Join(root, "app.secret"):     "fixture credential",
		filepath.Join(root, "state.json"):     "fixture state",
		filepath.Join(s.logDir, "stdout.log"): "fixture receiver log",
	}
	for path, content := range preserved {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, action := range []string{"start", "restart", "unregister"} {
		if err := s.manage(action, manager, io.Discard); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	print, disable, enable, bootout := "print "+s.target(), "disable "+s.target(), "enable "+s.target(), "bootout "+s.target()
	bootstrap := "bootstrap " + s.domain + " " + s.file
	want := []string{print, disable, print, enable, bootstrap, print, disable, bootout, print, enable, bootstrap, print, disable, bootout, print}
	if !slices.Equal(commands, want) {
		t.Fatalf("unexpected manager sequence: %q", commands)
	}
	if _, err := os.Stat(s.file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("definition still installed: %v", err)
	}
	if info, err := os.Stat(s.logDir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("expected retained private log directory, got %v: %v", info, err)
	}
	for path, content := range preserved {
		if data, err := os.ReadFile(path); err != nil || string(data) != content {
			t.Errorf("unregister changed %s: %q, %v", filepath.Base(path), data, err)
		}
	}
}

func TestServiceLeavesForeignDefinitionAlone(t *testing.T) {
	s := serviceInstance{platform: "linux", id: "t3-bridge-managed-test.service", file: filepath.Join(t.TempDir(), "test.service")}
	manager := func(command string, args ...string) ([]byte, error) {
		t.Fatalf("foreign definition reached manager: %s %q", command, args)
		return nil, nil
	}
	if err := os.WriteFile(s.file, []byte("external definition"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.install(s.render("/tmp/binary", "/tmp/config.json"), manager); err == nil {
		t.Fatal("accepted install over external definition")
	}
	if err := s.manage("unregister", manager, io.Discard); err == nil {
		t.Fatal("accepted unregister of external definition")
	}
	got, err := os.ReadFile(s.file)
	if err != nil || string(got) != "external definition" {
		t.Fatalf("foreign definition was changed: %q: %v", got, err)
	}
}

func TestServiceRejectsExternalManagerPath(t *testing.T) {
	s := serviceInstance{platform: "linux", id: "t3-bridge-managed-test.service", file: filepath.Join(t.TempDir(), "test.service")}
	if err := os.WriteFile(s.file, []byte(s.render("/tmp/binary", "/tmp/config.json")), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := func(command string, args ...string) ([]byte, error) {
		if args[2] != "show" {
			t.Fatalf("unexpected request to manager: %s %q", command, args)
		}
		return []byte("LoadState=loaded\nFragmentPath=/etc/systemd/user/foreign.service\nActiveState=active\n"), nil
	}
	if err := s.manage("stop", manager, io.Discard); err == nil {
		t.Fatal("accepted stop of a foreign loaded unit")
	}
}
