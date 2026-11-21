package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

func withLiveMeeting(t *testing.T, appID string) (*Engine, *fakeMeetingMessenger, string) {
	t.Helper()
	e, _, lark := meetingFixture(t)
	e.Config.AppID = appID
	e.Config.MeetingEnabled = true
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	meetingReply(t, e, lark, meetingMessage("join-live", "/meeting join 123456789"), "受理入会")
	return e, lark, e.Store.snapshot().Meetings["123456789"].ID
}

func TestLiveMeetingCaptionWindowChecksScopeAndDropsReplays(t *testing.T) {
	e, _, id := withLiveMeeting(t, "synthetic-app-a")
	other, _, otherID := withLiveMeeting(t, "synthetic-app-b")
	old := []meetingCaption{{SentenceID: "sentence", Text: "原字幕"}}
	updated := []meetingCaption{{SentenceID: "sentence", Text: "修订字幕合成内容"}}
	e.Store.keepMeetingCaptions(other.Config.AppID, id, "foreign-event", old)
	e.Store.keepMeetingCaptions(e.Config.AppID, id+"0", "wrong-room", old)
	if got := e.Store.copyMeetingCaptions(id).Captions; len(got) != 0 {
		t.Fatalf("unauthorized app or meeting changed window: %v", got)
	}
	e.Store.keepMeetingCaptions(e.Config.AppID, id, "earlier-event", old)
	e.Store.keepMeetingCaptions(e.Config.AppID, id, "later-event", updated)
	e.Store.keepMeetingCaptions(e.Config.AppID, id, "earlier-event", old)
	got := e.Store.copyMeetingCaptions(id).Captions
	if len(got) != 1 || got[0].Text != updated[0].Text || len(other.Store.copyMeetingCaptions(otherID).Captions) != 0 {
		t.Fatalf("replay replaced written text or crossed stores: %v", got)
	}
	got[0].Text = "调用者副本"
	if e.Store.copyMeetingCaptions(id).Captions[0].Text != updated[0].Text {
		t.Fatal("reading the caption window changed stored data")
	}
	raw, err := os.ReadFile(filepath.Join(e.Config.StateDir, "state.json"))
	if err != nil || strings.Contains(string(raw), updated[0].Text) {
		t.Fatalf("captions were written to state: %v", err)
	}
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if len(reopened.copyMeetingCaptions(id).Captions) != 0 || reopened.snapshot().Meetings["123456789"].ID != id {
		t.Fatal("receiver restart retained caption data or lost the meeting receipt")
	}
}

func TestLiveMeetingCaptionWindowBoundAndClearedOnLeave(t *testing.T) {
	e, lark, id := withLiveMeeting(t, "synthetic-app-a")
	for i := 0; i < 145; i++ {
		e.Store.keepMeetingCaptions(e.Config.AppID, id, fmt.Sprint(i), []meetingCaption{{SentenceID: fmt.Sprint(i), Text: fmt.Sprintf("第%d条", i), StartTime: fmt.Sprint(i + 1)}})
	}
	window := e.Store.copyMeetingCaptions(id)
	if len(window.Captions) != meetingCaptionMaxSentences || !window.Truncated || window.Captions[len(window.Captions)-1].Text != "第144条" {
		t.Fatalf("unexpected retained live captions: %+v", window)
	}
	meetingReply(t, e, lark, meetingMessage("leave-live", "/meeting leave 123456789"), "已确认离会")
	e.Store.keepMeetingCaptions(e.Config.AppID, id, "late", []meetingCaption{{Text: "延迟推送"}})
	if len(e.Store.copyMeetingCaptions(id).Captions) != 0 {
		t.Fatal("late caption re-created a closed meeting window")
	}
}

func TestLiveMeetingExcerptMergesPagesAndContinuesOnReadFailure(t *testing.T) {
	e, _, id := withLiveMeeting(t, "synthetic-app-a")
	var reads atomic.Int32
	lark := newMeetingCaptionSDKLoopback(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("meeting_id") != id {
			t.Errorf("paged read changed long meeting identity: %v", r.URL.Query())
		}
		if reads.Add(1) != 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, `{"code":0,"data":{"has_more":false,"events":[{"event_type":"transcript_received","payload":{"transcript_received_items":[
			{"sentence_id":"prior","text":"补读历史","start_time_ms":"10"},
			{"sentence_id":"same","text":"旧句正文","start_time_ms":"20"}
		]}}]}}`)
	})
	lark.Store = e.Store
	e.Store.keepMeetingCaptions(e.Config.AppID, id, "push", []meetingCaption{
		{SentenceID: "same", Text: "推送修订", StartTime: "20"},
		{SentenceID: "bot", Text: "机器人自身字幕", SpeakerID: "synthetic-bot", StartTime: "30"},
	})
	text, incomplete, err := lark.meetingExcerpt(context.Background(), id)
	if err != nil || incomplete || !strings.Contains(text, "补读历史") || !strings.Contains(text, "推送修订") || strings.Contains(text, "旧句正文") || strings.Contains(text, "机器人自身字幕") {
		t.Fatalf("paged and pushed excerpts did not merge: %q, incomplete=%t, %v", text, incomplete, err)
	}
	text, incomplete, err = lark.meetingExcerpt(context.Background(), id)
	if err == nil || !incomplete || !strings.Contains(text, "推送修订") || strings.Contains(text, "机器人自身字幕") {
		t.Fatalf("failed backfill discarded the usable window: %q, incomplete=%t, %v", text, incomplete, err)
	}
}

func TestLiveMeetingCaptionEventsThroughOfficialWebSocket(t *testing.T) {
	e, _, id := withLiveMeeting(t, "synthetic-app-a")
	e.Config.MeetingRoute = "mac"
	transport := newLoopbackLark(t)
	lark, err := transport.channel(e.Config, e.Store)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	exit := make(chan error, 1)
	go func() { exit <- lark.Channel.Start(ctx) }()
	defer lark.Channel.Stop(context.Background())
	var peer *websocket.Conn
	select {
	case peer = <-transport.peers:
	case err := <-exit:
		t.Fatalf("receiver exited before handshake: %v", err)
	case <-ctx.Done():
		t.Fatal("receiver did not connect")
	}
	defer peer.Close()
	activity := fmt.Sprintf(`{"meeting_activity_items":[
		{"meeting":{"id":%q},"activity_event_type":"chat_received","chat_received_items":[{"operator":{"id":"user"},"message_type":1,"content":"@bot /exit"}]},
		{"meeting":{"id":%q},"activity_event_type":"transcript_received","transcript_received_items":[{"speaker":{"id":"user"},"sentence_id":"one","text":"合成语音字幕"}]},
		{"meeting":{"id":%q},"activity_event_type":"transcript_received","transcript_received_items":[{"speaker":{"id":"user"},"sentence_id":"two","text":"同一推送的第二句"}]}
	]}`, id, id, id)
	sendMeetingEvent(t, peer, "synthetic-foreign-app", "foreign", "vc.bot.meeting_activity_v1", activity)
	if len(e.Store.copyMeetingCaptions(id).Captions) != 0 {
		t.Fatal("wrong app populated the buffer")
	}
	sendMeetingEvent(t, peer, e.Config.AppID, "captions", "vc.bot.meeting_activity_v1", activity)
	got := e.Store.copyMeetingCaptions(id).Captions
	if len(got) != 2 || got[0].Text != "合成语音字幕" || got[1].Text != "同一推送的第二句" {
		t.Fatalf("caption event did not reach the live buffer: %+v", got)
	}
	if st := e.Store.snapshot(); len(st.Inbox) != 0 || len(st.Tasks) != 0 {
		t.Fatal("conference chat or transcript created a task")
	}
	sendMeetingEvent(t, peer, e.Config.AppID, "end", "vc.bot.meeting_ended_v1", fmt.Sprintf(`{"meeting":{"id":%q}}`, id))
	sendMeetingEvent(t, peer, e.Config.AppID, "late", "vc.bot.meeting_activity_v1", activity)
	if len(e.Store.copyMeetingCaptions(id).Captions) != 0 {
		t.Fatal("ended meeting retained or accepted captions")
	}
}

func sendMeetingEvent(t *testing.T, peer *websocket.Conn, app, eventID, kind, event string) {
	t.Helper()
	payload := fmt.Sprintf(`{"schema":"2.0","header":{"event_id":%q,"event_type":%q,"app_id":%q,"create_time":"%d"},"event":%s}`, eventID, kind, app, time.Now().UnixMilli(), event)
	headers := larkws.Headers{}
	headers.Add(larkws.HeaderType, string(larkws.MessageTypeEvent))
	headers.Add(larkws.HeaderMessageID, eventID)
	frame := larkws.Frame{Method: int32(larkws.FrameTypeData), Service: 1, Payload: []byte(payload), Headers: headers}
	data, err := frame.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	if err := peer.SetWriteDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if err := peer.WriteMessage(websocket.BinaryMessage, data); err != nil {
		t.Fatal(err)
	}
	if err := peer.SetReadDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	for {
		_, encoded, err := peer.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		var ack larkws.Frame
		if err := ack.Unmarshal(encoded); err != nil {
			t.Fatal(err)
		}
		if larkws.Headers(ack.Headers).GetString(larkws.HeaderMessageID) != eventID {
			continue
		}
		var result larkws.Response
		if err := json.Unmarshal(ack.Payload, &result); err != nil || result.StatusCode != http.StatusOK {
			t.Fatalf("WebSocket rejected %s: code=%d, %v", eventID, result.StatusCode, err)
		}
		return
	}
}
