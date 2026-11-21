package bridge

import (
	"context"
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

func newMeetingCaptionSDKLoopback(t *testing.T, handle func(http.ResponseWriter, *http.Request)) *Lark {
	t.Helper()
	f := newLoopbackLark(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/open-apis/vc/v1/bots/events" {
			return false
		}
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer synthetic-feishu-token" {
			t.Error("unexpected VC method or authorization")
		}
		handle(w, r)
		return true
	})
	sdk, err := channel.New("synthetic-vc-caption-app", "synthetic-secret", channel.WithDomain(f.url),
		channel.WithReqTimeout(3*time.Second), channel.WithLogLevel(larkcore.LogLevelError))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sdk.Stop(context.Background()) })
	return &Lark{Channel: sdk}
}

func TestOfficialLarkSDKMeetingCaptionPages(t *testing.T) {
	const token = "Next+Page/=?"
	const sentenceID = "18446744073709551616"
	const target = `"meeting":{"id":"` + sdkMeetingID + `","topic":"synthetic-private-title"}`
	const ignored = `"transcript_received_items":[{"sentence_id":"ignore","text":"synthetic-unrelated-text"}]`
	var calls atomic.Int32
	l := newMeetingCaptionSDKLoopback(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("meeting_id") != sdkMeetingID || q.Get("page_size") != "20" || q.Get("user_id_type") != "open_id" {
			t.Errorf("unexpected caption query: %v", q)
		}
		switch calls.Add(1) {
		case 1:
			if q.Has("page_token") {
				t.Error("first caption request included a page token")
			}
			io.WriteString(w, `{"code":0,"data":{"has_more":true,"page_token":"`+token+`","events":[
				null,
				{"event_type":"transcript_received","payload":{`+target+`,"transcript_received_items":[
					{"sentence_id":"`+sentenceID+`","text":" 第一条字幕 ","language":"zh",
					 "speaker":{"id":"Ou_SyntheticSpeaker","user_name":"synthetic-name"},
					 "start_time_ms":"1789574400123","end_time_ms":"1789574400567"},
					null,{"text":"synthetic-no-sentence-id"},{"sentence_id":"blank","text":" "},{}
				]}},
				{"event_type":"vc.bot.meeting_activity_v1","payload":{`+target+`,"activity_event_type":"chat_received",
				 "chat_received_items":[{"content":"synthetic-private-chat"}],`+ignored+`}},
				{"event_type":"chat_received","payload":{`+target+`,"activity_event_type":"transcript_received",`+ignored+`}},
				{"event_type":"transcript_received","payload":{`+target+`,"activity_event_type":"participant_joined",`+ignored+`}},
				{"event_type":"vc.bot.meeting_activity_v1","payload":{"meeting":{"id":"9876543210987654322"},
				 "activity_event_type":"transcript_received",`+ignored+`}}
			]}}`)
		case 2:
			if q.Get("page_token") != token {
				t.Errorf("second caption request token = %q, want %q", q.Get("page_token"), token)
			}
			io.WriteString(w, `{"code":0,"data":{"has_more":false,"page_token":"ignored-final-token","events":[
				{"event_type":"vc.bot.meeting_activity_v1","payload":{`+target+`,"activity_event_type":"transcript_received",
				 "transcript_received_items":[
					{"sentence_id":"0002","text":"第二条字幕","start_time_ms":"1789574400568"},
					{"sentence_id":"synthetic-THIRD","text":"第三条字幕"}
				 ]}}
			]}}`)
		default:
			t.Error("unexpected additional caption request")
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	ctx := context.Background()
	got, next, err := l.ReadMeetingCaptionPage(ctx, sdkMeetingID, "")
	want := []meetingCaption{{
		SentenceID: sentenceID,
		SpeakerID:  "Ou_SyntheticSpeaker",
		Text:       " 第一条字幕 ",
		StartTime:  "1789574400123",
		EndTime:    "1789574400567",
	}, {Text: "synthetic-no-sentence-id"}, {SentenceID: "blank", Text: " "}}
	if err != nil || next != token || !reflect.DeepEqual(got, want) {
		t.Fatalf("first page = %+v, %q, %v, want %+v, %q, nil", got, next, err, want, token)
	}
	got, next, err = l.ReadMeetingCaptionPage(ctx, sdkMeetingID, next)
	want = []meetingCaption{
		{SentenceID: "0002", Text: "第二条字幕", StartTime: "1789574400568"},
		{SentenceID: "synthetic-THIRD", Text: "第三条字幕"},
	}
	if err != nil || next != "" || !reflect.DeepEqual(got, want) {
		t.Fatalf("second page = %+v, %q, %v, want %+v, empty cursor, nil", got, next, err, want)
	}
	if calls.Load() != 2 {
		t.Fatalf("sent %d caption requests, want 2", calls.Load())
	}
}

func TestOfficialLarkSDKMeetingCaptionPageResults(t *testing.T) {
	const sensitive = "synthetic-secret synthetic-private-chat synthetic-private-title"
	const rejected = `{"code":20017,"msg":"` + sensitive + `"}`
	const cursor = "current"
	for _, tc := range []struct {
		name, body, next string
		status           int
		captions         int
		rejected, ok     bool
	}{
		{name: "empty terminal page", status: 200, body: `{"code":0,"data":{"has_more":false}}`, ok: true},
		{name: "empty page with more", status: 200, body: `{"code":0,"data":{"has_more":true,"page_token":"next"}}`, next: "next", ok: true},
		{name: "missing next cursor", status: 200, body: `{"code":0,"data":{"has_more":true}}`},
		{name: "blank next cursor", status: 200, body: `{"code":0,"data":{"has_more":true,"page_token":" "}}`},
		{name: "repeated next cursor with usable captions", status: 200, captions: 1, body: `{"code":0,"data":{"has_more":true,"page_token":"` + cursor + `","events":[{"event_type":"transcript_received","payload":{"transcript_received_items":[{"text":"字幕"}]}}]}}`},
		{name: "business rejection", status: 200, body: rejected, rejected: true},
		{name: "forbidden business rejection", status: 403, body: rejected, rejected: true},
		{name: "bare forbidden", status: 403, body: `{"msg":"` + sensitive + `"}`},
		{name: "server error", status: 500, body: rejected},
		{name: "no business code", status: 200, body: `{"data":{"has_more":false}}`},
		{name: "null business code", status: 200, body: `{"code":null,"data":{}}`},
		{name: "no data", status: 200, body: `{"code":0}`},
		{name: "malformed body", status: 200, body: `{"msg":"` + sensitive + `"`},
		{name: "transport failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newMeetingCaptionSDKLoopback(t, func(w http.ResponseWriter, r *http.Request) {
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
			captions, next, err := l.ReadMeetingCaptionPage(context.Background(), sdkMeetingID, cursor)
			if len(captions) != tc.captions || next != tc.next {
				t.Fatalf("captions = %+v, cursor = %q, want %d captions and cursor %q", captions, next, tc.captions, tc.next)
			}
			var rejection *meetingRejection
			switch {
			case tc.rejected:
				const want = "飞书拒绝读取会议字幕，错误码 20017"
				if !errors.As(err, &rejection) || err.Error() != want {
					t.Errorf("error = %v, want typed rejection %q", err, want)
				}
			case tc.ok:
				if err != nil {
					t.Errorf("error = %v, want nil", err)
				}
			default:
				if err != errMeetingUnconfirmed {
					t.Errorf("error = %v, want %v", err, errMeetingUnconfirmed)
				}
			}
		})
	}
}
