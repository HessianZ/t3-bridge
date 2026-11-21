package bridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	channel "github.com/larksuite/channel-sdk-go"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	im "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type accessChannel struct {
	channel.Channel
	message func(context.Context, *channel.NormalizedMessage) error
	action  func(context.Context, *channel.CardActionEvent) error
}

func (c *accessChannel) OnMessage(h func(context.Context, *channel.NormalizedMessage) error) {
	c.message = h
	c.Channel.OnMessage(h)
}
func (c *accessChannel) OnCardAction(h func(context.Context, *channel.CardActionEvent) error) {
	c.action = h
	c.Channel.OnCardAction(h)
}

type accessFixture struct {
	extra        func(http.ResponseWriter, *http.Request) bool
	baseURL      string
	owner        string
	ownerFail    bool
	ownerCalls   int
	listCalls    int
	listFail     bool
	listLoop     bool
	replyFail    bool
	replyFailFor string
	replyUUIDs   []string
	replies      []string
}

func setupAccess(t *testing.T) (*Engine, *Lark, *accessChannel, *fakeExecutor, *accessFixture) {
	t.Helper()
	e, x, _, _ := testEngine(t)
	e.Config.Admins = []string{"admin"}
	f := &accessFixture{owner: "user"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if f.extra != nil && f.extra(w, r) {
			return
		}
		switch {
		case strings.Contains(r.URL.Path, "tenant_access_token"):
			io.WriteString(w, `{"code":0,"tenant_access_token":"synthetic-token","expire":7200}`)
		case strings.HasPrefix(r.URL.Path, "/open-apis/application/v6/applications/"):
			f.ownerCalls++
			if r.Method != "GET" || r.URL.Path != "/open-apis/application/v6/applications/fake-app" || r.URL.Query().Get("lang") != "zh_cn" || r.URL.Query().Get("user_id_type") != "open_id" {
				t.Error("wrong owner API or identity type")
			}
			if f.ownerFail {
				io.WriteString(w, `{"code":999,"msg":"private-owner-error"}`)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"app": map[string]any{"creator_id": "not-owner", "owner": map[string]string{"owner_id": f.owner, "name": "private-name", "email": "private-email", "phone": "private-phone"}}}})
		case r.URL.Path == "/open-apis/im/v1/chats":
			f.listCalls++
			if r.Method != "GET" || r.URL.Query().Get("user_id_type") != "open_id" || r.URL.Query().Get("page_size") != "100" {
				t.Error("wrong group API")
			}
			if f.listFail {
				io.WriteString(w, `{"code":999,"msg":"private-list-error"}`)
				return
			}
			if r.URL.Query().Get("page_token") == "" || f.listLoop {
				io.WriteString(w, `{"code":0,"data":{"items":[{"chat_id":"bulk-one"}],"has_more":true,"page_token":"next"}}`)
			} else {
				io.WriteString(w, `{"code":0,"data":{"items":[{"chat_id":"bulk-two"},{"chat_id":"bulk-one"}],"has_more":false}}`)
			}
		case strings.HasSuffix(r.URL.Path, "/reply"):
			var body struct {
				Content string
				MsgType string `json:"msg_type"`
				UUID    string `json:"uuid"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			f.replies = append(f.replies, body.Content)
			f.replyUUIDs = append(f.replyUUIDs, body.UUID)
			if f.replyFail || r.URL.Path == "/open-apis/im/v1/messages/"+f.replyFailFor+"/reply" {
				io.WriteString(w, `{"code":999,"msg":"synthetic-reply-failure"}`)
				return
			}
			io.WriteString(w, `{"code":0,"data":{"message_id":"card"}}`)
		case r.Method == "GET" && r.URL.Path == "/open-apis/im/v1/messages":
			io.WriteString(w, `{"code":230027,"msg":"synthetic missing history permission"}`)
		case r.Method == "PATCH":
			io.WriteString(w, `{"code":0,"data":{}}`)
		default:
			t.Errorf("unexpected API (no history or model allowed): %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	f.baseURL = server.URL
	ch, err := channel.New("fake-app", "synthetic-secret", channel.WithOpenBaseURL(server.URL), channel.WithHTTPClient(attachmentDownloadClient{server.Client()}), channel.WithEnableTokenCache(false), channel.WithLogLevel(larkcore.LogLevelError))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ch.Stop(context.Background()) })
	capture := &accessChannel{Channel: ch}
	l := newLark(capture, e.Config, e.Store)
	l.RefreshOwner(context.Background())
	e.Config = l.Config
	e.Lark = l
	return e, l, capture, x, f
}
func groupMessage(id, user, chat, text string, mention bool) *channel.NormalizedMessage {
	topic := "topic"
	return &channel.NormalizedMessage{MessageID: id, UserID: user, ChatID: chat, ChatType: "group", Content: text, RawContentType: "text", MentionedBot: mention, RawEvent: &im.P2MessageReceiveV1{Event: &im.P2MessageReceiveV1Data{Message: &im.EventMessage{ThreadId: &topic}}}}
}
func receiveAccess(t *testing.T, ch *accessChannel, m *channel.NormalizedMessage) {
	t.Helper()
	if err := ch.message(context.Background(), m); err != nil {
		t.Fatal(err)
	}
}
func accessCommand(t *testing.T, e *Engine, ch *accessChannel, id, user, chat, text string) {
	t.Helper()
	receiveAccess(t, ch, groupMessage(id, user, chat, text, true))
	tick(t, e)
}

func accessDMCommand(t *testing.T, e *Engine, ch *accessChannel, id, user, text string) {
	t.Helper()
	receiveAccess(t, ch, &channel.NormalizedMessage{MessageID: id, UserID: user, ChatID: "dm", ChatType: "p2p", Content: text, RawContentType: "text"})
	tick(t, e)
}

func restartAccess(t *testing.T, e *Engine, baseURL string) *accessChannel {
	t.Helper()
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	fresh, err := channel.New("fake-app", "synthetic-secret", channel.WithOpenBaseURL(baseURL), channel.WithEnableTokenCache(false), channel.WithLogLevel(larkcore.LogLevelError))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fresh.Stop(context.Background()) })
	ch := &accessChannel{Channel: fresh}
	l := newLark(ch, e.Config, s)
	l.RefreshOwner(context.Background())
	e.Config = l.Config
	e.Store = s
	e.Lark = l
	return ch
}

func TestGroupAccessSDKGateAndIdentity(t *testing.T) {
	e, l, ch, _, f := setupAccess(t)
	p := ch.GetPolicy()
	if len(p.GroupAllowlist) != 0 || p.DMMode != "open" || p.RequireMention == nil || *p.RequireMention || p.RespondToMentionAll == nil || *p.RespondToMentionAll {
		t.Fatal("SDK can swallow owner/new-group/scoped reply before app gate", p)
	}
	if f.ownerCalls != 1 || !l.Config.isAdmin("user") || !l.Config.isAdmin("admin") || l.Config.isAdmin("other") || l.Config.isAdmin("not-owner") {
		t.Fatal("Users/creator were promoted or real owner lost")
	}
	l.RefreshOwner(context.Background())
	if f.ownerCalls != 1 {
		t.Fatal("owner not cached")
	}
	for _, tc := range []struct {
		name, user, chat, kind string
		allowed                bool
	}{
		{"owner_new_group", "user", "new-group", "group", true},
		{"admin_new_group", "admin", "new-group", "group", true},
		{"member_new_group", "other", "new-group", "group", false},
		{"member_allowed_group", "stranger", "chat", "group", true},
		{"member_unknown_dm", "stranger", "chat", "p2p", false},
		{"owner_dm", "user", "new-dm", "p2p", true},
		{"admin_dm", "admin", "new-dm", "p2p", true},
		{"member_allowed_dm", "other", "chat", "p2p", true},
		{"member_unlisted_dm", "other", "new-dm", "p2p", false},
		{"legacy_allowed", "other", "chat", "", true},
		{"legacy_new_group", "user", "new-group", "", false},
		{"unknown_kind", "user", "new-group", "unknown", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := message(tc.name, "问候")
			m.ChatType = tc.kind
			m.UserID = tc.user
			m.ChatID = tc.chat
			if l.Config.Allowed(m) != tc.allowed {
				t.Fatal("incorrect usage ACL", m)
			}
		})
	}
	for _, user := range []string{"user", "admin"} {
		receiveAccess(t, ch, groupMessage("new-"+user, user, "new-group", "列一下工具", true))
	}
	if len(e.Store.snapshot().Inbox) != 2 {
		t.Fatal("owner/admin swallowed by app gate")
	}
	before := len(f.replies)
	denied := groupMessage("denied", "other", "new-group", "你好", true)
	receiveAccess(t, ch, denied)
	receiveAccess(t, ch, denied)
	if len(f.replies) != before+1 || len(e.Store.snapshot().Inbox) != 2 {
		t.Fatal("unknown group hint not short/deduped or queued execution")
	}
	receiveAccess(t, ch, groupMessage("quiet", "user", "new-group", "普通群消息", false))
	if len(e.Store.snapshot().Inbox) != 2 {
		t.Fatal("owner bypassed @ requirement")
	}
}

func TestDirectChatScopeIsSeparateFromGroupAccess(t *testing.T) {
	config := Config{Users: []string{"member"}, Chats: []string{"group"}, access: &accessRuntime{}}
	direct := Message{UserID: "member", ChatID: "dm", ChatType: "p2p"}
	if config.Allowed(direct) {
		t.Fatal("omitting directChats should preserve the inherited chat boundary")
	}
	config.DirectChats = &[]string{}
	if !config.Allowed(direct) || config.Allowed(Message{UserID: "stranger", ChatID: "dm", ChatType: "p2p"}) {
		t.Fatal("independent private chat access should require a previously authorized user")
	}
	config.DirectChats = &[]string{"another-dm"}
	if config.Allowed(direct) || !config.Allowed(Message{UserID: "member", ChatID: "group", ChatType: "group"}) {
		t.Fatal("private chat restriction changed the group allowlist")
	}
}

func TestGroupCommandsImmediatePersistedAndNoModel(t *testing.T) {
	e, _, ch, x, f := setupAccess(t)
	classifier := &fakeClassifier{d: Decision{"nas", "new_task"}}
	e.Classifier = classifier
	accessCommand(t, e, ch, "invite", "user", "new-group", "/invite group")
	if classifier.calls != 0 || x.opens != 0 || len(e.Store.snapshot().Tasks) != 0 {
		t.Fatal("admin command reached model/T3")
	}
	m := Normalize(groupMessage("member", "other", "new-group", "检查磁盘", true))
	if !e.Config.Allowed(m) {
		t.Fatal("group grant not immediate")
	}
	accessCommand(t, e, ch, "mention-off", "admin", "new-group", "/config mention off")
	receiveAccess(t, ch, groupMessage("unmentioned", "other", "new-group", "讨论", false))
	if len(e.Store.snapshot().Inbox) != 1 {
		t.Fatal("mention override not immediate")
	}
	if err := e.drop(Normalize(groupMessage("unmentioned", "other", "new-group", "讨论", false))); err != nil {
		t.Fatal(err)
	}
	// Reopen the same private state; fixed config seeds must not erase runtime overrides.
	ch = restartAccess(t, e, f.baseURL)
	if !e.Config.Allowed(m) || e.Config.requireMention("new-group") {
		t.Fatal("grant/mention override lost on restart")
	}
	accessCommand(t, e, ch, "config", "admin", "new-group", "/config")
	reply := f.replies[len(f.replies)-1]
	if !strings.Contains(reply, "route.users") || strings.Contains(reply, "private-") || strings.Contains(reply, "secret") {
		t.Fatal("/config leaked private owner/config data or hid route boundary", reply)
	}
	accessCommand(t, e, ch, "remove", "admin", "new-group", "/remove group")
	if e.Config.Allowed(m) {
		t.Fatal("revocation not immediate")
	}
	accessCommand(t, e, ch, "static-remove", "user", "chat", "/config group off")
	if e.Config.Allowed(Normalize(groupMessage("static-member", "other", "chat", "你好", true))) {
		t.Fatal("static seed bypassed runtime removal")
	}
	if classifier.calls != 0 || x.sends != 0 {
		t.Fatal("control or revoked inbox reached model/execute")
	}
	b, err := os.ReadFile(filepath.Join(e.Config.StateDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted State
	if err = json.Unmarshal(b, &persisted); err != nil {
		t.Fatal(err)
	}
	if allowed, ok := persisted.GroupAccess.Chats["chat"]; !ok || allowed || persisted.GroupAccess.Chats["new-group"] {
		t.Fatal("revocation not durably overriding static seed")
	}

}

func TestGroupMentionDefaultAndOverrides(t *testing.T) {
	e, l, ch, x, f := setupAccess(t)
	e.Config.Topics = nil
	l.Config.Topics = nil
	accessDMCommand(t, e, ch, "query-initial", "admin", "/config mention all")
	if !strings.Contains(f.replies[len(f.replies)-1], "本应用群聊默认需要@：true") || e.Store.snapshot().GroupAccess.RequireMention != nil {
		t.Fatal("read did not report the implicit require-mention default")
	}
	accessCommand(t, e, ch, "grant-override", "admin", "override-off", "/invite group")
	accessCommand(t, e, ch, "grant-inherited", "admin", "inherited", "/invite group")
	accessCommand(t, e, ch, "override-on", "user", "chat", "/config mention on")
	accessCommand(t, e, ch, "override-off", "user", "override-off", "/config mention off")
	accessDMCommand(t, e, ch, "all-off", "admin", "/config mention all off")
	accessCommand(t, e, ch, "unauthorized-all-on", "other", "inherited", "/config mention all on")
	if !strings.Contains(f.replies[len(f.replies)-1], "仅应用所有者") || e.Config.requireMention("inherited") {
		t.Fatal("non-admin changed the app default")
	}
	accessDMCommand(t, e, ch, "dm-default", "admin", "/config mention default")
	if !strings.Contains(f.replies[len(f.replies)-1], "请在目标群") {
		t.Fatal("DM could clear a group setting")
	}
	ch = restartAccess(t, e, f.baseURL)
	accessCommand(t, e, ch, "query-after-restart", "admin", "chat", "/config mention all")
	if !strings.Contains(f.replies[len(f.replies)-1], "本应用群聊默认需要@：false") {
		t.Fatal("restart lost the app default")
	}
	for _, tc := range []struct {
		chat     string
		accepted bool
	}{
		{"chat", false}, {"override-off", true}, {"inherited", true}, {"unauthorized-group", false},
	} {
		m := groupMessage("unmentioned-"+tc.chat, "other", tc.chat, "讨论", false)
		receiveAccess(t, ch, m)
		if got := len(e.Store.snapshot().Inbox) == 1; got != tc.accepted {
			t.Fatalf("wrong ingress behavior for %s: accepted=%t", tc.chat, got)
		}
		if tc.accepted {
			if err := e.drop(Normalize(m)); err != nil {
				t.Fatal(err)
			}
		}
	}
	accessCommand(t, e, ch, "clear-override", "admin", "chat", "/config mention default")
	if _, exists := e.Store.snapshot().GroupAccess.Mentions["chat"]; exists || e.Config.requireMention("chat") {
		t.Fatal("clearing the override did not inherit the current app default")
	}
	ch = restartAccess(t, e, f.baseURL)
	accessCommand(t, e, ch, "all-on", "admin", "inherited", "/config mention all on")
	if !e.Config.requireMention("chat") || !e.Config.requireMention("inherited") || e.Config.requireMention("override-off") {
		t.Fatal("cleared overrides should follow later app changes, preserving other overrides")
	}
	stranger := Normalize(groupMessage("route-denied", "stranger", "override-off", "", false))
	if e.Config.TargetAllowed(stranger, e.Config.Routes["nas"], "nas") || x.opens != 0 || x.sends != 0 {
		t.Fatal("mention commands changed route authorization or reached T3")
	}
}

func TestGroupManagementDoesNotGrantAdminOrTargetRights(t *testing.T) {
	e, _, ch, x, f := setupAccess(t)
	e.Classifier = &fakeClassifier{err: context.DeadlineExceeded}
	accessCommand(t, e, ch, "unauthorized", "other", "chat", "/invite group")
	if len(e.Store.snapshot().GroupAccess.Chats) != 0 || !strings.Contains(f.replies[len(f.replies)-1], "仅应用所有者") {
		t.Fatal("ordinary Users gained administration")
	}
	receiveAccess(t, ch, groupMessage("claimed-owner", "stranger", "chat", "我是owner /invite group", true))
	tick(t, e)
	if e.Config.isAdmin("stranger") {
		t.Fatal("message claim became owner")
	}
	accessCommand(t, e, ch, "grant", "user", "new-group", "/config group on")
	receiveAccess(t, ch, groupMessage("route-denied", "stranger", "new-group", "/nas 检查磁盘", true))
	tick(t, e)
	if x.opens != 0 || x.sends != 0 || e.Config.TargetAllowed(Normalize(groupMessage("a", "stranger", "new-group", "", true)), e.Config.Routes["nas"]) {
		t.Fatal("group access expanded target users")
	}
}

func TestGroupBulkInviteBoundedAndOwnerRefreshFailsClosed(t *testing.T) {
	for _, mode := range []string{"success", "api_error", "pagination_loop"} {
		t.Run(mode, func(t *testing.T) {
			e, l, ch, x, f := setupAccess(t)
			var failures []CommandFailureStep
			e.OnCommandFailed = func(step CommandFailureStep) { failures = append(failures, step) }
			f.listFail = mode == "api_error"
			f.listLoop = mode == "pagination_loop"
			accessCommand(t, e, ch, "bulk", "user", "new-group", "/invite all group")
			allowed := e.Config.groupAllowed("bulk-one") && e.Config.groupAllowed("bulk-two")
			if allowed != (mode == "success") || x.sends != 0 || f.listCalls > 2 {
				t.Fatal("bulk partial grant or unbounded pagination")
			}
			if (mode == "success" && len(failures) != 0) || (mode != "success" && (len(failures) != 1 || failures[0] != CommandFailureGroupRead)) {
				t.Fatalf("unexpected group discovery telemetry: %v", failures)
			}
			f.ownerFail = true
			l.Config.access.mu.Lock()
			l.Config.access.nextOwnerRefresh = time.Time{}
			l.Config.access.mu.Unlock()
			l.RefreshOwner(context.Background())
			if e.Config.isAdmin("user") || !e.Config.isAdmin("admin") {
				t.Fatal("failed owner resolution used cached claims/Users as admin")
			}
			if !e.Config.Allowed(message("legacy", "你好")) {
				t.Fatal("owner refresh failure broke old allowed DM/task records")
			}
		})
	}
}

func TestGroupSaveFailureDoesNotPublishSuccess(t *testing.T) {
	e, _, ch, _, f := setupAccess(t)
	var failures []CommandFailureStep
	e.OnCommandFailed = func(step CommandFailureStep) { failures = append(failures, step) }
	receiveAccess(t, ch, groupMessage("save-failure", "user", "new-group", "/invite group", true))
	original := e.Store.dir
	e.Store.dir = filepath.Join(t.TempDir(), "not-directory")
	if err := os.WriteFile(e.Store.dir, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	err := e.Tick(context.Background())
	e.Store.dir = original
	if err == nil || e.Config.groupAllowed("new-group") || len(e.Store.snapshot().Inbox) != 1 || !strings.Contains(f.replies[len(f.replies)-1], "保存失败") {
		t.Fatal("failed save published grant/success or consumed command", err)
	}
	if len(failures) != 1 || failures[0] != CommandFailureHandler {
		t.Fatalf("failed save produced wrong telemetry: %v", failures)
	}
}

func TestGroupReplyFailureRetainsCommandForRetry(t *testing.T) {
	e, _, ch, x, f := setupAccess(t)
	var failures []CommandFailureStep
	e.OnCommandFailed = func(step CommandFailureStep) { failures = append(failures, step) }
	f.replyFail = true
	receiveAccess(t, ch, groupMessage("reply-retry", "user", "new-group", "/invite group", true))
	tick(t, e)
	if !e.Config.groupAllowed("new-group") || len(e.Store.snapshot().Inbox) != 1 || x.sends != 0 {
		t.Fatal("reply failure lost command, authorization or execution boundary")
	}
	tick(t, e)
	f.replyFail = false
	tick(t, e)
	if len(e.Store.snapshot().Inbox) != 0 || len(f.replyUUIDs) != 3 || f.replyUUIDs[0] == "" || f.replyUUIDs[0] != f.replyUUIDs[1] || f.replyUUIDs[1] != f.replyUUIDs[2] || x.sends != 0 {
		t.Fatal("reply retry did not reuse its identity and consume only after success")
	}
	if len(failures) != 1 || failures[0] != CommandFailureReply {
		t.Fatalf("reply retries added samples: %v", failures)
	}
}

func TestGroupMentionReplyRetryKeepsNewerDecision(t *testing.T) {
	for _, tc := range []struct {
		name, first, latest string
		legacy              bool
	}{
		{"app-default", "all off", "all on", false},
		{"group-override", "off", "on", false},
		{"inherit", "default", "off", false},
		{"old-receipt", "off", "on", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _, ch, x, f := setupAccess(t)
			if tc.first == "default" {
				accessCommand(t, e, ch, "initial-override", "admin", "chat", "/config mention off")
			}
			f.replyFailFor = "first"
			i := len(f.replies)
			accessCommand(t, e, ch, "first", "admin", "chat", "/config mention "+tc.first)
			st := e.Store.snapshot()
			if len(st.Inbox) != 1 || e.Config.requireMention("chat") != (tc.first == "default") {
				t.Fatal("reply failure did not preserve both the committed setting and the pending command")
			}
			id := key(st.Inbox[0], "first")
			if cmd := st.Commands[id]; cmd == nil || cmd.Reply == "" || cmd.Issue == "" {
				t.Fatal("missing durable reply receipt")
			}
			accessCommand(t, e, ch, "latest", "admin", "chat", "/config mention "+tc.latest)
			want := tc.latest != "off"
			if e.Config.requireMention("chat") != want || len(e.Store.snapshot().Inbox) != 1 {
				t.Fatal("new decision did not take effect while the first reply was pending")
			}
			if tc.legacy {
				if err := e.Store.update(func(st *State) error {
					st.Commands[id].Reply = ""
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			restartAccess(t, e, f.baseURL)
			f.replyFailFor = ""
			tick(t, e)
			st = e.Store.snapshot()
			if e.Config.requireMention("chat") != want || len(st.Inbox) != 0 || st.Commands[id] != nil || x.sends != 0 {
				t.Fatal("reply recovery changed the new decision or retained completed work")
			}
			last := len(f.replies) - 1
			if f.replyUUIDs[i] == "" || f.replyUUIDs[last] != f.replyUUIDs[i] {
				t.Fatal("reply retry changed the original UUID")
			}
			if tc.legacy {
				if !strings.Contains(f.replies[last], "此前命令已处理") {
					t.Fatal("missing reply recovery status for older receipts")
				}
			} else if f.replies[last] != f.replies[i] {
				t.Fatal("reply recovery changed the original payload")
			}
		})
	}
}

func TestGroupOwnCardReplyAndStopKeepScope(t *testing.T) {
	e, _, ch, x, _ := setupAccess(t)
	classifier := &fakeClassifier{err: context.DeadlineExceeded}
	e.Classifier = classifier
	receiveAccess(t, ch, groupMessage("start", "user", "new-group", "/nas 检查磁盘", true))
	tick(t, e)
	task := onlyTask(t, e)
	if x.opens != 1 || x.sends != 1 || task.Source.ChatType != "group" {
		t.Fatal("owner in new group could not use already-authorized target")
	}
	x.observation.Status = "completed"
	tick(t, e)
	reply := groupMessage("reply", "user", "new-group", "继续核对", false)
	reply.RawEvent.(*im.P2MessageReceiveV1).Event.Message.ParentId = &task.CardID
	receiveAccess(t, ch, reply)
	tick(t, e)
	if x.sends != 2 || x.opens != 1 || classifier.calls != 0 {
		t.Fatal("same scoped card required another mention or lost binding")
	}
	for _, scope := range []string{"user", "chat", "topic", "unknown-card"} {
		t.Run(scope, func(t *testing.T) {
			denied := groupMessage("denied-"+scope, "user", "new-group", "继续", false)
			raw := denied.RawEvent.(*im.P2MessageReceiveV1).Event.Message
			raw.ParentId = &task.CardID
			if scope == "user" {
				denied.UserID = "admin"
			}
			if scope == "chat" {
				denied.ChatID = "different-group"
			}
			if scope == "topic" {
				topic := "different-topic"
				raw.ThreadId = &topic
			}
			if scope == "unknown-card" {
				parent := "unknown-card"
				raw.ParentId = &parent
			}
			receiveAccess(t, ch, denied)
			if len(e.Store.snapshot().Inbox) != 0 {
				t.Fatal("mention exemption escaped original user/chat/topic/card")
			}
		})
	}
	action := channel.CardActionEvent{EventID: "stop-event", MessageID: task.CardID, ChatID: task.Source.ChatID, Operator: channel.CardActionOperator{OpenID: "admin"}, Action: channel.CardActionPayload{Tag: "button", Value: map[string]any{"action": "stop", "taskId": task.ID, "requestId": e.Store.snapshot().Tasks[task.ID].CardRequestID}}}
	if err := ch.action(context.Background(), &action); err != nil {
		t.Fatal(err)
	}
	if e.Store.snapshot().Tasks[task.ID].StopRequested {
		t.Fatal("admin stopped another user's card")
	}
	action.Operator.OpenID = "user"
	if err := ch.action(context.Background(), &action); err != nil {
		t.Fatal(err)
	}
	if err := ch.action(context.Background(), &action); err != nil {
		t.Fatal(err)
	}
	tick(t, e)
	if x.stops != 1 || !e.Store.snapshot().Tasks[task.ID].StopRequested || classifier.calls != 0 {
		t.Fatal("stop ownership/dedup/control changed")
	}
	reply.MessageID = "after-stop"
	receiveAccess(t, ch, reply)
	tick(t, e)
	if x.sends != 2 {
		t.Fatal("stopped group task revived")
	}
}

func TestGroupOwnerTransferAndDMManagement(t *testing.T) {
	e, l, ch, x, f := setupAccess(t)
	m := groupMessage("dm-invite", "user", "new-dm", "/invite group", false)
	m.ChatType = "p2p"
	receiveAccess(t, ch, m)
	tick(t, e)
	if len(e.Store.snapshot().GroupAccess.Chats) != 0 || !strings.Contains(f.replies[len(f.replies)-1], "请在目标群") {
		t.Fatal("DM was treated as group")
	}
	f.owner = "new-owner"
	l.Config.access.mu.Lock()
	l.Config.access.nextOwnerRefresh = time.Time{}
	l.Config.access.mu.Unlock()
	l.RefreshOwner(context.Background())
	if e.Config.isAdmin("user") || !e.Config.isAdmin("new-owner") {
		t.Fatal("official ownership change did not update management rights")
	}
	receiveAccess(t, ch, groupMessage("transferred", "new-owner", "another-group", "/config", true))
	tick(t, e)
	if x.sends != 0 || len(e.Store.snapshot().Tasks) != 0 {
		t.Fatal("owner transfer expanded target/execution rights")
	}
	f.owner = ""
	l.Config.access.mu.Lock()
	l.Config.access.nextOwnerRefresh = time.Time{}
	l.Config.access.mu.Unlock()
	l.RefreshOwner(context.Background())
	if e.Config.isAdmin("new-owner") || e.Config.isAdmin("not-owner") {
		t.Fatal("missing owner fell back to creator/cached identity")
	}
}
