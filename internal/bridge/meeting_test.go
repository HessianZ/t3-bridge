package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	vc "github.com/larksuite/oapi-sdk-go/v3/service/vc/v1"
)

type fakeMeetingMessenger struct {
	*fakeMessenger
	joins, leaves []string
	joinErr       error
	leaveErr      error
	onJoin        func()
	onLeave       func()
	excerpts      []string
	caption       string
	onExcerpt     func()
}

func (f *fakeMeetingMessenger) meetingExcerpt(_ context.Context, id string) (string, bool, error) {
	f.excerpts = append(f.excerpts, id)
	if f.onExcerpt != nil {
		f.onExcerpt()
	}
	return f.caption, false, nil
}

func (f *fakeMeetingMessenger) JoinMeeting(_ context.Context, number string) (string, error) {
	f.joins = append(f.joins, number)
	if f.onJoin != nil {
		f.onJoin()
	}
	if f.joinErr != nil {
		return "", f.joinErr
	}
	return "7628568141510692381", nil
}

func (f *fakeMeetingMessenger) LeaveMeeting(_ context.Context, id string) error {
	f.leaves = append(f.leaves, id)
	if f.onLeave != nil {
		f.onLeave()
	}
	return f.leaveErr
}

func meetingFixture(t *testing.T) (*Engine, *fakeExecutor, *fakeMeetingMessenger) {
	t.Helper()
	e, x, l, _ := testEngine(t)
	e.Config.Topics = nil
	e.Config.Admins = []string{"user"}
	messenger := &fakeMeetingMessenger{fakeMessenger: l}
	e.Lark = messenger
	return e, x, messenger
}

func meetingMessage(id, command string) Message {
	m := message(id, command)
	m.ChatType, m.TopicID = "p2p", ""
	return m
}

func meetingReply(t *testing.T, e *Engine, f *fakeMeetingMessenger, m Message, want string) {
	t.Helper()
	before := len(f.replies)
	accept(t, e, m)
	tick(t, e)
	if len(f.replies) != before+1 || !strings.Contains(f.replies[before], want) {
		t.Fatalf("unexpected reply for %q: %v", m.Text, f.replies[before:])
	}
}

func TestMeetingLateJoinCannotReplaceNewPendingAttempt(t *testing.T) {
	e, _, f := meetingFixture(t)
	e.Config.MeetingEnabled = true
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	const number = "123456789"
	command := func(id string) *SessionCommand {
		return &SessionCommand{Source: meetingMessage(id, "/meeting join "+number)}
	}
	first, second := command("first"), command("second")
	f.onJoin = func() {
		f.onJoin = nil
		before := e.Store.snapshot().Meetings[number]
		closed, err := e.saveMeeting(number, before, meetingRecord{}, "confirm", command("confirm"))
		if !closed || err != nil {
			t.Fatalf("confirm receipt: %t, %v", closed, err)
		}
		f.joinErr = errMeetingUnconfirmed
		if err := e.changeMeeting(t.Context(), f, "join", number, meetingRecord{}, "second", second); err != nil {
			t.Fatal(err)
		}
		f.joinErr = nil
	}
	if err := e.changeMeeting(t.Context(), f, "join", number, meetingRecord{}, "first", first); err != nil {
		t.Fatal(err)
	}
	got := e.Store.snapshot().Meetings[number]
	if got.JoinAttempt != "second" || got.Phase != meetingJoinPending || got.ID != "" || !strings.Contains(first.Reply, "回执已更新") {
		t.Fatalf("late first response changed the second receipt: %+v, %q", got, first.Reply)
	}
}

func TestMeetingControlsAreOptInAndStayInOriginalPrivateScope(t *testing.T) {
	e, x, f := meetingFixture(t)
	meetingReply(t, e, f, meetingMessage("disabled", "/meeting join 123456789"), "尚未启用")
	e.Config.MeetingEnabled = true
	group := meetingMessage("group", "/meeting join 123456789")
	group.ChatType = "group"
	meetingReply(t, e, f, group, "私聊")
	other := meetingMessage("member", "/meeting join 123456789")
	other.UserID = "other"
	meetingReply(t, e, f, other, "管理员")
	meetingReply(t, e, f, meetingMessage("not-number", "/meeting join https://example.invalid/a"), "9 位会议号")
	if len(f.joins) != 0 || x.sends != 0 {
		t.Fatal("gate allowed an unauthorized VC or model request")
	}
	m := meetingMessage("join", "/meeting join 123 456 789")
	f.onJoin = func() {
		state := e.Store.snapshot()
		record := state.Meetings["123456789"]
		if record.Phase != meetingJoinPending || !strings.Contains(state.Commands[key(m, m.ID)].Reply, "待核对") {
			t.Fatal("meeting write happened before its receipt was persisted")
		}
	}
	meetingReply(t, e, f, m, "受理入会")
	meetingReply(t, e, f, meetingMessage("repeat-number", "/meeting join 123456789"), "已有回执")
	accept(t, e, m)
	tick(t, e)
	if len(f.joins) != 1 || f.joins[0] != "123456789" {
		t.Fatalf("repeated join or bad number: %v", f.joins)
	}
	e.Config.Admins = append(e.Config.Admins, "other")
	other = meetingMessage("other-status", "/meeting status")
	other.UserID = "other"
	meetingReply(t, e, f, other, "没有未关闭")
	other.ID, other.Text = "other-leave", "/meeting leave 123456789"
	meetingReply(t, e, f, other, "无法在当前私聊管理")
	if len(f.leaves) != 0 {
		t.Fatal("another admin could leave this meeting")
	}
	if err := e.Store.DrainCheck(e.Config.AppID); err == nil {
		t.Fatal("active meetings silently passed account drain")
	}
	e.Config.MeetingEnabled = false
	meetingReply(t, e, f, meetingMessage("status", "/meeting"), "123456789 · 飞书已受理入会")
	f.leaveErr = &meetingRejection{Reason: "飞书拒绝离会，错误码 20017"}
	meetingReply(t, e, f, meetingMessage("leave-denied", "/meeting leave"), "错误码 20017")
	if e.Store.snapshot().Meetings["123456789"].Phase != meetingJoinAccepted {
		t.Fatal("definite API rejection dropped or overwrote the existing record")
	}
	f.leaveErr = errors.New("synthetic lost leave response")
	meetingReply(t, e, f, meetingMessage("leave-unconfirmed", "/meeting leave"), "离会结果待核对")
	if e.Store.snapshot().Meetings["123456789"].Phase != meetingLeavePending {
		t.Fatal("uncertain leave was treated as a confirmed outcome")
	}
	f.leaveErr = nil
	meetingReply(t, e, f, meetingMessage("leave", "/meeting leave"), "已确认离会")
	if len(f.leaves) != 3 || f.leaves[0] != "7628568141510692381" || f.leaves[1] != f.leaves[0] || f.leaves[2] != f.leaves[0] || len(e.Store.snapshot().Meetings) != 0 || x.sends != 0 {
		t.Fatalf("leave changed the recorded meeting identity: %v", f.leaves)
	}
	if err := e.Store.DrainCheck(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
}

func TestMeetingConfigIsPerAccountAndPreservesDisabledRouteNames(t *testing.T) {
	primary := testConfig(t)
	secondary := testConfig(t)
	secondary.AppID = "synthetic-other-app"
	secondary.MeetingEnabled = true
	target := primary.Routes["mac"]
	target.Aliases = []string{"Meeting"}
	primary.Routes["mac"] = target
	primary.AdditionalAccounts = []Config{secondary}
	data, err := json.Marshal(primary)
	if err != nil {
		t.Fatal(err)
	}
	config, err := ParseConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	accounts := config.Accounts()
	if accounts[0].MeetingEnabled || !accounts[1].MeetingEnabled || slashWord(accounts[0], "/meeting repair") != "" || slashWord(accounts[1], "/meeting status") != "/meeting" {
		t.Fatal("meeting setting leaked across accounts or changed a legacy route")
	}
	accounts[0].MeetingEnabled = true
	if err := accounts[0].Validate(); err == nil || !strings.Contains(err.Error(), "reserves /meeting") {
		t.Fatalf("accepted conflicted meeting command and route: %v", err)
	}
}

func TestMeetingUncertainJoinSurvivesRestartWithoutRepeating(t *testing.T) {
	e, _, f := meetingFixture(t)
	e.Config.MeetingEnabled = true
	m := meetingMessage("unknown", "/meeting join 123456789")
	f.joinErr = errors.New("synthetic lost VC response")
	f.replyErr = errors.New("synthetic lost IM response")
	meetingReply(t, e, f, m, "入会结果待核对")
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	e.Store = s
	t.Cleanup(func() { s.Close() })
	f.replyErr = nil
	tick(t, e)
	meetingReply(t, e, f, meetingMessage("retry", m.Text), "已有回执")
	meetingReply(t, e, f, meetingMessage("leave-unknown", "/meeting leave 123456789"), "入会结果尚未确认")
	meetingReply(t, e, f, meetingMessage("confirm", "/meeting confirm-left 123456789"), "确认机器人不在")
	if len(f.joins) != 1 || len(f.leaves) != 0 || len(e.Store.snapshot().Meetings) != 0 {
		t.Fatal("recovery replayed a remote write or retained a manually cleared record")
	}
}

func TestMeetingEndPushMatchesAppAndLongIDAndWinsOverLateLeaveResult(t *testing.T) {
	e, _, f := meetingFixture(t)
	e.Config.MeetingEnabled = true
	meetingReply(t, e, f, meetingMessage("join", "/meeting join 123456789"), "受理入会")
	lark := &Lark{Store: e.Store, Config: e.Config}
	fixture := func(app, id string) {
		t.Helper()
		event := &vc.P2BotMeetingEndedV1{
			EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{AppID: app}},
			Event:       &vc.P2BotMeetingEndedV1Data{Meeting: &vc.MeetingAgentEventMeeting{Id: &id}},
		}
		if err := lark.receiveMeetingEnded(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	record := e.Store.snapshot().Meetings["123456789"]
	fixture("synthetic-other-app", record.ID)
	fixture(e.Config.AppID, "7628568141510692382")
	if e.Store.snapshot().Meetings["123456789"] != record {
		t.Fatal("unrelated meeting push cleared the receipt")
	}
	f.onLeave = func() { fixture(e.Config.AppID, record.ID) }
	f.leaveErr = &meetingRejection{Reason: "已结束"}
	meetingReply(t, e, f, meetingMessage("leave", "/meeting leave"), "会议回执已更新")
	fixture(e.Config.AppID, record.ID)
	if len(e.Store.snapshot().Meetings) != 0 || len(f.leaves) != 1 {
		t.Fatal("late leave response resurrected a finished meeting")
	}
}
