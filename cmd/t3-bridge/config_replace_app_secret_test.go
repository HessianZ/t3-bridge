package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestRunConfigReplaceAppEnterSecret(t *testing.T) {
	root := t.TempDir()
	for _, env := range []string{"HOME", "XDG_CONFIG_HOME", "APPDATA"} {
		t.Setenv(env, root)
	}
	profiles, err := localProfiles()
	if err != nil {
		t.Fatal(err)
	}
	const appID, value = "cli_manuallyentered", "synthetic-stage178-new-secret"
	secretPath := filepath.Join(profiles.root, "credentials", appID+".secret")
	source := filepath.Join(root, "fixture", "bridge.json")
	raw := existingBindConfigFixture(t, filepath.Dir(source))
	writeReplaceAppFixtureFile(t, source, raw)
	config, err := bridge.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	old := config.AdditionalAccounts[0]
	writeReplaceAppFixtureFile(t, old.AppSecretFile, []byte("synthetic-existing-app-secret\n"))
	args := []string{"-config", source, "-app-id", old.AppID, "-new-app-id", appID,
		"-tenant", "lark", "-user-id", "ou_manuallyentered", "-enter-secret"}
	var output bytes.Buffer
	for _, tc := range []struct {
		input string
		extra []string
	}{
		{value + "\nsecond-line", nil},
		{value, []string{"-app-id", "cli_wrong"}},
		{value, []string{"-app-secret-file", old.AppSecretFile}},
		{value, []string{"-draft-stdin"}},
	} {
		err := runConfigReplaceApp(append(args, tc.extra...), strings.NewReader(tc.input), &output)
		if err == nil || strings.Contains(err.Error(), value) || output.Len() != 0 {
			t.Fatalf("invalid request was accepted or exposed the credential")
		}
		if _, err := os.Lstat(secretPath); !os.IsNotExist(err) {
			t.Fatalf("rejected request touched the credential destination: %v", err)
		}
	}
	for range 2 {
		output.Reset()
		if err := runConfigReplaceApp(args, strings.NewReader(value+"\n"), &output); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(output.Bytes(), []byte(value)) {
			t.Fatal("candidate exposed the credential")
		}
		candidate, err := bridge.ParseConfig(output.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		account := candidate.AdditionalAccounts[0]
		if candidate.AppID != config.AppID || account.AppID != appID || account.Tenant != "lark" ||
			account.AppSecretFile != secretPath || account.ReceiverAuthorized || len(account.Users) != 1 || account.Users[0] != "ou_manuallyentered" {
			t.Fatal("candidate did not isolate the selected replacement")
		}
		if account.StateDir == old.StateDir {
			t.Fatal("candidate reused the old account state directory")
		}
		if _, err := os.Lstat(account.StateDir); !os.IsNotExist(err) {
			t.Fatalf("replacement created a state directory: %v", err)
		}
	}
	output.Reset()
	err = runConfigReplaceApp(args, strings.NewReader("synthetic-different-secret"), &output)
	if err == nil || output.Len() != 0 {
		t.Fatal("replacement overwrote a different credential")
	}
	if stored, err := readAppCredential(secretPath); err != nil || !bytes.Equal(stored, []byte(value+"\n")) {
		t.Fatalf("verified private credential bytes differ: %v", err)
	}
	if stored, err := readAppCredential(old.AppSecretFile); err != nil || string(stored) != "synthetic-existing-app-secret\n" {
		t.Fatalf("old credential changed: %v", err)
	}
	if saved, err := os.ReadFile(source); err != nil || !bytes.Equal(saved, raw) {
		t.Fatalf("preparing replacement changed the saved configuration: %v", err)
	}
}
