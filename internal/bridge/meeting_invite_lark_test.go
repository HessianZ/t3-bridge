package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	channel "github.com/larksuite/channel-sdk-go"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	vc "github.com/larksuite/oapi-sdk-go/v3/service/vc/v1"
)

const (
	sdkInviteAppID  = "cli_synthetic_invite"
	sdkInviteCallID = "7C9E6679-7425-40de-944b-e07fc1f90ae7"
	sdkInviteEvent  = `{"meeting":{"meeting_no":"` + sdkMeetingNumber + `","id":"` + sdkMeetingID + `","host_user":{"id":"ou_host"}},
		"bot":{"id":"ou_bot"},"inviter":{"id":"ou_inviter","open_id":"ou_alias"},"operator":{"id":"ou_operator"},
		"call_id":"` + sdkInviteCallID + `"}`
)

func TestMeetingInvitedThroughOfficialWebSocket(t *testing.T) {
	transport := newLoopbackLark(t)
	sdk, err := channel.New(sdkInviteAppID, "synthetic-secret", channel.WithDomain(transport.url),
		channel.WithLogLevel(larkcore.LogLevelError))
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan *meetingInviteEvent, 1)
	sdk.OnMeetingInvited(func(_ context.Context, event *vc.P2BotMeetingInvitedV1) error {
		got <- normalizeMeetingInvite(event, sdkInviteAppID)
		return nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()
	exit := make(chan error, 1)
	go func() { exit <- sdk.Start(ctx) }()
	defer sdk.Stop(context.Background())
	var peer *websocket.Conn
	select {
	case peer = <-transport.peers:
	case err := <-exit:
		t.Fatalf("receiver exited before handshake: %v", err)
	case <-ctx.Done():
		t.Fatal("receiver did not connect")
	}
	defer peer.Close()
	nestedIDs := strings.NewReplacer(
		`"id":"ou_bot"`, `"id":{"open_id":"ou_bot","user_id":"synthetic-bot-user-id"}`,
		`"id":"ou_inviter"`, `"id":{"open_id":"ou_inviter","user_id":"synthetic-inviter-user-id"}`,
		`"id":"ou_host"`, `"id":{"open_id":"ou_host"}`,
	).Replace(sdkInviteEvent)
	for _, tc := range []struct {
		eventID, data string
		accepted      bool
	}{
		{"evt-string-ids", sdkInviteEvent, true},
		{"evt-nested-ids", nestedIDs, true},
		{"evt-no-open-id", strings.Replace(nestedIDs, `"open_id":"ou_inviter",`, "", 1), false},
	} {
		sendMeetingEvent(t, peer, sdkInviteAppID, tc.eventID, "vc.bot.meeting_invited_v1", tc.data)
		want := meetingInviteEvent{
			AppID: sdkInviteAppID, EventID: tc.eventID, MeetingNumber: sdkMeetingNumber,
			MeetingID: sdkMeetingID, BotID: "ou_bot", InviterID: "ou_inviter", CallID: sdkInviteCallID,
		}
		select {
		case invite := <-got:
			if tc.accepted && (invite == nil || *invite != want) || !tc.accepted && invite != nil {
				t.Fatalf("%s normalized to %+v, want %+v (accepted=%t)", tc.eventID, invite, want, tc.accepted)
			}
		case <-ctx.Done():
			t.Fatal("typed invite handler did not run")
		}
	}
}

func TestNormalizeMeetingInviteRejectsInvalidFields(t *testing.T) {
	if got := normalizeMeetingInvite(nil, sdkInviteAppID); got != nil {
		t.Fatalf("nil SDK event normalized to %+v", got)
	}
	const header = `"header":{"app_id":"` + sdkInviteAppID + `","event_id":"evt-invited"}`
	const payload = `{` + header + `,"event":` + sdkInviteEvent + `}`
	for _, tc := range []struct{ name, old, replacement string }{
		{"missing header", header, `"schema":"2.0"`},
		{"wrong app", sdkInviteAppID, "cli_synthetic_elsewhere"},
		{"missing event ID", "evt-invited", ""},
		{"missing event", sdkInviteEvent, "null"},
		{"short meeting ID", sdkMeetingID, sdkMeetingNumber},
		{"nondigit long meeting ID", sdkMeetingID, "synthetic-long-id"},
		{"invalid meeting number", sdkMeetingNumber, "12345678 "},
		{"empty bot ID", "ou_bot", " "},
		{"inviter alias without id", `"id":"ou_inviter",`, ""},
		{"missing call ID", `"call_id":"` + sdkInviteCallID + `"`, `"callId":"fallback"`},
		{"blank call ID", sdkInviteCallID, " "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var event vc.P2BotMeetingInvitedV1
			if err := json.Unmarshal([]byte(strings.Replace(payload, tc.old, tc.replacement, 1)), &event); err != nil {
				t.Fatal(err)
			}
			if got := normalizeMeetingInvite(&event, sdkInviteAppID); got != nil {
				t.Fatalf("invalid SDK event normalized to %+v", got)
			}
		})
	}
}

func TestJoinInvitedMeetingRequest(t *testing.T) {
	l := newMeetingSDKLoopback(t, func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		want := map[string]any{"join_type": float64(1), "join_identify": map[string]any{"meeting_no": sdkMeetingNumber}, "call_id": sdkInviteCallID}
		if r.URL.Path != sdkMeetingJoinPath || !reflect.DeepEqual(got, want) {
			t.Errorf("join %s body = %v, want %v", r.URL.Path, got, want)
		}
		io.WriteString(w, `{"code":0,"data":{"meeting":{"id":"`+sdkMeetingID+`"}}}`)
	})
	if id, err := l.JoinInvitedMeeting(t.Context(), sdkMeetingNumber, sdkInviteCallID); id != sdkMeetingID || err != nil {
		t.Fatalf("join returned %q, %v", id, err)
	}
}

func TestJoinInvitedMeetingRejectsMissingInviteInputs(t *testing.T) {
	// A nil channel makes any attempted request fail the test.
	l := &Lark{}
	for _, args := range [][2]string{{sdkMeetingNumber, ""}, {sdkMeetingNumber, " "}, {sdkMeetingID, sdkInviteCallID}} {
		if id, err := l.JoinInvitedMeeting(t.Context(), args[0], args[1]); id != "" || err == nil || errors.Is(err, errMeetingUnconfirmed) {
			t.Errorf("invalid join returned %q, %v", id, err)
		}
	}
}

func TestJoinInvitedMeetingResults(t *testing.T) {
	const rejected = `{"code":20017,"msg":"synthetic private server payload"}`
	for _, tc := range []struct {
		name, body string
		status     int
		rejected   bool
	}{
		{"explicit rejection", rejected, http.StatusForbidden, true},
		{"server error", rejected, http.StatusServiceUnavailable, false},
		{"missing code", `{"data":{"meeting":{"id":"` + sdkMeetingID + `"}}}`, http.StatusOK, false},
		{"missing meeting ID", `{"code":0,"data":{"meeting":{}}}`, http.StatusOK, false},
		{"meeting number returned as ID", `{"code":0,"data":{"meeting":{"id":"` + sdkMeetingNumber + `"}}}`, http.StatusOK, false},
		{"malformed meeting ID", `{"code":0,"data":{"meeting":{"id":"synthetic-long-id"}}}`, http.StatusOK, false},
		{"malformed response", `{"code":`, http.StatusOK, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newMeetingSDKLoopback(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			id, err := l.JoinInvitedMeeting(t.Context(), sdkMeetingNumber, sdkInviteCallID)
			if id != "" {
				t.Fatalf("join returned unverified ID %q", id)
			}
			if tc.rejected {
				var rejection *meetingRejection
				if !errors.As(err, &rejection) || rejection.Reason != "飞书拒绝入会，错误码 20017" {
					t.Fatalf("join returned %v, want redacted typed rejection", err)
				}
			} else if !errors.Is(err, errMeetingUnconfirmed) {
				t.Fatalf("join returned %v, want unconfirmed result", err)
			}
		})
	}
}
