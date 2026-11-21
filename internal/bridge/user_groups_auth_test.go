package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

const (
	fixtureUserGroupsDeviceCode = "private-synthetic-device-code"
	fixtureUserGroupsAuthURL    = "https://accounts.feishu.cn/oauth/v1/device/verify?user_code=TEST"
)

func TestUserGroupsDeviceLoginResponse(t *testing.T) {
	const lifetime = 600 * time.Second
	data := []byte(`{"verification_url":"` + fixtureUserGroupsAuthURL + `","device_code":"` + fixtureUserGroupsDeviceCode + `","expires_in":600,"hint":"fixture"}`)
	gotURL, gotCode, gotLifetime, err := parseUserGroupsDeviceLogin(data, "feishu")
	if err != nil || gotURL != fixtureUserGroupsAuthURL || gotCode != fixtureUserGroupsDeviceCode || gotLifetime != lifetime {
		t.Fatalf("valid authorization response rejected: %v", err)
	}
	lark := strings.ReplaceAll(string(data), "accounts.feishu.cn", "accounts.larksuite.com")
	if _, _, _, err := parseUserGroupsDeviceLogin([]byte(lark), "lark"); err != nil {
		t.Fatalf("valid Lark authorization response rejected: %v", err)
	}
	for _, tc := range []struct{ name, from, to string }{
		{"scheme", "https:", "http:"},
		{"host suffix", "accounts.feishu.cn", "accounts.feishu.cn.example.test"},
		{"other tenant", "accounts.feishu.cn", "accounts.larksuite.com"},
		{"userinfo", "accounts.feishu.cn", "other.test@accounts.feishu.cn"},
		{"port", "accounts.feishu.cn", "accounts.feishu.cn:8443"},
		{"code in link", "user_code=TEST", "opaque=" + fixtureUserGroupsDeviceCode},
		{"escaped code in link", "user_code=TEST", "opaque=%70rivate-synthetic-device-code"},
		{"alternate URL key", "verification_url", "verification_uri_complete"},
		{"case-folded code key", "device_code", "Device_Code"},
		{"zero expiry", `"expires_in":600`, `"expires_in":0`},
		{"string expiry", `"expires_in":600`, `"expires_in":"600"`},
		{"overflow expiry", `"expires_in":600`, `"expires_in":9223372036854775807`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := strings.Replace(string(data), tc.from, tc.to, 1)
			if _, _, _, err := parseUserGroupsDeviceLogin([]byte(input), "feishu"); err == nil || strings.Contains(err.Error(), fixtureUserGroupsDeviceCode) {
				t.Fatal("expected a safe error for an invalid authorization response")
			}
		})
	}
}

func TestUserGroupsAuthTicketLifecycle(t *testing.T) {
	g := newUserGroupsAuthFixture(t)
	ctx := context.Background()
	if _, err := g.Start(ctx, "other"); err == nil {
		t.Fatal("unknown authorization purpose accepted")
	}
	startFail := filepath.Join(g.dir, "fixture-start-fail")
	if err := os.WriteFile(startFail, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Start(ctx, "list"); err == nil || strings.Contains(err.Error(), fixtureUserGroupsDeviceCode) {
		t.Fatal("expected a safe error when the CLI cannot start authorization")
	}
	assertUserGroupsAuthAbsent(t, g.userGroupsAuthPath())
	assertUserGroupsAuthAbsent(t, filepath.Join(g.dir, "cache", "auth_login_scopes"))
	if err := os.Remove(startFail); err != nil {
		t.Fatal(err)
	}
	challenge, err := g.Start(ctx, "list")
	if err != nil {
		t.Fatal(err)
	}
	public, err := json.Marshal(challenge)
	if err != nil || challenge.Ticket == "" || challenge.URL != fixtureUserGroupsAuthURL || !challenge.ExpiresAt.After(time.Now()) ||
		strings.Contains(string(public), fixtureUserGroupsDeviceCode) {
		t.Fatal("invalid public authorization challenge")
	}
	pending, err := g.readUserGroupsAuthTicket(g.userGroupsAuthPath())
	if err != nil || pending.DeviceCode != fixtureUserGroupsDeviceCode || pending.Purpose != "list" ||
		pending.AppID != g.account.AppID || pending.Tenant != g.account.Tenant || pending.Ticket != challenge.Ticket || !pending.ExpiresAt.Equal(challenge.ExpiresAt) {
		t.Fatal("authorization ticket was not saved in the private account directory")
	}
	assertUserGroupsAuthAbsent(t, filepath.Join(g.dir, "cache", "auth_login_scopes"))
	if scope, err := os.ReadFile(filepath.Join(g.dir, "fixture-scope")); err != nil || string(scope) != "im:chat:read" {
		t.Fatalf("unexpected list scopes: %s (%v)", scope, err)
	}
	otherEntry := *g
	if resumed, err := otherEntry.Start(ctx, "list"); err != nil || resumed != challenge {
		t.Fatalf("another entry could not recover the pending authorization: %v", err)
	}
	if _, err := otherEntry.Start(ctx, "invite"); err == nil {
		t.Fatal("another entry replaced the pending authorization")
	}
	if err := otherEntry.Cancel(ctx, "wrong-ticket"); !errors.Is(err, errUserGroupsAuthTicket) {
		t.Fatalf("wrong ticket canceled the authorization: %v", err)
	}
	if _, err := otherEntry.Finish(ctx, "wrong-ticket"); !errors.Is(err, errUserGroupsAuthTicket) {
		t.Fatalf("wrong ticket completed the authorization: %v", err)
	}
	otherApp := *g
	otherApp.account.AppID = "cli_other"
	if _, err := otherApp.Finish(ctx, challenge.Ticket); !errors.Is(err, errUserGroupsAuthTicket) {
		t.Fatalf("another app accepted the authorization: %v", err)
	}
	otherTenant := *g
	otherTenant.account.Tenant = "lark"
	if err := otherTenant.Cancel(ctx, challenge.Ticket); !errors.Is(err, errUserGroupsAuthTicket) {
		t.Fatalf("another tenant canceled the authorization: %v", err)
	}
	lock, err := g.lock()
	if err != nil {
		t.Fatal(err)
	}
	finishWhileLocked := otherEntry.Cancel(ctx, challenge.Ticket)
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(finishWhileLocked, localfile.ErrLocked) {
		t.Fatalf("authorization lock did not reject concurrent cancel: %v", finishWhileLocked)
	}
	assertUserGroupsAuthAbsent(t, filepath.Join(g.dir, "fixture-polls"))
	if _, err := otherEntry.Finish(ctx, challenge.Ticket); err == nil || strings.Contains(err.Error(), fixtureUserGroupsDeviceCode) {
		t.Fatal("expected a safe error for pending browser authorization")
	}
	if err := os.WriteFile(filepath.Join(g.dir, "fixture-complete"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	otherEntry.owner = func(context.Context) (string, error) { return "", context.DeadlineExceeded }
	if _, err := otherEntry.Finish(ctx, challenge.Ticket); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected transient owner lookup failure: %v", err)
	}
	pending, err = g.readUserGroupsAuthTicket(g.userGroupsAuthPath())
	if err != nil || pending.Ticket != challenge.Ticket || pending.DeviceCode != "" {
		t.Fatal("exchanged device code was not cleared while keeping the authorization ticket")
	}
	status, err := g.Finish(ctx, challenge.Ticket)
	if err != nil || !status.ListAuthorized || status.InviteAuthorized {
		t.Fatalf("list authorization was not verified: %+v (%v)", status, err)
	}
	if polls, err := os.ReadFile(filepath.Join(g.dir, "fixture-polls")); err != nil || string(polls) != "poll\npoll\n" {
		t.Fatalf("unexpected number of device exchanges: %q (%v)", polls, err)
	}
	assertUserGroupsAuthAbsent(t, g.userGroupsAuthPath())
}

func TestUserGroupsAuthCancelExpiryAndVerification(t *testing.T) {
	g := newUserGroupsAuthFixture(t)
	ctx := context.Background()
	start := func(purpose string) UserGroupsChallenge {
		t.Helper()
		challenge, err := g.Start(ctx, purpose)
		if err != nil {
			t.Fatal(err)
		}
		return challenge
	}
	canceled := start("list")
	if err := g.Cancel(ctx, canceled.Ticket); err != nil {
		t.Fatal(err)
	}
	assertUserGroupsAuthAbsent(t, g.userGroupsAuthPath())
	if _, err := g.Finish(ctx, canceled.Ticket); !errors.Is(err, errUserGroupsAuthTicket) {
		t.Fatalf("canceled ticket still works: %v", err)
	}
	expired := start("list")
	pending, err := g.readUserGroupsAuthTicket(g.userGroupsAuthPath())
	if err != nil {
		t.Fatal(err)
	}
	pending.ExpiresAt = time.Now().Add(-time.Second)
	if err := writeUserGroupsAuthTicket(g.userGroupsAuthPath(), pending, false); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Finish(ctx, expired.Ticket); !errors.Is(err, errUserGroupsAuthTicket) {
		t.Fatalf("expired ticket still works: %v", err)
	}
	assertUserGroupsAuthAbsent(t, g.userGroupsAuthPath())
	assertUserGroupsAuthAbsent(t, filepath.Join(g.dir, "fixture-polls"))
	if err := os.WriteFile(filepath.Join(g.dir, "fixture-complete"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	invite := start("invite")
	if scope, err := os.ReadFile(filepath.Join(g.dir, "fixture-scope")); err != nil || string(scope) != "im:chat:read im:chat.members:write_only" {
		t.Fatalf("unexpected invite scopes: %s (%v)", scope, err)
	}
	if status, err := g.Finish(ctx, invite.Ticket); err == nil || status.InviteAuthorized || status.ListAuthorized {
		t.Fatalf("invite authorization accepted insufficient scopes: %+v (%v)", status, err)
	}
	assertUserGroupsAuthAbsent(t, g.userGroupsAuthPath())
	if err := os.WriteFile(filepath.Join(g.dir, "fixture-invite-granted"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	invite = start("invite")
	if status, err := g.Finish(ctx, invite.Ticket); err != nil || !status.InviteAuthorized || !status.ListAuthorized {
		t.Fatalf("invite authorization did not verify both scopes: %+v (%v)", status, err)
	}
	assertUserGroupsAuthAbsent(t, g.userGroupsAuthPath())
	ownerMismatch := start("list")
	g.owner = func(context.Context) (string, error) { return "ou_other", nil }
	if status, err := g.Finish(ctx, ownerMismatch.Ticket); err == nil || status.ListAuthorized {
		t.Fatalf("authorization accepted a login by a different user: %+v (%v)", status, err)
	}
	assertUserGroupsAuthAbsent(t, g.userGroupsAuthPath())
}

func newUserGroupsAuthFixture(t *testing.T) *UserGroups {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("synthetic CLI fixture uses /bin/sh")
	}
	root := t.TempDir()
	secret := filepath.Join(root, "fixture.secret")
	if _, err := localfile.WritePrivateAtomic(secret, []byte("synthetic-app-secret\n"), true); err != nil {
		t.Fatal(err)
	}
	g, err := NewUserGroups(Config{AppID: "cli_fixture", AppSecretFile: secret, Tenant: "feishu"}, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := localfile.MkdirAllPrivate(g.dir); err != nil {
		t.Fatal(err)
	}
	app, err := json.Marshal(map[string]any{
		"currentApp": g.account.AppID,
		"apps": []map[string]any{{"appId": g.account.AppID, "brand": g.account.Tenant,
			"appSecret": map[string]string{"source": "file", "id": secret}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localfile.WritePrivateAtomic(filepath.Join(g.dir, "config.json"), app, true); err != nil {
		t.Fatal(err)
	}
	const script = `#!/bin/sh
set -eu
test "$1" = auth
case "$2:$3" in
  status:--json)
    if test -f fixture-exchanged; then
      scope=im:chat:read
      if test -f fixture-invite-granted; then
        scope="$scope im:chat.members:write_only"
      fi
      printf '{"appId":"cli_fixture","brand":"feishu","identities":{"user":{"available":true,"tokenStatus":"valid","openId":"ou_owner","scope":"%s"}}}\n' "$scope"
    else
      printf '%s\n' '{"appId":"cli_fixture","brand":"feishu","identities":{"user":{"available":false}}}'
    fi
    ;;
  login:--scope)
    test "$#" -eq 6
    test "$5" = --no-wait
    test "$6" = --json
    printf '%s' "$4" > fixture-scope
    mkdir -p cache/auth_login_scopes
    touch cache/auth_login_scopes/` + fixtureUserGroupsDeviceCode + `.json
    if test -f fixture-start-fail; then
      printf '%s\n' 'Failed: ` + fixtureUserGroupsDeviceCode + `' >&2
      exit 1
    fi
    printf '%s\n' '{"verification_url":"` + fixtureUserGroupsAuthURL + `","device_code":"` + fixtureUserGroupsDeviceCode + `","expires_in":600}'
    ;;
  login:--device-code)
    test "$#" -eq 5
    test "$4" = '` + fixtureUserGroupsDeviceCode + `'
    test "$5" = --json
    printf 'poll\n' >> fixture-polls
    if ! test -f fixture-complete; then
      printf 'Pending: %s\n' "$4" >&2
      exit 1
    fi
    touch fixture-exchanged
    printf '%s\n' '{"event":"authorization_success"}'
    ;;
  *) exit 1 ;;
esac
`
	g.cliPath = filepath.Join(root, "fixture-lark-cli")
	if err := os.WriteFile(g.cliPath, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	g.owner = func(context.Context) (string, error) { return "ou_owner", nil }
	return g
}

func assertUserGroupsAuthAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected %s to be absent: %v", filepath.Base(path), err)
	}
}
