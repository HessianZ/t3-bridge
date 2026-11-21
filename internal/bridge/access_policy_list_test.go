package bridge

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestAccessPolicyListUsesPrivateAppScopeAndPages(t *testing.T) {
	e, l, ch, x, f := setupAccess(t)
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	e.Config.Topics = nil
	e.Config.DirectChats = &[]string{"dm"}
	e.Config.Users = []string{"ou_list_07", "ou_list_06", "ou_list_05", "ou_list_04", "ou_list_03", "ou_list_02", "ou_list_01"}
	target := e.Config.Routes["nas"]
	target.Users = []string{"ou_list_route"}
	e.Config.Routes["nas"] = target
	l.Config = e.Config
	if _, err := e.LocalAccessPolicy(context.Background(), LocalAccessPolicyRequest{Kind: "user", UserID: "ou_list_01", Mode: "deny"}); err != nil {
		t.Fatal(err)
	}
	before := e.Store.snapshot().AccessPolicy
	read := func(id, actor, command string) string {
		t.Helper()
		seen := len(f.replies)
		accessDMCommand(t, e, ch, id, actor, command)
		if len(f.replies) != seen+1 {
			t.Fatalf("%s: expected one reply", command)
		}
		return f.replies[seen]
	}
	first := read("user-first", "user", "/config access user")
	if !strings.Contains(first, "第 1/2 页") || !strings.Contains(first, "ou_list_01 · 配置 true · 运行覆盖 deny · 本层生效 false") ||
		!strings.Contains(first, "ou_list_06") || strings.Contains(first, "ou_list_07") {
		t.Fatalf("first page = %q", first)
	}
	second := read("user-second", "user", "/config access user 2")
	if !strings.Contains(second, "ou_list_07") || !strings.Contains(second, "所有者 是") || strings.Contains(second, "ou_list_06") {
		t.Fatalf("second page = %q", second)
	}
	if route := read("one-route", "user", "/config access route nas"); !strings.Contains(route, "ou_list_route") || strings.Contains(route, "ou_list_01") {
		t.Fatalf("route list = %q", route)
	}
	if admin := read("admin-list", "user", "/config access admin"); !strings.Contains(admin, "所有者 是") || strings.Contains(admin, "ou_list_01") {
		t.Fatalf("admin list = %q", admin)
	}
	if bad := read("case-sensitive-route", "user", "/config access route NAS"); !strings.Contains(bad, "未知受控路由") || strings.Contains(bad, "ou_list_route") {
		t.Fatalf("unknown route list = %q", bad)
	}
	if denied := read("not-admin", "ou_list_02", "/config access user"); strings.Contains(denied, "ou_list_") {
		t.Fatalf("unauthorized private reply = %q", denied)
	}
	accessCommand(t, e, ch, "public-admin", "user", "chat", "/config access user")
	if last := f.replies[len(f.replies)-1]; strings.Contains(last, "ou_list_") {
		t.Fatalf("group reply exposed a permission list: %q", last)
	}
	if !reflect.DeepEqual(before, e.Store.snapshot().AccessPolicy) || x.opens != 0 || x.sends != 0 || f.listCalls != 0 {
		t.Fatal("list read changed policy or accessed T3 or the group directory")
	}
}

func TestAccessPolicyListWithholdsRetryAfterAdminRevocation(t *testing.T) {
	e, l, ch, _, f := setupAccess(t)
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	const actor = "ou_list_admin"
	e.Config.Topics = nil
	e.Config.Admins = []string{actor}
	e.Config.Users = append(e.Config.Users, actor)
	e.Config.DirectChats = &[]string{"dm"}
	l.Config = e.Config
	f.replyFailFor = "pending-list"
	accessDMCommand(t, e, ch, "pending-list", actor, "/config access admin")
	st := e.Store.snapshot()
	cmd := st.Commands[key(Message{ChatID: "dm"}, "pending-list")]
	if cmd == nil || cmd.Kind != accessPolicyListCommand || cmd.Issue == "" || len(st.Inbox) != 1 {
		t.Fatal("list reply was not retained for retry")
	}
	if _, err := e.LocalAccessPolicy(context.Background(), LocalAccessPolicyRequest{Kind: "admin", UserID: actor, Mode: "deny"}); err != nil {
		t.Fatal(err)
	}
	f.replyFailFor = ""
	seen := len(f.replies)
	tick(t, e)
	if len(f.replies) != seen || len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("pending permission list was sent after admin revocation")
	}
}
