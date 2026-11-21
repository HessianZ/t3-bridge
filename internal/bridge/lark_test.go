package bridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	channel "github.com/larksuite/channel-sdk-go"
	im "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestChannelNormalizeRetainsTopicAndResources(t *testing.T) {
	parent, root, topic := "parent", "root", "topic"
	n := &channel.NormalizedMessage{MessageID: "message", ChatID: "chat", UserID: "user", Content: "用户原文", RawContentType: "image", Resources: []channel.Resource{{Type: "image", FileKey: "image-key"}}, RawEvent: &im.P2MessageReceiveV1{Event: &im.P2MessageReceiveV1Data{Message: &im.EventMessage{ParentId: &parent, RootId: &root, ThreadId: &topic}}}}
	m := Normalize(n)
	if m.ParentID != parent || m.RootID != root || m.TopicID != topic || m.Text != n.Content || len(m.Images) != 1 || m.Images[0].MessageID != "message" {
		t.Fatalf("lost inbound identity %+v", m)
	}
}

func TestAttachmentDownloadBoundsSDKBufferWithoutContentLength(t *testing.T) {
	source := strings.NewReader(strings.Repeat("x", 100))
	client := attachmentDownloadClient{&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(source), ContentLength: -1}, nil
	})}}
	ctx := context.WithValue(context.Background(), attachmentDownloadLimit{}, 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test/open-apis/im/v1/messages/m/resources/f", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || len(data) != 11 || source.Len() != 89 {
		t.Fatalf("read %d bytes, %d left upstream: %v", len(data), source.Len(), err)
	}
}
func TestOfficialLarkSDKReplyPatchQuoteAndImage(t *testing.T) {
	uuid := ""
	replies, patches := 0, 0
	contents := []string{}
	patchPath := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "tenant_access_token"):
			io.WriteString(w, `{"code":0,"tenant_access_token":"synthetic-token","expire":7200}`)
		case strings.HasSuffix(r.URL.Path, "/reply"):
			replies++
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			contents = append(contents, body["content"].(string))
			if r.URL.Path != "/open-apis/im/v1/messages/one/reply" {
				t.Error("reply changed source message")
			}
			current := body["uuid"].(string)
			if uuid != "" && current != uuid {
				t.Error("unstable notification UUID")
			}
			uuid = current
			if body["msg_type"] != "interactive" || body["reply_in_thread"] != true {
				t.Error("lost card/topic")
			}
			io.WriteString(w, `{"code":0,"data":{"message_id":"card"}}`)
		case r.Method == "PATCH":
			patches++
			patchPath = r.URL.Path
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			contents = append(contents, body["content"].(string))
			io.WriteString(w, `{"code":0,"data":{}}`)
		case strings.Contains(r.URL.Path, "/resources/"):
			w.Header().Set("Content-Type", "image/png")
			io.WriteString(w, "image-bytes")
		default:
			io.WriteString(w, `{"code":0,"data":{"items":[{"message_id":"parent","chat_id":"chat","thread_id":"topic","msg_type":"image","body":{"content":"{\"image_key\":\"quoted-key\"}"}}]}}`)
		}
	}))
	defer server.Close()
	ch, err := channel.New("synthetic-app", "synthetic-secret", channel.WithOpenBaseURL(server.URL), channel.WithEnableTokenCache(false))
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Stop(context.Background())
	l := &Lark{Channel: ch}
	task := &Task{ID: "task", Source: message("one", "NAS检查磁盘"), Phase: "tracking", RouteID: "nas", Summary: "## 合成回答\n**粗体**与`mcp`"}
	id, err := l.Notify(context.Background(), task)
	if err != nil || id != "card" {
		t.Fatalf("reply %s %v", id, err)
	}
	// Lost response simulated by leaving CardID empty: UUID must remain unchanged.
	if _, err = l.Notify(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	task.CardID = id
	task.Phase = "completed"
	if _, err = l.Notify(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	m := task.Source
	m.ParentID = "parent"
	m, err = l.Resolve(context.Background(), m)
	if err != nil || len(m.Images) != 1 || m.Images[0].Key != "quoted-key" || m.Images[0].MessageID != "parent" {
		t.Fatalf("quote %+v %v", m, err)
	}
	b, err := l.Image(context.Background(), m, m.Images[0], maxNativeImageBytes)
	if err != nil || string(b) != "image-bytes" {
		t.Fatal("image SDK boundary", err)
	}
	if len(contents) != 3 || patchPath != "/open-apis/im/v1/messages/card" {
		t.Fatal("notification changed original CardID or transport", patchPath)
	}
	for _, content := range contents {
		elements := card2Elements(t, content)
		if elements[1].(map[string]any)["tag"] != "markdown" || elements[1].(map[string]any)["content"] != task.Summary {
			t.Fatal("actual Reply/Patch did not send markdown answer")
		}
	}
	if len(card2Elements(t, contents[0])) != 3 || len(card2Elements(t, contents[2])) != 2 {
		t.Fatal("completed Patch retained stop button")
	}
	if replies != 2 || patches != 1 {
		t.Fatal("same card not updated")
	}
}

func TestBotMentionRoutingPreservesOriginalAndReplyPolicy(t *testing.T) {
	c := testConfig(t)
	m := Normalize(&channel.NormalizedMessage{MessageID: "one", ChatID: "chat", UserID: "user", Content: "@_user_1 NAS检查磁盘", RawContentType: "text", Mentions: []channel.Mention{{Key: "@_user_1", Name: "NAS bot", IsBot: true}}})
	d, err := Route(context.Background(), c, m, nil, nil)
	if err != nil || d.RouteID != "nas" || m.Text != "@_user_1 NAS检查磁盘" {
		t.Fatalf("mention lost original/routing: %+v %v", m, err)
	}
}
