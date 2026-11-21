package bridge

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	channel "github.com/larksuite/channel-sdk-go"
)

func TestFeishuAccessPolicyBatchChangesOnlySelectedLayerAndReplaysOnce(t *testing.T) {
	e, _, ch, x, f := setupAccess(t)
	classifier := &fakeClassifier{d: Decision{RouteID: "nas", Action: "new_task"}}
	e.Classifier = classifier
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.update(func(st *State) error {
		st.AccessPolicy = AccessPolicy{
			Users:  map[string]bool{"ou_existing": true},
			Admins: map[string]bool{"ou_existing": true},
			Routes: map[string]map[string]bool{
				"nas": {"ou_existing": true},
				"mac": {"ou_first": true, "ou_second": true, "ou_literal": true},
			},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.publishPolicy()
	want := e.Store.snapshot().AccessPolicy

	for _, change := range []struct {
		kind  string
		allow bool
	}{
		{"user", true}, {"admin", true}, {"route", true},
		{"route", false}, {"admin", false}, {"user", false},
	} {
		verb := "/remove"
		if change.allow {
			verb = "/invite"
		}
		args := change.kind
		var layer map[string]bool
		switch change.kind {
		case "user":
			layer = want.Users
		case "admin":
			layer = want.Admins
		case "route":
			args += " nas"
			layer = want.Routes["nas"]
		}
		for _, id := range []string{"ou_first", "ou_second", "ou_literal"} {
			layer[id] = change.allow
		}

		id := fmt.Sprintf("batch-%s-%t", change.kind, change.allow)
		message := groupMessage(id, "user", "chat", "@_user_0 "+verb+" "+args+" @_user_1 @_user_2 ou_literal ou_first @_user_3", true)
		message.Mentions = []channel.Mention{
			{Key: "@_user_0", IsBot: true},
			{Key: "@_user_1", OpenID: "ou_first"},
			{Key: "@_user_2", OpenID: "ou_second"},
			{Key: "@_user_3", OpenID: "ou_second"},
		}
		replies := len(f.replies)
		receiveAccess(t, ch, message)
		tick(t, e)
		assertFeishuBatchPolicy(t, e, want)
		summary := fmt.Sprintf("已为 3 人保存 %s 权限 · 允许 %t", change.kind, change.allow)
		if change.kind == "route" {
			summary += " · route=nas"
		}
		if len(f.replies) != replies+1 || !strings.Contains(f.replies[replies], summary) {
			t.Fatalf("%s %s reply = %v, want one reply containing %q", verb, args, f.replies[replies:], summary)
		}

		if change.kind == "user" && !change.allow {
			m := Normalize(message)
			commandID := key(m, m.ID)
			receipt := e.Store.snapshot().Commands[commandID]
			if receipt == nil || receipt.ReplyAt == "" || !strings.Contains(receipt.Reply, summary) {
				t.Fatalf("missing successful revoke receipt: %+v", receipt)
			}
			if _, err := e.LocalAccessPolicy(t.Context(), LocalAccessPolicyRequest{Kind: "user", UserID: "ou_first", Mode: "allow"}); err != nil {
				t.Fatal(err)
			}
			want.Users["ou_first"] = true
			assertFeishuBatchPolicy(t, e, want)
			receiveAccess(t, ch, message)
			tick(t, e)
			assertFeishuBatchPolicy(t, e, want)
			if len(f.replies) != replies+1 || !reflect.DeepEqual(e.Store.snapshot().Commands[commandID], receipt) {
				t.Fatal("replayed batch changed the original receipt or sent another reply")
			}
		}
	}
	if st := e.Store.snapshot(); len(st.Tasks) != 0 || classifier.calls != 0 || x.preflights != 0 || x.opens != 0 || x.sends != 0 {
		t.Fatal("successful access commands reached the classifier or T3")
	}
}

func TestFeishuAccessPolicyBatchRejectsAllTargets(t *testing.T) {
	for _, tc := range []struct {
		name, command, reply string
		mention              channel.Mention
		withOwner            bool
	}{
		{name: "unrecognized name", command: "/invite user @_user_1 unknown-name ou_literal", reply: "open_id"},
		{name: "at all", command: "/invite admin @_user_1 @all ou_literal", reply: "open_id", mention: channel.Mention{Key: "@all", OpenID: "all"}},
		{name: "at underscore all", command: "/invite route nas @_user_1 @_all ou_literal", reply: "open_id", mention: channel.Mention{Key: "@_all", OpenID: "all"}},
		{name: "unknown route", command: "/invite route missing @_user_1 ou_literal", reply: "未知受控route"},
		{name: "owner after user", command: "/remove user @_user_1 @_user_2", reply: "Owner", withOwner: true},
		{name: "owner after admin", command: "/remove admin @_user_1 @_user_2", reply: "Owner", withOwner: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, l, ch, x, f := setupAccess(t)
			classifier := &fakeClassifier{d: Decision{RouteID: "nas", Action: "new_task"}}
			e.Classifier = classifier
			m := groupMessage("batch-rejected", "admin", "chat", tc.command, true)
			m.Mentions = []channel.Mention{{Key: "@_user_1", OpenID: "ou_first"}}
			if tc.mention.Key != "" {
				m.Mentions = append(m.Mentions, tc.mention)
				m.MentionAll = true
			}
			if err := e.Store.update(func(st *State) error {
				st.AccessPolicy = AccessPolicy{
					Users:  map[string]bool{"ou_existing": true},
					Admins: map[string]bool{"ou_existing": true},
					Routes: map[string]map[string]bool{"nas": {"ou_existing": true}},
				}
				if tc.withOwner {
					st.AccessPolicy.Users["ou_first"] = true
					st.AccessPolicy.Admins["ou_first"] = true
					st.AccessPolicy.Routes["nas"]["ou_first"] = true
					st.PendingReconnect = "waiting-for-reconnect"
					st.Tasks["waiting"] = &Task{Source: Message{UserID: "ou_first"}, RouteID: "nas", Phase: "waiting_capacity"}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			e.publishPolicy()
			if tc.withOwner {
				f.owner = "ou_owner"
				e.Config.access.mu.Lock()
				e.Config.access.nextOwnerRefresh = time.Time{}
				e.Config.access.mu.Unlock()
				l.RefreshOwner(t.Context())
				if e.Config.ownerID() != f.owner {
					t.Fatal("loopback App owner was not refreshed")
				}
				m.Mentions = append(m.Mentions, channel.Mention{Key: "@_user_2", OpenID: f.owner})
				// Keep ordinary work queued while the worker handles the immediate command.
				e.PollInterval = time.Hour
				e.nextPoll = e.now().Add(e.PollInterval)
				receiveAccess(t, ch, groupMessage("waiting-message", "ou_first", "chat", "等待派发", true))
			}
			before := e.Store.snapshot()
			if tc.withOwner && (len(before.Inbox) != 1 || before.Inbox[0].Cancelled) {
				t.Fatalf("expected an existing pending message, got %+v", before.Inbox)
			}
			receiveAccess(t, ch, m)
			tick(t, e)
			assertFeishuBatchPolicy(t, e, before.AccessPolicy)
			after := e.Store.snapshot()
			if len(after.Inbox) != len(before.Inbox) || (len(before.Inbox) > 0 && !reflect.DeepEqual(after.Inbox, before.Inbox)) || !reflect.DeepEqual(after.Tasks, before.Tasks) {
				t.Fatalf("rejected batch changed queued work: inbox = %+v, waiting task = %+v", after.Inbox, after.Tasks["waiting"])
			}
			if len(f.replies) != 1 || !strings.Contains(f.replies[0], tc.reply) || strings.Contains(f.replies[0], "已保存") {
				t.Fatalf("expected one rejection containing %q, got %v", tc.reply, f.replies)
			}
			if classifier.calls != 0 || x.preflights != 0 || x.opens != 0 || x.sends != 0 || x.stops != 0 {
				t.Fatal("rejected access command reached the classifier or T3")
			}
		})
	}
}

func assertFeishuBatchPolicy(t *testing.T, e *Engine, want AccessPolicy) {
	t.Helper()
	e.Config.access.mu.RLock()
	published := e.Config.access.policy
	e.Config.access.mu.RUnlock()
	disk, err := readStateSnapshot(e.Store.dir)
	if err != nil {
		t.Fatal(err)
	}
	if stored := e.Store.snapshot().AccessPolicy; !reflect.DeepEqual(stored, want) || !reflect.DeepEqual(disk.AccessPolicy, want) || !reflect.DeepEqual(published, want) {
		t.Fatalf("policy = %+v in store, %+v on disk, %+v in receiver; want %+v", stored, disk.AccessPolicy, published, want)
	}
}
