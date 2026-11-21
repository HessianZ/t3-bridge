package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	channel "github.com/larksuite/channel-sdk-go"
	im "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestGroupEventGrantRevocationAndRestart(t *testing.T) {
	e, _, ch, _, f := setupAccess(t)
	e.Config.EventChats = []string{"chat"}
	e.Config.EventRoutes = map[string]string{"example-nas": "nas"}
	ev := syntheticEvent("example-nas", "hardware:smart", "fault", e.now())
	e.Store.update(func(st *State) error { st.EventRecords[ev.ID] = ev; return nil })
	m := Normalize(groupMessage("quote", "user", "invited-group", "只解释这条告警", true))
	m.Quote = "事件：" + ev.ID
	check := func(want bool) {
		t.Helper()
		linked, err := e.linkEvent(m)
		if err != nil || (linked.EventID == ev.ID) != want || (want && linked.EventRoute != "nas") {
			t.Fatal("explicit event group boundary", linked.EventID, err)
		}
	}
	reopen := func() {
		t.Helper()
		if err := e.Store.Close(); err != nil {
			t.Fatal(err)
		}
		s, err := OpenStore(e.Config.StateDir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		fresh, err := channel.New("fake-app", "synthetic-secret", channel.WithOpenBaseURL(f.baseURL), channel.WithEnableTokenCache(false))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { fresh.Stop(context.Background()) })
		ch = &accessChannel{Channel: fresh}
		l := newLark(ch, e.Config, s)
		l.RefreshOwner(context.Background())
		e.Store, e.Config, e.Lark = s, l.Config, l
	}
	if !e.Config.Allowed(m) || e.Config.groupAllowed(m.ChatID) {
		t.Fatal("owner exemption was confused with explicit group grant")
	}
	check(false)
	accessCommand(t, e, ch, "invite", "user", m.ChatID, "/invite group")
	check(true)
	reopen()
	check(true)
	accessCommand(t, e, ch, "remove", "user", m.ChatID, "/remove group")
	check(false)
	reopen()
	check(false)
	if e.Store.snapshot().GroupAccess.Chats[m.ChatID] || len(e.Config.EventChats) != 1 || e.Config.EventChats[0] != "chat" {
		t.Fatal("revocation lost or static event destinations mutated")
	}
	m.ChatID, m.ChatType = "chat", "p2p"
	check(true) // Preserve the existing explicit static EventChats path.
}

func TestManagedGroupEventReportRoundAndScope(t *testing.T) {
	e, l, ch, x, f := setupAccess(t)
	setupReports(t, e)
	e.Config.Topics, l.Config.Topics = nil, nil
	e.Config.DefaultRoute = "nas"
	e.Classifier = &fakeClassifier{err: errors.New("trusted event must bypass model")}
	ev := syntheticEvent("example-nas", "hardware:smart", "fault", e.now())
	recovery := ev
	recovery.Phase = "recovery"
	recovery.ID = eventID(recovery.Node, recovery.Check, recovery.FailureStart, recovery.Phase)
	e.Store.update(func(st *State) error {
		st.EventRecords[ev.ID] = ev
		st.EventRecords[recovery.ID] = recovery
		return nil
	})
	f.extra = func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/open-apis/im/v1/chats/managed-group":
			io.WriteString(w, `{"code":0,"data":{"chat_mode":"group","group_message_type":"chat"}}`)
			return true
		case r.Method == "POST" && r.URL.Path == "/open-apis/im/v1/chats":
			io.WriteString(w, `{"code":0,"data":{"chat_id":"managed-group"}}`)
			return true
		case r.Method == "POST" && r.URL.Path == "/open-apis/im/v1/messages":
			io.WriteString(w, `{"code":0,"data":{"message_id":"welcome"}}`)
			return true
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages/"):
			id := strings.TrimPrefix(r.URL.Path, "/open-apis/im/v1/messages/")
			event := ev.ID
			if id == "recovery-alert" {
				event = recovery.ID
			}
			content, _ := json.Marshal(map[string]string{"text": "告警资料\n事件：" + event})
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"items": []any{map[string]any{"message_id": id, "chat_id": "managed-group", "thread_id": "", "msg_type": "text", "body": map[string]string{"content": string(content)}}}}})
			return true
		}
		return false
	}
	accessCommand(t, e, ch, "create", "user", "chat", "/new chat 告警讨论")
	if !e.Config.groupAllowed("managed-group") || !e.Store.snapshot().GroupAccess.Chats["managed-group"] || x.sends != 0 {
		t.Fatal("managed group was not explicitly persisted")
	}
	// A dynamic group grant does not make it an automatic investigation destination.
	c := e.Config
	c.Chats = append(append([]string{}, c.Chats...), "managed-group")
	c.Health = []HealthSource{{"http://127.0.0.1/healthz", "example-nas", "127.0.0.1", "/synthetic/key"}}
	c.AutoInvestigations = AutoInvestigations{Enabled: true, LegacyDispatcherStopped: true, UserID: "user", ChatID: "managed-group", ChatType: "group"}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "automatic investigation requires") {
		t.Fatal("dynamic human group became an automatic destination without EventChats")
	}
	n := groupMessage("investigate", "user", "managed-group", "只解释这条告警，不实施", true)
	n.RawEvent.(*im.P2MessageReceiveV1).Event.Message.ThreadId = nil
	parent := "fault-alert"
	n.RawEvent.(*im.P2MessageReceiveV1).Event.Message.ParentId = &parent
	receiveAccess(t, ch, n)
	tick(t, e)
	first := onlyTask(t, e)
	if x.sends != 1 || first.DispatchSource.EventID != ev.ID || first.EventContext == "" || !strings.Contains(first.ReportInstructions, first.RequestID) || !strings.Contains(x.request, "仅获只读调查授权") {
		t.Fatal("managed quote lost incident/report identity")
	}
	x.observation.Status = "completed"
	tick(t, e)
	e.Classifier = nil
	x.observation.Status = "running"
	follow := groupMessage("follow", "user", "managed-group", "继续解释证据", true)
	follow.RawEvent.(*im.P2MessageReceiveV1).Event.Message.ThreadId = nil
	receiveAccess(t, ch, follow)
	tick(t, e)
	current := onlyTask(t, e)
	if x.sends != 2 || x.opens != 1 || current.ID != first.ID || current.RequestID == first.RequestID || current.DispatchSource.EventID != ev.ID || !strings.Contains(current.ReportInstructions, current.RequestID) || strings.Contains(current.ReportInstructions, first.RequestID) || len(current.Replies) != 1 || current.Replies[0].Source.EventID != ev.ID || current.Source.ID != first.Source.ID {
		t.Fatal("group followup reused old report parameters or replaced source")
	}
	foreign := groupMessage("borrow", "other", "managed-group", "继续", true)
	foreign.RawEvent.(*im.P2MessageReceiveV1).Event.Message.ThreadId = nil
	foreign.RawEvent.(*im.P2MessageReceiveV1).Event.Message.ParentId = &current.CardID
	receiveAccess(t, ch, foreign)
	tick(t, e)
	if x.sends != 2 || len(e.Store.snapshot().Tasks) != 1 {
		t.Fatal("another authorized route user borrowed owner's card/context")
	}
	stop := groupMessage("stop", "user", "managed-group", "/stop", true)
	stop.RawEvent.(*im.P2MessageReceiveV1).Event.Message.ThreadId = nil
	receiveAccess(t, ch, stop)
	tick(t, e)
	if x.stops != 1 || e.Store.task(first.ID).Phase != "stopped" {
		t.Fatal("group investigation stop lost")
	}
	n.MessageID = "recover"
	parent = "recovery-alert"
	receiveAccess(t, ch, n)
	tick(t, e)
	if x.sends != 2 || e.Store.task(first.ID).Phase != "stopped" {
		t.Fatal("recovery quote revived stopped investigation")
	}
	if len(e.Config.EventChats) != 1 || e.Config.EventChats[0] != "chat" || e.Config.AutoInvestigations.Enabled {
		t.Fatal("manual group flow expanded automatic config")
	}
}

func TestGroupEventACLAndUntrustedReferences(t *testing.T) {
	for _, kind := range []string{"member", "owner-ungranted", "foreign-user", "route-denied", "p2p", "unknown", "multiple", "foreign-topic"} {
		t.Run(kind, func(t *testing.T) {
			e, _, ch, x, _ := setupAccess(t)
			e.Config.EventChats = []string{"chat"}
			e.Config.EventRoutes = map[string]string{"example-nas": "nas"}
			ev := syntheticEvent("example-nas", "hardware:smart", "fault", e.now())
			next := syntheticEvent("example-nas", "service:gitea", "fault", e.now())
			e.Store.update(func(st *State) error { st.EventRecords[ev.ID] = ev; st.EventRecords[next.ID] = next; return nil })
			if kind != "owner-ungranted" {
				accessCommand(t, e, ch, "grant", "user", "manual-group", "/invite group")
			}
			e.Classifier = &fakeClassifier{d: Decision{"nas", "new_task"}}
			messenger := &fakeMessenger{message: Message{Quote: "事件：" + ev.ID}}
			e.Lark = messenger
			m := Normalize(groupMessage("quote", "other", "manual-group", "解释原因", true))
			m.ParentID = "alert"
			wantSend, wantEvent := false, false
			switch kind {
			case "member":
				wantSend, wantEvent = true, true
			case "owner-ungranted":
				m.UserID = "user"
				wantSend = true
			case "foreign-user":
				m.UserID = "stranger"
			case "route-denied":
				v := e.Config.Routes["nas"]
				v.Users = []string{"user"}
				e.Config.Routes["nas"] = v
			case "p2p":
				m.UserID, m.ChatType = "user", "p2p"
				wantSend = true
			case "unknown":
				messenger.message.Quote = "事件：eb1_" + strings.Repeat("f", 32)
			case "multiple":
				messenger.message.Quote += "\n事件：" + next.ID
			case "foreign-topic":
				m.TopicID = "foreign"
			}
			if e.Config.Allowed(m) {
				accept(t, e, m)
				tick(t, e)
			}
			if (x.sends == 1) != wantSend || x.sends > 1 {
				t.Fatal("group event bypassed scope/route boundary", x.sends)
			}
			for _, task := range e.Store.snapshot().Tasks {
				if (task.DispatchSource.EventID == ev.ID) != wantEvent || (!wantEvent && task.EventContext != "") {
					t.Fatal("unauthorized/untrusted event identity persisted", task.RouteID)
				}
			}
		})
	}
}
