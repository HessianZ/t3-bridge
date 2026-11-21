package bridge

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"testing"
	"time"

	channel "github.com/larksuite/channel-sdk-go"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

func TestOfficialLarkSDKMeetingSummary(t *testing.T) {
	const chatID = "oc_synthetic"
	const openID = "synthetic-owner"
	const uuid = " 7f3b7fd2-1b64-4f5d-9e8b-2f2d8d3b0c11 "
	const summary = " 合成纪要\n第二行 \"引号\" "
	const messageID = "om_synthetic"
	const data = `"data":{"message_id":"` + messageID + `"}`
	const matchingChat = `{"code":0,"data":{"message_id":"` + messageID + `","chat_id":"` + chatID + `"}}`
	const rejected = `{"code":230002,"msg":"synthetic-private-response"}`
	for _, tc := range []struct {
		name, body string
		status     int
		direct     bool
		success    bool
		rejected   bool
	}{
		{name: "matching chat", body: matchingChat, success: true},
		{name: "omitted chat", body: `{"code":0,` + data + `}`, success: true},
		{name: "direct message", body: matchingChat, direct: true, success: true},
		{name: "missing message ID", body: `{"code":0,"data":{}}`},
		{name: "mismatched chat", body: `{"code":0,"data":{"message_id":"om_synthetic","chat_id":"oc_other"}}`},
		{name: "empty chat", body: `{"code":0,"data":{"message_id":"om_synthetic","chat_id":""}}`},
		{name: "missing business code", body: `{` + data + `}`},
		{name: "null business code", body: `{"code":null,` + data + `}`},
		{name: "malformed response", body: `{"code":0,`},
		{name: "success body on HTTP failure", status: http.StatusForbidden, body: matchingChat},
		{name: "business rejection", body: rejected, rejected: true},
		{name: "client error with rejection", status: http.StatusForbidden, body: rejected, rejected: true},
		{name: "server error", status: http.StatusBadGateway, body: rejected},
		{name: "rate limit", status: http.StatusTooManyRequests, body: rejected},
		{name: "lost response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receiverType, recipient := "chat_id", chatID
			if tc.direct {
				receiverType, recipient = "open_id", openID
			}
			f := newLoopbackLark(t, func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != "/open-apis/im/v1/messages" {
					return false
				}
				if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer synthetic-feishu-token" ||
					r.URL.Query().Get("receive_id_type") != receiverType {
					t.Error("unexpected IM create method, authorization or recipient type")
				}
				var body map[string]string
				want := map[string]string{
					"receive_id": recipient, "uuid": uuid, "msg_type": "text",
					"content": `{"text":" 合成纪要\n第二行 \"引号\" "}`,
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				} else if !reflect.DeepEqual(body, want) {
					t.Errorf("request body = %v, want %v", body, want)
				}
				if tc.body == "" {
					conn, _, err := http.NewResponseController(w).Hijack()
					if err != nil {
						t.Error(err)
					} else {
						conn.Close()
					}
					return true
				}
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				io.WriteString(w, tc.body)
				return true
			})
			sdk, err := channel.New("synthetic-im-summary-app", "synthetic-secret", channel.WithDomain(f.url),
				channel.WithReqTimeout(3*time.Second), channel.WithLogLevel(larkcore.LogLevelError))
			if err != nil {
				t.Fatal(err)
			}
			l := &Lark{Channel: sdk, Config: Config{AppID: "synthetic-im-summary-app"}}
			id, err := l.SendMeetingSummary(t.Context(), receiverType, recipient, uuid, summary)
			if tc.success {
				if id != messageID || err != nil {
					t.Fatalf("result = %q, %v; want %q, nil", id, err, messageID)
				}
			} else if tc.rejected {
				var rejection *meetingRejection
				if id != "" || !errors.As(err, &rejection) || err.Error() != "飞书拒绝发送会后纪要，错误码 230002" {
					t.Fatalf("result = %q, %v; want safe typed rejection", id, err)
				}
			} else if id != "" || err != errMeetingUnconfirmed {
				t.Fatalf("result = %q, %v; want %v", id, err, errMeetingUnconfirmed)
			}
		})
	}
}

func TestOfficialLarkSDKMeetingSummaryRejectsInvalidInput(t *testing.T) {
	l := &Lark{}
	for _, args := range [][3]string{
		{"user_id", "synthetic", "synthetic"},
		{"chat_id", "", "synthetic"},
		{"open_id", "synthetic", " "},
	} {
		id, err := l.SendMeetingSummary(t.Context(), args[0], args[1], args[2], "synthetic")
		var rejection *meetingRejection
		if id != "" || !errors.As(err, &rejection) {
			t.Fatalf("result = %q, %v; want typed input rejection", id, err)
		}
	}
}
