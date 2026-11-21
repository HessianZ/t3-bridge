package bridge

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	vc "github.com/larksuite/oapi-sdk-go/v3/service/vc/v1"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestManualMeetingJoinWithEarlyEnd(t *testing.T) {
	const number = "123456789"
	const longID = "7628568141510692381" // Successful response from fakeMeetingMessenger.
	for _, tc := range []struct {
		name, endNumber, endID string
		foreignApp, lostReply  bool
		wantPhase, wantReply   string
	}{
		{name: "matching end", endNumber: number, endID: longID, wantReply: "会议已结束"},
		{name: "different long ID", endNumber: number, endID: "7628568141510692382", wantPhase: meetingJoinAccepted, wantReply: "受理入会"},
		{name: "foreign app", endNumber: number, endID: longID, foreignApp: true, wantPhase: meetingJoinAccepted, wantReply: "受理入会"},
		{name: "missing meeting number", endID: longID, wantPhase: meetingJoinAccepted, wantReply: "受理入会"},
		{name: "invalid meeting number", endNumber: "123 456 789", endID: longID, wantPhase: meetingJoinAccepted, wantReply: "受理入会"},
		{name: "different meeting number", endNumber: "987654321", endID: longID, wantPhase: meetingJoinAccepted, wantReply: "受理入会"},
		{name: "lost join response", endNumber: number, endID: longID, lostReply: true, wantPhase: meetingJoinPending, wantReply: "入会结果待核对"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _, f := meetingFixture(t)
			e.Config.MeetingEnabled, e.Config.MeetingRoute, e.Config.MeetingSummaryTarget = true, "mac", "origin"
			if err := e.Store.BindAccount(e.Config.AppID); err != nil {
				t.Fatal(err)
			}
			m := meetingMessage("early-end-join", "/meeting join "+number)
			commandID := key(m, m.ID)
			lark := &Lark{Config: e.Config, Store: e.Store}
			app := e.Config.AppID
			if tc.foreignApp {
				app = "synthetic-other-app"
			}
			ended := &vc.MeetingAgentEventMeeting{Id: &tc.endID}
			if tc.endNumber != "" {
				ended.MeetingNo = &tc.endNumber
			}
			end := func(eventID string) {
				t.Helper()
				event := &vc.P2BotMeetingEndedV1{
					EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{AppID: app, EventID: eventID}},
					Event:       &vc.P2BotMeetingEndedV1Data{Meeting: ended},
				}
				if err := lark.receiveMeetingEnded(t.Context(), event); err != nil {
					t.Fatal(err)
				}
			}
			if tc.lostReply {
				f.joinErr = errors.New("synthetic lost VC response")
			}
			closed := false
			f.onJoin = func() {
				before, err := readStateSnapshot(e.Config.StateDir)
				if err != nil {
					t.Fatal(err)
				}
				pending, cmd := before.Meetings[number], before.Commands[commandID]
				if pending.Phase != meetingJoinPending || pending.ID != "" || cmd == nil || !strings.Contains(cmd.Reply, "待核对") {
					t.Fatalf("join started without its original pending receipts: meeting=%+v command=%+v", pending, cmd)
				}
				end("early-end")
				after := e.Store.snapshot()
				if current := after.Commands[commandID]; after.Meetings[number] != pending || current == nil || current.Reply != cmd.Reply || len(after.Inbox) != 1 {
					t.Fatalf("end changed the pending join or queued a summary before the response: meeting=%+v inbox=%d", after.Meetings[number], len(after.Inbox))
				}
				if tc.wantPhase == "" {
					// Inspect the first publication that can close the pending receipt.
					e.Store.writeState = func(path string, data []byte, createOnly bool) (bool, error) {
						if !closed {
							var st State
							if err := json.Unmarshal(data, &st); err != nil {
								t.Fatal(err)
							}
							if record, exists := st.Meetings[number]; !exists {
								closed = true
								queued := slices.ContainsFunc(st.Inbox, func(m Message) bool { return m.MeetingSummary != nil })
								if cmd := st.Commands[commandID]; cmd == nil || !strings.Contains(cmd.Reply, tc.wantReply) || !queued {
									t.Fatal("closure was published separately from the original reply or queued summary")
								}
							} else if record != pending {
								t.Fatalf("published an intermediate meeting receipt: %+v", record)
							}
						}
						return localfile.WritePrivateAtomic(path, data, createOnly)
					}
				}
			}

			meetingReply(t, e, f, m, tc.wantReply)
			st := e.Store.snapshot()
			if tc.wantPhase == "" {
				if _, exists := st.Meetings[number]; exists || !closed || len(st.Inbox) != 1 {
					t.Fatalf("matching end did not close the receipt and queue one summary: meeting=%+v inbox=%d", st.Meetings[number], len(st.Inbox))
				}
				summary := st.Inbox[0]
				if summary.Meeting == nil || summary.Meeting.ID != longID || summary.Meeting.Number != number ||
					summary.MeetingSummary == nil || summary.ChatID != m.ChatID || summary.UserID != m.UserID ||
					summary.MeetingSummary.ReceiverType != "chat_id" || summary.MeetingSummary.Recipient != m.ChatID ||
					!e.Config.meetingSummaryAllowed(summary, summary.RouteAtReceive) {
					t.Fatalf("unexpected summary for the closed meeting: %+v", summary)
				}
				end("early-end")
				end("early-end-redelivered")
				if count := len(e.Store.snapshot().Inbox); count != 1 {
					t.Fatalf("duplicate end changed the summary queue: inbox=%d", count)
				}
			} else if record := st.Meetings[number]; record.Phase != tc.wantPhase || len(st.Inbox) != 0 ||
				(tc.lostReply && record.ID != "") || (!tc.lostReply && record.ID != longID) {
				t.Fatalf("unconfirmed end changed the join outcome: meeting=%+v inbox=%d", record, len(st.Inbox))
			}

			accept(t, e, m)
			tick(t, e)
			if len(f.joins) != 1 || f.joins[0] != number || len(f.replies) != 1 {
				t.Fatalf("join replay issued another request or reply: joins=%v replies=%v", f.joins, f.replies)
			}
		})
	}
}
