package bridge

import (
	"context"
	"time"
)

type commentReactionMessenger interface {
	updateCommentWorkingReaction(context.Context, CommentSource, bool)
}

func (e *Engine) notifyCommentWorkingReaction(ctx context.Context, t *Task) error {
	api, ok := e.Lark.(commentReactionMessenger)
	if !ok || t.ReceiverAppID != e.Config.AppID {
		return nil
	}
	ended := terminal(t.Phase) || t.StopRequested
	if ref := t.CommentReaction; ref != nil {
		if !ended {
			return nil
		}
		call, cancel := context.WithTimeout(ctx, time.Second)
		api.updateCommentWorkingReaction(call, *ref, false)
		cancel()
		t.CommentReaction = nil
		return e.commit(t)
	}
	m := replySource(t)
	if ended || t.Phase != "tracking" || t.RunID == "" || m.Comment == nil || m.Comment.ReplyID == "" || !scopedTask(e.Config, m, t, t.RouteID) {
		return nil
	}
	// Unlike IM, Drive deletes by reply and emoji. Keep the intended target even
	// if the first response is lost, so restart can still remove the marker.
	ref := *m.Comment
	t.CommentReaction = &ref
	if err := e.commit(t); err != nil {
		return err
	}
	call, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	api.updateCommentWorkingReaction(call, ref, true)
	return nil
}
