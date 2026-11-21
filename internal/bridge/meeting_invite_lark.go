package bridge

import (
	"context"
	"errors"
	"strings"

	vc "github.com/larksuite/oapi-sdk-go/v3/service/vc/v1"
)

type meetingInviteEvent struct {
	AppID         string
	EventID       string
	MeetingNumber string
	MeetingID     string
	BotID         string
	InviterID     string
	CallID        string
}

// normalizeMeetingInvite retains the IDs from the typed event for the caller to verify.
func normalizeMeetingInvite(event *vc.P2BotMeetingInvitedV1, expectedAppID string) *meetingInviteEvent {
	if event == nil || event.EventV2Base == nil || event.EventV2Base.Header == nil || event.Event == nil {
		return nil
	}
	header, data := event.EventV2Base.Header, event.Event
	if strings.TrimSpace(expectedAppID) == "" || header.AppID != expectedAppID || strings.TrimSpace(header.EventID) == "" ||
		data.Meeting == nil || data.Bot == nil || data.Inviter == nil {
		return nil
	}
	invite := meetingInviteEvent{
		AppID:         header.AppID,
		EventID:       header.EventID,
		MeetingNumber: value(data.Meeting.MeetingNo),
		MeetingID:     value(data.Meeting.Id),
		BotID:         value(data.Bot.Id),
		InviterID:     value(data.Inviter.Id),
		CallID:        value(data.CallId),
	}
	if !validMeetingNumber(invite.MeetingNumber) || !validInvitedMeetingID(invite.MeetingID) || strings.TrimSpace(invite.CallID) == "" ||
		strings.TrimSpace(invite.BotID) == "" || strings.TrimSpace(invite.InviterID) == "" {
		return nil
	}
	return &invite
}

// JoinInvitedMeeting accepts an invitation using the call ID carried by the event.
func (l *Lark) JoinInvitedMeeting(ctx context.Context, meetingNumber, callID string) (string, error) {
	if !validMeetingNumber(meetingNumber) || strings.TrimSpace(callID) == "" {
		return "", errors.New("飞书会议邀请缺少有效会议号或 call_id")
	}
	body := vc.NewJoinBotReqBodyBuilder().
		JoinType(1).
		JoinIdentify(vc.NewJoinIdentifyBuilder().MeetingNo(meetingNumber).Build()).
		CallId(callID).
		Build()
	resp, err := l.Channel.RawClient().Vc.V1.Bot.Join(ctx, vc.NewJoinBotReqBuilder().Body(body).Build())
	if err != nil {
		return "", errMeetingUnconfirmed
	}
	if err := checkMeetingResponse("入会", resp.ApiResp); err != nil {
		return "", err
	}
	if resp.Data == nil || resp.Data.Meeting == nil {
		return "", errMeetingUnconfirmed
	}
	id := value(resp.Data.Meeting.Id)
	if !validInvitedMeetingID(id) {
		return "", errMeetingUnconfirmed
	}
	return id, nil
}

func validInvitedMeetingID(id string) bool {
	return len(id) > 9 && strings.Trim(id, "0123456789") == ""
}
