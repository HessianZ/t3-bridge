package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"testing"

	channel "github.com/larksuite/channel-sdk-go"
)

const (
	stage163Admin  = "ou_admin"
	stage163Target = "oc_target"
	stage163Other  = "oc_other"
)

func setupStage163GroupAccess(t *testing.T) (*Engine, *Lark, *accessChannel, *accessFixture) {
	t.Helper()
	e, l, ch, x, f := setupAccess(t)
	if err := e.Store.BindAccount("fake-app"); err != nil {
		t.Fatal(err)
	}
	e.Config.Topics, l.Config.Topics = nil, nil
	e.Config.Admins, l.Config.Admins = []string{stage163Admin}, []string{stage163Admin}
	e.Config.Users = append(e.Config.Users, stage163Admin)
	e.Config.DirectChats = &[]string{"dm"}
	l.Config.Users, l.Config.DirectChats = e.Config.Users, e.Config.DirectChats
	if err := e.Store.update(func(st *State) error {
		st.GroupAccess.setAllowed(stage163Other, true)
		st.GroupAccess.setAllowed("oc_unjoined", true)
		st.GroupAccess.setMention(stage163Other, false)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	groups, _ := e.Store.groupAccess()
	e.Config.access.publishGroups(groups)
	stubStage163JoinedGroups(f, stage163Target, stage163Other)
	t.Cleanup(func() {
		if tasks := len(e.Store.snapshot().Tasks); x.opens != 0 || x.sends != 0 || x.stops != 0 || x.uploads != 0 || x.preflights != 0 || tasks != 0 {
			t.Errorf("group access reached T3 workflow: tasks=%d open=%d send=%d stop=%d upload=%d preflight=%d", tasks, x.opens, x.sends, x.stops, x.uploads, x.preflights)
		}
	})
	return e, l, ch, f
}

func stubStage163JoinedGroups(f *accessFixture, ids ...string) {
	items := make([]map[string]string, 0, len(ids))
	for _, id := range ids {
		items = append(items, map[string]string{"chat_id": id, "name": "Joined " + id})
	}
	f.extra = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodGet || r.URL.Path != "/open-apis/im/v1/chats" {
			return false
		}
		f.listCalls++
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"items": items, "has_more": false}})
		return true
	}
}

func stage163GroupCard(t *testing.T, e *Engine, id string) *SessionCommand {
	t.Helper()
	cmd := e.Store.snapshot().Commands[key(Message{ChatID: "dm"}, id)]
	if cmd == nil || cmd.Card == nil || cmd.Card.ReplyID == "" {
		t.Fatalf("%s has no delivered group access card: %+v", id, cmd)
	}
	return cmd
}

func stage163GroupClick(t *testing.T, f *accessFixture, cmd *SessionCommand, page int, chat, operation, event string) channel.CardActionEvent {
	t.Helper()
	for i, b := range cmd.Card.Buttons {
		g := b.GroupAccess
		if g == nil || g.ChatID != chat || g.Operation != operation || (chat == "" && g.Page != page) {
			continue
		}
		for _, raw := range card2Elements(t, f.replies[len(f.replies)-1]) {
			element := raw.(map[string]any)
			if element["tag"] != "button" {
				continue
			}
			for _, rawBehavior := range element["behaviors"].([]any) {
				behavior := rawBehavior.(map[string]any)
				value, _ := behavior["value"].(map[string]any)
				if behavior["type"] != "callback" || value["id"] != commandButtonID(cmd, i) {
					continue
				}
				confirm, _ := element["confirm"].(map[string]any)
				if operation != "" && (confirm["title"] == nil || confirm["text"] == nil) {
					t.Fatalf("%s has no native Feishu confirmation: %+v", operation, element)
				}
				return channel.CardActionEvent{EventID: event, ChatID: cmd.Source.ChatID, MessageID: cmd.Card.ReplyID,
					Operator: channel.CardActionOperator{OpenID: cmd.Source.UserID}, Action: channel.CardActionPayload{Tag: "button", Value: value}}
			}
		}
	}
	t.Fatalf("card %s: no callback for page=%d chat=%q operation=%q", cmd.Source.ID, page, chat, operation)
	return channel.CardActionEvent{}
}

func stage163ListedGroups(cmd *SessionCommand) []string {
	var ids []string
	for _, b := range cmd.Card.Buttons {
		if g := b.GroupAccess; g != nil && g.ChatID != "" && g.Operation == "" {
			ids = append(ids, g.ChatID)
		}
	}
	slices.Sort(ids)
	return ids
}

func assertStage163GroupAccess(t *testing.T, e *Engine, want GroupAccess) {
	t.Helper()
	if got := e.Store.snapshot().GroupAccess; !reflect.DeepEqual(got, want) {
		t.Fatalf("group overrides = %+v, want %+v", got, want)
	}
}

func TestStage163GroupAccessCardPagesAndDetailAreReadOnly(t *testing.T) {
	e, l, ch, f := setupStage163GroupAccess(t)
	joined := []string{"oc_a", "oc_b", "oc_c", "oc_d", "oc_e", stage163Other, stage163Target}
	stubStage163JoinedGroups(f, joined...)
	before := e.Store.snapshot().GroupAccess

	accessDMCommand(t, e, ch, "groups", stage163Admin, "/config groups")
	first := stage163GroupCard(t, e, "groups")
	firstIDs := stage163ListedGroups(first)
	if len(firstIDs) != 6 {
		t.Fatalf("first page groups = %v, want six", firstIDs)
	}
	clickCommandCard(t, l, stage163GroupClick(t, f, first, 2, "", "", "next"))
	tick(t, e)
	second := stage163GroupCard(t, e, "card:next")
	secondIDs := stage163ListedGroups(second)
	all := append(slices.Clone(firstIDs), secondIDs...)
	slices.Sort(all)
	if len(secondIDs) != 1 || !slices.Equal(all, joined) {
		t.Fatalf("group pagination = %v / %v, want %v once each", firstIDs, secondIDs, joined)
	}
	clickCommandCard(t, l, stage163GroupClick(t, f, second, 0, secondIDs[0], "", "detail"))
	tick(t, e)
	detail := stage163GroupCard(t, e, "card:detail")
	for _, operation := range []string{"allow", "deny", "inherit", "mention on", "mention off", "mention inherit"} {
		stage163GroupClick(t, f, detail, 0, secondIDs[0], operation, "unused")
	}
	accessDMCommand(t, e, ch, "groups-2", stage163Admin, "/config groups 2")
	if ids := stage163ListedGroups(stage163GroupCard(t, e, "groups-2")); !slices.Equal(ids, secondIDs) {
		t.Fatalf("explicit second page groups = %v, want %v", ids, secondIDs)
	}
	assertStage163GroupAccess(t, e, before)
}

func TestStage163GroupAccessCardWritesOnlySelectedGroupOnce(t *testing.T) {
	e, l, ch, f := setupStage163GroupAccess(t)
	before := e.Store.snapshot()
	accessDMCommand(t, e, ch, "target", stage163Admin, "/config groups "+stage163Target)
	detail := stage163GroupCard(t, e, "target")
	grant := stage163GroupClick(t, f, detail, 0, stage163Target, "allow", "grant")
	mention := stage163GroupClick(t, f, detail, 0, stage163Target, "mention off", "reuse")
	wrongUser, wrongChat := grant, grant
	wrongUser.EventID, wrongUser.Operator.OpenID = "wrong-user", "user"
	wrongChat.EventID, wrongChat.ChatID = "wrong-chat", "other-dm"
	listCalls := f.listCalls
	clickCommandCard(t, l, wrongUser)
	clickCommandCard(t, l, wrongChat)
	accessCommand(t, e, ch, "group-member", "other", "chat", "/config groups")
	assertStage163GroupAccess(t, e, before.GroupAccess)
	if f.listCalls != listCalls || stage163GroupCard(t, e, "target").Card.Used {
		t.Fatal("out-of-scope request read the group directory or consumed the detail card")
	}

	clickCommandCard(t, l, grant)
	tick(t, e)
	want := before.GroupAccess
	want.setAllowed(stage163Target, true)
	assertStage163GroupAccess(t, e, want)
	if !l.Config.groupAllowed(stage163Target) || !stage163GroupCard(t, e, "target").Card.Used {
		t.Fatal("grant did not reach the receiver or consume its detail card")
	}
	listCalls = f.listCalls
	clickCommandCard(t, l, mention)
	tick(t, e)
	assertStage163GroupAccess(t, e, want)
	if f.listCalls != listCalls {
		t.Fatal("used detail card reentered the write path")
	}

	accessDMCommand(t, e, ch, "fresh-target", stage163Admin, "/config groups "+stage163Target)
	fresh := stage163GroupCard(t, e, "fresh-target")
	clickCommandCard(t, l, stage163GroupClick(t, f, fresh, 0, stage163Target, "mention off", "mention"))
	tick(t, e)
	want.setMention(stage163Target, false)
	assertStage163GroupAccess(t, e, want)
	if l.Config.requireMention(stage163Target) || !reflect.DeepEqual(e.Store.snapshot().AccessPolicy, before.AccessPolicy) {
		t.Fatal("mention change did not reach the receiver or changed the independent access policy")
	}
}

func TestStage163GroupAccessCardRejectsChangedWriteConditions(t *testing.T) {
	for _, name := range []string{"stale", "default-changed", "left", "list-failed", "admin-revoked"} {
		t.Run(name, func(t *testing.T) {
			e, l, ch, f := setupStage163GroupAccess(t)
			operation := "allow"
			if name == "default-changed" {
				operation = "mention inherit"
				if _, err := e.LocalGroupAccess(context.Background(), GroupAccessRequest{ChatID: stage163Target, Mention: "on"}); err != nil {
					t.Fatal(err)
				}
			}
			accessDMCommand(t, e, ch, "target", stage163Admin, "/config groups "+stage163Target)
			detail := stage163GroupCard(t, e, "target")
			grant := stage163GroupClick(t, f, detail, 0, stage163Target, operation, "grant")
			switch name {
			case "stale":
				if _, err := e.LocalGroupAccess(context.Background(), GroupAccessRequest{ChatID: stage163Target, Access: "deny"}); err != nil {
					t.Fatal(err)
				}
			case "default-changed":
				if _, err := e.LocalGroupAccess(context.Background(), GroupAccessRequest{Mention: "off"}); err != nil {
					t.Fatal(err)
				}
			case "left":
				stubStage163JoinedGroups(f, stage163Other)
			case "list-failed":
				f.extra, f.listFail = nil, true
			}
			before := e.Store.snapshot().GroupAccess
			listCalls := f.listCalls
			clickCommandCard(t, l, grant)
			if name == "admin-revoked" {
				if inbox := e.Store.snapshot().Inbox; len(inbox) != 1 || inbox[0].ID != "card:grant" {
					t.Fatalf("admin callback was not queued before revocation: %+v", inbox)
				}
				if _, err := e.LocalAccessPolicy(context.Background(), LocalAccessPolicyRequest{Kind: "admin", UserID: stage163Admin, Mode: "deny"}); err != nil {
					t.Fatal(err)
				}
				if l.Config.isAdmin(stage163Admin) || !l.Config.Allowed(detail.Source) {
					t.Fatal("fixture did not remove only the administrator permission")
				}
			}
			tick(t, e)
			assertStage163GroupAccess(t, e, before)
			if l.Config.groupAllowed(stage163Target) {
				t.Fatal("rejected grant changed the receiver's group policy")
			}
			if (name == "left" || name == "list-failed") && f.listCalls == listCalls {
				t.Fatal("write did not recheck the joined-group directory")
			}
		})
	}
}
