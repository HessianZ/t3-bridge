package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/config"
)

func TestConfigCLIVersionedSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "local.json")
	backup := path + ".backup.json"
	var out bytes.Buffer
	run := func(command, input string, args ...string) error {
		out.Reset()
		return runConfig(append([]string{command, "-config", path}, args...), strings.NewReader(input), &out)
	}
	if err := run("init", ""); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	revision := fmt.Sprintf("%x", sha256.Sum256(original))
	if got := strings.TrimSpace(out.String()); got != revision {
		t.Fatalf("init revision = %q, want %q", got, revision)
	}
	if err := run("init", ""); !errors.Is(err, errConfigRevisionConflict) {
		t.Fatalf("repeated init = %v, want revision conflict", err)
	}
	if err := run("check", ""); err != nil || out.String() != "configuration valid\n" {
		t.Fatalf("check = %q, %v", out.String(), err)
	}
	if err := run("version", ""); err != nil || strings.TrimSpace(out.String()) != revision {
		t.Fatalf("version = %q, %v", out.String(), err)
	}
	if err := run("show", ""); err != nil || !strings.Contains(out.String(), `"model": "REPLACE_MODEL_ID"`) {
		t.Fatalf("show = %q, %v", out.String(), err)
	}
	candidate := bytes.ReplaceAll(original, []byte("REPLACE_MODEL_ID"), []byte("replacement"))
	if err := run("diff", string(candidate)); err != nil || !strings.Contains(out.String(), `+      "model": "replacement"`) {
		t.Fatalf("diff = %q, %v", out.String(), err)
	}
	if err := run("save", string(candidate)); err == nil {
		t.Fatal("save accepted an omitted revision")
	}
	if err := run("save", `{}`, "-expected-revision", revision); err == nil {
		t.Fatal("save accepted invalid configuration")
	}
	if _, err := os.Stat(backup); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup exists before the first valid replacement: %v", err)
	}
	if err := run("save", string(candidate), "-expected-revision", revision); err != nil {
		t.Fatal(err)
	}
	savedRevision := fmt.Sprintf("%x", sha256.Sum256(candidate))
	if got := strings.TrimSpace(out.String()); got != savedRevision {
		t.Fatalf("save revision = %q, want %q", got, savedRevision)
	}
	if err := run("save", string(candidate), "-expected-revision", savedRevision); err != nil {
		t.Fatalf("unchanged save: %v", err)
	}
	if err := run("save", string(original), "-expected-revision", revision); !errors.Is(err, errConfigRevisionConflict) {
		t.Fatalf("stale save = %v, want revision conflict", err)
	}
	for file, want := range map[string][]byte{path: candidate, backup: original} {
		got, err := os.ReadFile(file)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("unexpected content in %s: %v", file, err)
		}
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0600 {
			t.Fatalf("mode of %s = %o, want 0600", file, mode)
		}
	}
}

func TestSaveConfigCandidateConcurrent(t *testing.T) {
	original, err := config.Examples.ReadFile("generic.example.json")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "local.json")
	revision, err := saveConfigCandidate(path, original, "missing")
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for _, model := range []string{"first-model", "second-model"} {
		go func() {
			candidate := bytes.ReplaceAll(original, []byte("REPLACE_MODEL_ID"), []byte(model))
			_, err := saveConfigCandidate(path, candidate, revision)
			results <- err
		}()
	}
	first, second := <-results, <-results
	if first != nil || !errors.Is(second, errConfigRevisionConflict) {
		if second != nil || !errors.Is(first, errConfigRevisionConflict) {
			t.Fatalf("got %v and %v, want one success and one revision conflict", first, second)
		}
	}
}
