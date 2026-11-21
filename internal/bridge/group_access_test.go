package bridge

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"

	channel "github.com/larksuite/channel-sdk-go"
)

func TestLocalGroupAccessView(t *testing.T) {
	ctx := context.Background()
	e, l, _, _, f := setupAccess(t)
	ch, err := channel.New(e.Config.AppID, "synthetic-secret", channel.WithOpenBaseURL(f.baseURL), channel.WithEnableTokenCache(true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ch.Stop(ctx) })
	l.Channel = ch
	e.Config.Chats = []string{"oc_static", "oc_denied"}
	l.Config.Chats = e.Config.Chats
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.update(func(st *State) error {
		st.GroupAccess.setAllowed("oc_denied", false)
		st.GroupAccess.setAllowed("oc_granted", true)
		st.GroupAccess.setMention("oc_mention", false)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	appPath := "/open-apis/application/v6/applications/" + e.Config.AppID
	var versionReads atomic.Int32
	f.extra = func(w http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case appPath:
			fmt.Fprintf(w, `{"code":0,"data":{"app":{"app_id":%q,"online_version_id":"online_v1","scopes":[{"scope":"im:message.group_msg","token_types":["tenant"]}]}}}`, e.Config.AppID)
			return true
		case appPath + "/app_versions/online_v1":
			versionReads.Add(1)
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer synthetic-token" {
				t.Error("unexpected app version request")
			}
			fmt.Fprintf(w, `{"code":0,"data":{"app_version":{"app_id":%q,"version_id":"online_v1","events":["接收消息"],"event_infos":[{"event_type":"im.message.receive_v1","event_name":"接收消息"}]}}}`, e.Config.AppID)
			return true
		}
		if r.URL.Path != "/open-apis/im/v1/chats" {
			return false
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected group list method %s", r.Method)
		}
		io.WriteString(w, `{"code":0,"data":{"items":[{"chat_id":"oc_joined","name":"Joined"},{"chat_id":"oc_denied","name":"Denied"}],"has_more":false}}`)
		return true
	}

	legacyView, err := e.LocalGroupAccess(ctx, GroupAccessRequest{})
	if err != nil || legacyView.MessageEvent != "" || versionReads.Load() != 0 {
		t.Fatalf("legacy caller received a subscription probe: %+v, err=%v", legacyView, err)
	}
	view, err := e.LocalGroupAccess(ctx, GroupAccessRequest{IncludeMessageEvent: true})
	want := []GroupAccessEntry{
		{ID: "oc_denied", Name: "Denied", Joined: true, AccessOverride: "deny", RequiresMention: true, MentionOverride: "inherit"},
		{ID: "oc_granted", Allowed: true, AccessOverride: "allow", RequiresMention: true, MentionOverride: "inherit"},
		{ID: "oc_joined", Name: "Joined", Joined: true, AccessOverride: "inherit", RequiresMention: true, MentionOverride: "inherit"},
		{ID: "oc_mention", AccessOverride: "inherit", MentionOverride: "off"},
		{ID: "oc_static", Allowed: true, AccessOverride: "inherit", RequiresMention: true, MentionOverride: "inherit"},
	}
	if err != nil || !view.JoinedAvailable || !view.DefaultRequiresMention || view.MessageScope != GroupMessageScopeGranted ||
		view.MessageEvent != "subscribed" || versionReads.Load() != 1 || !slices.Equal(view.Groups, want) {
		t.Fatalf("group view = %+v, err=%v, want groups %+v", view, err, want)
	}

	f.extra = nil
	f.listFail = true
	view, err = e.LocalGroupAccess(ctx, GroupAccessRequest{IncludeMessageEvent: true})
	want = slices.Delete(want, 2, 3)
	want[0].Name, want[0].Joined = "", false
	if err != nil || view.JoinedAvailable || !view.DefaultRequiresMention || view.MessageScope != GroupMessageScopeUnknown ||
		view.MessageEvent != "unknown" || !slices.Equal(view.Groups, want) {
		t.Fatalf("fallback view = %+v, err=%v, want groups %+v", view, err, want)
	}
}

func TestLocalGroupAccessWritesReachReceiver(t *testing.T) {
	ctx := context.Background()
	e, l, ch, _, f := setupAccess(t)
	e.Config.Chats = []string{"oc_static"}
	l.Config.Chats = e.Config.Chats
	e.Config.Topics, l.Config.Topics = nil, nil
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	f.listFail = true
	write := func(request GroupAccessRequest) GroupAccessView {
		t.Helper()
		request.IncludeMessageEvent = true
		view, err := e.LocalGroupAccess(ctx, request)
		if err != nil || view.JoinedAvailable || view.MessageEvent != "unknown" {
			t.Fatalf("write %+v yielded %+v, err=%v", request, view, err)
		}
		return view
	}
	accessDMCommand(t, e, ch, "existing-admin", "user", "/invite admin ou_existing")
	accessDMCommand(t, e, ch, "existing-user", "user", "/invite user ou_existing")
	accessDMCommand(t, e, ch, "existing-route", "user", "/invite route nas ou_existing")
	policy := e.Store.snapshot().AccessPolicy
	if !policy.Admins["ou_existing"] || !policy.Users["ou_existing"] || !policy.Routes["nas"]["ou_existing"] {
		t.Fatalf("failed to seed independent policy: %+v", policy)
	}
	view := write(GroupAccessRequest{ChatID: "oc_static", Access: "deny", Mention: "off"})
	want := GroupAccessEntry{ID: "oc_static", AccessOverride: "deny", MentionOverride: "off"}
	if !slices.Equal(view.Groups, []GroupAccessEntry{want}) || e.Config.groupAllowed("oc_static") || l.Config.requireMention("oc_static") {
		t.Fatalf("deny/off did not update view and receiver: %+v", view)
	}
	view = write(GroupAccessRequest{ChatID: "oc_static", Access: "inherit"})
	want.Allowed, want.AccessOverride = true, "inherit"
	if !slices.Equal(view.Groups, []GroupAccessEntry{want}) || !l.Config.groupAllowed("oc_static") {
		t.Fatalf("inherited access did not restore static grant and retain @ override: %+v", view)
	}

	accessCommand(t, e, ch, "im-deny", "user", "oc_static", "/config group off")
	view = write(GroupAccessRequest{ChatID: "oc_static", Mention: "inherit"})
	want.Allowed, want.AccessOverride, want.RequiresMention, want.MentionOverride = false, "deny", true, "inherit"
	if !slices.Equal(view.Groups, []GroupAccessEntry{want}) || l.Config.groupAllowed("oc_static") || !l.Config.requireMention("oc_static") {
		t.Fatalf("local write did not preserve IM access override: %+v", view)
	}
	view = write(GroupAccessRequest{Mention: "off"})
	want.RequiresMention = false
	if view.DefaultRequiresMention || !slices.Equal(view.Groups, []GroupAccessEntry{want}) ||
		e.Config.requireMention("oc_static") || !e.Config.isAdmin("ou_existing") ||
		!reflect.DeepEqual(e.Store.snapshot().AccessPolicy, policy) {
		t.Fatalf("app default or independent access policy differs: %+v", view)
	}

	ch = restartAccess(t, e, f.baseURL)
	if e.Config.groupAllowed("oc_static") || e.Config.requireMention("oc_static") || !e.Config.isAdmin("ou_existing") {
		t.Fatal("persisted deny, app default, or administrator missing after reopen")
	}
	view = write(GroupAccessRequest{ChatID: "oc_dynamic", Access: "allow"})
	if len(view.Groups) != 2 || !e.Config.groupAllowed("oc_dynamic") {
		t.Fatalf("dynamic grant did not take effect: %+v", view)
	}
	receiveAccess(t, ch, groupMessage("live-inbound", "other", "oc_dynamic", "group message", false))
	inbox := e.Store.snapshot().Inbox
	if len(inbox) != 1 || inbox[0].ID != "live-inbound" {
		t.Fatalf("receiver did not accept the group message without @: %+v", inbox)
	}
	view = write(GroupAccessRequest{Mention: "inherit"})
	if !view.DefaultRequiresMention || !e.Config.requireMention("oc_dynamic") || e.Store.snapshot().GroupAccess.RequireMention != nil {
		t.Fatalf("inherit did not restore the built-in app default: %+v", view)
	}
}

func TestLocalGroupAccessRejectsInvalidAndFailedWrites(t *testing.T) {
	ctx := context.Background()
	e, l, _, _, f := setupAccess(t)
	request := GroupAccessRequest{ChatID: "oc_new", Access: "allow"}
	if _, err := e.LocalGroupAccess(ctx, request); err == nil {
		t.Fatal("unbound store accepted a write")
	}
	if err := e.Store.BindAccount("other-app"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.LocalGroupAccess(ctx, GroupAccessRequest{}); err == nil {
		t.Fatal("read accepted a store bound to another app")
	}
	e.Config.AppID, l.Config.AppID = "other-app", "other-app"

	for _, invalid := range []GroupAccessRequest{
		{Access: "allow"},
		{Mention: "default"},
		{ChatID: "oc_new"},
		{ChatID: "oc_", Access: "allow"},
		{ChatID: "oc_a/b", Access: "allow"},
		{ChatID: "ou_user", Mention: "on"},
		{ChatID: "oc_new", Access: "on"},
		{ChatID: "oc_new", Mention: "deny"},
	} {
		if _, err := e.LocalGroupAccess(ctx, invalid); err == nil {
			t.Fatalf("invalid request accepted: %+v", invalid)
		}
	}
	originalDir := e.Store.dir
	e.Store.dir = filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(e.Store.dir, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := e.LocalGroupAccess(ctx, request)
	e.Store.dir = originalDir
	if err == nil || len(e.Store.snapshot().GroupAccess.Chats) != 0 || l.Config.groupAllowed("oc_new") || f.listCalls != 0 {
		t.Fatalf("failed write touched persistent state, receiver policy, or group API: err=%v", err)
	}
}
