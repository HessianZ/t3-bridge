package bridge

import (
	"context"
	"strings"
	"time"

	im "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type workingReactionReceipt struct {
	MessageID  string `json:"messageId"`
	ReactionID string `json:"reactionId,omitempty"`
}

type workingReactionMessenger interface {
	addWorkingReaction(context.Context, string) string
	removeWorkingReaction(context.Context, string, string)
}

func (e *Engine) notifyWorkingReaction(ctx context.Context, t *Task) error {
	api, ok := e.Lark.(workingReactionMessenger)
	if !ok || t.ReceiverAppID != e.Config.AppID {
		return nil
	}
	ended := terminal(t.Phase) || t.StopRequested
	if r := t.WorkingReaction; r != nil {
		if !ended {
			return nil
		}
		if r.ReactionID != "" {
			call, cancel := context.WithTimeout(ctx, time.Second)
			api.removeWorkingReaction(call, r.MessageID, r.ReactionID)
			cancel()
		}
		t.WorkingReaction = nil
		return e.commit(t)
	}
	m := replySource(t)
	if ended || t.Phase != "tracking" || t.RunID == "" || !t.TextReply || t.Process != nil ||
		!strings.HasPrefix(m.ID, "om_") || m.Comment != nil || m.Meeting != nil || m.MeetingSummary != nil ||
		m.InputKind == "automatic" || m.InputKind == "form" || !scopedTask(e.Config, m, t, t.RouteID) {
		return nil
	}
	// The decoration must not be posted again when its first response is lost.
	t.WorkingReaction = &workingReactionReceipt{MessageID: m.ID}
	if err := e.commit(t); err != nil {
		return err
	}
	call, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if id := api.addWorkingReaction(call, m.ID); id != "" {
		t.WorkingReaction.ReactionID = id
		return e.commit(t)
	}
	return nil
}

func (l *Lark) addWorkingReaction(ctx context.Context, messageID string) string {
	reactions := l.Channel.RawClient().Im.V1.MessageReaction
	body := im.NewCreateMessageReactionReqBodyBuilder().ReactionType(im.NewEmojiBuilder().EmojiType("Typing").Build()).Build()
	result, err := reactions.Create(ctx, im.NewCreateMessageReactionReqBuilder().MessageId(messageID).Body(body).Build())
	if err != nil || result == nil || !result.Success() || result.Data == nil {
		return ""
	}
	return value(result.Data.ReactionId)
}

func (l *Lark) removeWorkingReaction(ctx context.Context, messageID, reactionID string) {
	reactions := l.Channel.RawClient().Im.V1.MessageReaction
	_, _ = reactions.Delete(ctx, im.NewDeleteMessageReactionReqBuilder().MessageId(messageID).ReactionId(reactionID).Build())
}
