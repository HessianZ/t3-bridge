package bridge

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	channel "github.com/larksuite/channel-sdk-go"
)

func TestGroupDoctorEchoUsesOriginalSenderOnly(t *testing.T) {
	const (
		sender      = "ou_Original-Sender_1"
		content     = "模型回声通过\n\"OK\""
		receipt     = "om_synthetic_dm"
		invalid     = "invalid group doctor echo notification"
		unconfirmed = "group doctor echo delivery unconfirmed"
	)
	for _, tc := range []struct {
		name     string
		response string
		status   int
		change   func(*Task)
		wantID   string
		wantErr  string
	}{
		{name: "direct text", response: `{"code":0,"data":{"message_id":"om_synthetic_dm"}}`, wantID: receipt},
		{name: "API rejection", response: `{"code":230013,"msg":"synthetic private API error"}`, wantErr: unconfirmed},
		{name: "missing receipt", response: `{"code":0,"data":{}}`, wantErr: unconfirmed},
		{name: "unreadable receipt", response: "synthetic private response", wantErr: unconfirmed},
		{name: "failed HTTP receipt", response: `{"code":0,"data":{"message_id":"om_synthetic_dm"}}`, status: http.StatusBadGateway, wantErr: unconfirmed},
		{name: "invalid open ID", change: func(task *Task) { task.Source.UserID = " " + sender }, wantErr: invalid},
		{name: "nonterminal", change: func(task *Task) { task.Phase = "tracking" }, wantErr: invalid},
		{name: "interactive", change: func(task *Task) { task.TextReply = false }, wantErr: invalid},
		{name: "markdown post", change: func(task *Task) { task.MarkdownReply = true }, wantErr: invalid},
		{name: "comment source", change: func(task *Task) { task.Source.Comment = &CommentSource{} }, wantErr: invalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := &Task{
				ID: "synthetic-echo-task", CardRequestID: "synthetic-echo-request",
				Phase: "completed", TextReply: true, TextReplyBody: content,
				Source: Message{
					ID: "om_original", ChatID: "oc_original_group", ChatType: "group",
					TopicID: "omt_original_topic", UserID: sender, InputKind: doctorEchoKind,
				},
				ReplySource: &Message{
					ID: "om_forged", ChatID: "oc_forged_group", ChatType: "group",
					TopicID: "omt_forged_topic", UserID: "ou_forged_sender",
				},
			}
			if tc.change != nil {
				tc.change(task)
			}
			wantBody := map[string]string{
				"receive_id": sender,
				"msg_type":   "text",
				"content":    `{"text":"模型回声通过\n\"OK\""}`,
				"uuid":       cardUUID(task),
			}
			var requests, sends atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
					io.WriteString(w, `{"code":0,"tenant_access_token":"synthetic-token","expire":7200}`)
					return
				}
				sends.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/open-apis/im/v1/messages" || r.URL.RawQuery != "receive_id_type=open_id" {
					t.Errorf("unexpected outbound request %s %s", r.Method, r.URL)
				}
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !maps.Equal(body, wantBody) {
					t.Errorf("unexpected create body %v, want %v (decode error %v)", body, wantBody, err)
				}
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				io.WriteString(w, tc.response)
			}))
			defer server.Close()
			ch, err := channel.New("synthetic-app", "synthetic-secret", channel.WithOpenBaseURL(server.URL), channel.WithEnableTokenCache(false))
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			defer ch.Stop(ctx)
			lark := &Lark{Channel: ch}

			id, err := lark.Notify(ctx, task)
			gotErr := ""
			if err != nil {
				gotErr = err.Error()
			}
			if id != tc.wantID || gotErr != tc.wantErr {
				t.Errorf("Notify returned (%q, %q), want (%q, %q)", id, gotErr, tc.wantID, tc.wantErr)
			}
			if tc.wantErr == invalid {
				if got := requests.Load(); got != 0 {
					t.Errorf("invalid task made %d HTTP requests", got)
				}
			} else if got := sends.Load(); got != 1 {
				t.Errorf("expected one direct send, got %d send attempts", got)
			}
		})
	}
}
