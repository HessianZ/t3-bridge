package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	channel "github.com/larksuite/channel-sdk-go"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

func TestDocumentCommentLoopback(t *testing.T) {
	e, exec, _, _ := testEngine(t)
	e.Config.CommentRoute = "mac"
	e.Config.DefaultRoute = "nas"
	e.Classifier = &fakeClassifier{}
	writes := make(chan string, 8)
	reactions := make(chan string, 8)
	const doc = "/open-apis/drive/v1/files/docFake/comments"
	lark := newLoopbackLark(t, func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/open-apis/drive/v2/files/docFake/comments/reaction":
			var body struct {
				Action string `json:"action"`
				Reply  string `json:"reply_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			reactions <- body.Action + " " + body.Reply
			if body.Reply == "reply2" {
				io.WriteString(w, `{"code":99991672,"msg":"synthetic missing permission"}`)
				break
			}
			io.WriteString(w, `{"code":0}`)
		case r.Method == http.MethodGet && r.URL.Path == doc+"/thread1":
			io.WriteString(w, `{"code":0,"data":{"comment_id":"thread1","quote":"原文摘录","is_whole":false,"reply_list":{"replies":[
				{"reply_id":"reply1","user_id":"user","content":{"elements":[{"type":"person"},{"type":"text_run","text_run":{"text":"/reset 是什么意思"}}]}},
				{"reply_id":"reply2","user_id":"user","content":{"elements":[{"type":"text_run","text_run":{"text":"继续上一条"}}]}}
			]}}}`)
		case r.Method == http.MethodGet && r.URL.Path == doc+"/whole":
			io.WriteString(w, `{"code":0,"data":{"comment_id":"whole","is_whole":true,"reply_list":{"replies":[
				{"reply_id":"whole1","user_id":"user","content":{"elements":[{"type":"text_run","text_run":{"text":"全文问题"}}]}}
			]}}}`)
		case r.Method == http.MethodPost && (r.URL.Path == doc+"/thread1/replies" || r.URL.Path == doc):
			raw, _ := io.ReadAll(r.Body)
			writes <- r.URL.Path + " " + string(raw)
			io.WriteString(w, `{"code":0,"data":{"reply_id":"result-reply","comment_id":"result-comment"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/open-apis/docx/v1/documents/docFake/raw_content":
			io.WriteString(w, `{"code":0,"data":{"content":"合成文档正文"}}`)
			return true
		default:
			return false
		}
		if r.URL.Query().Get("file_type") != "docx" {
			t.Errorf("missing file type: %s", r.URL.RawQuery)
		}
		return true
	})
	receiver, err := lark.channel(e.Config, e.Store)
	if err != nil {
		t.Fatal(err)
	}
	e.Lark, e.Config = receiver, receiver.Config
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	delivered := make(chan string, 8)
	// Install before sending any event. The SDK uses the same socket for IM and Drive.
	receiver.Channel.OnComment(func(_ context.Context, event *channel.CommentEvent) error {
		delivered <- event.EventID
		return nil
	})
	done := make(chan struct{})
	go func() { defer close(done); _ = receiver.Channel.Start(ctx) }()
	defer func() { cancel(); _ = receiver.Channel.Stop(context.Background()); <-done }()
	var peer *websocket.Conn
	select {
	case peer = <-lark.peers:
		defer peer.Close()
	case <-ctx.Done():
		t.Fatal("missing WS connection")
	}
	send := func(eventID, comment, reply, user string, mention bool) {
		t.Helper()
		sendTestComment(t, peer, eventID, comment, reply, user, mention)
		select {
		case id := <-delivered:
			if id != eventID {
				t.Fatalf("received event %s, want %s", id, eventID)
			}
		case <-ctx.Done():
			t.Fatalf("comment event %s never reached the SDK handler", eventID)
		}
	}
	assertReaction := func(want string) {
		t.Helper()
		select {
		case got := <-reactions:
			if got != want {
				t.Fatalf("reaction = %q, want %q", got, want)
			}
		default:
			t.Fatalf("missing reaction %q", want)
		}
	}
	finish := func(wantPath, reply string) {
		t.Helper()
		assertReaction("add " + reply)
		exec.observation = Observation{Status: "completed", Summary: "评论答复"}
		tick(t, e)
		assertReaction("delete " + reply)
		select {
		case got := <-writes:
			if !strings.HasPrefix(got, wantPath+" ") || !strings.Contains(got, "评论答复") {
				t.Fatalf("unexpected comment reply: %s", got)
			}
		default:
			t.Fatal("missing document reply")
		}
	}
	send("denied", "denied", "denied", "intruder", true)
	send("silent", "silent", "silent", "user", false)
	send("self", "mine", "mine", "synthetic-bot", true)
	if len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("unauthorized/unmentioned comment reached durable queue")
	}
	send("event1", "thread1", "reply1", "user", true)
	tick(t, e)
	first := onlyTask(t, e)
	if first.RouteID != "mac" || !strings.HasPrefix(exec.request, "/reset 是什么意思") || !strings.Contains(exec.request, "原文摘录") ||
		!strings.Contains(exec.request, "合成文档正文") || len(writes) != 0 {
		t.Fatal("comment was not routed as text to its explicit route, or was posted before completion")
	}
	finish(doc+"/thread1/replies", "reply1")
	sendTestComment(t, peer, "duplicate", "thread1", "reply1", "user", true)
	send("event2", "thread1", "reply2", "user", true)
	exec.observation.Status = "running"
	tick(t, e)
	if next := onlyTask(t, e); next.ID != first.ID || exec.opens != 1 || exec.sends != 2 ||
		!strings.HasPrefix(exec.request, "继续上一条") || !strings.Contains(exec.request, "/reset 是什么意思") {
		t.Fatal("new reply was lost, duplicated, or did not continue the document session")
	}
	finish(doc+"/thread1/replies", "reply2")
	send("event3", "whole", "", "user", true)
	exec.observation.Status = "running"
	tick(t, e)
	finish(doc, "whole1")
	if exec.sends != 3 || exec.opens != 1 || len(onlyTask(t, e).Replies) != 2 || e.Classifier.(*fakeClassifier).calls != 0 || len(reactions) != 0 {
		t.Fatal("document and reply scopes diverged")
	}
}

func sendTestComment(t *testing.T, peer *websocket.Conn, eventID, comment, reply, user string, mention bool) {
	t.Helper()
	payload := fmt.Sprintf(`{"schema":"2.0","header":{"event_id":%q,"event_type":"drive.notice.comment_add_v1","create_time":%q},
		"event":{"file_token":"docFake","file_type":"docx","comment_id":%q,"reply_id":%q,"is_mentioned":%t,"user_id":{"open_id":%q}}}`,
		eventID, fmt.Sprint(time.Now().UnixMilli()), comment, reply, mention, user)
	frame := larkws.Frame{Method: int32(larkws.FrameTypeData), Service: 1, Payload: []byte(payload), Headers: []larkws.Header{
		{Key: larkws.HeaderType, Value: string(larkws.MessageTypeEvent)}, {Key: larkws.HeaderMessageID, Value: eventID},
	}}
	raw, err := frame.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.WriteMessage(websocket.BinaryMessage, raw); err != nil {
		t.Fatal(err)
	}
}

func TestCommentNotificationUncertainAfterRestart(t *testing.T) {
	e, _, messenger, _ := testEngine(t)
	e.Config.CommentRoute = "mac"
	ref := &CommentSource{FileType: "docx", FileToken: "doc", CommentID: "thread"}
	m := Message{ID: "event", ChatID: "document:doc", UserID: "user", Text: "问题", Comment: ref}
	if err := e.newTask(m, Decision{Action: "new_task", RouteID: "mac"}, "reply"); err != nil {
		t.Fatal(err)
	}
	first := onlyTask(t, e)
	first.Phase = "completed"
	first.CardAttemptAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := e.commit(first); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	e.Store = restored
	tick(t, e)
	if messenger.notifications != 0 || onlyTask(t, e).NotificationIssue == "" {
		t.Fatal("restart repeated an unconfirmed comment write")
	}
}

func TestFeishuAccountIsolation(t *testing.T) {
	a, _, _, _ := testEngine(t)
	b, _, _, _ := testEngine(t)
	b.Config.AppID = "cli_second"
	if err := a.Store.BindAccount(a.Config.AppID); err != nil {
		t.Fatal(err)
	}
	if err := b.Store.BindAccount(b.Config.AppID); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.BindAccount(b.Config.AppID); err == nil {
		t.Fatal("second app adopted first app's durable store")
	}
	m := message("same-im-id", "/mac 同一条群消息")
	for _, e := range []*Engine{a, b} {
		if err := e.newTask(m, Decision{Action: "new_task", RouteID: "mac"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	first, second := onlyTask(t, a), onlyTask(t, b)
	if first.OpenRequestID == second.OpenRequestID || first.RequestID == second.RequestID {
		t.Fatal("two app IDs collided at the shared T3 backend")
	}
	raw, err := json.Marshal(a.Config)
	if err != nil {
		t.Fatal(err)
	}
	config, err := ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	config.AdditionalAccounts = []Config{b.Config}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	accounts := config.Accounts()
	if !slices.EqualFunc(accounts, []Config{a.Config, b.Config}, func(left, right Config) bool {
		return left.AppID == right.AppID && left.StateDir == right.StateDir
	}) {
		t.Fatal("independent accounts were not loaded with their own identities")
	}
	b.Config = accounts[1]
	b.Config.InvestigationDB = "/test/database"
	second.DispatchSource.EventID = "test-event"
	if !strings.Contains(b.reportInstructions(second), `"-app-id","cli_second"`) {
		t.Fatal("report command was not scoped to the additional app")
	}
	config.AdditionalAccounts[0].AppID = a.Config.AppID
	if err := config.Validate(); err == nil {
		t.Fatal("duplicate App ID accepted")
	}
}
