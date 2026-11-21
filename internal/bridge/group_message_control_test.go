package bridge

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type testGroupMessageControl struct {
	*Lark
	scope          GroupMessageScope
	grant          groupMessageGrant
	checks, grants int
	loseGrantReply bool
}

func (m *testGroupMessageControl) groupMessageScope(context.Context) GroupMessageScope {
	m.checks++
	return m.scope
}

func (m *testGroupMessageControl) beginGroupMessageGrant(context.Context) (groupMessageGrant, error) {
	m.grants++
	return m.grant, nil
}

func (m *testGroupMessageControl) ReplyCommandCard(ctx context.Context, source Message, content string, card bool) (string, error) {
	if m.loseGrantReply && card && strings.Contains(content, m.grant.URL) {
		return "", errors.New("synthetic send unconfirmed")
	}
	return m.Lark.ReplyCommandCard(ctx, source, content, card)
}

func TestGroupMessageControlPrivateAuthorizationAndRecheck(t *testing.T) {
	e, lark, ch, _, fixture := setupStage68RunSettings(t)
	messenger := &testGroupMessageControl{Lark: lark, scope: GroupMessageScopeMissing, grant: groupMessageGrant{
		URL: "https://accounts.feishu.cn/page/launcher?ticket=synthetic", ExpiresAt: time.Now().Add(time.Minute),
	}}
	e.Lark = messenger
	replies := captureReplyModes(t, fixture)
	accessDMCommand(t, e, ch, "non-admin", "other", groupMessageAuthorizeCommand)
	accessCommand(t, e, ch, "wrong-chat", stage68Admin, "chat", groupMessageAuthorizeCommand)
	if messenger.checks != 0 || messenger.grants != 0 {
		t.Fatal("non-admin or group command called the scope API")
	}

	accessDMCommand(t, e, ch, "check", stage68Admin, groupMessageCheckCommand)
	check := e.Store.snapshot().Commands[key(Message{ChatID: "dm"}, "check")]
	if check == nil || check.Card == nil || len(replies["check"]) != 1 || messenger.checks != 1 || messenger.grants != 0 {
		t.Fatal("checking missing permission did not offer one card without starting authorization")
	}
	choice := commandCardClick(t, check, replies["check"][0].Content, "申请群消息权限", "ask")
	forged := choice
	forged.Operator.OpenID = "user"
	clickCommandCard(t, lark, forged)
	tick(t, e)
	if messenger.grants != 0 {
		t.Fatal("another administrator could reuse the original user's card")
	}
	clickCommandCard(t, lark, choice)
	clickCommandCard(t, lark, choice)
	tick(t, e)
	grantReply := replies[check.Card.ReplyID]
	receipt := e.Store.snapshot().Commands[key(Message{ChatID: "dm"}, "card:ask")]
	if messenger.grants != 1 || messenger.checks != 2 || len(grantReply) != 1 || grantReply[0].MsgType != "interactive" ||
		!strings.Contains(grantReply[0].Content, messenger.grant.URL) || receipt == nil || receipt.Card == nil || receipt.GroupMessageGrant != nil {
		t.Fatal("private authorization did not offer one link and clear it after delivery")
	}

	messenger.scope = GroupMessageScopeGranted
	recheck := commandCardClick(t, receipt, grantReply[0].Content, "复查权限", "recheck")
	clickCommandCard(t, lark, recheck)
	tick(t, e)
	requireStage68Text(t, replies[receipt.Card.ReplyID], "已授予 im:message.group_msg")
	if messenger.checks != 3 || messenger.grants != 1 || !e.Store.snapshot().GroupAccess.defaultRequiresMention() {
		t.Fatal("checking authorization changed Bridge policy or started a new flow")
	}
}

func TestGroupMessageGrantDeliveryRetryAndExpiry(t *testing.T) {
	e, lark, ch, _, fixture := setupStage68RunSettings(t)
	messenger := &testGroupMessageControl{Lark: lark, scope: GroupMessageScopeUnknown, grant: groupMessageGrant{
		URL: "https://accounts.feishu.cn/page/launcher?ticket=synthetic", ExpiresAt: time.Now().Add(time.Minute),
	}}
	e.Lark = messenger
	replies := captureReplyModes(t, fixture)
	accessDMCommand(t, e, ch, "unknown", stage68Admin, groupMessageAuthorizeCommand)
	requireStage68Text(t, replies["unknown"], "暂无法确认")
	if messenger.grants != 0 {
		t.Fatal("unknown scope triggered an authorization request")
	}

	messenger.scope = GroupMessageScopeMissing
	messenger.loseGrantReply = true
	accessDMCommand(t, e, ch, "pending", stage68Admin, groupMessageAuthorizeCommand)
	id := key(Message{ChatID: "dm"}, "pending")
	if err := e.Store.update(func(st *State) error {
		if cmd := st.Commands[id]; cmd != nil && cmd.GroupMessageGrant != nil && cmd.GroupMessageGrantUntil.IsZero() && cmd.Issue != "" {
			cmd.GroupMessageGrant.ExpiresAt = time.Now().Add(-time.Second)
			return nil
		}
		t.Fatal("failed delivery lost its grant receipt")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	count := len(replies["pending"])
	messenger.loseGrantReply = false
	tick(t, e)
	requireStage68Text(t, replies["pending"][count:], "链接已过期")
	receipt := e.Store.snapshot().Commands[id]
	if messenger.grants != 1 || strings.Contains(replies["pending"][count].Content, messenger.grant.URL) ||
		receipt.GroupMessageGrant != nil || !receipt.GroupMessageGrantUntil.IsZero() {
		t.Fatal("expired link was replayed, regenerated or retained")
	}
}

func TestStage87GroupMessageGrantWatchRetriesOriginalCardAfterRestart(t *testing.T) {
	e, lark, ch, _, fixture := setupStage68RunSettings(t)
	now := time.Now()
	e.Now = func() time.Time { return now }
	messenger := &testGroupMessageControl{Lark: lark, scope: GroupMessageScopeMissing, grant: groupMessageGrant{
		URL: "https://accounts.feishu.cn/page/launcher?ticket=synthetic", ExpiresAt: now.Add(2 * time.Minute),
	}}
	e.Lark = messenger
	replies := captureReplyModes(t, fixture)
	firstID, first := authorizeStage87GroupMessageGrant(t, e, ch, messenger, replies, "grant-first")
	secondID, second := authorizeStage87GroupMessageGrant(t, e, ch, messenger, replies, "grant-second")
	if first.Card.ReplyID == second.Card.ReplyID {
		t.Fatal("expected distinct reply IDs for the two grant cards")
	}
	firstPatchPath := "/open-apis/im/v1/messages/" + first.Card.ReplyID
	secondPatchPath := "/open-apis/im/v1/messages/" + second.Card.ReplyID
	patches := map[string]int{}
	capture := fixture.extra
	fixture.extra = func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages") {
			return capture(w, r)
		}
		if r.Method != http.MethodPatch || (r.URL.Path != firstPatchPath && r.URL.Path != secondPatchPath) {
			t.Errorf("grant watch must PATCH an original reply ID, got %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return true
		}
		patches[r.URL.Path]++
		if r.URL.Path == firstPatchPath && patches[r.URL.Path] == 1 {
			capture(httptest.NewRecorder(), r)
			io.WriteString(w, `{"code":`) // Feishu did not return a usable PATCH confirmation.
			return true
		}
		return capture(w, r)
	}
	requireCalls := func(checks, firstEdits, secondEdits int) {
		t.Helper()
		if messenger.checks != checks || messenger.grants != 2 || patches[firstPatchPath] != firstEdits || patches[secondPatchPath] != secondEdits {
			t.Fatalf("want %d scope reads, 2 grants and PATCH counts %d/%d, got %d reads, %d grants and %v",
				checks, firstEdits, secondEdits, messenger.checks, messenger.grants, patches)
		}
	}
	requireCalls(2, 0, 0)
	replyAt, err := time.Parse(time.RFC3339Nano, first.ReplyAt)
	if err != nil {
		t.Fatal(err)
	}
	now = replyAt.Add(15*time.Second - time.Nanosecond)
	tick(t, e)
	requireCalls(2, 0, 0)

	now = now.Add(groupMessageGrantPollInterval)
	tick(t, e)
	requireCalls(3, 0, 0)
	messenger.scope = GroupMessageScopeGranted
	tick(t, e)
	requireCalls(3, 0, 0)

	now = now.Add(groupMessageGrantPollInterval)
	tick(t, e)
	requireCalls(4, 1, 1)
	requireGrantedStage87GroupMessageCard(t, e.Store.snapshot().Commands[secondID], second, replies[second.Card.ReplyID], messenger.grant.URL)
	stale := commandCardClick(t, second, replies["grant-second"][0].Content, "复查权限", "stale-recheck")
	clickCommandCard(t, lark, stale)
	tick(t, e)
	requireCalls(4, 1, 1)

	restartAccess(t, e, fixture.baseURL)
	e.nextGroupMessageGrantPoll = time.Time{}
	messenger.Lark = e.Lark.(*Lark)
	e.Lark = messenger
	pending := e.Store.snapshot().Commands[firstID]
	if pending == nil || pending.Card == nil || pending.Card.ReplyID != first.Card.ReplyID || pending.GroupMessageGrant != nil ||
		!pending.GroupMessageGrantUntil.Equal(first.GroupMessageGrantUntil) || pending.Reply != first.Reply || len(pending.Card.Buttons) != 1 {
		t.Fatalf("unconfirmed PATCH did not preserve the watch and original receipt across restart: %+v", pending)
	}
	now = now.Add(groupMessageGrantPollInterval)
	tick(t, e)
	requireCalls(5, 2, 1)
	persisted, err := readStateSnapshot(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	firstEdits := replies[first.Card.ReplyID]
	requireGrantedStage87GroupMessageCard(t, persisted.Commands[firstID], first, firstEdits, messenger.grant.URL)
	if len(firstEdits) != 2 || firstEdits[0].Content != firstEdits[1].Content {
		t.Fatalf("retry changed the original PATCH content: %+v", firstEdits)
	}
	now = now.Add(groupMessageGrantPollInterval)
	tick(t, e)
	requireCalls(5, 2, 1)
}

func TestStage87GroupMessageGrantWatchStopsForExpiredOrDemotedAdmin(t *testing.T) {
	for _, name := range []string{"expired", "demoted"} {
		t.Run(name, func(t *testing.T) {
			e, lark, ch, _, fixture := setupStage68RunSettings(t)
			now := time.Now()
			e.Now = func() time.Time { return now }
			messenger := &testGroupMessageControl{Lark: lark, scope: GroupMessageScopeMissing, grant: groupMessageGrant{
				URL: "https://accounts.feishu.cn/page/launcher?ticket=synthetic", ExpiresAt: now.Add(time.Minute),
			}}
			e.Lark = messenger
			replies := captureReplyModes(t, fixture)
			id, original := authorizeStage87GroupMessageGrant(t, e, ch, messenger, replies, "grant")
			if name == "expired" {
				now = messenger.grant.ExpiresAt.Add(time.Second)
			} else {
				if _, err := e.LocalAccessPolicy(context.Background(), LocalAccessPolicyRequest{Kind: "admin", UserID: stage68Admin, Mode: "deny"}); err != nil {
					t.Fatal(err)
				}
				now = now.Add(2 * groupMessageGrantPollInterval)
			}
			messenger.scope = GroupMessageScopeGranted
			capture := fixture.extra
			fixture.extra = func(w http.ResponseWriter, r *http.Request) bool {
				if strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages") {
					t.Errorf("inactive grant watch contacted the message API: %s %s", r.Method, r.URL.Path)
				}
				return capture(w, r)
			}
			tick(t, e)
			persisted, err := readStateSnapshot(e.Config.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			cmd := persisted.Commands[id]
			if messenger.checks != 1 || messenger.grants != 1 || cmd == nil || cmd.Card == nil ||
				cmd.Card.ReplyID != original.Card.ReplyID || !cmd.GroupMessageGrantUntil.IsZero() || cmd.GroupMessageGrant != nil {
				t.Fatalf("inactive grant watch should clear only the marker without reading scope again: reads=%d grants=%d receipt=%+v",
					messenger.checks, messenger.grants, cmd)
			}
		})
	}
}

func authorizeStage87GroupMessageGrant(t *testing.T, e *Engine, ch *accessChannel, messenger *testGroupMessageControl, replies map[string][]replyModeRequest, messageID string) (string, *SessionCommand) {
	t.Helper()
	accessDMCommand(t, e, ch, messageID, stage68Admin, groupMessageAuthorizeCommand)
	id := key(Message{ChatID: "dm"}, messageID)
	persisted, err := readStateSnapshot(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	cmd := persisted.Commands[id]
	got := replies[messageID]
	if cmd == nil || cmd.Card == nil || cmd.Card.ReplyID == "" || cmd.GroupMessageGrant != nil ||
		!cmd.GroupMessageGrantUntil.Equal(messenger.grant.ExpiresAt) || len(cmd.Card.Buttons) != 1 ||
		len(got) != 1 || got[0].MsgType != "interactive" || !strings.Contains(got[0].Content, messenger.grant.URL) {
		t.Fatalf("grant delivery should persist the watch and clear the URL: replies=%+v receipt=%+v", got, cmd)
	}
	return id, cmd
}

func requireGrantedStage87GroupMessageCard(t *testing.T, cmd, original *SessionCommand, edits []replyModeRequest, grantURL string) {
	t.Helper()
	const granted = "已授予 im:message.group_msg"
	if len(edits) == 0 || !strings.Contains(edits[0].Content, granted) || strings.Contains(edits[0].Content, grantURL) ||
		strings.Contains(edits[0].Content, commandButtonID(original, 0)) || strings.Contains(edits[0].Content, "复查权限") {
		t.Fatalf("PATCH should display granted permission and remove the grant link and old callback: %+v", edits)
	}
	if cmd == nil || cmd.Card == nil || cmd.Card.ReplyID != original.Card.ReplyID || cmd.ReplyAt != original.ReplyAt ||
		len(cmd.Card.Buttons) != 0 || cmd.GroupMessageGrant != nil || !cmd.GroupMessageGrantUntil.IsZero() || !strings.Contains(cmd.Reply, granted) {
		t.Fatalf("successful PATCH should settle the original grant receipt: %+v", cmd)
	}
}

func TestMentionOffSeparatesBridgePolicyAndFeishuPermission(t *testing.T) {
	e, lark, ch, _, fixture := setupStage68RunSettings(t)
	messenger := &testGroupMessageControl{Lark: lark, scope: GroupMessageScopeMissing, grant: groupMessageGrant{
		URL: "https://accounts.feishu.cn/page/launcher?ticket=synthetic", ExpiresAt: time.Now().Add(time.Minute),
	}}
	e.Lark = messenger
	replies := captureReplyModes(t, fixture)
	reply := fixture.extra
	fixture.extra = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/open-apis/application/v6/applications/"+e.Config.AppID {
			io.WriteString(w, `{"code":0,"data":{"app":{"scopes":[]}}}`)
			return true
		}
		return reply(w, r)
	}

	accessCommand(t, e, ch, "off-one", stage68Admin, "chat", "/config mention off")
	requireStage68Text(t, replies["off-one"], "尚未授予 im:message.group_msg")
	groups := e.Store.snapshot().GroupAccess
	if groups.requiresMention("chat") || !groups.defaultRequiresMention() || strings.Contains(replies["off-one"][0].Content, "https://") {
		t.Fatal("the single-group change was not applied or exposed a link publicly")
	}

	card := openStage68RunSettings(t, e, ch, "form", "/config run")
	action := stage68FormAction(card, "off-default", map[string]any{"require_mention": "off"})
	clickCommandCard(t, lark, action)
	tick(t, e)
	got := replies[card.Card.ReplyID]
	state := e.Store.snapshot()
	result := state.Commands[key(card.Source, "card:off-default")]
	if state.GroupAccess.defaultRequiresMention() || messenger.checks != 1 || messenger.grants != 0 ||
		result == nil || result.Card == nil || result.Card.ReplyID == "" || len(got) != 1 || got[0].MsgType != "interactive" ||
		!strings.Contains(got[0].Content, "已保存运行设置") || !strings.Contains(got[0].Content, "尚未授予 im:message.group_msg") ||
		strings.Contains(got[0].Content, messenger.grant.URL) {
		t.Fatalf("save should report missing Feishu permission without starting a grant: %+v", got)
	}

	clickCommandCard(t, lark, commandCardClick(t, result, got[0].Content, "申请群消息权限", "ask-from-form"))
	tick(t, e)
	grantReply := replies[result.Card.ReplyID]
	if messenger.grants != 1 || messenger.checks != 2 || len(grantReply) != 1 || grantReply[0].MsgType != "interactive" ||
		!strings.Contains(grantReply[0].Content, messenger.grant.URL) {
		t.Fatalf("explicit authorization button did not return the existing grant flow: %+v", grantReply)
	}
}

func TestMentionOffScopeCardRetryAfterRestart(t *testing.T) {
	ctx := context.Background()
	e, lark, ch, _, fixture := setupStage68RunSettings(t)
	messenger := &testGroupMessageControl{Lark: lark, scope: GroupMessageScopeMissing}
	e.Lark = messenger
	replies := captureReplyModes(t, fixture)
	form := openStage68RunSettings(t, e, ch, "form", "/config run")
	parent := form.Card.ReplyID
	action := stage68FormAction(form, "off-default", map[string]any{"require_mention": "off", "max_concurrent": "4"})
	id := key(form.Source, "card:"+action.EventID)
	loseReply := true
	reply := fixture.extra
	fixture.extra = func(w http.ResponseWriter, r *http.Request) bool {
		if !loseReply || r.URL.Path != "/open-apis/im/v1/messages/"+parent+"/reply" {
			return reply(w, r)
		}
		state, err := readStateSnapshot(e.Config.StateDir)
		cmd := state.Commands[id]
		if err != nil || cmd == nil || cmd.ReplyAt == "" || cmd.Card == nil || cmd.GroupMessageScopePending ||
			!strings.Contains(cmd.Reply, "尚未授予 im:message.group_msg") {
			t.Errorf("scope result was not persisted before sending: command=%+v error=%v", cmd, err)
		}
		reply(httptest.NewRecorder(), r)
		io.WriteString(w, `{"code":0}`) // Feishu did not return the reply message ID.
		return true
	}
	clickCommandCard(t, lark, action)
	tick(t, e)
	state := e.Store.snapshot()
	cmd := state.Commands[id]
	if state.GroupAccess.defaultRequiresMention() || state.Settings.MaxConcurrent != 4 || len(state.Inbox) != 1 ||
		cmd == nil || cmd.Issue == "" || cmd.Card == nil || cmd.Card.ReplyID != "" || messenger.checks != 1 || messenger.grants != 0 {
		t.Fatalf("unconfirmed delivery should leave a retryable receipt after saving the form: %+v", cmd)
	}

	tick(t, e)
	if len(replies[parent]) != 2 || messenger.checks != 1 || messenger.grants != 0 {
		t.Fatal("same-process retry did not reuse the original scope result")
	}
	limit := 8
	if _, err := e.LocalRunSettings(ctx, RunSettingsPatch{MaxConcurrent: &limit}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.LocalGroupAccess(ctx, GroupAccessRequest{Mention: "on"}); err != nil {
		t.Fatal(err)
	}
	loseReply = false
	restartAccess(t, e, fixture.baseURL)
	messenger.Lark = e.Lark.(*Lark)
	messenger.scope = GroupMessageScopeGranted
	e.Lark = messenger
	tick(t, e)
	state = e.Store.snapshot()
	result := state.Commands[id]
	got := replies[parent]
	if !state.GroupAccess.defaultRequiresMention() || state.Settings.MaxConcurrent != limit || len(state.Inbox) != 0 ||
		result == nil || result.Issue != "" || result.Card == nil || result.Card.ReplyID == "" || messenger.checks != 1 || messenger.grants != 0 ||
		len(got) != 3 || got[0] != got[1] || got[0] != got[2] || got[0].MsgType != "interactive" || got[0].UUID == "" {
		t.Fatalf("restart should deliver the original card without checking or saving again: replies=%+v receipt=%+v", got, result)
	}
}
