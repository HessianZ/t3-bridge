package bridge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	vc "github.com/larksuite/oapi-sdk-go/v3/service/vc/v1"
)

const (
	summaryMeetingNumber = "123456789"
	summaryOrigin        = "oc_synthetic_join"
	summaryOwner         = "ou_synthetic_owner"
	summaryCaption       = "下周复核合成会议事项。"
	summaryResult        = "合成纪要正文"
	summaryMessageID     = "om_synthetic_summary"
)

type meetingSummaryDelivery struct{ receiverType, recipient, uuid, text string }

type fakeSummaryMessenger struct {
	*fakeMeetingMessenger
	deliveries []meetingSummaryDelivery
	sendErr    error
}

func (f *fakeSummaryMessenger) meetingExcerptWithCaptions(_ context.Context, id string, live meetingCaptionResult) (string, bool, error) {
	f.excerpts = append(f.excerpts, id)
	lines := make([]string, 0, len(live.Captions))
	for _, caption := range live.Captions {
		lines = append(lines, caption.Text)
	}
	return strings.Join(lines, "\n"), live.Truncated || live.Incomplete, nil
}

func (f *fakeSummaryMessenger) SendMeetingSummary(_ context.Context, receiverType, recipient, uuid, text string) (string, error) {
	f.deliveries = append(f.deliveries, meetingSummaryDelivery{receiverType, recipient, uuid, text})
	if f.sendErr != nil {
		return "", f.sendErr
	}
	return summaryMessageID, nil
}

func summaryTestFixture(t *testing.T, policy string) (*Engine, *fakeExecutor, *fakeSummaryMessenger, string) {
	t.Helper()
	e, x, f := meetingFixture(t)
	e.Config.MeetingEnabled, e.Config.MeetingRoute, e.Config.MeetingSummaryTarget = true, "mac", policy
	e.Config.access = &accessRuntime{owner: summaryOwner}
	target := e.Config.Routes["mac"]
	target.InteractionMode = "plan"
	target.Users = []string{"user", summaryOwner}
	e.Config.Routes["mac"] = target
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	messenger := &fakeSummaryMessenger{fakeMeetingMessenger: f}
	e.Lark = messenger
	m := meetingMessage("summary-join", "/meeting join "+summaryMeetingNumber)
	m.ChatID = summaryOrigin
	meetingReply(t, e, f, m, "受理入会")
	record := e.Store.snapshot().Meetings[summaryMeetingNumber]
	if record.ID == "" || record.Actor != m.UserID || record.Origin != m.ChatID {
		t.Fatalf("join did not record the long ID, actor and original chat: %+v", record)
	}
	return e, x, messenger, record.ID
}

func pushSummaryMeetingEnded(t *testing.T, e *Engine, appID, meetingID, eventID string) {
	t.Helper()
	lark := &Lark{Config: e.Config, Store: e.Store}
	event := &vc.P2BotMeetingEndedV1{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{AppID: appID, EventID: eventID}},
		Event:       &vc.P2BotMeetingEndedV1Data{Meeting: &vc.MeetingAgentEventMeeting{Id: &meetingID}},
	}
	if err := lark.receiveMeetingEnded(t.Context(), event); err != nil {
		t.Fatal(err)
	}
}

func reopenSummaryStore(t *testing.T, e *Engine) *Store {
	t.Helper()
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMeetingSummaryEndQueuesOneSnapshotForOriginalPrivateChat(t *testing.T) {
	e, x, f, id := summaryTestFixture(t, "origin")
	app := e.Config.AppID
	e.Store.keepMeetingCaptions(app, id, "captions", []meetingCaption{{SentenceID: "one", Text: summaryCaption}})
	pushSummaryMeetingEnded(t, e, "foreign-app", id, "foreign-end")
	pushSummaryMeetingEnded(t, e, app, summaryMeetingNumber, "short-id-end")
	if st := e.Store.snapshot(); len(st.Meetings) != 1 || len(st.Inbox) != 0 || len(e.Store.copyMeetingCaptions(id).Captions) != 1 {
		t.Fatalf("unrelated end push changed the receipt, captions or inbox: meetings=%d inbox=%d", len(st.Meetings), len(st.Inbox))
	}
	pushSummaryMeetingEnded(t, e, app, id, "end")
	pushSummaryMeetingEnded(t, e, app, id, "end")
	pushSummaryMeetingEnded(t, e, app, id, "end-redelivered")
	if st := e.Store.snapshot(); len(st.Meetings) != 0 || len(st.Inbox) != 1 || len(e.Store.copyMeetingCaptions(id).Captions) != 0 {
		t.Fatalf("matching end did not close the meeting and queue one summary: meetings=%d inbox=%d", len(st.Meetings), len(st.Inbox))
	}
	e.Store = reopenSummaryStore(t, e)
	tick(t, e)
	task := onlyTask(t, e)
	if x.sends != 1 || len(f.excerpts) != 1 || f.excerpts[0] != id || !strings.Contains(x.request, summaryCaption) ||
		task.Source.UserID != "user" || task.Source.ChatID != summaryOrigin || task.Target.InteractionMode != "plan" || task.Target.RuntimeMode != "approval-required" {
		t.Fatalf("durable caption or approved actor and route lost: sends=%d excerpts=%v actor=%q chat=%q target=%+v", x.sends, f.excerpts, task.Source.UserID, task.Source.ChatID, task.Target)
	}
	x.observation.Status, x.observation.Summary = "completed", summaryResult
	tick(t, e)
	pushSummaryMeetingEnded(t, e, app, id, "end-after-delivery")
	tick(t, e)
	if len(f.deliveries) != 1 || x.sends != 1 || f.notifications != 0 {
		t.Fatalf("expected one private delivery and no ordinary cards: deliveries=%v sends=%d cards=%d", f.deliveries, x.sends, f.notifications)
	}
	sent := f.deliveries[0]
	if sent.receiverType != "chat_id" || sent.recipient != summaryOrigin || sent.uuid == "" || !strings.Contains(sent.text, summaryResult) || onlyTask(t, e).CardID != summaryMessageID {
		t.Fatalf("unexpected original private chat delivery or missing receipt: %+v", sent)
	}
}

func TestMeetingSummaryRetriesOnlyOriginalOwnerSendAfterRestart(t *testing.T) {
	e, x, f, id := summaryTestFixture(t, "owner")
	e.Store.keepMeetingCaptions(e.Config.AppID, id, "captions", []meetingCaption{{Text: summaryCaption}})
	pushSummaryMeetingEnded(t, e, e.Config.AppID, id, "owner-end")
	tick(t, e)
	x.observation.Status, x.observation.Summary = "completed", summaryResult
	f.sendErr = errors.New("synthetic IM response lost")
	tick(t, e)
	task := onlyTask(t, e)
	if len(f.deliveries) != 1 || task.NotifyHash != "" || task.CardID != "" || task.NotificationIssue == "" {
		t.Fatalf("uncertain owner delivery did not preserve the pending receipt: deliveries=%v issue=%q", f.deliveries, task.NotificationIssue)
	}
	first := f.deliveries[0]
	if first.receiverType != "open_id" || first.recipient != summaryOwner || first.uuid == "" || !strings.Contains(first.text, summaryResult) || task.Source.UserID != "user" {
		t.Fatalf("owner delivery changed the execution actor or recipient: actor=%q delivery=%+v", task.Source.UserID, first)
	}
	updated, err := time.Parse(time.RFC3339Nano, task.Updated)
	if err != nil {
		t.Fatal(err)
	}
	recovered := &fakeSummaryMessenger{fakeMeetingMessenger: &fakeMeetingMessenger{fakeMessenger: &fakeMessenger{}}}
	e = &Engine{Config: e.Config, Store: reopenSummaryStore(t, e), T3: x, Lark: recovered, Now: func() time.Time { return updated.Add(5 * time.Second) }}
	tick(t, e)
	tick(t, e)
	task = onlyTask(t, e)
	if len(recovered.deliveries) != 1 || recovered.deliveries[0] != first || len(recovered.excerpts) != 0 || x.sends != 1 || task.CardID != summaryMessageID || task.NotifyHash == "" {
		t.Fatalf("restart did not recover only the original private send: retries=%v excerpts=%v sends=%d message=%q", recovered.deliveries, recovered.excerpts, x.sends, task.CardID)
	}
}

func TestMeetingSummaryOriginForInvitedMeetingBindsVerifiedOwner(t *testing.T) {
	e, x, f, id := summaryTestFixture(t, "origin")
	e.Config.MeetingAutoJoinUsers = []string{"user"}
	if err := e.Store.update(func(st *State) error {
		st.Meetings[summaryMeetingNumber] = meetingRecord{ID: id, Phase: meetingJoinAccepted, Inviter: "user"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.Store.keepMeetingCaptions(e.Config.AppID, id, "captions", []meetingCaption{{Text: summaryCaption}})
	pushSummaryMeetingEnded(t, e, e.Config.AppID, id, "invite-end")
	tick(t, e)
	x.observation.Status, x.observation.Summary = "completed", summaryResult
	tick(t, e)
	if len(f.deliveries) != 1 || f.deliveries[0].receiverType != "open_id" || f.deliveries[0].recipient != summaryOwner || onlyTask(t, e).Source.UserID != "user" {
		t.Fatalf("invite summary lost its real actor or verified owner: %+v", f.deliveries)
	}
}

func TestMeetingSummaryEndAndDeliveryGuards(t *testing.T) {
	for _, tc := range []struct {
		name, policy, wantSummary string
		captions                  bool
		wantReads, wantRuns       int
		wantDeliveries            int
	}{
		{name: "disabled", captions: true},
		{name: "empty captions", policy: "origin", wantSummary: "没有可用字幕", wantReads: 1},
		{name: "actor route revoked", policy: "origin", captions: true, wantSummary: "授权不可用"},
		{name: "route changed after ending", policy: "origin", captions: true, wantSummary: "授权不可用"},
		{name: "owner route revoked before delivery", policy: "owner", captions: true, wantSummary: "权限已变化", wantReads: 1, wantRuns: 1},
		{name: "definite rejection", policy: "origin", captions: true, wantSummary: "投递被拒绝", wantReads: 1, wantRuns: 1, wantDeliveries: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, x, f, id := summaryTestFixture(t, tc.policy)
			if tc.captions {
				e.Store.keepMeetingCaptions(e.Config.AppID, id, "captions", []meetingCaption{{Text: summaryCaption}})
			}
			pushSummaryMeetingEnded(t, e, e.Config.AppID, id, "end-guard")
			if tc.name == "actor route revoked" {
				e.Config.access.policy.Routes = map[string]map[string]bool{"mac": {"user": false}}
			}
			if tc.name == "route changed after ending" {
				target := e.Config.Routes["mac"]
				target.ProjectID = "different-project"
				e.Config.Routes["mac"] = target
			}
			tick(t, e)
			if tc.name == "owner route revoked before delivery" {
				e.Config.access.policy.Routes = map[string]map[string]bool{"mac": {summaryOwner: false}}
			}
			if tc.name == "definite rejection" {
				f.sendErr = &meetingRejection{Reason: "合成拒绝"}
			}
			x.observation.Status, x.observation.Summary = "completed", summaryResult
			tick(t, e)
			tick(t, e)
			if st := e.Store.snapshot(); len(st.Meetings) != 0 || len(st.Inbox) != 0 || len(e.Store.copyMeetingCaptions(id).Captions) != 0 {
				t.Fatalf("guard left a meeting, work item or live captions behind: meetings=%d inbox=%d", len(st.Meetings), len(st.Inbox))
			}
			if len(f.excerpts) != tc.wantReads || x.sends != tc.wantRuns || len(f.deliveries) != tc.wantDeliveries || f.notifications != 0 {
				t.Fatalf("unexpected reads, runs, deliveries or ordinary cards: %v, %d, %v, %d", f.excerpts, x.sends, f.deliveries, f.notifications)
			}
			if tc.wantSummary == "" {
				if len(e.Store.snapshot().Tasks) != 0 {
					t.Fatal("disabled summary created a task")
				}
				return
			}
			task := onlyTask(t, e)
			if !strings.Contains(task.Summary, tc.wantSummary) || task.NotifyHash == "" || task.CardID != "" || task.TextReplyBody != "" {
				t.Fatalf("guard did not settle the receipt: summary=%q issue=%q message=%q", task.Summary, task.NotificationIssue, task.CardID)
			}
		})
	}
}
