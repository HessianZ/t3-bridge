package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	im "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// SendMeetingSummary sends the summary as a text message using the caller's UUID.
func (l *Lark) SendMeetingSummary(ctx context.Context, receiverType, recipient, uuid, text string) (string, error) {
	if (receiverType != "chat_id" && receiverType != "open_id") ||
		strings.TrimSpace(recipient) == "" || strings.TrimSpace(uuid) == "" {
		return "", &meetingRejection{Reason: "会后纪要缺少有效接收人类型、接收人或 UUID"}
	}
	// Application ownership can change between selection and delivery.
	if receiverType == "open_id" {
		owner := l.lookupAppOwner(ctx)
		if owner == "" {
			return "", errMeetingUnconfirmed
		}
		if owner != recipient {
			return "", &meetingRejection{Reason: "应用所有者已变化，原接收人不再有效。"}
		}
	}
	content, _ := json.Marshal(map[string]string{"text": text})
	body := im.NewCreateMessageReqBodyBuilder().
		ReceiveId(recipient).
		MsgType("text").
		Content(string(content)).
		Uuid(uuid).
		Build()
	req := im.NewCreateMessageReqBuilder().ReceiveIdType(receiverType).Body(body).Build()
	resp, err := l.Channel.RawClient().Im.V1.Message.Create(ctx, req)
	if err != nil || resp.StatusCode == http.StatusTooManyRequests {
		return "", errMeetingUnconfirmed
	}
	if err := checkMeetingResponse("发送会后纪要", resp.ApiResp); err != nil {
		return "", err
	}
	if resp.Data == nil || strings.TrimSpace(value(resp.Data.MessageId)) == "" ||
		(receiverType == "chat_id" && resp.Data.ChatId != nil && *resp.Data.ChatId != recipient) {
		return "", errMeetingUnconfirmed
	}
	return *resp.Data.MessageId, nil
}
