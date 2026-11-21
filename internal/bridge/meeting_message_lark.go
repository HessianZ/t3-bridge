package bridge

import (
	"context"
	"errors"
	"strings"

	vc "github.com/larksuite/oapi-sdk-go/v3/service/vc/v1"
)

// SendMeetingText sends text to the long meeting ID returned by JoinMeeting.
// The caller supplies the stable UUID for this message.
func (l *Lark) SendMeetingText(ctx context.Context, meetingID, uuid, text string) error {
	if strings.TrimSpace(meetingID) == "" || strings.TrimSpace(uuid) == "" {
		return errors.New("发送会议消息需要会议 ID 和 UUID")
	}
	body := vc.NewMessageBotReqBodyBuilder().
		MeetingId(meetingID).
		MsgType("text").
		Uuid(uuid).
		Content(text).
		Build()
	resp, err := l.Channel.RawClient().Vc.V1.Bot.Message(ctx, vc.NewMessageBotReqBuilder().Body(body).Build())
	if err != nil {
		return errMeetingUnconfirmed
	}
	if err := checkMeetingResponse("发送会议消息", resp.ApiResp); err != nil {
		if _, rejected := err.(*meetingRejection); rejected {
			return &meetingRejection{Reason: "飞书拒绝发送会议消息"}
		}
		return errMeetingUnconfirmed
	}
	if resp.Data == nil || resp.Data.Uuid == nil || *resp.Data.Uuid != uuid {
		return errMeetingUnconfirmed
	}
	return nil
}
