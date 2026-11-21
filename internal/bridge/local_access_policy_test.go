package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/localfile"
	channel "github.com/larksuite/channel-sdk-go"
)

func TestLocalAccessPolicySharesFeishuState(t *testing.T) {
	ctx := context.Background()
	e, _, lark, _ := testEngine(t)
	e.Config.Users = []string{"ou_member"}
	e.Config.Admins = []string{"ou_member"}
	e.Config.access = &accessRuntime{owner: "ou_owner"}
	e.Config.Routes["Empty.Mixed"] = Target{}
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	user := LocalAccessPolicyRequest{Kind: "user", UserID: "ou_member", Mode: "deny"}
	view, err := e.LocalAccessPolicy(ctx, user)
	if err != nil || view.OwnerID != "ou_owner" {
		t.Fatalf("local write = %+v, %v", view, err)
	}
	if !slices.Contains(view.RouteIDs, "Empty.Mixed") {
		t.Fatal("a route without an explicit user needs to remain selectable")
	}
	want := LocalAccessPolicyEntry{Kind: "user", UserID: "ou_member", Configured: true, Override: "deny"}
	assertLocalAccessEntry(t, view, want)
	assertLocalAccessEntry(t, view, LocalAccessPolicyEntry{Kind: "admin", UserID: "ou_member", Configured: true, Override: "inherit", Effective: true})
	assertLocalAccessEntry(t, view, LocalAccessPolicyEntry{Kind: "user", UserID: "ou_owner", Override: "inherit", Effective: true})
	assertLocalAccessEntry(t, view, LocalAccessPolicyEntry{Kind: "admin", UserID: "ou_owner", Override: "inherit", Effective: true})
	if e.Config.userAllowed("ou_member") || !e.Config.isAdmin("ou_member") {
		t.Fatal("local user revocation did not publish independently of admin rights")
	}
	user.Mode = "inherit"
	view, err = e.LocalAccessPolicy(ctx, user)
	if err != nil || !e.Config.userAllowed("ou_member") {
		t.Fatalf("static user access not restored: %v", err)
	}
	want.Override, want.Effective = "inherit", true
	assertLocalAccessEntry(t, view, want)

	admin := LocalAccessPolicyRequest{Kind: "admin", UserID: "ou_member", Mode: "deny"}
	view, err = e.LocalAccessPolicy(ctx, admin)
	if err != nil || e.Config.isAdmin("ou_member") || !e.Config.userAllowed("ou_member") {
		t.Fatalf("admin revocation changed user access or failed to publish: %v", err)
	}
	assertLocalAccessEntry(t, view, LocalAccessPolicyEntry{Kind: "admin", UserID: "ou_member", Configured: true, Override: "deny"})
	denied := message("after-local-admin-revocation", "/invite route mac ou_member")
	denied.UserID = "ou_member"
	accept(t, e, denied)
	tick(t, e)
	if len(lark.replies) != 1 || !strings.HasPrefix(lark.replies[0], "仅owner/admin") {
		t.Fatalf("Feishu did not apply the local admin revocation: %v", lark.replies)
	}
	admin.Mode = "inherit"
	if _, err := e.LocalAccessPolicy(ctx, admin); err != nil || !e.Config.isAdmin("ou_member") {
		t.Fatalf("static admin rights not restored: %v", err)
	}

	n := groupMessage("route-from-feishu", "ou_member", "chat", "@bot /invite route nas @person", true)
	n.Mentions = []channel.Mention{{Key: "@bot", IsBot: true}, {Key: "@person", OpenID: "ou_member"}}
	m := Normalize(n)
	accept(t, e, m)
	tick(t, e)
	view, err = e.LocalAccessPolicy(ctx, LocalAccessPolicyRequest{})
	if err != nil {
		t.Fatal(err)
	}
	assertLocalAccessEntry(t, view, LocalAccessPolicyEntry{Kind: "route", RouteID: "nas", UserID: "ou_member", Override: "allow", Effective: true})
	if !e.Config.TargetAllowed(m, e.Config.Routes["nas"], "nas") || e.Config.TargetAllowed(m, e.Config.Routes["mac"], "mac") {
		t.Fatal("Feishu route grant escaped its route")
	}
	if len(lark.replies) != 2 || !strings.HasPrefix(lark.replies[1], "已保存route授权：ou_member=true · route=nas") {
		t.Fatalf("Feishu replies = %v", lark.replies)
	}

	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e.Store = store
	view, err = e.LocalAccessPolicy(ctx, LocalAccessPolicyRequest{})
	if err != nil {
		t.Fatal(err)
	}
	assertLocalAccessEntry(t, view, want)
	assertLocalAccessEntry(t, view, LocalAccessPolicyEntry{Kind: "route", RouteID: "nas", UserID: "ou_member", Override: "allow", Effective: true})
	st := store.snapshot()
	if _, ok := st.AccessPolicy.Users["ou_member"]; ok {
		t.Fatal("inherit persisted a user override")
	}
	if receipt := st.Commands[key(m, m.ID)]; receipt == nil || receipt.Reply != lark.replies[1] {
		t.Fatalf("missing original Feishu receipt: %+v", receipt)
	}
}

func TestLocalAccessPolicyRejectsOtherAppInvalidRouteAndOwnerRemoval(t *testing.T) {
	ctx := context.Background()
	e, _, _, _ := testEngine(t)
	e.Config.access = &accessRuntime{owner: "ou_owner"}
	read := LocalAccessPolicyRequest{}
	write := LocalAccessPolicyRequest{Kind: "user", UserID: "ou_member", Mode: "allow"}
	if _, err := e.LocalAccessPolicy(ctx, read); err == nil {
		t.Fatal("unbound Store was readable")
	}
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	foreign := &Engine{Config: Config{AppID: "another-app"}, Store: e.Store}
	for _, request := range []LocalAccessPolicyRequest{read, write} {
		if _, err := foreign.LocalAccessPolicy(ctx, request); err == nil {
			t.Fatalf("cross-App request accepted: %+v", request)
		}
	}
	for _, request := range []LocalAccessPolicyRequest{
		{Kind: "route", RouteID: "NAS", UserID: "ou_member", Mode: "allow"},
		{Kind: "user", RouteID: "nas", UserID: "ou_member", Mode: "allow"},
		{Kind: "admin", UserID: "name", Mode: "allow"},
		{Kind: "user", UserID: "ou_member"},
	} {
		if _, err := e.LocalAccessPolicy(ctx, request); err == nil {
			t.Fatalf("invalid request accepted: %+v", request)
		}
	}
	for _, kind := range []string{"user", "admin"} {
		request := LocalAccessPolicyRequest{Kind: kind, UserID: "ou_owner", Mode: "deny"}
		if _, err := e.LocalAccessPolicy(ctx, request); !errors.Is(err, errAccessOwnerRemoval) {
			t.Fatalf("owner %s removal returned %v", kind, err)
		}
	}
	e.Config.access.owner = ""
	write.Mode = "deny"
	if _, err := e.LocalAccessPolicy(ctx, write); !errors.Is(err, errAccessOwnerUnknown) {
		t.Fatalf("removal with unknown owner returned %v", err)
	}
	if p := e.Store.snapshot().AccessPolicy; len(p.Users)+len(p.Admins)+len(p.Routes) != 0 {
		t.Fatalf("rejected requests changed policy: %+v", p)
	}
}

func TestLocalAccessPolicyInheritRevokesOnlyPendingRouteWork(t *testing.T) {
	ctx := context.Background()
	e, _, _, _ := testEngine(t)
	e.Config.access = &accessRuntime{owner: "ou_owner"}
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	req := LocalAccessPolicyRequest{Kind: "route", RouteID: "nas", UserID: "ou_member", Mode: "allow"}
	if _, err := e.LocalAccessPolicy(ctx, req); err != nil {
		t.Fatal(err)
	}
	m := message("queued", "later")
	m.UserID, m.ChatType, m.RouteAtReceive = req.UserID, "group", req.RouteID
	otherRoute := m
	otherRoute.ID, otherRoute.RouteAtReceive = "other-queue", "mac"
	if err := e.Store.update(func(st *State) error {
		st.Inbox = []Message{m, otherRoute}
		st.Tasks["queued"] = &Task{Source: m, RouteID: "nas", Phase: "waiting_capacity"}
		st.Tasks["other"] = &Task{Source: m, RouteID: "mac", Phase: "ready"}
		st.Tasks["running"] = &Task{Source: m, RouteID: "nas", Phase: "running"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	req.Mode = "inherit"
	dir := e.Store.dir
	e.Store.dir = filepath.Join(dir, "missing")
	_, err := e.LocalAccessPolicy(ctx, req)
	e.Store.dir = dir
	if err == nil {
		t.Fatal("expected Store write failure")
	}
	st := e.Store.snapshot()
	target := e.Config.Routes["nas"]
	if !st.AccessPolicy.Routes["nas"][req.UserID] || !e.Config.TargetAllowed(m, target, "nas") || st.Inbox[0].Cancelled || st.Tasks["queued"].Phase != "waiting_capacity" {
		t.Fatal("failed write changed saved policy, published ACL, or queued work")
	}
	if _, err := e.LocalAccessPolicy(ctx, req); err != nil {
		t.Fatal(err)
	}
	st = e.Store.snapshot()
	if len(st.AccessPolicy.Routes) != 0 || e.Config.TargetAllowed(m, target, "nas") || !st.Inbox[0].Cancelled || st.Tasks["queued"].Phase != "blocked" {
		t.Fatal("inherit did not revoke the dynamic grant and block pending work")
	}
	if st.Inbox[1].Cancelled || st.Tasks["other"].Phase != "ready" || st.Tasks["running"].Phase != "running" {
		t.Fatal("inherit changed unrelated route or already dispatched work")
	}
	req.Mode = "allow"
	if _, err := e.LocalAccessPolicy(ctx, req); err != nil {
		t.Fatal(err)
	}
	st = e.Store.snapshot()
	if !st.Inbox[0].Cancelled || st.Tasks["queued"].Phase != "blocked" {
		t.Fatal("restoring the grant revived revoked work")
	}
}

func TestStorePermissionPublicationBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name      string
		published bool
	}{
		{"before publish", false},
		{"after publish", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _, _, _ := testEngine(t)
			e.Config.access = &accessRuntime{owner: "ou_owner"}
			e.Config.Users = []string{"ou_member"}
			e.Config.Chats = []string{"oc_group"}
			if err := e.Store.BindAccount(e.Config.AppID); err != nil {
				t.Fatal(err)
			}
			writeErr := errors.New("synthetic state write failure")
			e.Store.writeState = func(path string, data []byte, createOnly bool) (bool, error) {
				if tc.published {
					if published, err := localfile.WritePrivateAtomic(path, data, createOnly); !published || err != nil {
						t.Fatalf("synthetic publication setup: published=%v, err=%v", published, err)
					}
				}
				return tc.published, writeErr
			}
			if _, err := e.LocalAccessPolicy(t.Context(), LocalAccessPolicyRequest{Kind: "user", UserID: "ou_member", Mode: "deny"}); err != writeErr {
				t.Fatalf("local user revocation error = %v, want original write error", err)
			}
			if _, err := e.LocalGroupAccess(t.Context(), GroupAccessRequest{ChatID: "oc_group", Access: "deny", Mention: "off"}); err != writeErr {
				t.Fatalf("local group change error = %v, want original write error", err)
			}

			data, err := os.ReadFile(filepath.Join(e.Store.dir, "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			var disk State
			if err := json.Unmarshal(data, &disk); err != nil {
				t.Fatal(err)
			}
			for _, st := range []State{e.Store.snapshot(), disk} {
				user, userSet := st.AccessPolicy.Users["ou_member"]
				group, groupSet := st.GroupAccess.Chats["oc_group"]
				mention, mentionSet := st.GroupAccess.Mentions["oc_group"]
				if user || group || mention || userSet != tc.published || groupSet != tc.published || mentionSet != tc.published {
					t.Fatalf("unexpected stored overrides: user=%v, groups=%+v", st.AccessPolicy.Users, st.GroupAccess)
				}
			}
			want := !tc.published
			if e.Config.userAllowed("ou_member") != want || e.Config.groupAllowed("oc_group") != want || e.Config.requireMention("oc_group") != want {
				t.Fatal("runtime policy disagrees with the published state")
			}
		})
	}
}

func assertLocalAccessEntry(t *testing.T, view LocalAccessPolicyView, want LocalAccessPolicyEntry) {
	t.Helper()
	for _, entry := range view.Entries {
		if entry.Kind == want.Kind && entry.UserID == want.UserID && entry.RouteID == want.RouteID {
			if entry != want {
				t.Fatalf("entry = %+v, want %+v", entry, want)
			}
			return
		}
	}
	t.Fatalf("missing %+v in %+v", want, view)
}
