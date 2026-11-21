package bridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type meetingRoomDelivery struct{ meetingID, uuid, text string }

type fakeRoomMessenger struct {
	*fakeMeetingMessenger
	deliveries []meetingRoomDelivery
	failSend   bool
}

func (f *fakeRoomMessenger) SendMeetingText(_ context.Context, meetingID, uuid, text string) error {
	f.deliveries = append(f.deliveries, meetingRoomDelivery{meetingID, uuid, text})
	if f.failSend {
		return errors.New("synthetic response lost")
	}
	return nil
}

func roomTestFixture(t *testing.T) (*Engine, *fakeExecutor, *fakeRoomMessenger, string) {
	t.Helper()
	e, x, f := meetingFixture(t)
	e.Config.MeetingEnabled, e.Config.MeetingRoomReplies = true, true
	e.Config.MeetingRoute = "mac"
	target := e.Config.Routes["mac"]
	target.InteractionMode = "plan"
	e.Config.Routes["mac"] = target
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	meetingReply(t, e, f, meetingMessage("join-room", "/meeting join 123456789"), "受理入会")
	messenger := &fakeRoomMessenger{fakeMeetingMessenger: f}
	e.Lark = messenger
	return e, x, messenger, e.Store.snapshot().Meetings["123456789"].ID
}

func roomInput(meetingID, user, id, kind, text string) Message {
	return Message{ID: id, UserID: user, ChatID: "meeting:" + meetingID, ChatType: "meeting", InputKind: kind, Text: text,
		Meeting: &meetingSource{ID: meetingID, Room: true}}
}

func TestMeetingRoomCommandsRequireFreshIdentityAndExplicitAddress(t *testing.T) {
	e, _, _, id := roomTestFixture(t)
	target := e.Config.Routes[e.Config.MeetingRoute]
	target.Users = []string{"user"}
	e.Config.Routes[e.Config.MeetingRoute] = target
	sdk := newLoopbackLark(t)
	lark, err := sdk.channel(e.Config, e.Store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lark.Channel.Stop(context.Background()) })
	at := fmt.Sprint(time.Now().UnixMilli())
	chats := []meetingActivityChat{
		{OperatorID: "unknown", MessageID: "unknown", Content: "@bot /ask 隔离", SendTime: at},
		{OperatorID: "other", MessageID: "no-route-acl", Content: "@bot /ask 目标权限", SendTime: at},
		{OperatorID: "user", MessageID: "stale", Content: "@bot /ask 旧事件", SendTime: fmt.Sprint(time.Now().Add(-time.Hour).UnixMilli())},
		{OperatorID: "user", MessageID: "ordinary", Content: "这是普通聊天", SendTime: at},
		{OperatorID: "user", MessageID: "mismatch", Content: "@LoopbackOther /ask 匹配边界", SendTime: at},
		{OperatorID: "user", MessageID: "exit", Content: "@bot /exit 任意指令", SendTime: at},
		{OperatorID: "user", MessageID: "missing-explicit", Content: "@bot 只做普通聊天", SendTime: at},
		{OperatorID: "user", MessageID: "oversized", Content: "@bot /ask " + strings.Repeat("x", 24001), SendTime: at},
		{OperatorID: "user", MessageID: "question", Content: "@Loopback /ask 原问题\n保留  缩进", SendTime: at},
		{OperatorID: "user", MessageID: "question", Content: "@bot /ask 重复事件", SendTime: at},
	}
	event := &meetingActivityEvent{AppID: e.Config.AppID, EventID: "evt",
		Items: []meetingActivityItem{{MeetingID: id + "0", Chats: chats}, {MeetingID: id, Chats: chats}}}
	if err := lark.receiveMeetingRoomCommands(context.Background(), event, at); err != nil {
		t.Fatal(err)
	}
	state := e.Store.snapshot()
	if len(state.Inbox) != 1 || state.Inbox[0].UserID != "user" || state.Inbox[0].Text != "原问题\n保留  缩进" || state.Inbox[0].Meeting.ID != id {
		t.Fatalf("unexpected conference ingress: %+v", state.Inbox)
	}
}

func TestMeetingRoomPublicationIsDurableAndSuppressedAfterEnd(t *testing.T) {
	e, x, f, id := roomTestFixture(t)
	f.caption = "合成字幕；password=secret-caption"
	m := roomInput(id, "user", "room-question", meetingRoomAsk, "整理会中分歧")
	accept(t, e, m)
	accept(t, e, m)
	tick(t, e)
	if tsk := onlyTask(t, e); tsk.Source.UserID != "user" || !tsk.Source.Meeting.Room || tsk.Target.InteractionMode != "plan" || x.opens != 1 || x.sends != 1 || len(f.deliveries) != 0 ||
		!strings.HasPrefix(x.request, "整理会中分歧\n") || strings.Contains(x.request, "secret-caption") {
		t.Fatal("room question changed actor, target, message scope, or cleared its privacy boundary")
	}
	x.observation = Observation{Status: "completed", Summary: "公开结论 password=secret-result"}
	f.failSend = true
	tick(t, e)
	before := onlyTask(t, e)
	if len(f.deliveries) != 1 || before.NotifyHash != "" || before.TextReplyBody == "" || before.NotificationIssue == "" || strings.Contains(f.deliveries[0].text, "secret-result") {
		t.Fatal("unconfirmed publish did not preserve the redacted message receipt")
	}
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	e.Store = s
	t.Cleanup(func() { s.Close() })
	f.failSend = false
	tick(t, e)
	tick(t, e)
	if len(f.deliveries) != 2 || f.deliveries[0] != f.deliveries[1] || f.deliveries[0].meetingID != id || x.sends != 1 || onlyTask(t, e).NotifyHash == "" {
		t.Fatal("restart did not retry only the original publish UUID and text")
	}
	x.observation.Status = "running"
	accept(t, e, roomInput(id, "user", "room-notes", meetingRoomNotes, meetingNotesPrompt))
	tick(t, e)
	if x.opens != 1 || x.sends != 2 || onlyTask(t, e).RequestID == before.RequestID {
		t.Fatal("authorized room follow-up did not reuse the original speaker thread")
	}
	if err := e.Store.update(func(st *State) error {
		delete(st.Meetings, "123456789")
		closeMeetingRoom(st, id)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	x.observation.Status = "completed"
	tick(t, e)
	if len(f.deliveries) != 2 {
		t.Fatal("results published after the meeting ended")
	}
}

func TestMeetingRoomStopTargetsOnlyTheSpeakersRunAndEarlierQueue(t *testing.T) {
	e, x, _, id := roomTestFixture(t)
	for _, user := range []string{"user", "other"} {
		accept(t, e, roomInput(id, user, "run-"+user, meetingRoomAsk, "测试问题"))
	}
	tick(t, e)
	accept(t, e, roomInput(id, "user", "old", meetingRoomAsk, "较早排队的请求"))
	accept(t, e, roomInput(id, "user", "stop", meetingRoomStop, "/stop"))
	accept(t, e, roomInput(id, "other", "kept", meetingRoomAsk, "另一个人的请求"))
	before := e.Store.snapshot()
	if len(before.Inbox) != 2 || !before.Inbox[0].Cancelled || before.Inbox[1].Cancelled {
		t.Fatal("stop cancelled the wrong queued messages")
	}
	tick(t, e)
	after := e.Store.snapshot()
	for _, task := range after.Tasks {
		if task.StopRequested != (task.Source.UserID == "user") {
			t.Fatal("stop crossed a speaker scope")
		}
	}
	if x.stops != 1 || x.sends != 2 || len(after.Inbox) != 1 || after.Inbox[0].UserID != "other" {
		t.Fatal("stop did not preserve the other participant's run and queue")
	}
}

func TestMeetingRoomReplyWaitsBetweenRetriesAndKeepsExpiredReceipt(t *testing.T) {
	e, x, f, id := roomTestFixture(t)
	now := time.Now()
	e.Now = func() time.Time { return now }
	accept(t, e, roomInput(id, "user", "timed-retry", meetingRoomAsk, "测试投递"))
	tick(t, e)
	x.observation = Observation{Status: "completed", Summary: "可公开发送的合成结果"}
	f.failSend = true
	tick(t, e)
	tick(t, e)
	if len(f.deliveries) != 1 {
		t.Fatal("busy worker bypassed the time between publish retries")
	}
	now = now.Add(meetingReplyWindow + time.Second)
	f.failSend = false
	tick(t, e)
	if task := onlyTask(t, e); len(f.deliveries) != 1 || task.NotifyHash == "" || !strings.Contains(task.NotificationIssue, "结束自动重试") {
		t.Fatal("expired publish retried or lost its unresolved receipt")
	}
}
