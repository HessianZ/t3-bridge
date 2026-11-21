package bridge

import (
	"context"
	"errors"
	"strings"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	vc "github.com/larksuite/oapi-sdk-go/v3/service/vc/v1"
)

const meetingCaptionPageSize = 20

// IDs are opaque SDK strings. StartTime and EndTime hold Unix milliseconds
// as strings, with an empty string for an omitted timestamp.
type meetingCaption struct {
	SentenceID string
	SpeakerID  string
	Text       string
	StartTime  string
	EndTime    string
}

// ReadMeetingCaptionPage reads one page for the long ID returned by JoinMeeting.
// If pagination cannot continue, it returns any valid captions with the error.
func (l *Lark) ReadMeetingCaptionPage(ctx context.Context, meetingID, cursor string) (captions []meetingCaption, nextCursor string, err error) {
	if strings.TrimSpace(meetingID) == "" {
		return nil, "", errors.New("读取会议字幕需要会议 ID")
	}
	req := vc.NewEventsBotReqBuilder().MeetingId(meetingID).PageSize(meetingCaptionPageSize).UserIdType("open_id")
	if cursor != "" {
		req.PageToken(cursor)
	}
	resp, err := l.Channel.RawClient().Vc.V1.Bot.Events(ctx, req.Build())
	if err != nil {
		return nil, "", errMeetingUnconfirmed
	}
	if err := checkMeetingResponse("读取会议字幕", resp.ApiResp); err != nil {
		return nil, "", err
	}
	if resp.Data == nil {
		return nil, "", errMeetingUnconfirmed
	}
	if larkcore.BoolValue(resp.Data.HasMore) {
		nextCursor = larkcore.StringValue(resp.Data.PageToken)
		if strings.TrimSpace(nextCursor) == "" || nextCursor == cursor {
			nextCursor, err = "", errMeetingUnconfirmed
		}
	}
	for _, event := range resp.Data.Events {
		if !isMeetingCaptionEvent(event, meetingID) {
			continue
		}
		for _, item := range event.Payload.TranscriptReceivedItems {
			if caption, ok := meetingCaptionFromSDK(item); ok {
				captions = append(captions, caption)
			}
		}
	}
	return captions, nextCursor, err
}

func meetingCaptionFromSDK(item *vc.TranscriptItem) (meetingCaption, bool) {
	if item == nil {
		return meetingCaption{}, false
	}
	caption := meetingCaption{
		SentenceID: larkcore.StringValue(item.SentenceId),
		Text:       larkcore.StringValue(item.Text),
		StartTime:  larkcore.StringValue(item.StartTimeMs),
		EndTime:    larkcore.StringValue(item.EndTimeMs),
	}
	if strings.TrimSpace(caption.SentenceID) == "" && strings.TrimSpace(caption.Text) == "" {
		return meetingCaption{}, false
	}
	if item.Speaker != nil {
		caption.SpeakerID = larkcore.StringValue(item.Speaker.Id)
	}
	return caption, true
}

func isMeetingCaptionEvent(event *vc.Event, meetingID string) bool {
	if event == nil || event.Payload == nil {
		return false
	}
	payload := event.Payload
	// The query scopes events whose payload omits the meeting object.
	if payload.Meeting != nil {
		if id := larkcore.StringValue(payload.Meeting.Id); id != "" && id != meetingID {
			return false
		}
	}
	const transcript = "transcript_received"
	subtype := larkcore.StringValue(payload.ActivityEventType)
	switch larkcore.StringValue(event.EventType) {
	case transcript:
		return subtype == "" || subtype == transcript
	case "", "vc.bot.meeting_activity_v1":
		return subtype == transcript
	default:
		return false
	}
}
