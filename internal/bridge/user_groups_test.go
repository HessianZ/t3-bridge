package bridge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

const testUserGroupsCLI = `#!/bin/sh
printf '%s\n' "$*" >> "$T3_TEST_USER_GROUPS_CALLS"
case "$1 $2" in
  'auth status')
    test "$*" = 'auth status --json' || exit 72
    test -z "$LARKSUITE_CLI_APP_ID$LARKSUITE_CLI_APP_SECRET$LARKSUITE_CLI_PROFILE$LARKSUITE_CLI_USER_ACCESS_TOKEN" || exit 73
    test -z "$LARK_CHANNEL$LARK_CHANNEL_CONFIG$OPENCLAW_CLI$HERMES_SESSION_KEY" || exit 77
    test "$LARKSUITE_CLI_CONFIG_DIR" = "$T3_TEST_USER_GROUPS_DIR" || exit 74
    test "$PATH" != "$T3_TEST_USER_GROUPS_INHERITED_PATH" || exit 75
    printf '%s\n' "$T3_TEST_USER_GROUPS_STATUS"
    ;;
  'im +chat-list'|'im +chat-search')
    printf '%s\n' '{"ok":true,"identity":"user","data":{"chats":[{"chat_id":"oc_a","name":"甲群"},{"chat_id":"oc_b","name":"乙群"},{"chat_id":"ou_invalid","name":"ignored"}],"has_more":true,"page_token":"next"}}'
    ;;
  'im chat.members')
    printf '%s\n' "$T3_TEST_USER_GROUPS_OUT"
    printf '%s\n' "$T3_TEST_USER_GROUPS_ERR" >&2
    exit "${T3_TEST_USER_GROUPS_CODE:-0}"
    ;;
  'test overflow')
    /usr/bin/head -c 1048577 /dev/zero
    ;;
  *) exit 76 ;;
esac
`

const testUserGroupsOwnerStatus = `{"appId":"cli_fixture","brand":"feishu","identities":{"user":{"status":"ready","available":true,"openId":"ou_owner","tokenStatus":"valid","scope":"im:chat:read im:chat.members:write_only"}}}`

func setupTestUserGroups(t *testing.T) (*UserGroups, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake CLI uses a POSIX script")
	}
	root := t.TempDir()
	secretFile := filepath.Join(root, "app.secret")
	if _, err := localfile.WritePrivateAtomic(secretFile, []byte("synthetic secret"), true); err != nil {
		t.Fatal(err)
	}
	groups, err := NewUserGroups(Config{AppID: "cli_fixture", AppSecretFile: secretFile}, root)
	if err != nil {
		t.Fatal(err)
	}
	groups.cliPath = filepath.Join(root, "fake-lark-cli")
	if err := os.WriteFile(groups.cliPath, []byte(testUserGroupsCLI), 0700); err != nil {
		t.Fatal(err)
	}
	groups.owner = func(context.Context) (string, error) { return "ou_owner", nil }
	groups.joinedGroups = func(context.Context) ([]FeishuGroup, error) { return nil, nil }
	calls := filepath.Join(root, "calls")
	t.Setenv("T3_TEST_USER_GROUPS_CALLS", calls)
	t.Setenv("T3_TEST_USER_GROUPS_DIR", groups.dir)
	t.Setenv("T3_TEST_USER_GROUPS_STATUS", testUserGroupsOwnerStatus)
	return groups, calls
}

func seedTestUserGroupsProfile(t *testing.T, groups *UserGroups) {
	t.Helper()
	lock, err := groups.lock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := groups.prepareCLIProfile(); err != nil {
		t.Fatal(err)
	}
}

func readTestUserGroupsCalls(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestUserGroupsPrivateProfileAndOwnerStatus(t *testing.T) {
	groups, calls := setupTestUserGroups(t)
	ctx := context.Background()
	if status, err := groups.Status(ctx); status != (UserGroupsStatus{}) || err != nil {
		t.Fatalf("uninitialized: %+v, %v", status, err)
	}
	if _, err := os.Stat(calls); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status contacted CLI without a profile: %v", err)
	}
	t.Setenv("LARKSUITE_CLI_APP_ID", "cli_other")
	t.Setenv("LARKSUITE_CLI_APP_SECRET", "synthetic injected secret")
	t.Setenv("LARKSUITE_CLI_PROFILE", "other")
	t.Setenv("LARKSUITE_CLI_USER_ACCESS_TOKEN", "synthetic injected token")
	t.Setenv("LARKSUITE_CLI_CONFIG_DIR", filepath.Dir(groups.dir))
	t.Setenv("LARK_CHANNEL", "1")
	t.Setenv("LARK_CHANNEL_CONFIG", "synthetic-external-profile")
	t.Setenv("OPENCLAW_CLI", "1")
	t.Setenv("HERMES_SESSION_KEY", "synthetic-external-session")
	t.Setenv("PATH", "/untrusted-fixture")
	t.Setenv("T3_TEST_USER_GROUPS_INHERITED_PATH", "/untrusted-fixture")
	lock, err := groups.lock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := groups.Status(ctx); !errors.Is(err, localfile.ErrLocked) {
		t.Fatalf("profile lock: %v", err)
	}
	if err := groups.prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if err := groups.prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if status, err := groups.status(ctx); status != (UserGroupsStatus{true, true}) || err != nil {
		t.Fatalf("authorized owner: %+v, %v", status, err)
	}
	if got := readTestUserGroupsCalls(t, calls); got != strings.Repeat("auth status --json\n", 3) {
		t.Fatalf("CLI arguments: %q", got)
	}
	groups.owner = func(context.Context) (string, error) { return "ou_replacement", nil }
	if status, err := groups.status(ctx); status != (UserGroupsStatus{}) || err != nil {
		t.Fatalf("previous owner: %+v, %v", status, err)
	}
	t.Setenv("T3_TEST_USER_GROUPS_STATUS", strings.Replace(testUserGroupsOwnerStatus, `"feishu"`, `"lark"`, 1))
	if status, err := groups.status(ctx); status != (UserGroupsStatus{}) || err == nil {
		t.Fatalf("wrong brand: %+v, %v", status, err)
	}
}

func TestUserGroupsListsAndSearchesWithBotMembership(t *testing.T) {
	groups, calls := setupTestUserGroups(t)
	seedTestUserGroupsProfile(t, groups)
	groups.joinedGroups = func(context.Context) ([]FeishuGroup, error) { return []FeishuGroup{{ID: "oc_a"}}, nil }
	page, err := groups.List(context.Background(), "", "previous")
	if err != nil || !page.BotKnown || page.NextPageToken != "next" || len(page.Groups) != 2 ||
		page.Groups[0] != (UserGroup{ID: "oc_a", Name: "甲群", BotJoined: true}) || page.Groups[1].BotJoined {
		t.Fatalf("list: %+v, %v", page, err)
	}
	groups.joinedGroups = func(context.Context) ([]FeishuGroup, error) { return nil, errors.New("unavailable") }
	page, err = groups.List(context.Background(), "  开发群  ", "")
	if err != nil || page.BotKnown || len(page.Groups) != 2 || page.Groups[0].BotJoined {
		t.Fatalf("search: %+v, %v", page, err)
	}
	want := "auth status --json\nim +chat-list --as user --json --sort active_time --page-token previous\n" +
		"auth status --json\nim +chat-search --as user --json --query 开发群 --search-types private,public_joined,external\n"
	if got := readTestUserGroupsCalls(t, calls); got != want {
		t.Fatalf("CLI arguments: %q", got)
	}
}

func TestUserGroupsInviteRechecksScopeAndRejectsInvalidChat(t *testing.T) {
	groups, calls := setupTestUserGroups(t)
	seedTestUserGroupsProfile(t, groups)
	ctx := context.Background()
	if _, err := groups.Invite(ctx, "--yes"); err == nil {
		t.Fatal("invalid chat ID was accepted")
	}
	t.Setenv("T3_TEST_USER_GROUPS_STATUS", strings.Replace(testUserGroupsOwnerStatus, userGroupsInviteScope, "im:chat.members:write_only_other", 1))
	if _, err := groups.Invite(ctx, "oc_target"); err == nil {
		t.Fatal("missing write scope was accepted")
	}
	if got := readTestUserGroupsCalls(t, calls); got != "auth status --json\n" {
		t.Fatalf("CLI arguments: %q", got)
	}
}

func TestUserGroupsInviteNeverResends(t *testing.T) {
	const success = `{"ok":true,"identity":"user","data":`
	for _, tt := range []struct {
		name, out, stderr, code, state string
		check, joined, alreadyJoined   bool
	}{
		{name: "already joined", state: "joined", alreadyJoined: true},
		{name: "joined", out: success + `{}}`, state: "joined", check: true, joined: true},
		{name: "accepted unconfirmed", out: success + `{}}`, state: "unknown", check: true},
		{name: "pending", out: success + `{"pending_approval_id_list":["cli_fixture"]}}`, state: "pending"},
		{name: "invalid", out: success + `{"invalid_id_list":["cli_fixture"]}}`, state: "rejected"},
		{name: "missing", out: success + `{"not_existed_id_list":["cli_fixture"]}}`, state: "rejected"},
		{name: "other-app", out: success + `{"pending_approval_id_list":["cli_other"]}}`, state: "unknown", check: true},
		{name: "confirmation", code: "10", stderr: "synthetic CLI output", state: "confirmation-required"},
		{name: "business-error", code: "1", stderr: `{"ok":false,"error":{"type":"api","code":232012}}`, state: "rejected"},
		{name: "transport-joined", code: "1", stderr: "connection reset", state: "joined", check: true, joined: true},
		{name: "transport-unknown", code: "1", stderr: "connection reset", state: "unknown", check: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			groups, calls := setupTestUserGroups(t)
			seedTestUserGroupsProfile(t, groups)
			t.Setenv("T3_TEST_USER_GROUPS_OUT", tt.out)
			t.Setenv("T3_TEST_USER_GROUPS_ERR", tt.stderr)
			t.Setenv("T3_TEST_USER_GROUPS_CODE", tt.code)
			checks := 0
			groups.joinedGroups = func(context.Context) ([]FeishuGroup, error) {
				checks++
				if tt.alreadyJoined || (tt.joined && checks > 1) {
					return []FeishuGroup{{ID: "oc_target"}}, nil
				}
				return []FeishuGroup{{ID: "oc_other"}}, nil
			}
			result, err := groups.Invite(context.Background(), "oc_target")
			wantChecks := 1
			if tt.check {
				wantChecks = 2
			}
			if err != nil || result.State != tt.state || checks != wantChecks {
				t.Fatalf("invite: %+v, %v; membership checks: %d", result, err, checks)
			}
			want := "auth status --json\n"
			if !tt.alreadyJoined {
				want += `im chat.members create --chat-id oc_target --member-id-type app_id --succeed-type 1 --data {"id_list":["cli_fixture"]} --as user --json` + "\n"
			}
			if got := readTestUserGroupsCalls(t, calls); got != want {
				t.Fatalf("CLI arguments: %q", got)
			}
		})
	}
}

func TestUserGroupsCLIOutputIsBoundedAndErrorsAreRedacted(t *testing.T) {
	groups, _ := setupTestUserGroups(t)
	lock, err := groups.lock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx := context.Background()
	if out, err := groups.command(ctx, "", "test", "overflow"); len(out) != 0 || err == nil {
		t.Fatalf("overflow: %d bytes, %v", len(out), err)
	}
	t.Setenv("T3_TEST_USER_GROUPS_CODE", "3")
	t.Setenv("T3_TEST_USER_GROUPS_ERR", "synthetic sensitive stderr")
	if _, err := groups.command(ctx, "", "im", "chat.members"); err == nil || strings.Contains(err.Error(), "synthetic") {
		t.Fatalf("CLI error: %v", err)
	}
}
