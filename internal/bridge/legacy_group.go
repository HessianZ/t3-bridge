package bridge

import (
	"context"
	"errors"
	"time"
)

type legacyGroupThreadReader interface {
	verifyLegacySession(context.Context, Target, string) error
	readLegacyGroupEvidence(context.Context, Target, string, Message, string) (legacyGroupEvidence, error)
	verifyLegacyGroupEvidence(context.Context, Target, string, string) error
}

type legacyGroupFeishuReader interface {
	legacyGroupBotID(context.Context) (string, error)
	verifyLegacyGroupMessages(context.Context, Message, []string) error
}

func (e *Engine) verifyLegacyGroup(ctx context.Context, t *Task) error {
	seed := t.LegacyGroupSeed
	thread, threadAvailable := e.T3.(legacyGroupThreadReader)
	feishu, feishuAvailable := e.Lark.(legacyGroupFeishuReader)
	if !threadAvailable || !feishuAvailable || !sameWorkspacePath(seed.ProjectPath, t.Target.ProjectPath) {
		return errors.New("legacy group verification unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := thread.verifyLegacySession(ctx, t.Target, seed.ThreadID); err != nil {
		return err
	}
	bot, err := feishu.legacyGroupBotID(ctx)
	if err != nil {
		return err
	}
	proof, err := thread.readLegacyGroupEvidence(ctx, t.Target, seed.ThreadID, t.Source, bot)
	if err != nil {
		return err
	}
	if err := feishu.verifyLegacyGroupMessages(ctx, t.Source, proof.MessageIDs); err != nil {
		return err
	}
	return thread.verifyLegacyGroupEvidence(ctx, t.Target, seed.ThreadID, proof.Revision)
}

func (l *Lark) legacyGroupBotID(ctx context.Context) (string, error) {
	if bot := l.Channel.GetBotIdentity(ctx); bot != nil && validLegacySessionScopeID(bot.OpenID, "ou_") {
		return bot.OpenID, nil
	}
	return "", errors.New("cannot verify the receiver bot identity")
}

func (l *Lark) verifyLegacyGroupMessages(ctx context.Context, scope Message, ids []string) error {
	for _, id := range ids {
		items, err := l.messageItems(ctx, id)
		if err != nil || !messageScope(items[0], scope, false) {
			return errors.New("legacy Feishu message is unavailable in the selected scope")
		}
		message := items[0]
		sender := message.Sender
		if value(message.MessageId) != id || sender == nil || value(sender.IdType) != "open_id" ||
			value(sender.SenderType) != "user" || value(sender.Id) != scope.UserID {
			return errors.New("legacy Feishu author does not match the selected user")
		}
	}
	return nil
}
