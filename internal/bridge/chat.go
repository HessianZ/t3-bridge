package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	im "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

var errChatRejected = errors.New("chat creation rejected")

func (l *Lark) ReplyControl(ctx context.Context, m Message, content string, card bool) error {
	if card {
		return l.replyWithUUID(ctx, m, "interactive", content)
	}
	return l.replyAccess(ctx, m, display(content, 1800))
}

func (l *Lark) replyMessage(ctx context.Context, m Message, replyTo, msgType, content string) (*im.ReplyMessageResp, error) {
	body := im.NewReplyMessageReqBodyBuilder().MsgType(msgType).Content(content).Uuid(stable("access", key(m, m.ID))).ReplyInThread(m.TopicID != "").Build()
	return l.Channel.RawClient().Im.V1.Message.Reply(ctx, im.NewReplyMessageReqBuilder().MessageId(replyTo).Body(body).Build())
}

func (l *Lark) ReplyCommandCard(ctx context.Context, m Message, content string, card bool) (string, error) {
	replyTo := m.ID
	if m.CommandAction != "" {
		replyTo = m.ParentID
	}
	msgType := "interactive"
	if !card {
		msgType = "text"
		body, _ := json.Marshal(map[string]string{"text": display(content, 1800)})
		content = string(body)
	}
	r, err := l.replyMessage(ctx, m, replyTo, msgType, content)
	if err != nil || r == nil {
		return "", errors.New("command reply unconfirmed")
	}
	if !r.Success() {
		if card {
			return "", errCommandCardRejected
		}
		return "", errors.New("command text reply rejected")
	}
	if !card {
		return "", nil
	}
	if r.Data == nil || value(r.Data.MessageId) == "" ||
		(value(r.Data.ParentId) != "" && value(r.Data.ParentId) != replyTo) ||
		(value(r.Data.ChatId) != "" && value(r.Data.ChatId) != m.ChatID) ||
		(value(r.Data.ThreadId) != "" && value(r.Data.ThreadId) != m.TopicID) {
		return "", errors.New("command card reply scope unconfirmed")
	}
	return value(r.Data.MessageId), nil
}
func (l *Lark) CreatePrivateChat(ctx context.Context, m Message, name, uuid string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	body := im.NewCreateChatReqBodyBuilder().Name(name).ChatMode("group").ChatType("private").UserIdList([]string{m.UserID}).Build()
	r, err := l.Channel.RawClient().Im.V1.Chat.Create(ctx, im.NewCreateChatReqBuilder().UserIdType("open_id").Uuid(uuid).Body(body).Build())
	if err != nil || r == nil {
		return "", errors.New("chat creation result unknown")
	}
	if !r.Success() {
		return "", errChatRejected
	}
	if r.Data == nil || value(r.Data.ChatId) == "" {
		return "", errors.New("chat creation identity unknown")
	}
	return *r.Data.ChatId, nil
}
func (l *Lark) WelcomeChat(ctx context.Context, chat, text, uuid string) error {
	b, _ := json.Marshal(map[string]string{"text": text})
	r, err := l.Channel.RawClient().Im.V1.Message.Create(ctx, im.NewCreateMessageReqBuilder().ReceiveIdType("chat_id").Body(im.NewCreateMessageReqBodyBuilder().ReceiveId(chat).MsgType("text").Content(string(b)).Uuid(uuid).Build()).Build())
	if err != nil || r == nil || !r.Success() {
		return errors.New("welcome result unknown")
	}
	return nil
}
