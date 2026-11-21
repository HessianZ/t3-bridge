package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/localfile"
	channel "github.com/larksuite/channel-sdk-go"
)

const stage68Admin = "ou_settings_admin"

func setupStage68RunSettings(t *testing.T) (*Engine, *Lark, *accessChannel, *fakeExecutor, *accessFixture) {
	t.Helper()
	e, l, ch, x, f := setupAccess(t)
	l.Config.Topics = nil
	l.Config.Admins = []string{stage68Admin}
	l.Config.Users = append(l.Config.Users, stage68Admin)
	l.Config.DirectChats = &[]string{"dm"}
	e.Config = l.Config
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	return e, l, ch, x, f
}

func openStage68RunSettings(t *testing.T, e *Engine, ch *accessChannel, id, command string) *SessionCommand {
	t.Helper()
	accessDMCommand(t, e, ch, id, stage68Admin, command)
	origin := e.Store.snapshot().Commands[key(Message{ChatID: "dm"}, id)]
	if origin == nil || origin.Kind != runSettingsCardCommand || origin.Card == nil || origin.RouteID != "" ||
		origin.Card.AppID != e.Config.AppID || origin.Card.ReplyID == "" || origin.Card.RunSettingsForm == nil ||
		len(origin.Card.Buttons) != 1 || origin.Card.Buttons[0].Command != runSettingsCardCommand {
		t.Fatalf("missing stored settings form for %q: %+v", command, origin)
	}
	return origin
}

func stage68FormAction(origin *SessionCommand, event string, fields map[string]any) channel.CardActionEvent {
	return channel.CardActionEvent{
		EventID: event, MessageID: origin.Card.ReplyID, ChatID: origin.Source.ChatID,
		Operator: channel.CardActionOperator{OpenID: origin.Source.UserID},
		Action: channel.CardActionPayload{
			Tag: "button", Name: "run_settings_submit",
			Value:     map[string]any{"action": commandCardActionType, "id": commandButtonID(origin, 0)},
			FormValue: fields,
		},
	}
}

func requireStage68Text(t *testing.T, replies []replyModeRequest, fragment string) {
	t.Helper()
	if len(replies) != 1 || replies[0].MsgType != "text" || !strings.Contains(replies[0].Content, fragment) {
		t.Fatalf("want one text reply containing %q, got %+v", fragment, replies)
	}
}

func TestStage68RunSettingsFormFeishuFlow(t *testing.T) {
	ctx := context.Background()
	e, l, ch, x, f := setupStage68RunSettings(t)
	replies := captureReplyModes(t, f)
	textMode := "text"
	base, err := e.LocalRunSettings(ctx, RunSettingsPatch{ReplyMode: &textMode})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []GroupAccessRequest{
		{ChatID: "oc_override", Access: "allow", Mention: "on"},
		{ChatID: "oc_denied", Access: "deny", Mention: "off"},
	} {
		if _, err := e.LocalGroupAccess(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	groups := e.Store.snapshot().GroupAccess

	accessCommand(t, e, ch, "group-config", stage68Admin, "chat", "/config")
	requireStage68Text(t, replies["group-config"], "当前群授权")
	accessCommand(t, e, ch, "group-non-admin", "other", "chat", "/config")
	requireStage68Text(t, replies["group-non-admin"], "仅应用所有者")
	accessCommand(t, e, ch, "group-run", stage68Admin, "chat", "/config run")
	requireStage68Text(t, replies["group-run"], "已授权私聊")
	accessDMCommand(t, e, ch, "private-non-admin", "other", "/config run")
	requireStage68Text(t, replies["private-non-admin"], "owner/admin")

	origin := openStage68RunSettings(t, e, ch, "explicit", "/config run")
	alias := openStage68RunSettings(t, e, ch, "alias", "/config")
	for _, cmd := range []*SessionCommand{origin, alias} {
		if got := *cmd.Card.RunSettingsForm; got != (RunSettingsForm{Settings: base, RequireMention: true}) {
			t.Fatalf("%q stored the wrong base snapshot: %+v", cmd.Source.Text, got)
		}
		got := replies[cmd.Source.ID]
		if len(got) != 1 || got[0].MsgType != "interactive" || got[0].UUID == "" || got[0].ReplyInThread || cmd.Card.ReplyID != "reply-"+got[0].UUID {
			t.Fatalf("%q did not publish an interactive private reply: %+v", cmd.Source.Text, got)
		}
	}
	fields := map[string]any{
		"max_concurrent": "50", "idle_timeout_minutes": "21", "progress": "off", "reply_mode": "markdown",
		"tool_activity": "detailed", "process_mode": "stream-brief", "require_mention": "off",
	}
	action := stage68FormAction(origin, "full-form", fields)
	action.Action.Name = checkStage68FormWire(t, origin, replies["explicit"][0].Content, fields)
	checkStage68FormWire(t, alias, replies["alias"][0].Content, fields)
	other := action
	other.EventID, other.Operator.OpenID = "other-admin", "user" // The fixture's application owner.
	clickCommandCard(t, l, other)
	wrongCard := action
	wrongCard.EventID, wrongCard.MessageID = "wrong-card", alias.Card.ReplyID
	clickCommandCard(t, l, wrongCard)
	otherApp := *l
	otherApp.Config.AppID = "other-app"
	clickCommandCard(t, &otherApp, action)
	if len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("wrong actor, application or reply card could enqueue a form")
	}

	clickCommandCard(t, l, action)
	clickCommandCard(t, l, action)
	queued := e.Store.snapshot().Inbox
	max, idle, off := 50, 21, false
	reply, tools, process := "markdown", "detailed", "stream-brief"
	wantSubmission := &RunSettingsFormSubmission{
		Settings:       RunSettingsPatch{MaxConcurrent: &max, IdleTimeoutMinutes: &idle, Progress: &off, ReplyMode: &reply, ToolActivity: &tools, ProcessMode: &process},
		RequireMention: &off,
	}
	if len(queued) != 1 || queued[0].ID != "card:"+action.EventID || queued[0].ParentID != origin.Card.ReplyID ||
		queued[0].ChatType != "p2p" || queued[0].Text != "" || !reflect.DeepEqual(queued[0].RunSettingsForm, wantSubmission) {
		t.Fatalf("form submission was not queued once with typed fields: %+v", queued)
	}
	want := RunSettingsView{MaxConcurrent: max, IdleTimeoutMinutes: idle, Progress: off, ReplyMode: reply, ToolActivity: tools, ProcessMode: process}
	if e.Store.runSettings().view() != base || !l.Config.requireMention("chat") {
		t.Fatal("callback applied settings before the worker ran")
	}
	originKey, submitKey := key(origin.Source, origin.Source.ID), key(queued[0], queued[0].ID)
	checkCommit := func(st State) {
		t.Helper()
		card, receipt := st.Commands[originKey], st.Commands[submitKey]
		if st.Settings.view() != want || st.GroupAccess.RequireMention == nil || st.GroupAccess.defaultRequiresMention() ||
			!reflect.DeepEqual(st.GroupAccess.Chats, groups.Chats) || !reflect.DeepEqual(st.GroupAccess.Mentions, groups.Mentions) ||
			card == nil || card.Card == nil || !card.Card.Used || receipt == nil || receipt.Kind != runSettingsCardCommand || !strings.Contains(receipt.Reply, "已保存运行设置") {
			t.Errorf("settings, group overrides, use and receipt did not commit together: settings=%+v groups=%+v card=%+v receipt=%+v", st.Settings.view(), st.GroupAccess, card, receipt)
		}
	}
	firstWrite := true
	e.Store.writeState = func(path string, data []byte, createOnly bool) (bool, error) {
		if firstWrite {
			firstWrite = false
			var candidate State
			if err := json.Unmarshal(data, &candidate); err != nil {
				return false, err
			}
			checkCommit(candidate)
			if !l.Config.requireMention("chat") {
				t.Error("receiver group default changed before state publication")
			}
		}
		return localfile.WritePrivateAtomic(path, data, createOnly)
	}
	tick(t, e)
	e.Store.writeState = nil
	disk, err := readStateSnapshot(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	checkCommit(disk)
	requireStage68Text(t, replies[origin.Card.ReplyID], "已保存运行设置")
	if firstWrite || l.Config.requireMention("chat") || !l.Config.requireMention("oc_override") ||
		!l.Config.groupAllowed("oc_override") || l.Config.groupAllowed("oc_denied") || len(disk.Inbox) != 0 || x.sends != 0 {
		t.Fatal("published receiver policy, overrides, inbox or executor differed after saving")
	}

	newLimit := 8
	if _, err := e.LocalRunSettings(ctx, RunSettingsPatch{MaxConcurrent: &newLimit}); err != nil {
		t.Fatal(err)
	}
	clickCommandCard(t, l, action)
	action.EventID = "repeat-press"
	clickCommandCard(t, l, action)
	if len(e.Store.snapshot().Inbox) != 0 || e.Store.runSettings().MaxConcurrent != newLimit || len(replies[origin.Card.ReplyID]) != 1 {
		t.Fatal("a replay or second press reached the worker or changed the newer setting")
	}
}

func checkStage68FormWire(t *testing.T, origin *SessionCommand, body string, fields map[string]any) string {
	t.Helper()
	var form map[string]any
	for _, raw := range card2Elements(t, body) {
		if el, ok := raw.(map[string]any); ok && el["tag"] == "form" {
			if form != nil {
				t.Fatal("multiple forms in run settings card")
			}
			form = el
		}
	}
	elements, ok := form["elements"].([]any)
	if !ok || form["name"] == nil || form["name"] == "" {
		t.Fatalf("missing named form: %+v", form)
	}
	seen := make(map[string]bool)
	var submit string
	for _, raw := range elements {
		el, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("unexpected form element: %+v", raw)
		}
		name, _ := el["name"].(string)
		if el["tag"] == "button" {
			behaviors, _ := el["behaviors"].([]any)
			want := []any{map[string]any{"type": "callback", "value": map[string]any{"action": commandCardActionType, "id": commandButtonID(origin, 0)}}}
			if submit != "" || name == "" || el["form_action_type"] != "submit" || !reflect.DeepEqual(behaviors, want) {
				t.Fatalf("unexpected form submit binding: %+v", el)
			}
			submit = name
		} else if _, expected := fields[name]; expected {
			if seen[name] {
				t.Fatalf("duplicate form field %q", name)
			}
			seen[name] = true
		}
	}
	if submit == "" || len(seen) != len(fields) {
		t.Fatalf("missing form fields or submit: fields=%v submit=%q", seen, submit)
	}
	return submit
}

func TestStage68RunSettingsFormRejectsInvalidAndLegacyCallbacks(t *testing.T) {
	e, l, ch, x, f := setupStage68RunSettings(t)
	replies := captureReplyModes(t, f)
	origin := openStage68RunSettings(t, e, ch, "open", "/config run")
	fields := map[string]any{"progress": "off"}
	accessDMCommand(t, e, ch, "legacy", stage68Admin, "/config display")
	legacy := e.Store.snapshot().Commands[key(origin.Source, "legacy")]
	if legacy == nil || legacy.Card == nil || legacy.Card.RunSettingsForm != nil || len(replies["legacy"]) != 1 {
		t.Fatalf("legacy display card missing: %+v", legacy)
	}
	forged := commandCardClick(t, legacy, replies["legacy"][0].Content, "终态 Markdown", "forged")
	forged.Action.Name, forged.Action.FormValue = "run_settings_submit", fields
	clickCommandCard(t, l, forged)
	if len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("legacy display accepted a forged form")
	}

	for _, tc := range []struct {
		name, field string
		value       any
	}{
		{"bad-number-type", "max_concurrent", float64(4)},
		{"bad-idle-range", "idle_timeout_minutes", "121"},
		{"bad-toggle", "require_mention", "false"},
		{"bad-option", "reply_mode", "unsupported"},
		{"unknown-field", "other", "synthetic-unrecognized-value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := e.Store.snapshot()
			count := len(replies[origin.Card.ReplyID])
			action := stage68FormAction(origin, tc.name, map[string]any{"progress": "off", tc.field: tc.value})
			clickCommandCard(t, l, action)
			queued := e.Store.snapshot().Inbox
			if len(queued) != 1 || queued[0].RunSettingsForm != nil || queued[0].Text != "" {
				t.Fatalf("invalid form input reached the queue as settings or text: %+v", queued)
			}
			tick(t, e)
			requireStage68Text(t, replies[origin.Card.ReplyID][count:], "输入无效")
			after := e.Store.snapshot()
			if !reflect.DeepEqual(after.Settings, before.Settings) || !reflect.DeepEqual(after.GroupAccess, before.GroupAccess) ||
				after.Commands[key(origin.Source, origin.Source.ID)].Card.Used || after.Commands[key(legacy.Source, legacy.Source.ID)].Card.Used ||
				len(after.Inbox) != 0 || x.sends != 0 {
				t.Fatal("invalid or forged submission changed state, consumed a card, or dispatched to T3")
			}
		})
	}
}

func TestStage68RunSettingsFormNoChangeAndPartialUpdate(t *testing.T) {
	e, l, ch, x, f := setupStage68RunSettings(t)
	replies := captureReplyModes(t, f)
	before := e.Store.snapshot()
	origin := openStage68RunSettings(t, e, ch, "unchanged", "/config run")
	clickCommandCard(t, l, stage68FormAction(origin, "same-effective-settings", map[string]any{
		"max_concurrent": "0", "idle_timeout_minutes": "0", "progress": "on", "reply_mode": "card",
		"tool_activity": "default", "process_mode": "off", "require_mention": "on",
	}))
	tick(t, e)
	requireStage68Text(t, replies[origin.Card.ReplyID], "没有变化")
	unchanged := e.Store.snapshot()
	if !reflect.DeepEqual(unchanged.Settings, before.Settings) || !reflect.DeepEqual(unchanged.GroupAccess, before.GroupAccess) {
		t.Fatal("submitting the effective defaults materialized setting overrides")
	}

	origin = openStage68RunSettings(t, e, ch, "partial", "/config")
	clickCommandCard(t, l, stage68FormAction(origin, "only-progress", map[string]any{"progress": "off"}))
	tick(t, e)
	requireStage68Text(t, replies[origin.Card.ReplyID], "已保存运行设置")
	after := e.Store.snapshot()
	want := before.Settings.view()
	want.Progress = false
	if after.Settings.view() != want || !reflect.DeepEqual(after.GroupAccess, before.GroupAccess) ||
		!after.Commands[key(origin.Source, origin.Source.ID)].Card.Used || len(after.Inbox) != 0 || x.sends != 0 {
		t.Fatalf("partial form did not save just progress: settings=%+v groups=%+v", after.Settings.view(), after.GroupAccess)
	}
}

func TestStage68RunSettingsFormRejectsStaleBase(t *testing.T) {
	for _, field := range []string{"run-settings", "group-default"} {
		t.Run(field, func(t *testing.T) {
			ctx := context.Background()
			e, l, ch, x, f := setupStage68RunSettings(t)
			replies := captureReplyModes(t, f)
			origin := openStage68RunSettings(t, e, ch, "open", "/config run")
			clickCommandCard(t, l, stage68FormAction(origin, "pending", map[string]any{"reply_mode": "markdown"}))
			if len(e.Store.snapshot().Inbox) != 1 {
				t.Fatal("valid partial callback was not queued")
			}
			// Neither interleaved write touches the field in the queued submission.
			if field == "run-settings" {
				idle := 7
				if _, err := e.LocalRunSettings(ctx, RunSettingsPatch{IdleTimeoutMinutes: &idle}); err != nil {
					t.Fatal(err)
				}
			} else if _, err := e.LocalGroupAccess(ctx, GroupAccessRequest{Mention: "off"}); err != nil {
				t.Fatal(err)
			}
			latest := e.Store.snapshot()
			tick(t, e)
			requireStage68Text(t, replies[origin.Card.ReplyID], "设置已变化")
			after := e.Store.snapshot()
			if !reflect.DeepEqual(after.Settings, latest.Settings) || !reflect.DeepEqual(after.GroupAccess, latest.GroupAccess) ||
				after.Commands[key(origin.Source, origin.Source.ID)].Card.Used || len(after.Inbox) != 0 || x.sends != 0 {
				t.Fatal("stale submission overwrote an interleaved change or consumed the card")
			}
		})
	}
}

func TestStage68RunSettingsFormRevalidatesQueuedCallback(t *testing.T) {
	for _, name := range []string{"revoked-admin", "different-app", "replaced-card", "expired-card"} {
		t.Run(name, func(t *testing.T) {
			e, l, ch, x, _ := setupStage68RunSettings(t)
			origin := openStage68RunSettings(t, e, ch, "open", "/config run")
			clickCommandCard(t, l, stage68FormAction(origin, "pending", map[string]any{"max_concurrent": "7", "require_mention": "off"}))
			queued := e.Store.snapshot().Inbox
			if len(queued) != 1 || queued[0].RunSettingsForm == nil {
				t.Fatalf("valid callback was not queued: %+v", queued)
			}
			originKey := key(origin.Source, origin.Source.ID)
			switch name {
			case "revoked-admin":
				request := LocalAccessPolicyRequest{Kind: "admin", UserID: stage68Admin, Mode: "deny"}
				if _, err := e.LocalAccessPolicy(context.Background(), request); err != nil {
					t.Fatal(err)
				}
				if e.Config.isAdmin(stage68Admin) || !e.Config.Allowed(queued[0]) || len(e.Store.snapshot().Inbox) != 1 {
					t.Fatal("fixture must revoke administrator authority while retaining private-chat access and the queued callback")
				}
			case "different-app":
				e.Config.AppID = "another-app"
			default:
				if err := e.Store.update(func(st *State) error {
					if name == "expired-card" {
						st.Commands[originKey].ReplyAt = time.Now().Add(-2 * commandReplyWindow).Format(time.RFC3339Nano)
					} else {
						st.Commands[originKey].Card.ReplyID = "replacement-reply"
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			before := e.Store.snapshot()
			tick(t, e)
			after := e.Store.snapshot()
			if !reflect.DeepEqual(after.Settings, before.Settings) || !reflect.DeepEqual(after.GroupAccess, before.GroupAccess) ||
				!l.Config.requireMention("chat") || after.Commands[originKey].Card.Used || len(after.Inbox) != 0 || x.sends != 0 {
				t.Fatal("callback changed settings or consumed the origin after losing its authorization or binding")
			}
		})
	}
}

func TestStage68RunSettingsFormTextFallback(t *testing.T) {
	for _, name := range []string{"unavailable", "rejected"} {
		t.Run(name, func(t *testing.T) {
			e, l, ch, x, f := setupStage68RunSettings(t)
			replies := captureReplyModes(t, f)
			const id = "open-with-fallback"
			if name == "unavailable" {
				e.Lark = struct {
					Messenger
					sessionMessenger
				}{l, l}
			} else {
				capture := f.extra
				f.extra = func(w http.ResponseWriter, r *http.Request) bool {
					f.replyFail = r.URL.Path == "/open-apis/im/v1/messages/"+id+"/reply" && len(replies[id]) == 0
					return capture(w, r)
				}
			}
			accessDMCommand(t, e, ch, id, stage68Admin, "/config run")
			got := replies[id]
			if name == "rejected" {
				if len(got) != 2 || got[0].MsgType != "interactive" || got[0].UUID == "" || got[0].UUID != got[1].UUID {
					t.Fatalf("rejected card did not retry with the same reply anchor and UUID: %+v", got)
				}
				got = got[1:]
			}
			requireStage68Text(t, got, "并发预算 0")
			for _, command := range []string{"/config concurrency", "/config idle-timeout", "/config progress", "/config reply", "/config tools", "/config process", "/config mention all"} {
				if !strings.Contains(got[0].Content, command) {
					t.Errorf("text fallback omits %q", command)
				}
			}
			st := e.Store.snapshot()
			cmd := st.Commands[key(Message{ChatID: "dm"}, id)]
			if cmd == nil || cmd.Card != nil || cmd.Issue != "" || len(st.Inbox) != 0 || x.sends != 0 {
				t.Fatalf("plain fallback left an actionable card or an unresolved receipt: %+v", cmd)
			}
		})
	}
}
