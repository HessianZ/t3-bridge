package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	vc "github.com/larksuite/oapi-sdk-go/v3/service/vc/v1"
)

type meetingRejection struct{ Reason string }

func (e *meetingRejection) Error() string { return e.Reason }

var errMeetingUnconfirmed = errors.New("飞书会议操作结果待核对")

func (l *Lark) JoinMeeting(ctx context.Context, number string) (string, error) {
	body := vc.NewJoinBotReqBodyBuilder().
		JoinType(1).
		JoinIdentify(vc.NewJoinIdentifyBuilder().MeetingNo(number).Build()).
		Build()
	resp, err := l.Channel.RawClient().Vc.V1.Bot.Join(ctx, vc.NewJoinBotReqBuilder().Body(body).Build())
	if err != nil {
		return "", errMeetingUnconfirmed
	}
	if err := checkMeetingResponse("入会", resp.ApiResp); err != nil {
		return "", err
	}
	if resp.Data == nil || resp.Data.Meeting == nil || resp.Data.Meeting.Id == nil ||
		strings.TrimSpace(*resp.Data.Meeting.Id) == "" {
		return "", errMeetingUnconfirmed
	}
	return *resp.Data.Meeting.Id, nil
}

func (l *Lark) LeaveMeeting(ctx context.Context, id string) error {
	body := vc.NewLeaveBotReqBodyBuilder().MeetingId(id).Build()
	resp, err := l.Channel.RawClient().Vc.V1.Bot.Leave(ctx, vc.NewLeaveBotReqBuilder().Body(body).Build())
	if err != nil {
		return errMeetingUnconfirmed
	}
	return checkMeetingResponse("离会", resp.ApiResp)
}

func checkMeetingResponse(action string, resp *larkcore.ApiResp) error {
	status := resp.StatusCode
	ok := status >= http.StatusOK && status < http.StatusMultipleChoices
	if !ok && (status < http.StatusBadRequest || status >= http.StatusInternalServerError) {
		return errMeetingUnconfirmed
	}
	// The SDK decodes a missing or null code into the same zero as success.
	var body struct {
		Code *int `json:"code"`
	}
	if err := json.Unmarshal(resp.RawBody, &body); err != nil || body.Code == nil {
		return errMeetingUnconfirmed
	}
	if *body.Code != 0 {
		return &meetingRejection{Reason: fmt.Sprintf("飞书拒绝%s，错误码 %d", action, *body.Code)}
	}
	if !ok {
		return errMeetingUnconfirmed
	}
	return nil
}
