package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	channel "github.com/larksuite/channel-sdk-go"
	im "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const (
	topicIntakeChatPath    = "/open-apis/im/v1/chats/chat"
	topicIntakeMessagePath = "/open-apis/im/v1/messages/"
)

func topiclessGroupMessage(id, user, chat string, mentioned bool) *channel.NormalizedMessage {
	n := groupMessage(id, user, chat, "事件原文", mentioned)
	n.RawEvent.(*im.P2MessageReceiveV1).Event.Message.ThreadId = nil
	return n
}

func TestTopicIntakeRecoversStoreScopeAndRepliesInThread(t *testing.T) {
	e, l, ch, _, f := setupAccess(t)
	e.Config.Topics = []string{"topic-a", "topic-b"}
	l.Config.Topics = e.Config.Topics
	scopeA := Message{UserID: "user", ChatID: "chat", TopicID: "topic-a"}
	scopeB := Message{UserID: "user", ChatID: "chat", TopicID: "topic-b"}
	parent := "existing-card"
	if err := e.Store.update(func(st *State) error {
		st.Selected = map[string]string{userScope(scopeA): "nas", userScope(scopeB): "mac"}
		st.Tasks["existing-task"] = &Task{ID: "existing-task", Source: scopeB, CardID: parent, RouteID: "mac", Phase: "completed"}
		st.Bindings[key(scopeB, parent)] = "existing-task"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var chatRead atomic.Bool
	f.extra = func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == topicIntakeChatPath:
			chatRead.Store(true)
			io.WriteString(w, `{"code":0,"data":{"chat_mode":"group","group_message_type":"thread"}}`)
		case r.Method == http.MethodGet && (r.URL.Path == topicIntakeMessagePath+"a" || r.URL.Path == topicIntakeMessagePath+"b"):
			if !chatRead.Load() {
				t.Error("message lookup occurred before chat mode lookup")
			}
			id := r.URL.Path[len(topicIntakeMessagePath):]
			fmt.Fprintf(w, `{"code":0,"data":{"items":[{"message_id":%q,"chat_id":"chat","thread_id":%q,"deleted":false,"body":{"content":"lookup text"}}]}}`, id, "topic-"+id)
		case r.Method == http.MethodPost && r.URL.Path == topicIntakeMessagePath+"a/reply":
			var body replyModeRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !body.ReplyInThread {
				t.Errorf("reply_in_thread = %t, decode error = %v", body.ReplyInThread, err)
			}
			io.WriteString(w, `{"code":0,"data":{"message_id":"topic-reply"}}`)
		default:
			return false
		}
		return true
	}

	a := topiclessGroupMessage("a", "user", "chat", true)
	b := topiclessGroupMessage("b", "user", "chat", false)
	b.RawEvent.(*im.P2MessageReceiveV1).Event.Message.ParentId = &parent
	receiveAccess(t, ch, a)
	receiveAccess(t, ch, b)
	receiveAccess(t, ch, a)
	st := e.Store.snapshot()
	if len(st.Inbox) != 2 || len(st.Tasks) != 1 {
		t.Fatalf("inbox = %d, tasks = %d, want two queued messages and one seeded task", len(st.Inbox), len(st.Tasks))
	}
	for i, want := range []struct{ id, topic, route string }{{"a", "topic-a", "nas"}, {"b", "topic-b", "mac"}} {
		m := st.Inbox[i]
		if m.ID != want.id || m.TopicID != want.topic || m.RouteAtReceive != want.route || m.Text != a.Content {
			t.Fatalf("queued %s = %+v, want topic %s, route %s and event text", want.id, m, want.topic, want.route)
		}
	}
	cardID, err := l.Notify(context.Background(), &Task{ID: "synthetic-task", Source: st.Inbox[0], Phase: "completed"})
	if err != nil || cardID != "topic-reply" {
		t.Fatalf("reply to recovered topic = %q, %v", cardID, err)
	}
}

func TestTopicIntakeRejectsUnverifiedOrDisallowedScope(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		topicACL       bool
	}{
		{name: "read failure", response: `{"code":230027,"msg":"synthetic read failure"}`},
		{name: "no items", response: `{"code":0,"data":{"items":[]}}`},
		{name: "deleted", response: `{"code":0,"data":{"items":[{"message_id":"message","chat_id":"chat","thread_id":"topic","deleted":true}]}}`},
		{name: "other chat", response: `{"code":0,"data":{"items":[{"message_id":"message","chat_id":"other-chat","thread_id":"topic"}]}}`},
		{name: "wrong first message", response: `{"code":0,"data":{"items":[{"message_id":"other","chat_id":"chat","thread_id":"topic"},{"message_id":"message","chat_id":"chat","thread_id":"topic"}]}}`},
		{name: "missing thread", response: `{"code":0,"data":{"items":[{"message_id":"message","chat_id":"chat"}]}}`},
		{name: "disallowed topic in same chat", response: `{"code":0,"data":{"items":[{"message_id":"message","chat_id":"chat","thread_id":"other-topic"}]}}`, topicACL: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, l, ch, _, f := setupAccess(t)
			if !tc.topicACL {
				e.Config.Topics, l.Config.Topics = nil, nil
			}
			var messageRead atomic.Bool
			f.extra = func(w http.ResponseWriter, r *http.Request) bool {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == topicIntakeChatPath:
					io.WriteString(w, `{"code":0,"data":{"chat_mode":"topic"}}`)
				case r.Method == http.MethodGet && r.URL.Path == topicIntakeMessagePath+"message":
					messageRead.Store(true)
					io.WriteString(w, tc.response)
				default:
					return false
				}
				return true
			}
			receiveAccess(t, ch, topiclessGroupMessage("message", "user", "chat", true))
			if inbox := e.Store.snapshot().Inbox; !messageRead.Load() || len(inbox) != 0 {
				t.Fatalf("message read = %t, inbox = %+v, want lookup and rejection", messageRead.Load(), inbox)
			}
		})
	}
}

func TestTopicIntakeSkipsLookupsForFilteredAndNativeEvents(t *testing.T) {
	e, _, ch, _, f := setupAccess(t)
	if err := e.Store.update(func(st *State) error {
		st.AccessPolicy.Users = map[string]bool{"other": false}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.publishPolicy()
	f.extra = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodGet {
			t.Errorf("unexpected lookup for filtered or native event: %s", r.URL.Path)
			w.WriteHeader(http.StatusForbidden)
			return true
		}
		return false
	}
	receiveAccess(t, ch, topiclessGroupMessage("denied-user", "other", "chat", true))
	receiveAccess(t, ch, topiclessGroupMessage("denied-chat", "member", "other-chat", true))
	receiveAccess(t, ch, topiclessGroupMessage("quiet", "user", "chat", false))
	receiveAccess(t, ch, groupMessage("native", "user", "chat", "原生话题消息", true))
	if inbox := e.Store.snapshot().Inbox; len(inbox) != 1 || inbox[0].ID != "native" || inbox[0].TopicID != "topic" {
		t.Fatalf("inbox = %+v, want only the native topic message", inbox)
	}
}

func TestTopicIntakeHandlesGroupAndUnknownModes(t *testing.T) {
	for _, tc := range []struct {
		name, chatResponse              string
		topicACL, nativeTopic, accepted bool
	}{
		{name: "ordinary group", chatResponse: `{"code":0,"data":{"chat_mode":"group"}}`, accepted: true},
		{name: "unknown mode", chatResponse: `{"code":0,"data":{"chat_mode":"unrecognized"}}`, accepted: true},
		{name: "chat read failure", chatResponse: `{"code":230027}`, accepted: true},
		{name: "unknown mode with topic ACL", chatResponse: `{"code":0,"data":{}}`, topicACL: true},
		{name: "unknown mode with native topic evidence", chatResponse: `{"code":230027}`, nativeTopic: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, l, ch, _, f := setupAccess(t)
			if !tc.topicACL {
				e.Config.Topics, l.Config.Topics = nil, nil
			}
			var chatRead atomic.Bool
			f.extra = func(w http.ResponseWriter, r *http.Request) bool {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == topicIntakeChatPath:
					chatRead.Store(true)
					io.WriteString(w, tc.chatResponse)
				case r.Method == http.MethodGet && r.URL.Path == topicIntakeMessagePath+"message":
					if tc.name == "ordinary group" {
						t.Error("ordinary group attempted a message lookup")
					}
					io.WriteString(w, `{"code":230027,"msg":"synthetic read failure"}`)
				default:
					return false
				}
				return true
			}
			if tc.nativeTopic {
				receiveAccess(t, ch, groupMessage("native", "user", "chat", "原生话题消息", true))
			}
			receiveAccess(t, ch, topiclessGroupMessage("message", "user", "chat", true))
			inbox := e.Store.snapshot().Inbox
			switch {
			case tc.nativeTopic:
				if len(inbox) != 1 || inbox[0].ID != "native" || inbox[0].TopicID != "topic" {
					t.Fatalf("inbox = %+v, want only the native topic message", inbox)
				}
			case !chatRead.Load():
				t.Fatal("chat mode was not read")
			case tc.accepted:
				if len(inbox) != 1 || inbox[0].ID != "message" || inbox[0].TopicID != "" {
					t.Fatalf("inbox = %+v, want the message in ordinary group scope", inbox)
				}
			case len(inbox) != 0:
				t.Fatalf("inbox = %+v, want rejection", inbox)
			}
		})
	}
}
