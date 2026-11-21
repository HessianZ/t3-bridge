package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	channel "github.com/larksuite/channel-sdk-go"
)

const nativeQuoteCard = `{
  "schema":"2.0",
  "header":{"title":{"tag":"plain_text","content":"可见卡片标题"},"subtitle":{"tag":"plain_text","content":"可见卡片副标题"}},
  "config":{"summary":{"content":"hidden-summary"}},
  "body":{"elements":[
    {"tag":"markdown","content":"可见卡片正文"},
    {"tag":"column_set","columns":[{"tag":"column","elements":[{"tag":"div","text":{"tag":"plain_text","content":"可见分栏文字"}}]}]},
    {"tag":"button","text":{"tag":"plain_text","content":"hidden-button"},"behaviors":[{"type":"open_url","default_url":"https://example.invalid/hidden-action"}]},
    {"tag":"form","elements":[{"tag":"markdown","content":"hidden-form"}]},
    {"tag":"img","img_key":"hidden-resource"}
  ]}
}`

func quoteFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/quotes/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func quoteAPI(t *testing.T, response func(string) []byte) *Lark {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			io.WriteString(w, `{"code":0,"tenant_access_token":"synthetic-token","expire":7200}`)
			return
		}
		if r.Method != "GET" || !strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages/") {
			t.Errorf("unexpected quote request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
			return
		}
		if got := r.URL.Query().Get("card_msg_content_type"); got != "user_card_content" {
			t.Errorf("card_msg_content_type = %q", got)
		}
		w.Write(response(strings.TrimPrefix(r.URL.Path, "/open-apis/im/v1/messages/")))
	}))
	t.Cleanup(server.Close)
	ch, err := channel.New("synthetic-app", "synthetic-secret", channel.WithOpenBaseURL(server.URL), channel.WithEnableTokenCache(false))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ch.Stop(context.Background()) })
	return &Lark{Channel: ch}
}

type quoteMessenger struct {
	*fakeMessenger
	lark *Lark
}

func (q quoteMessenger) Resolve(ctx context.Context, m Message) (Message, error) {
	return q.lark.Resolve(ctx, m)
}

func TestResolveOfficialPostAndInteractiveQuotes(t *testing.T) {
	for _, tc := range []struct {
		file, parent, text string
		images             int
	}{
		{"post-alert-get.json", "post-alert", "合成磁盘告警", 1},
		{"interactive-fault-get.json", "fault-alert", "HomeOps 合成故障", 0},
		{"interactive-recovery-get.json", "recovery-alert", "HomeOps 合成恢复", 0},
		{"interactive-null-title-get.json", "completed-card", "synthetic-content", 0},
		{"interactive-card2-get.json", "card2-message", "目标：nas", 0},
	} {
		t.Run(tc.file, func(t *testing.T) {
			api := quoteAPI(t, func(id string) []byte {
				if id != tc.parent {
					t.Error("lost quoted message identity")
				}
				return quoteFixture(t, tc.file)
			})
			m := message("user-message", "查一下")
			m.ParentID = tc.parent
			got, err := api.Resolve(context.Background(), m)
			if err != nil || !strings.Contains(got.Quote, tc.text) || len(got.Images) != tc.images {
				t.Fatalf("quote=%+v err=%v", got, err)
			}
			if got.UserID != m.UserID || got.ChatID != m.ChatID || got.TopicID != m.TopicID || got.Text != m.Text || got.EventRoute != "" {
				t.Fatal("quote altered user identity, original or authorization")
			}
			for _, hidden := range []string{"hidden-", "https://", "ignored-v2", "Mac重启", strings.Repeat("f", 32)} {
				if strings.Contains(got.Quote, hidden) {
					t.Fatal("hidden/action/url data entered quote")
				}
			}
			for _, image := range got.Images {
				if image.MessageID != m.ParentID {
					t.Fatal("post image lost original message binding")
				}
			}
		})
	}
}

func TestResolveOriginalInteractiveCard(t *testing.T) {
	const nativeText = "可见卡片标题\n可见卡片副标题\n可见卡片正文\n可见分栏文字"
	for _, tc := range []struct {
		name, raw, want string
	}{
		{"native", nativeQuoteCard, nativeText},
		{"wrapped", nativeQuoteCard, nativeText},
		{"legacy", `{"schema":"1.0","header":{"title":{"tag":"plain_text","content":"旧卡片标题"}},"elements":[{"tag":"div","text":{"tag":"lark_md","content":"正文"},"fields":[{"text":{"tag":"plain_text","content":"字段"}}]},{"tag":"note","elements":[{"tag":"plain_text","content":"备注"}]}]}`, "旧卡片标题\n正文\n字段\n备注"},
	} {
		raw := tc.raw
		if tc.name == "wrapped" {
			envelope, err := json.Marshal(map[string]any{"user_dsl": raw, "title": "hidden-downgrade"})
			if err != nil {
				t.Fatal(err)
			}
			raw = string(envelope)
		}
		response, err := json.Marshal(map[string]any{"code": 0, "data": map[string]any{"items": []any{map[string]any{
			"message_id": "original-card", "chat_id": "chat", "thread_id": "topic", "msg_type": "interactive",
			"body": map[string]string{"content": raw},
		}}}})
		if err != nil {
			t.Fatal(err)
		}
		api := quoteAPI(t, func(id string) []byte {
			if id != "original-card" {
				t.Error("raw card lookup lost the cited message identity")
			}
			return response
		})
		m := message("user-message", "请总结这张卡片")
		m.ParentID = "original-card"
		got, err := api.Resolve(t.Context(), m)
		if err != nil || got.Text != m.Text || got.Quote != tc.want || len(got.Images)+len(got.Files) != 0 {
			t.Fatalf("%s card quote = %q, want %q; error %v", tc.name, got.Quote, tc.want, err)
		}
	}
}

func TestQuotedContentBoundsAndAllowlist(t *testing.T) {
	cases := []struct {
		name, kind, raw string
		valid           bool
	}{
		{"text", "text", `{"text":"正文"}`, true},
		{"post_title_only", "post", `{"title":"标题","content":[]}`, true},
		{"post_image_only", "post", `{"content":[[{"tag":"img","image_key":"image"}]]}`, true},
		{"note", "interactive", `{"title":null,"elements":[[{"tag":"note","elements":[{"tag":"text","text":"注释"}]}]]}`, true},
		{"normalized_card_with_schema", "interactive", `{"schema":"1.0","elements":[[{"tag":"text","text":"行式正文"}]]}`, true},
		{"unknown_node_hidden_text", "interactive", `{"elements":[[{"tag":"form","text":"hidden","elements":[{"tag":"text","text":"hidden"}]}]]}`, false},
		{"button_only", "interactive", `{"elements":[[{"tag":"button","text":"hidden"}]]}`, false},
		{"original_card1_shape", "interactive", `{"elements":[{"tag":"div","text":{"tag":"plain_text","content":"send JSON"}}]}`, true},
		{"original_card2_shape", "interactive", nativeQuoteCard, true},
		{"malformed_wrapped_card", "interactive", `{"user_dsl":"{","elements":[[{"tag":"text","text":"fallback"}]]}`, false},
		{"original_card_action_only", "interactive", `{"schema":"2.0","body":{"elements":[{"tag":"button","text":{"tag":"plain_text","content":"hidden-button"}}]}}`, false},
		{"original_card_text_limit", "interactive", `{"schema":"2.0","body":{"elements":[{"tag":"markdown","content":"` + strings.Repeat("x", 8001) + `"}]}}`, false},
		{"malformed", "post", `{"content":`, false},
		{"bad_rows", "post", `{"content":[{"tag":"text","text":"not a row"}]}`, false},
		{"text_limit", "text", `{"text":"` + strings.Repeat("x", 8001) + `"}`, false},
		{"body_limit", "post", `{"hidden":"` + strings.Repeat("x", 32769) + `"}`, false},
	}
	nodes := []any{}
	for i := 0; i < 257; i++ {
		nodes = append(nodes, map[string]string{"tag": "text", "text": "x"})
	}
	raw, _ := json.Marshal(map[string]any{"elements": []any{nodes}})
	cases = append(cases, struct {
		name, kind, raw string
		valid           bool
	}{"node_limit", "interactive", string(raw), false})
	note := map[string]any{"tag": "text", "text": "deep"}
	for i := 0; i < 6; i++ {
		note = map[string]any{"tag": "note", "elements": []any{note}}
	}
	raw, _ = json.Marshal(map[string]any{"elements": []any{[]any{note}}})
	cases = append(cases, struct {
		name, kind, raw string
		valid           bool
	}{"depth_limit", "interactive", string(raw), false})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := quotedContent(tc.kind, tc.raw, "parent")
			if (err == nil) != tc.valid {
				t.Fatal("unexpected quote acceptance", err)
			}
		})
	}
}

func TestResolveQuoteScopeDeletionAndCombinedImageLimit(t *testing.T) {
	for _, kind := range []string{"chat", "topic", "deleted", "images"} {
		t.Run(kind, func(t *testing.T) {
			var response map[string]any
			json.Unmarshal(quoteFixture(t, "post-alert-get.json"), &response)
			item := response["data"].(map[string]any)["items"].([]any)[0].(map[string]any)
			switch kind {
			case "chat":
				item["chat_id"] = "other"
			case "topic":
				item["thread_id"] = "other"
			case "deleted":
				item["deleted"] = true
			}
			b, _ := json.Marshal(response)
			api := quoteAPI(t, func(string) []byte { return b })
			m := message("one", "查一下")
			m.ParentID = "post-alert"
			if kind == "images" {
				m.Images = make([]Image, maxTurnAttachments)
			}
			if _, err := api.Resolve(context.Background(), m); err == nil {
				t.Fatal("quote boundary bypassed")
			}
		})
	}
}

func TestAlertGetFixturesLinkFaultRecoveryToSameInvestigation(t *testing.T) {
	for _, firstQuote := range []struct {
		file, parent string
		images       int
	}{{"post-alert-get.json", "post-alert", 1}, {"interactive-fault-get.json", "fault-alert", 0}} {
		t.Run(firstQuote.file, func(t *testing.T) {
			e, x, notifications, _ := testEngine(t)
			e.Config.EventChats = []string{"chat"}
			e.Config.EventRoutes = map[string]string{"example-nas": "nas"}
			fault := Event{Node: "example-nas", Check: "hardware:smart", FailureStart: "2026-11-17T11:57:00.123456Z", Phase: "fault"}
			fault.ID = eventID(fault.Node, fault.Check, fault.FailureStart, fault.Phase)
			recovery := fault
			recovery.Phase = "recovery"
			recovery.ID = eventID(recovery.Node, recovery.Check, recovery.FailureStart, recovery.Phase)
			if err := e.Store.update(func(st *State) error {
				st.EventRecords[fault.ID] = fault
				st.EventRecords[recovery.ID] = recovery
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			api := quoteAPI(t, func(id string) []byte {
				if id == firstQuote.parent {
					return quoteFixture(t, firstQuote.file)
				}
				return quoteFixture(t, "interactive-recovery-get.json")
			})
			e.Lark = quoteMessenger{notifications, api}
			e.Classifier = &fakeClassifier{err: errors.New("known referenced investigation must not call classifier")}
			tick(t, e)
			if x.sends != 0 {
				t.Fatal("health polling started investigation")
			}
			first := message("one", "查一下")
			first.ParentID = firstQuote.parent
			accept(t, e, first)
			tick(t, e)
			original := onlyTask(t, e)
			if original.Source.EventID != fault.ID || x.sends != 1 || x.uploads != firstQuote.images || !strings.Contains(x.request, "仅获只读调查授权") {
				t.Fatal("fault not linked as read-only investigation")
			}
			x.observation.Status = "completed"
			tick(t, e)
			next := message("two", "排查这个告警")
			next.ParentID = "recovery-alert"
			accept(t, e, next)
			tick(t, e)
			task := onlyTask(t, e)
			if e.Store.snapshot().Events[recovery.ID] != original.ID {
				t.Fatal("recovery phase identity not durably bound")
			}
			if task.ID != original.ID || task.ThreadID != original.ThreadID || task.CardID != original.CardID || task.DispatchSource.EventID != recovery.ID || x.opens != 1 || x.sends != 2 {
				t.Fatal("recovery did not continue original scoped investigation")
			}
			x.observation.Status = "completed"
			tick(t, e)
			own := message("three", "继续核对")
			own.ParentID = task.CardID
			accept(t, e, own)
			tick(t, e)
			if x.sends != 3 || x.opens != 1 || !strings.Contains(x.request, "仅获只读调查授权") {
				t.Fatal("own-card continuation lost read-only event context")
			}
		})
	}
}

func TestAlertReferenceRejectsUnknownOrMultipleVisibleEvents(t *testing.T) {
	for _, kind := range []string{"unknown", "known_and_unknown", "two_known", "malformed", "known_and_malformed", "repeated_known", "no_request"} {
		t.Run(kind, func(t *testing.T) {
			e, x, notifications, _ := testEngine(t)
			e.Config.EventChats = []string{"chat"}
			e.Config.EventRoutes = map[string]string{"example-nas": "nas"}
			fault := Event{Node: "example-nas", Check: "hardware:smart", FailureStart: "2026-11-17T11:57:00.123456Z", Phase: "fault"}
			fault.ID = eventID(fault.Node, fault.Check, fault.FailureStart, fault.Phase)
			recovery := fault
			recovery.Phase = "recovery"
			recovery.ID = eventID(recovery.Node, recovery.Check, recovery.FailureStart, recovery.Phase)
			if err := e.Store.update(func(st *State) error {
				st.EventRecords[fault.ID] = fault
				st.EventRecords[recovery.ID] = recovery
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			visible := fault.ID
			unknown := "eb1_" + strings.Repeat("f", 32)
			switch kind {
			case "unknown":
				visible = unknown
			case "known_and_unknown":
				visible += "\n" + unknown
			case "known_and_malformed":
				visible += "\neb1_bad"
			case "two_known":
				visible += "\n" + recovery.ID
			case "malformed":
				visible += "extra"
			case "repeated_known":
				visible += "\n" + fault.ID
			}
			var response map[string]any
			json.Unmarshal(quoteFixture(t, "interactive-fault-get.json"), &response)
			item := response["data"].(map[string]any)["items"].([]any)[0].(map[string]any)
			body, _ := json.Marshal(map[string]any{"title": nil, "elements": []any{[]any{map[string]string{"tag": "text", "text": visible}}}})
			item["body"] = map[string]string{"content": string(body)}
			b, _ := json.Marshal(response)
			e.Lark = quoteMessenger{notifications, quoteAPI(t, func(string) []byte { return b })}
			classifier := &fakeClassifier{d: Decision{"nas", "new_task"}}
			e.Classifier = classifier
			m := message("one", "查一下")
			m.ParentID = "fault-alert"
			if kind == "no_request" {
				m.Text = "收到"
				classifier.d = Decision{Action: "clarify"}
			}
			accept(t, e, m)
			tick(t, e)
			task := onlyTask(t, e)
			if kind == "repeated_known" || kind == "no_request" {
				if x.sends != 1 || task.Source.EventID != fault.ID {
					t.Fatal("same visible event repeated should remain unique")
				}
				return
			}
			if x.sends != 0 || task.Phase != "clarify" || task.Source.EventID != "" {
				t.Fatal("ambiguous quote acquired an investigation target")
			}
			if kind != "no_request" && classifier.calls != 0 {
				t.Fatal("unknown event fell through to model routing")
			}
		})
	}
}

func TestReferencedInvestigationKeepsUserTargetAndQuoteScope(t *testing.T) {
	for _, kind := range []string{"other_user", "other_chat", "other_topic", "target_acl", "cross_target", "stopped"} {
		t.Run(kind, func(t *testing.T) {
			e, x, notifications, _ := testEngine(t)
			e.Config.EventChats = []string{"chat", "other-chat"}
			e.Config.EventRoutes = map[string]string{"example-nas": "nas"}
			fault := Event{Node: "example-nas", Check: "hardware:smart", FailureStart: "2026-11-17T11:57:00.123456Z", Phase: "fault"}
			fault.ID = eventID(fault.Node, fault.Check, fault.FailureStart, fault.Phase)
			recovery := fault
			recovery.Phase = "recovery"
			recovery.ID = eventID(recovery.Node, recovery.Check, recovery.FailureStart, recovery.Phase)
			if err := e.Store.update(func(st *State) error {
				st.EventRecords[fault.ID] = fault
				st.EventRecords[recovery.ID] = recovery
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			api := quoteAPI(t, func(id string) []byte {
				if id == "fault-alert" {
					return quoteFixture(t, "interactive-fault-get.json")
				}
				return quoteFixture(t, "interactive-recovery-get.json")
			})
			e.Lark = quoteMessenger{notifications, api}
			m := message("one", "排查这个告警")
			m.ParentID = "fault-alert"
			accept(t, e, m)
			tick(t, e)
			x.observation.Status = "completed"
			tick(t, e)
			original := onlyTask(t, e)
			reply := message("two", "查一下")
			reply.ParentID = "recovery-alert"
			switch kind {
			case "other_user":
				reply.UserID = "other"
			case "other_chat":
				reply.ChatID = "other-chat"
				e.Config.Chats = append(e.Config.Chats, reply.ChatID)
			case "other_topic":
				reply.TopicID = "other-topic"
				e.Config.Topics = append(e.Config.Topics, reply.TopicID)
			case "target_acl":
				target := e.Config.Routes["nas"]
				target.Users = []string{"other"}
				e.Config.Routes["nas"] = target
			case "cross_target":
				target := e.Config.Routes["nas"]
				target.ProjectID = "other-project"
				e.Config.Routes["nas-other"] = target
				e.Config.EventRoutes["example-nas"] = "nas-other"
				reply.RootID = "one"
			case "stopped":
				stop := message("stop", "停止")
				stop.ParentID = original.CardID
				accept(t, e, stop)
				tick(t, e)
			}
			accept(t, e, reply)
			tick(t, e)
			current := e.Store.snapshot().Tasks[original.ID]
			if kind == "other_user" {
				st := e.Store.snapshot()
				if x.sends != 2 || x.opens != 2 || len(st.Tasks) != 2 || !reflect.DeepEqual(current, original) {
					t.Fatal("another authorized user borrowed the original context instead of a separate investigation")
				}
				return
			}
			if x.sends != 1 || x.opens != 1 || current.CardID != original.CardID || current.RouteID != original.RouteID {
				t.Fatal("quote crossed task/ACL/target/stop boundary")
			}
			if kind == "stopped" && (current.Phase != "stopped" || x.stops != 0) {
				t.Fatal("completed investigation revived or interrupted unrelated T3 work")
			}
		})
	}
}

func TestPostQuoteFourImageLimit(t *testing.T) {
	for _, count := range []int{4, 5} {
		images := []any{}
		for i := 0; i < count; i++ {
			images = append(images, []any{map[string]string{"tag": "img", "image_key": "synthetic-image"}})
		}
		raw, _ := json.Marshal(map[string]any{"content": images})
		_, got, err := quotedContent("post", string(raw), "post-alert")
		if count == 4 {
			if err != nil || len(got) != 4 {
				t.Fatal("four post images should fit", err)
			}
		} else if err == nil {
			t.Fatal("fifth post image accepted")
		}
	}
}
