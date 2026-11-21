package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func writeCredentialConfig(t *testing.T, path, secret, token string) []byte {
	t.Helper()
	template, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	edits, err := json.Marshal([]legacyImportEdit{
		{[]string{"appId"}, "cli_fixture"},
		{[]string{"appSecretFile"}, secret},
		{[]string{"mcp", "tokenFile"}, token},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := bridge.EditConfigJSON(template, edits)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return raw
}

func fixtureAppCredential(t *testing.T, configPath string) appCredential {
	t.Helper()
	items, err := listAppCredentials(configPath)
	if err != nil {
		t.Fatal(err)
	}
	item, err := selectAppCredential(items, "cli_fixture")
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func TestAppCredentialMaintenance(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	secret := filepath.Join(root, "credentials", "app.secret")
	raw := writeCredentialConfig(t, path, secret, filepath.Join(root, "token"))
	item := fixtureAppCredential(t, path)
	if item.Status != "missing" || !item.Editable {
		t.Fatalf("unexpected initial credential state: %s editable=%v", item.Status, item.Editable)
	}
	const initial = "synthetic-initial-value"
	if err := item.update("set", initial+"\n"); err != nil {
		t.Fatal(err)
	}
	current := fixtureAppCredential(t, path)
	if value, err := current.value(); err != nil || value != initial {
		t.Fatalf("could not read back fixture credential: %v", err)
	}
	meta, err := json.Marshal(current)
	if err != nil || bytes.Contains(meta, []byte(initial)) || bytes.Contains(meta, []byte(current.fileVersion)) {
		t.Fatal("metadata included the credential value or review fingerprint")
	}
	const rotated = "synthetic-rotated-value"
	if err := current.update("set", rotated); err != nil {
		t.Fatal(err)
	}
	if err := current.update("remove", ""); !errors.Is(err, errAppCredentialConflict) {
		t.Fatalf("stale read could remove the rotated credential: %v", err)
	}
	current = fixtureAppCredential(t, path)
	if value, err := current.value(); err != nil || value != rotated {
		t.Fatalf("rotation changed unexpectedly: %v", err)
	}
	if err := current.update("remove", ""); err != nil {
		t.Fatal(err)
	}
	if got := fixtureAppCredential(t, path); got.Status != "missing" {
		t.Fatalf("credential status after removal = %s", got.Status)
	}
	if stored, err := os.ReadFile(path); err != nil || !bytes.Equal(raw, stored) {
		t.Fatalf("credential maintenance changed the saved config: %v", err)
	}
}

func TestAppCredentialRejectsChangedConfigAndSharedPaths(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	secret := filepath.Join(root, "credentials", "app.secret")
	raw := writeCredentialConfig(t, path, secret, filepath.Join(root, "token"))
	item := fixtureAppCredential(t, path)
	if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := item.update("set", "synthetic"); !errors.Is(err, errAppCredentialConflict) {
		t.Fatalf("accepted a changed config: %v", err)
	}
	if _, err := os.Lstat(filepath.Dir(secret)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale review created a credential directory: %v", err)
	}
	for _, protected := range []string{path, configLockPath(path), filepath.Join(root, "token")} {
		writeCredentialConfig(t, path, protected, filepath.Join(root, "token"))
		item := fixtureAppCredential(t, path)
		if item.Status != "shared" || item.Editable {
			t.Fatalf("protected reference was editable: %s", item.Status)
		}
		if _, err := item.value(); err == nil {
			t.Fatal("protected file reference became readable as an App Secret")
		}
	}
}

func TestAppCredentialConcurrentProfiles(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "credentials", "app.secret")
	var items []appCredential
	for _, name := range []string{"first.json", "second.json"} {
		path := filepath.Join(root, name)
		writeCredentialConfig(t, path, secret, filepath.Join(root, "token"))
		items = append(items, fixtureAppCredential(t, path))
	}
	done := make(chan error, len(items))
	for i, item := range items {
		go func() { done <- item.update("set", strings.Repeat("fixture", i+1)) }()
	}
	published, conflicts := 0, 0
	for range items {
		switch err := <-done; {
		case err == nil:
			published++
		case errors.Is(err, errAppCredentialConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if published != 1 || conflicts != 1 {
		t.Fatalf("expected one publication and one conflict, got %d and %d", published, conflicts)
	}
}

func TestAppCredentialLinkIsReadOnlyAndCannotAliasToken(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	secret := filepath.Join(root, "credentials", "app.secret")
	token := filepath.Join(root, "token")
	writeCredentialConfig(t, path, secret, token)
	const value = "synthetic-linked-value"
	if err := fixtureAppCredential(t, path).update("set", value); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked.secret")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symbolic link fixture unavailable: %v", err)
	}
	writeCredentialConfig(t, path, link, token)
	item := fixtureAppCredential(t, path)
	if item.Status != "linked" || item.Editable || item.update("remove", "") == nil {
		t.Fatalf("linked credential was editable: %s", item.Status)
	}
	if read, err := item.value(); err != nil || read != value {
		t.Fatalf("could not read the unchanged linked credential: %v", err)
	}
	writeCredentialConfig(t, path, link, secret)
	item = fixtureAppCredential(t, path)
	if item.Status != "shared" || item.Editable {
		t.Fatalf("token alias was editable: %s", item.Status)
	}
	if _, err := item.value(); err == nil {
		t.Fatal("token alias exposed a credential value")
	}
}
