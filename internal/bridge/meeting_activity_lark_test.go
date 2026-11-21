package bridge

import (
	"context"
	"reflect"
	"testing"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	vc "github.com/larksuite/oapi-sdk-go/v3/service/vc/v1"
)

func TestNormalizeMeetingActivitySDKPayload(t *testing.T) {
	const payload = `{
		"schema":"2.0",
		"header":{"app_id":"cli_synthetic","event_id":"event-1","event_type":"vc.bot.meeting_activity_v1"},
		"event":{"meeting_activity_items":[
			null,
			{"meeting":{"meeting_no":"123456789"},"activity_event_type":"transcript_received",
			 "transcript_received_items":[{"text":"missing id"}]},
			{"meeting":{"id":"123456789"},"activity_event_type":"transcript_received",
			 "transcript_received_items":[{"text":"short id"}]},
			{"meeting":{"id":"invalid-long-id"},"activity_event_type":"transcript_received",
			 "transcript_received_items":[{"text":"malformed id"}]},
			{"meeting":{"id":"7628568141510692381","topic":"discard topic"},"activity_event_type":"transcript_received",
			 "transcript_received_items":[
				{"speaker":{"id":"ou_speaker","user_name":"discard name"},"sentence_id":"s1","text":"第一句","start_time_ms":"1000","end_time_ms":"2000"},
				null,
				{"text":"第二句","start_time_ms":"2001"},
				{"text":" "}
			 ],"chat_received_items":[{"operator":{"id":"ou_other"},"message_type":1,"content":"ignore mismatched array"}]},
			{"meeting":{"id":"7628568141510692382"},"activity_event_type":"chat_received","chat_received_items":[
				{"operator":{"id":"ou_author"},"message_type":1,"content":" 问题一？ ","message_id":"msg-1","send_time":"2100"},
				{"operator":{"id":"ou_other"},"message_type":1,"content":"问题二？","message_id":"msg-2","send_time":"2200"},
				null,
				{"sender":{"id":"spoof"},"message_type":1,"content":"missing operator"},
				{"operator":{"user_name":"display only"},"message_type":1,"content":"missing identity"},
				{"operator":{"id":" "},"message_type":1,"content":"blank identity"},
				{"operator":{"id":"ou_author"},"message_type":1,"content":" "},
				{"operator":{"id":"ou_author"},"message_type":2,"content":"system"},
				{"operator":{"id":"ou_author"},"message_type":3,"content":"THUMBSUP"},
				{"operator":{"id":"ou_author"},"message_type":4,"content":"encrypted"},
				{"operator":{"id":"ou_author"},"message_type":99,"content":"unknown type"},
				{"operator":{"id":"ou_author"},"content":"missing type"}
			 ]},
			{"meeting":{"id":"7628568141510692381"},"activity_event_type":"magic_share_started",
			 "magic_share_started_items":[{"share_doc":{"title":"discard title","url":"https://example.invalid/share"}}],
			 "transcript_received_items":[{"text":"ignore mismatched subtype"}]},
			{"meeting":{"id":"7628568141510692381"},"activity_event_type":"transcript_received",
			 "transcript_received_items":[{"sentence_id":"s1","text":"第一句修订"}]}
		]}
	}`

	var got *meetingActivityEvent
	dispatch := dispatcher.NewEventDispatcher("", "").OnP2BotMeetingActivityV1(func(_ context.Context, event *vc.P2BotMeetingActivityV1) error {
		got = normalizeMeetingActivity(event, "cli_synthetic")
		return nil
	})
	if _, err := dispatch.Do(t.Context(), []byte(payload)); err != nil {
		t.Fatal(err)
	}
	want := &meetingActivityEvent{
		AppID: "cli_synthetic", EventID: "event-1",
		Items: []meetingActivityItem{
			{MeetingID: "7628568141510692381", Captions: []meetingCaption{
				{SentenceID: "s1", SpeakerID: "ou_speaker", Text: "第一句", StartTime: "1000", EndTime: "2000"},
				{Text: "第二句", StartTime: "2001"},
			}},
			{MeetingID: "7628568141510692382", Chats: []meetingActivityChat{
				{OperatorID: "ou_author", Content: " 问题一？ ", MessageID: "msg-1", MessageType: 1, SendTime: "2100"},
				{OperatorID: "ou_other", Content: "问题二？", MessageID: "msg-2", MessageType: 1, SendTime: "2200"},
			}},
			{MeetingID: "7628568141510692381", Captions: []meetingCaption{{SentenceID: "s1", Text: "第一句修订"}}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeMeetingActivity() = %+v, want %+v", got, want)
	}
}

func TestNormalizeMeetingActivityRejectsInvalidEnvelope(t *testing.T) {
	data := &vc.P2BotMeetingActivityV1Data{MeetingActivityItems: []*vc.MeetingActivityItem{
		vc.NewMeetingActivityItemBuilder().
			Meeting(vc.NewMeetingAgentEventMeetingBuilder().Id("7628568141510692381").Build()).
			ActivityEventType("transcript_received").
			TranscriptReceivedItems([]*vc.TranscriptItem{vc.NewTranscriptItemBuilder().Text("合成字幕").Build()}).
			Build(),
	}}
	base := &larkevent.EventV2Base{Header: &larkevent.EventHeader{AppID: "cli_synthetic", EventID: "event-1"}}
	for _, tc := range []struct {
		name  string
		event *vc.P2BotMeetingActivityV1
		appID string
	}{
		{"nil event", nil, "cli_synthetic"},
		{"missing base", &vc.P2BotMeetingActivityV1{Event: data}, "cli_synthetic"},
		{"missing header", &vc.P2BotMeetingActivityV1{EventV2Base: &larkevent.EventV2Base{}, Event: data}, "cli_synthetic"},
		{"missing payload", &vc.P2BotMeetingActivityV1{EventV2Base: base}, "cli_synthetic"},
		{"wrong app", &vc.P2BotMeetingActivityV1{EventV2Base: base, Event: data}, "cli_other"},
		{"blank app", &vc.P2BotMeetingActivityV1{EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "event-1"}}, Event: data}, ""},
		{"missing event ID", &vc.P2BotMeetingActivityV1{EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{AppID: "cli_synthetic"}}, Event: data}, "cli_synthetic"},
		{"empty items", &vc.P2BotMeetingActivityV1{EventV2Base: base, Event: &vc.P2BotMeetingActivityV1Data{}}, "cli_synthetic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeMeetingActivity(tc.event, tc.appID); got != nil {
				t.Fatalf("unexpected activity: %+v", got)
			}
		})
	}
}
