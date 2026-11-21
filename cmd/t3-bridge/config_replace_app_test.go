package main

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestRunConfigReplaceAppCandidate(t *testing.T) {
	const (
		appID = "cli_manualreplacement"
		user  = "ou_manualreplacement"
	)
	for _, tc := range []struct {
		source string
		index  int
		tenant string
	}{
		{"config", 0, "lark"},
		{"profile", 2, "feishu"},
		{"draft-stdin", 2, "feishu"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", root)
			t.Setenv("XDG_CONFIG_HOME", root)
			t.Setenv("APPDATA", root)
			t.Chdir(root)

			dir := filepath.Join(root, "fixture")
			saved := existingBindConfigFixture(t, dir)
			original, err := bridge.ParseConfig(saved)
			if err != nil {
				t.Fatal(err)
			}
			old := original.Accounts()[tc.index]
			newSecret := filepath.Join(root, "supplied", "existing-app.secret")
			writeReplaceAppFixtureFile(t, newSecret, []byte("synthetic-stage177-new-secret\r\n"))
			writeReplaceAppFixtureFile(t, old.AppSecretFile, []byte("synthetic-stage177-old-secret\n"))
			writeReplaceAppFixtureFile(t, old.MCP.TokenFile, []byte("synthetic-stage177-t3-token\n"))
			writeReplaceAppFixtureFile(t, filepath.Join(old.StateDir, "state.json"), []byte(`{"lastRun":"synthetic-stage177-old-run"}`))

			configPath := filepath.Join(root, "saved", "config.json")
			args := []string{"-config", configPath}
			draft := saved
			var input io.Reader
			switch tc.source {
			case "profile":
				profiles, err := localProfiles()
				if err != nil {
					t.Fatal(err)
				}
				configPath, err = profiles.file("replace-fixture")
				if err != nil {
					t.Fatal(err)
				}
				args = []string{"-profile", "replace-fixture"}
			case "draft-stdin":
				configPath = filepath.Join(root, defaultConfigPath)
				args = []string{"-draft-stdin"}
				draft, err = bridge.EditConfigJSON(saved, []byte(`[
					{"path":["additionalAccounts","0","routes","work","options","MixedCase"],"value":9007199254740995}
				]`))
				if err != nil || !bytes.Contains(draft, []byte("9007199254740995")) {
					t.Fatalf("could not prepare an unsaved edit for the other account: %v", err)
				}
				input = bytes.NewReader(draft)
			}
			writeReplaceAppFixtureFile(t, configPath, saved)
			beforeFiles := replaceAppFixtureTree(t, root)
			args = append(args, "-app-id", old.AppID, "-new-app-id", appID, "-tenant", tc.tenant,
				"-app-secret-file", newSecret, "-user-id", user)
			var output bytes.Buffer
			if err := runConfigReplaceApp(args, input, &output); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(output.Bytes(), []byte("synthetic-stage177-")) {
				t.Error("candidate exposed fixture file contents")
			}
			candidate, err := bridge.ParseConfig(output.Bytes())
			if err != nil {
				t.Fatalf("invalid candidate JSON: %v", err)
			}
			accounts := candidate.Accounts()
			if len(accounts) != 3 || accounts[tc.index].AppID != appID {
				t.Fatalf("expected three accounts with the new App in slot %d", tc.index)
			}
			before, after := replaceBindJSONAccounts(t, draft), replaceBindJSONAccounts(t, output.Bytes())
			for i, account := range accounts {
				if i != tc.index && !reflect.DeepEqual(before[i], after[i]) {
					t.Errorf("candidate changed the draft for account %q", account.AppID)
				}
				if account.AppID == old.AppID {
					t.Errorf("candidate still contains the replaced App %q", old.AppID)
				}
			}
			got := accounts[tc.index]
			if got.Tenant != tc.tenant || got.ReceiverAuthorized || got.AppSecretFile != newSecret {
				t.Error("expected the requested tenant, disabled receiver, and original credential path")
			}
			if !slices.Equal(got.Users, []string{user}) || len(got.Admins) != 0 || len(got.Chats) != 0 ||
				!slices.Equal(got.Routes[got.DefaultRoute].Users, []string{user}) {
				t.Error("candidate should grant access only to the new user")
			}
			if !filepath.IsAbs(got.StateDir) || filepath.Clean(got.StateDir) == filepath.Clean(old.StateDir) {
				t.Errorf("expected a separate absolute state path, got %q", got.StateDir)
			}
			if _, err := os.Lstat(got.StateDir); !os.IsNotExist(err) {
				t.Errorf("expected the new state path to be absent: %v", err)
			}
			if !reflect.DeepEqual(beforeFiles, replaceAppFixtureTree(t, root)) {
				t.Error("replacement changed fixture files or created a credential copy or state entry")
			}
		})
	}
}

func TestManualReplacementFieldsRejectsInvalidInput(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("APPDATA", root)
	dir := filepath.Join(root, "fixture")
	source := filepath.Join(dir, "config.json")
	raw := existingBindConfigFixture(t, dir)
	config, err := bridge.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	valid := manualReplacementFields{
		ReplaceAppID: config.AppID, AppID: "cli_manualreplacement", AppSecretFile: filepath.Join(dir, "new.secret"),
		User: "ou_manualreplacement", Tenant: "feishu",
	}
	emptySecret := filepath.Join(dir, "empty.secret")
	writeReplaceAppFixtureFile(t, source, raw)
	writeReplaceAppFixtureFile(t, valid.AppSecretFile, []byte("synthetic-new-secret\n"))
	writeReplaceAppFixtureFile(t, config.AppSecretFile, []byte("synthetic-old-secret\n"))
	writeReplaceAppFixtureFile(t, config.MCP.TokenFile, []byte("synthetic-t3-token\n"))
	writeReplaceAppFixtureFile(t, emptySecret, []byte(" \r\n"))

	reject := func(t *testing.T, fields manualReplacementFields) {
		t.Helper()
		input := bytes.Clone(raw)
		beforeFiles := replaceAppFixtureTree(t, root)
		candidate, err := fields.apply(input, source)
		if err == nil {
			t.Error("invalid replacement was accepted")
		}
		if !bytes.Equal(input, raw) || (len(candidate) != 0 && !bytes.Equal(candidate, raw)) {
			t.Error("rejected replacement changed the draft")
		}
		if !reflect.DeepEqual(beforeFiles, replaceAppFixtureTree(t, root)) {
			t.Error("rejected replacement changed the fixture files")
		}
	}
	for _, tc := range []struct {
		name   string
		change func(*manualReplacementFields)
	}{
		{"old App secret", func(f *manualReplacementFields) { f.AppSecretFile = config.AppSecretFile }},
		{"T3 token file", func(f *manualReplacementFields) { f.AppSecretFile = config.MCP.TokenFile }},
		{"missing secret", func(f *manualReplacementFields) { f.AppSecretFile = filepath.Join(dir, "missing.secret") }},
		{"blank secret", func(f *manualReplacementFields) { f.AppSecretFile = emptySecret }},
		{"directory as secret", func(f *manualReplacementFields) { f.AppSecretFile = dir }},
		{"relative secret path", func(f *manualReplacementFields) { f.AppSecretFile = "new.secret" }},
		{"wrong old App ID", func(f *manualReplacementFields) { f.ReplaceAppID = "cli_main" }},
		{"invalid new App ID", func(f *manualReplacementFields) { f.AppID = "wrong-id" }},
		{"same App ID", func(f *manualReplacementFields) { f.AppID = config.AppID }},
		{"already configured new App ID", func(f *manualReplacementFields) { f.AppID = config.AdditionalAccounts[0].AppID }},
		{"invalid user Open ID", func(f *manualReplacementFields) { f.User = "oc_not_a_user" }},
		{"invalid tenant", func(f *manualReplacementFields) { f.Tenant = "unknown" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := valid
			tc.change(&fields)
			reject(t, fields)
		})
	}
	for _, tc := range []struct {
		name string
		link func(string, string) error
	}{
		{"symlink", os.Symlink},
		{"hardlink", os.Link},
	} {
		t.Run("old App secret "+tc.name, func(t *testing.T) {
			fields := valid
			fields.AppSecretFile = filepath.Join(dir, tc.name+".secret")
			if err := tc.link(config.AppSecretFile, fields.AppSecretFile); err != nil {
				t.Skipf("cannot create %s on this platform: %v", tc.name, err)
			}
			reject(t, fields)
		})
	}
}

func writeReplaceAppFixtureFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := localfile.MkdirAllPrivate(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if _, err := localfile.WritePrivateAtomic(path, data, true); err != nil {
		t.Fatal(err)
	}
}

func replaceAppFixtureTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := make(map[string]string)
	files := os.DirFS(root)
	if err := fs.WalkDir(files, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			tree[path+"/"] = ""
			return nil
		}
		data, err := fs.ReadFile(files, path)
		tree[path] = string(data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return tree
}
