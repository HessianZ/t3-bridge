package bridge

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestUserGroupsRecoversTheSameUserAuthorizationTicket(t *testing.T) {
	root := t.TempDir()
	groups, err := NewUserGroups(Config{
		AppID: "cli_fixture", Tenant: "feishu", AppSecretFile: filepath.Join(root, "app.secret"),
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := localfile.MkdirAllPrivate(groups.dir); err != nil {
		t.Fatal(err)
	}
	pending := userGroupsAuthTicket{Ticket: "opaque-ticket", AppID: "cli_fixture", Tenant: "feishu",
		Purpose: "list", URL: "https://accounts.feishu.cn/oauth/v1/device/verify?user_code=FIXTURE",
		DeviceCode: "synthetic-private-device-code", ExpiresAt: time.Now().Add(time.Minute).UTC()}
	if err := writeUserGroupsAuthTicket(groups.userGroupsAuthPath(), pending, true); err != nil {
		t.Fatal(err)
	}
	got, err := groups.Start(context.Background(), "list")
	if err != nil || got.Ticket != pending.Ticket || got.URL != pending.URL || !got.ExpiresAt.Equal(pending.ExpiresAt) {
		t.Fatalf("pending authorization did not resume: %+v, %v", got, err)
	}
	if _, err := groups.Start(context.Background(), "invite"); err == nil {
		t.Fatal("another authorization purpose replaced the pending ticket")
	}
}

func TestUserGroupsProfileStoresAFileReference(t *testing.T) {
	root := t.TempDir()
	secretFile := filepath.Join(root, "app.secret")
	const secret = "synthetic-user-groups-secret"
	if _, err := localfile.WritePrivateAtomic(secretFile, []byte(secret), true); err != nil {
		t.Fatal(err)
	}
	groups, err := NewUserGroups(Config{AppID: "cli_fixture", AppSecretFile: secretFile, Tenant: "feishu"}, root)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := groups.lock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := groups.prepareCLIProfile(); err != nil {
		t.Fatal(err)
	}
	profile, ref, err := groups.readCLIProfile()
	if err != nil || ref.Source != "file" || ref.ID != secretFile || strings.Contains(string(profile.Apps[0]), secret) {
		t.Fatalf("incorrect private file reference: %+v, %v", ref, err)
	}
}

func TestUserGroupsFindsNativeCLIBehindASDFShim(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("asdf fixture uses Unix executables")
	}
	root := t.TempDir()
	shim := filepath.Join(root, "shims")
	scripts := filepath.Join(root, "@larksuite", "cli", "scripts")
	native := filepath.Join(root, "@larksuite", "cli", "bin", "lark-cli")
	launcher := filepath.Join(scripts, "run.js")
	for _, dir := range []string{shim, scripts, filepath.Dir(native)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{filepath.Join(shim, "lark-cli"), launcher, native} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", shim)
	t.Setenv("ASDF_DATA_DIR", root)
	t.Setenv("T3_TEST_LARK_LAUNCHER", launcher)
	if err := os.WriteFile(filepath.Join(root, "asdf"), []byte("#!/bin/sh\nprintf '%s\\n' \"$T3_TEST_LARK_LAUNCHER\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	got := installedUserGroupsCLI(context.Background(), nil, root)
	want, err := filepath.EvalSymlinks(native)
	if err != nil || got != want {
		t.Fatalf("dedicated discovery chose %q instead of the installed native CLI %q: %v", got, want, err)
	}
}
