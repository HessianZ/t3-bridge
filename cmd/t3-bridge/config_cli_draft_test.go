package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestConfigCLIFirstRunDraft(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("APPDATA", root)
	t.Chdir(root)

	var draft bytes.Buffer
	if err := runConfig([]string{"init", "-draft"}, nil, &draft); err != nil {
		t.Fatal(err)
	}
	config, err := bridge.ParseConfig(draft.Bytes())
	if err != nil || config.ReceiverAuthorized || config.DefaultRoute != "work" {
		t.Fatalf("invalid starter draft: %v", err)
	}
	var check bytes.Buffer
	if err := runConfig([]string{"check", "-draft-stdin"}, bytes.NewReader(draft.Bytes()), &check); err != nil || check.String() != "configuration valid\n" {
		t.Fatalf("draft validation returned %q, %v", check.String(), err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("building and validating a draft touched the filesystem: %v, %v", entries, err)
	}
	if err := runConfig([]string{"check", "-draft-stdin"}, strings.NewReader(strings.Repeat(" ", bridge.MaxConfigBytes+1)), io.Discard); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("oversize draft was accepted: %v", err)
	}

	file := filepath.Join(root, "candidate.json")
	for _, command := range []string{"check", "bind-feishu", "local-t3", "authorize-t3", "catalog"} {
		t.Run(command, func(t *testing.T) {
			args := []string{command, "-draft-stdin", "-config", file}
			if command == "bind-feishu" {
				args = append(args, "-output", file)
			}
			input := bytes.NewReader(draft.Bytes())
			if err := runConfig(args, input, io.Discard); err == nil || !strings.Contains(err.Error(), "cannot be combined") || input.Len() != draft.Len() {
				t.Fatalf("ambiguous source was not rejected before reading stdin: %v", err)
			}
		})
	}
	if err := runConfig([]string{"init", "-draft", "-config", file}, nil, io.Discard); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("conflicting template destination accepted: %v", err)
	}
	if _, err := os.Lstat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid options created a file: %v", err)
	}
}
