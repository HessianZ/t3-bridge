package bridge

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	vc "github.com/larksuite/oapi-sdk-go/v3/service/vc/v1"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestLocalMeetingRequestValidation(t *testing.T) {
	const number = "123456789"
	revision := strings.Repeat("ab", 16)
	for _, request := range []LocalMeetingRequest{
		{Action: "status"}, {Action: "join", Number: number},
		{Action: "leave", Number: number, Revision: revision}, {Action: "confirm-left", Number: number, Revision: revision},
	} {
		if err := request.Validate(); err != nil {
			t.Fatalf("valid request %+v: %v", request, err)
		}
	}
	for _, request := range []LocalMeetingRequest{
		{}, {Action: "status", Number: number}, {Action: "status", Revision: revision},
		{Action: "join", Number: "123 456 789"}, {Action: "join", Number: number, Revision: revision},
		{Action: "leave", Number: number}, {Action: "leave", Number: number, Revision: strings.ToUpper(revision)},
		{Action: "confirm-left", Number: number, Revision: strings.Repeat("g", 32)},
	} {
		if err := request.Validate(); err == nil {
			t.Fatalf("invalid request accepted: %+v", request)
		}
	}
}

func localMeetingCall(t *testing.T, e *Engine, request LocalMeetingRequest, want string) LocalMeetingView {
	t.Helper()
	view, err := e.LocalMeeting(t.Context(), request)
	if err != nil || view.Outcome != want {
		t.Fatalf("%s: got %+v, %v, want %q", request.Action, view, err, want)
	}
	return view
}

func TestLocalMeetingDurableTransitionsAndOrigins(t *testing.T) {
	const number = "123456789"
	const chatNumber = "223456789"
	const inviteNumber = "323456789"
	const chatID = "7628568141510692382"
	e, _, f := meetingFixture(t)
	status := LocalMeetingRequest{Action: "status"}
	join := LocalMeetingRequest{Action: "join", Number: number}
	leave := LocalMeetingRequest{Action: "leave", Number: number}
	confirm := LocalMeetingRequest{Action: "confirm-left", Number: number}
	if _, err := e.LocalMeeting(t.Context(), status); err == nil {
		t.Fatal("unbound store accepted local request")
	}
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	view := localMeetingCall(t, e, join, "unavailable")
	if view.Enabled || !view.Available || view.Meetings == nil || len(f.joins) != 0 {
		t.Fatalf("disabled meeting snapshot: %+v, calls %v", view, f.joins)
	}
	e.Config.MeetingEnabled = true
	chat := meetingMessage("chat-owner", "/meeting status")
	chatReceipt := meetingRecord{Phase: meetingJoinAccepted, ID: chatID, Scope: userScope(chat), Actor: chat.UserID, Origin: chat.ChatID}
	if err := e.Store.update(func(st *State) error {
		e.Store.setMeetingRecord(e.Config, st, chatNumber, meetingRecord{}, chatReceipt)
		st.Meetings[inviteNumber] = meetingRecord{Phase: meetingJoinRejected, Inviter: "ou_inviter"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	foreign := *e
	foreign.Config.AppID = "another-app"
	if _, err := foreign.LocalMeeting(t.Context(), status); err == nil {
		t.Fatal("foreign account could read the selected store")
	}
	view = localMeetingCall(t, e, status, "")
	if len(view.Meetings) != 2 || view.Meetings[0].Origin != "chat" || view.Meetings[1].Origin != "invite" {
		t.Fatalf("wrong account receipts: %+v", view.Meetings)
	}
	encoded, err := json.Marshal(view)
	if err != nil || strings.Contains(string(encoded), chatID) || strings.Contains(string(encoded), "ou_inviter") {
		t.Fatalf("receipt leaks SDK or actor data: %s, %v", encoded, err)
	}
	f.onJoin = func() {
		data, err := os.ReadFile(filepath.Join(e.Config.StateDir, "state.json"))
		if err != nil {
			t.Fatal(err)
		}
		var st State
		if err := json.Unmarshal(data, &st); err != nil {
			t.Fatal(err)
		}
		pending := st.Meetings[f.joins[len(f.joins)-1]]
		if !pending.Local || pending.LocalAttempt == "" || pending.Phase != meetingJoinPending ||
			pending.ID != "" || pending.Actor != "" || pending.Origin != "" || pending.Scope != "" || pending.Inviter != "" || len(st.Commands) != 0 {
			t.Fatalf("join called without a durable actor-free pending receipt: %+v", pending)
		}
	}
	f.joinErr = &meetingRejection{Reason: "synthetic private SDK rejection"}
	view = localMeetingCall(t, e, LocalMeetingRequest{Action: "join", Number: inviteNumber}, "rejected")
	if view.Meetings[1].Origin != "invite" || view.Meetings[1].Phase != meetingJoinRejected {
		t.Fatalf("known rejection changed the original invite: %+v", view.Meetings)
	}
	f.joinErr = errors.New("synthetic lost join response")
	view = localMeetingCall(t, e, join, "pending")
	pending := view.Meetings[0]
	if pending.Number != number || pending.Origin != "local" || pending.Phase != meetingJoinPending {
		t.Fatalf("wrong local receipt: %+v", pending)
	}
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	e.Store = store
	t.Cleanup(func() { store.Close() })
	view = localMeetingCall(t, e, join, "pending")
	if view.Meetings[0] != pending || len(f.joins) != 2 {
		t.Fatal("restart changed the pending receipt or replayed the SDK join")
	}
	confirm.Revision = localMeetingRevision("another-app", number, e.Store.snapshot().Meetings[number])
	localMeetingCall(t, e, confirm, "conflict")
	confirm.Revision = pending.Revision
	localMeetingCall(t, e, confirm, "confirmed")
	localMeetingCall(t, e, confirm, "conflict")
	localMeetingCall(t, e, join, "pending")
	localMeetingCall(t, e, confirm, "conflict")
	confirm.Revision = localMeetingCall(t, e, status, "").Meetings[0].Revision
	localMeetingCall(t, e, confirm, "confirmed")

	f.joinErr = nil
	view = localMeetingCall(t, e, join, "joined")
	accepted := view.Meetings[0]
	leave.Revision, confirm.Revision = accepted.Revision, accepted.Revision
	meetingReply(t, e, f, chat, chatNumber)
	if strings.Contains(f.replies[len(f.replies)-1], number) {
		t.Fatal("remote status exposed the locally initiated receipt")
	}
	meetingReply(t, e, f, meetingMessage("remote-leave-local", "/meeting leave "+number), "无法在当前私聊管理")
	if len(f.leaves) != 0 || e.Store.snapshot().Meetings[chatNumber] != chatReceipt {
		t.Fatal("local join changed remote ownership")
	}
	localID := e.Store.snapshot().Meetings[number].ID
	f.onLeave = func() {
		pending := e.Store.snapshot().Meetings[number]
		if pending.Phase != meetingLeavePending || !pending.Local || f.leaves[len(f.leaves)-1] != localID {
			t.Fatalf("leave called without pending receipt and original long ID: %+v, %v", pending, f.leaves)
		}
	}
	e.Config.MeetingEnabled = false
	f.leaveErr = &meetingRejection{Reason: "synthetic leave rejection"}
	view = localMeetingCall(t, e, leave, "rejected")
	if view.Meetings[0] != accepted {
		t.Fatal("definite leave rejection did not restore the accepted receipt")
	}
	f.leaveErr = errors.New("synthetic lost leave response")
	view = localMeetingCall(t, e, leave, "pending")
	localMeetingCall(t, e, leave, "conflict")
	leave.Revision = view.Meetings[0].Revision
	localMeetingCall(t, e, leave, "conflict")
	confirm.Revision = leave.Revision
	localMeetingCall(t, e, confirm, "confirmed")
	f.onLeave, f.leaveErr = nil, nil
	leave.Number, leave.Revision = chatNumber, view.Meetings[1].Revision
	localMeetingCall(t, e, leave, "left")
	if len(f.joins) != 4 || len(f.leaves) != 3 || f.leaves[2] != chatID {
		t.Fatalf("unexpected SDK calls: joins=%v leaves=%v", f.joins, f.leaves)
	}
}

func TestLocalMeetingEndWinsAndSkipsSummaryWithoutActor(t *testing.T) {
	const number = "123456789"
	e, _, f := meetingFixture(t)
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	e.Config.MeetingEnabled, e.Config.MeetingSummaryTarget = true, "owner"
	join := LocalMeetingRequest{Action: "join", Number: number}
	end := func(id string) {
		lark := &Lark{Store: e.Store, Config: e.Config}
		event := &vc.P2BotMeetingEndedV1{
			EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{AppID: e.Config.AppID}},
			Event:       &vc.P2BotMeetingEndedV1Data{Meeting: &vc.MeetingAgentEventMeeting{Id: &id}},
		}
		if err := lark.receiveMeetingEnded(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	initial := localMeetingCall(t, e, join, "joined").Meetings[0]
	longID := e.Store.snapshot().Meetings[number].ID
	end(longID)
	if st := e.Store.snapshot(); len(st.Meetings) != 0 || len(st.Inbox) != 0 {
		t.Fatal("local meeting end retained the receipt or queued an actorless summary")
	}
	view := localMeetingCall(t, e, join, "joined")
	f.onLeave = func() { end(longID) }
	f.leaveErr = &meetingRejection{Reason: "synthetic late leave rejection"}
	view = localMeetingCall(t, e, LocalMeetingRequest{Action: "leave", Number: number, Revision: view.Meetings[0].Revision}, "conflict")
	if len(view.Meetings) != 0 || len(f.leaves) != 1 {
		t.Fatalf("late result resurrected a closed receipt: %+v", view)
	}
	// An end event without a meeting number cannot match the pending receipt.
	f.onJoin = func() { end(longID) }
	accepted := localMeetingCall(t, e, join, "joined").Meetings[0]
	confirm := LocalMeetingRequest{Action: "confirm-left", Number: number, Revision: initial.Revision}
	localMeetingCall(t, e, confirm, "conflict")
	confirm.Revision = accepted.Revision
	view = localMeetingCall(t, e, confirm, "confirmed")
	if len(view.Meetings) != 0 || len(f.leaves) != 1 || len(e.Store.snapshot().Inbox) != 0 {
		t.Fatalf("operator confirmation did not just close the locally accepted receipt: %+v", view)
	}
}

func TestLocalMeetingEndBeforeJoinResponseClosesReceipt(t *testing.T) {
	e, _, f := meetingFixture(t)
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	e.Config.MeetingEnabled, e.Config.MeetingSummaryTarget = true, "owner"
	number, id := "123456789", "7628568141510692381"
	lark := &Lark{Store: e.Store, Config: e.Config}
	event := &vc.P2BotMeetingEndedV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{AppID: e.Config.AppID}},
		Event:       &vc.P2BotMeetingEndedV1Data{Meeting: &vc.MeetingAgentEventMeeting{Id: &id, MeetingNo: &number}},
	}
	end := func() {
		if err := lark.receiveMeetingEnded(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	f.onJoin = end
	view := localMeetingCall(t, e, LocalMeetingRequest{Action: "join", Number: number}, "ended")
	if len(view.Meetings) != 0 || len(f.joins) != 1 {
		t.Fatalf("early end left a receipt or replayed join: %+v, joins %v", view, f.joins)
	}
	end()
	if st := e.Store.snapshot(); len(st.Meetings) != 0 || len(st.Inbox) != 0 {
		t.Fatal("duplicate meeting end reopened the receipt or queued an actorless summary")
	}
}

func TestLocalMeetingPublicationAmbiguity(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failWrite int
		published bool
		outcome   string
		phase     string
		joins     int
	}{
		{"pending receipt not published", 1, false, "unavailable", "", 0},
		{"pending receipt sync failed", 1, true, "pending", meetingJoinPending, 0},
		{"result not published", 2, false, "pending", meetingJoinPending, 1},
		{"result sync failed", 2, true, "pending", meetingJoinAccepted, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _, f := meetingFixture(t)
			e.Config.MeetingEnabled = true
			if err := e.Store.BindAccount(e.Config.AppID); err != nil {
				t.Fatal(err)
			}
			writes := 0
			e.Store.writeState = func(path string, data []byte, createOnly bool) (bool, error) {
				writes++
				if writes != tc.failWrite {
					return localfile.WritePrivateAtomic(path, data, createOnly)
				}
				if tc.published {
					if published, err := localfile.WritePrivateAtomic(path, data, createOnly); !published || err != nil {
						t.Fatalf("synthetic publication setup: published=%v, err=%v", published, err)
					}
				}
				return tc.published, errors.New("synthetic private publication error")
			}
			join := LocalMeetingRequest{Action: "join", Number: "123456789"}
			localMeetingCall(t, e, join, tc.outcome)
			if phase := e.Store.snapshot().Meetings[join.Number].Phase; phase != tc.phase || len(f.joins) != tc.joins {
				t.Fatalf("publication failure produced phase %q and SDK calls %v", phase, f.joins)
			}
			if tc.phase != "" {
				outcome := "pending"
				if tc.phase == meetingJoinAccepted {
					outcome = "conflict"
				}
				localMeetingCall(t, e, join, outcome)
				if writes != tc.failWrite || len(f.joins) != tc.joins {
					t.Fatal("retry replayed a persisted meeting operation")
				}
			}
		})
	}
}
