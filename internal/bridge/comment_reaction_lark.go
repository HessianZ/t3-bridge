package bridge

import (
	"context"

	drive "github.com/larksuite/oapi-sdk-go/v3/service/drive/v2"
)

func (l *Lark) updateCommentWorkingReaction(ctx context.Context, ref CommentSource, active bool) {
	if !supportedCommentType(ref.FileType) || !documentToken.MatchString(ref.FileToken) ||
		!documentToken.MatchString(ref.ReplyID) || (ref.CommentID != "" && !documentToken.MatchString(ref.CommentID)) {
		return
	}
	action := "delete"
	if active {
		action = "add"
	}
	body := drive.NewUpdateReactionCommentReactionReqBodyBuilder().ReplyId(ref.ReplyID).ReactionType("Typing").Action(action).Build()
	req := drive.NewUpdateReactionCommentReactionReqBuilder().FileToken(ref.FileToken).FileType(ref.FileType).Body(body).Build()
	_, _ = l.Channel.RawClient().Drive.V2.CommentReaction.UpdateReaction(ctx, req)
}
