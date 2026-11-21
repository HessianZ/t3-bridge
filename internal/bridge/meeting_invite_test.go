package bridge

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type fakeInvitingMessenger struct {
	*fakeMeetingMessenger
	inviteJoins []string
	joinedID    string
	inviteError error
}

func (f *fakeInvitingMessenger) JoinInvitedMeeting(_ context.Context, number, call string) (string, error) {
	f.inviteJoins = append(f.inviteJoins, number+"/"+call)
	if f.onJoin != nil {
		f.onJoin()
	}
	return f.joinedID, f.inviteError
}

func meetingInviteFixture(t *testing.T) (*Engine, *fakeInvitingMessenger) {
	t.Helper()
	e, _, meetings := meetingFixture(t)
	e.Config.MeetingEnabled = true
	e.Config.MeetingAutoJoinUsers = []string{"user", "other"}
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	f := &fakeInvitingMessenger{fakeMeetingMessenger: meetings, joinedID: sdkMeetingID}
	e.Lark = f
	return e, f
}

func TestMeetingAutoJoinThroughLongConnection(t *testing.T) {
	e, f := meetingInviteFixture(t)
	// This admin is not on the allowlist. The listed "other" is not an admin.
	e.Config.Admins = append(e.Config.Admins, "unlisted")
	loop := newLoopbackLark(t)
	l, err := loop.channel(e.Config, e.Store)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()
	exit := make(chan error, 1)
	go func() { exit <- l.Channel.Start(ctx) }()
	defer l.Channel.Stop(context.Background())
	var peer *websocket.Conn
	select {
	case peer = <-loop.peers:
	case err := <-exit:
		t.Fatalf("receiver exited before handshake: %v", err)
	case <-ctx.Done():
		t.Fatal("receiver did not connect")
	}
	defer peer.Close()
	emit := func(eventID, app, bot, inviter, call string) {
		t.Helper()
		payload := fmt.Sprintf(`{"meeting":{"id":%q,"meeting_no":%q},"bot":{"id":%q},"inviter":{"id":%q},"call_id":%q}`,
			sdkMeetingID, sdkMeetingNumber, bot, inviter, call)
		sendMeetingEvent(t, peer, app, eventID, "vc.bot.meeting_invited_v1", payload)
	}
	emit("wrong-app", "wrong-app", "synthetic-bot", "user", "incorrect-app")
	emit("wrong-bot", e.Config.AppID, "wrong-bot", "user", "incorrect-bot")
	emit("member", e.Config.AppID, "synthetic-bot", "other", "not-admin")
	emit("admin", e.Config.AppID, "synthetic-bot", "unlisted", "not-listed")
	if got := len(e.Store.snapshot().Inbox); got != 0 {
		t.Fatalf("accepted %d ineligible invitations", got)
	}
	emit("accepted", e.Config.AppID, "synthetic-bot", "user", sdkInviteCallID)
	emit("duplicate-call", e.Config.AppID, "synthetic-bot", "user", sdkInviteCallID)
	emit("accepted", e.Config.AppID, "synthetic-bot", "user", "duplicate-event")
	if got := len(e.Store.snapshot().Inbox); got != 1 {
		t.Fatalf("durable inbox size = %d, want 1", got)
	}
	f.onJoin = func() {
		st := e.Store.snapshot()
		if st.Meetings[sdkMeetingNumber].Phase != meetingJoinPending || len(st.Inbox) != 0 {
			t.Fatal("remote attempt ran before the pending receipt replaced the queued call")
		}
	}
	tick(t, e)
	if !slices.Equal(f.inviteJoins, []string{sdkMeetingNumber + "/" + sdkInviteCallID}) || len(f.replies) != 0 {
		t.Fatal("invitation replayed the call or sent an unsolicited private reply")
	}
	if record := e.Store.snapshot().Meetings[sdkMeetingNumber]; record.Inviter != "user" || record.Scope != "" || record.Phase != meetingJoinAccepted {
		t.Fatalf("unexpected accepted receipt: %+v", record)
	}
	e.Config.Admins = append(e.Config.Admins, "other")
	outsider := meetingMessage("outsider", "/meeting leave "+sdkMeetingNumber)
	outsider.UserID = "other"
	meetingReply(t, e, f.fakeMeetingMessenger, outsider, "无法在当前私聊管理")
	meetingReply(t, e, f.fakeMeetingMessenger, meetingMessage("status", "/meeting status"), "飞书已受理入会 · 邀请")
	meetingReply(t, e, f.fakeMeetingMessenger, meetingMessage("leave", "/meeting leave "+sdkMeetingNumber), "已确认离会")
	if !slices.Equal(f.leaves, []string{sdkMeetingID}) {
		t.Fatalf("leave used the wrong ID: %v", f.leaves)
	}
}

func TestMeetingAutoJoinReplayAndOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, stage, joined string
		failure             error
	}{
		{"unknown", meetingJoinPending, "", errMeetingUnconfirmed},
		{"different meeting", meetingJoinPending, "7628568141510692382", nil},
		{"rejected", meetingJoinRejected, "", &meetingRejection{Reason: "飞书拒绝入会，错误码 20017"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, f := meetingInviteFixture(t)
			f.joinedID, f.inviteError = tc.joined, tc.failure
			now := e.now()
			e.Now = func() time.Time { return now }
			invite := meetingInviteEvent{AppID: e.Config.AppID, InviterID: "user", MeetingNumber: sdkMeetingNumber,
				MeetingID: sdkMeetingID, EventID: "original", CallID: sdkInviteCallID}
			queue := func() {
				t.Helper()
				if err := e.Store.acceptMeetingInvite(invite, now); err != nil {
					t.Fatal(err)
				}
			}
			reopen := func() {
				t.Helper()
				if err := e.Store.Close(); err != nil {
					t.Fatal(err)
				}
				store, err := OpenStore(e.Config.StateDir)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { store.Close() })
				e.Store = store
			}
			queue()
			reopen()
			tick(t, e)
			reopen()
			invite.EventID = "retry"
			queue()
			tick(t, e)
			st := e.Store.snapshot()
			if record := st.Meetings[sdkMeetingNumber]; record.Phase != tc.stage || record.ID != sdkMeetingID || len(st.Inbox) != 0 || len(f.inviteJoins) != 1 {
				t.Fatalf("reopened receipt: %+v, %d queued, %d writes", record, len(st.Inbox), len(f.inviteJoins))
			}
			if tc.stage == meetingJoinRejected {
				if err := e.Store.DrainCheck(e.Config.AppID); err != nil {
					t.Fatal(err)
				}
				meetingReply(t, e, f.fakeMeetingMessenger, meetingMessage("manual", "/meeting join "+sdkMeetingNumber), "受理入会")
			} else if err := e.Store.DrainCheck(e.Config.AppID); err == nil || !strings.Contains(err.Error(), "meeting receipts") {
				t.Fatalf("unknown outcome passed account drain: %v", err)
			}
		})
	}
}

func TestMeetingAutoJoinDropsClosedExpiredOrRevokedInvites(t *testing.T) {
	e, f := meetingInviteFixture(t)
	now := e.now()
	for i, tc := range []struct {
		actor, app string
		sent       time.Time
	}{
		{"user", e.Config.AppID, now.Add(-3 * time.Minute)},
		{"other", e.Config.AppID, now},
		{"user", "wrong-app", now},
		{"user", e.Config.AppID, now},
	} {
		if err := e.Store.acceptMeetingInvite(meetingInviteEvent{AppID: tc.app, InviterID: tc.actor, EventID: fmt.Sprint(i),
			CallID: fmt.Sprint(i), MeetingNumber: sdkMeetingNumber, MeetingID: sdkMeetingID}, tc.sent); err != nil {
			t.Fatal(err)
		}
		if i == 3 {
			if err := e.Store.update(func(st *State) error {
				closeMeetingRoom(st, sdkMeetingID)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		tick(t, e)
	}
	if state := e.Store.snapshot(); len(state.Inbox) != 0 || len(state.Meetings) != 0 || len(f.inviteJoins) != 0 {
		t.Fatal("ineligible invitations reached the remote join attempt")
	}
}
