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

const (
	sdkMeetingJoinPath  = "/open-apis/vc/v1/bots/join"
	sdkMeetingLeavePath = "/open-apis/vc/v1/bots/leave"
	sdkMeetingNumber    = "012345678"
	sdkMeetingID        = "9876543210987654321"
)

func newMeetingSDKLoopback(t *testing.T, handle func(http.ResponseWriter, *http.Request)) *Lark {
	t.Helper()
	f := newLoopbackLark(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != sdkMeetingJoinPath && r.URL.Path != sdkMeetingLeavePath {
			return false
		}
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer synthetic-feishu-token" {
			t.Error("unexpected VC method or authorization")
		}
		handle(w, r)
		return true
	})
	sdk, err := channel.New("synthetic-vc-app", "synthetic-secret", channel.WithDomain(f.url),
		channel.WithReqTimeout(3*time.Second), channel.WithLogLevel(larkcore.LogLevelError))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sdk.Stop(context.Background()) })
	return &Lark{Channel: sdk}
}

func TestOfficialLarkSDKMeetingRequests(t *testing.T) {
	var joined, left atomic.Int32
	l := newMeetingSDKLoopback(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, "invalid synthetic request", http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case sdkMeetingJoinPath:
			joined.Add(1)
			want := map[string]any{"join_type": float64(1), "join_identify": map[string]any{"meeting_no": sdkMeetingNumber}}
			if !reflect.DeepEqual(body, want) {
				t.Errorf("join body = %v, want %v", body, want)
			}
			io.WriteString(w, `{"code":0,"data":{"meeting":{"id":"`+sdkMeetingID+`","topic":"synthetic-private-title"}}}`)
		case sdkMeetingLeavePath:
			left.Add(1)
			want := map[string]any{"meeting_id": sdkMeetingID}
			if !reflect.DeepEqual(body, want) {
				t.Errorf("leave body = %v, want %v", body, want)
			}
			io.WriteString(w, `{"code":0}`)
		}
	})
	id, err := l.JoinMeeting(context.Background(), sdkMeetingNumber)
	if err != nil || id != sdkMeetingID {
		t.Fatalf("join returned ID %q, error %v", id, err)
	}
	if err := l.LeaveMeeting(context.Background(), id); err != nil {
		t.Fatalf("leave returned %v", err)
	}
	if joined.Load() != 1 || left.Load() != 1 {
		t.Fatalf("requests = %d join, %d leave", joined.Load(), left.Load())
	}
}

func TestOfficialLarkSDKMeetingResults(t *testing.T) {
	const rejected = `{"code":20017,"msg":"synthetic-secret and private-title"}`
	for _, tc := range []struct {
		name, body string
		status     int
		rejected   bool
		leaveOK    bool
	}{
		{name: "business rejection HTTP 200", status: 200, body: rejected, rejected: true},
		{name: "business rejection HTTP 403", status: 403, body: rejected, rejected: true},
		{name: "bare HTTP 403", status: 403, body: `{"msg":"synthetic-secret"}`},
		{name: "HTTP 403 with zero code", status: 403, body: `{"code":0}`},
		{name: "server error with rejection code", status: 500, body: rejected},
		{name: "server error with success code", status: 503, body: `{"code":0,"data":{"meeting":{"id":"` + sdkMeetingID + `"}}}`},
		{name: "gateway timeout", status: 504, body: rejected},
		{name: "no business code with meeting ID", status: 200, body: `{"data":{"meeting":{"id":"` + sdkMeetingID + `"}}}`},
		{name: "null business code", status: 200, body: `{"code":null}`},
		{name: "string business code", status: 200, body: `{"code":"20017"}`},
		{name: "malformed response", status: 200, body: `{"code":20017,"msg":"synthetic-secret"`},
		{name: "no join data", status: 200, body: `{"code":0}`, leaveOK: true},
		{name: "missing meeting ID", status: 200, body: `{"code":0,"data":{"meeting":{}}}`, leaveOK: true},
		{name: "blank meeting ID", status: 200, body: `{"code":0,"data":{"meeting":{"id":" "}}}`, leaveOK: true},
		{name: "lost response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newMeetingSDKLoopback(t, func(w http.ResponseWriter, r *http.Request) {
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
			id, joinErr := l.JoinMeeting(context.Background(), sdkMeetingNumber)
			if id != "" {
				t.Fatalf("unconfirmed join returned ID %q", id)
			}
			leaveErr := l.LeaveMeeting(context.Background(), sdkMeetingID)
			for _, result := range []struct {
				action string
				err    error
				ok     bool
			}{{"入会", joinErr, false}, {"离会", leaveErr, tc.leaveOK}} {
				var rejection *meetingRejection
				switch {
				case tc.rejected:
					want := "飞书拒绝" + result.action + "，错误码 20017"
					if !errors.As(result.err, &rejection) || rejection.Reason != want || result.err.Error() != want {
						t.Errorf("%s error = %v, want typed rejection %q", result.action, result.err, want)
					}
				case result.ok:
					if result.err != nil {
						t.Errorf("%s error = %v, want nil", result.action, result.err)
					}
				default:
					if result.err != errMeetingUnconfirmed {
						t.Errorf("%s error = %v, want %v", result.action, result.err, errMeetingUnconfirmed)
					}
				}
			}
		})
	}
}
