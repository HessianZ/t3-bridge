package bridge

import (
	"strings"

	vc "github.com/larksuite/oapi-sdk-go/v3/service/vc/v1"
)

type meetingActivityEvent struct {
	AppID   string
	EventID string
	Items   []meetingActivityItem
}

type meetingActivityItem struct {
	MeetingID string
	Captions  []meetingCaption
	Chats     []meetingActivityChat
}

type meetingActivityChat struct {
	OperatorID  string
	Content     string
	MessageID   string
	MessageType int
	SendTime    string // Unix milliseconds as a string, empty when omitted.
}

// normalizeMeetingActivity keeps each SDK activity item in order under its event ID.
// A nil result means the event had no accepted items.
func normalizeMeetingActivity(event *vc.P2BotMeetingActivityV1, expectedAppID string) *meetingActivityEvent {
	if event == nil || event.EventV2Base == nil || event.EventV2Base.Header == nil || event.Event == nil {
		return nil
	}
	header := event.EventV2Base.Header
	if strings.TrimSpace(expectedAppID) == "" || header.AppID != expectedAppID || strings.TrimSpace(header.EventID) == "" {
		return nil
	}

	result := meetingActivityEvent{AppID: header.AppID, EventID: header.EventID}
	for _, activity := range event.Event.MeetingActivityItems {
		if activity == nil || activity.Meeting == nil {
			continue
		}
		id := value(activity.Meeting.Id)
		// The long numeric ID and the nine-digit meeting number are separate SDK fields.
		if len(id) <= 9 || strings.Trim(id, "0123456789") != "" {
			continue
		}
		item := meetingActivityItem{MeetingID: id}
		switch value(activity.ActivityEventType) {
		case "transcript_received":
			item.Captions = normalizeActivityCaptions(activity.TranscriptReceivedItems)
		case "chat_received":
			item.Chats = normalizeActivityChats(activity.ChatReceivedItems)
		}
		if len(item.Captions) != 0 || len(item.Chats) != 0 {
			result.Items = append(result.Items, item)
		}
	}
	if len(result.Items) == 0 {
		return nil
	}
	return &result
}

func normalizeActivityCaptions(items []*vc.TranscriptItem) []meetingCaption {
	var captions []meetingCaption
	for _, item := range items {
		if caption, ok := meetingCaptionFromSDK(item); ok {
			captions = append(captions, caption)
		}
	}
	return captions
}

func normalizeActivityChats(items []*vc.ChatMessageItem) []meetingActivityChat {
	// The SDK carries the message kind in message_type and has no separate visibility field.
	const textMessageType = 1
	var chats []meetingActivityChat
	for _, item := range items {
		if item == nil || item.Operator == nil || item.MessageType == nil || *item.MessageType != textMessageType {
			continue
		}
		operatorID, content := value(item.Operator.Id), value(item.Content)
		if strings.TrimSpace(operatorID) == "" || strings.TrimSpace(content) == "" {
			continue
		}
		chats = append(chats, meetingActivityChat{
			OperatorID:  operatorID,
			Content:     content,
			MessageID:   value(item.MessageId),
			MessageType: *item.MessageType,
			SendTime:    value(item.SendTime),
		})
	}
	return chats
}
