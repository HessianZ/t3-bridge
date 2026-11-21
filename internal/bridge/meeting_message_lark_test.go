package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	channel "github.com/larksuite/channel-sdk-go"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

const sdkMeetingMessageUUID = "7f3b7fd2-1b64-4f5d-9e8b-2f2d8d3b0c11"

func newMeetingMessageSDKLoopback(t *testing.T, handle http.HandlerFunc) *Lark {
	t.Helper()
	f := newLoopbackLark(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/open-apis/vc/v1/bots/message" {
			return false
		}
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer synthetic-feishu-token" {
			t.Error("unexpected VC method or authorization")
		}
		handle(w, r)
		return true
	})
	sdk, err := channel.New("synthetic-vc-message-app", "synthetic-secret", channel.WithDomain(f.url),
		channel.WithReqTimeout(3*time.Second), channel.WithLogLevel(larkcore.LogLevelError))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sdk.Stop(context.Background()) })
	return &Lark{Channel: sdk}
}

func TestOfficialLarkSDKMeetingTextRequest(t *testing.T) {
	const message = " 合成正文\n第二行 \"引号\" "
	want := map[string]string{"meeting_id": sdkMeetingID, "msg_type": "text", "uuid": sdkMeetingMessageUUID, "content": message}
	var calls atomic.Int32
	l := newMeetingMessageSDKLoopback(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		} else if !reflect.DeepEqual(body, want) {
			t.Errorf("message body = %v, want %v", body, want)
		}
		io.WriteString(w, `{"code":0,"data":{"uuid":"`+sdkMeetingMessageUUID+`"}}`)
	})
	for range 2 {
		if err := l.SendMeetingText(context.Background(), sdkMeetingID, sdkMeetingMessageUUID, message); err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("message calls = %d, want 2", got)
	}
}

func TestOfficialLarkSDKMeetingTextResults(t *testing.T) {
	const message = "synthetic-private-text"
	const data = `"data":{"uuid":"` + sdkMeetingMessageUUID + `"}`
	const confirmed = `{"code":0,` + data + `}`
	const rejected = `{"code":20017,"msg":"` + message + ` synthetic-private-response",` + data + `}`
	for _, tc := range []struct {
		name, body string
		status     int
		rejected   bool
	}{
		{name: "missing data", status: http.StatusOK, body: `{"code":0}`},
		{name: "missing UUID", status: http.StatusOK, body: `{"code":0,"data":{}}`},
		{name: "null UUID", status: http.StatusOK, body: `{"code":0,"data":{"uuid":null}}`},
		{name: "drifted UUID", status: http.StatusOK, body: `{"code":0,"data":{"uuid":"synthetic-private-response"}}`},
		{name: "missing code", status: http.StatusOK, body: `{` + data + `}`},
		{name: "null code", status: http.StatusOK, body: `{"code":null,` + data + `}`},
		{name: "malformed response", status: http.StatusOK, body: `{"code":0,"msg":"synthetic-private-response"`},
		{name: "success body on HTTP failure", status: http.StatusForbidden, body: confirmed},
		{name: "business rejection", status: http.StatusOK, body: rejected, rejected: true},
		{name: "client error with rejection", status: http.StatusForbidden, body: rejected, rejected: true},
		{name: "server error with rejection", status: http.StatusBadGateway, body: rejected},
		{name: "lost response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newMeetingMessageSDKLoopback(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.status == 0 {
					conn, _, err := http.NewResponseController(w).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					conn.Close()
					return
				}
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			err := l.SendMeetingText(context.Background(), sdkMeetingID, sdkMeetingMessageUUID, message)
			if tc.rejected {
				var rejection *meetingRejection
				if !errors.As(err, &rejection) || err.Error() != "飞书拒绝发送会议消息" {
					t.Fatalf("error = %v, want fixed typed rejection", err)
				}
			} else if err != errMeetingUnconfirmed {
				t.Fatalf("error = %v, want %v", err, errMeetingUnconfirmed)
			}
		})
	}
}

func TestOfficialLarkSDKMeetingTextRequiresIDs(t *testing.T) {
	l := newMeetingMessageSDKLoopback(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("unexpected message request with missing ID")
		w.WriteHeader(http.StatusBadRequest)
	})
	for _, ids := range [][2]string{{"", sdkMeetingMessageUUID}, {sdkMeetingID, " "}} {
		err := l.SendMeetingText(context.Background(), ids[0], ids[1], "synthetic-private-text")
		if err == nil || err.Error() != "发送会议消息需要会议 ID 和 UUID" {
			t.Fatalf("error = %v, want fixed input error", err)
		}
	}
}
