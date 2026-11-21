package bridge

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"

	channel "github.com/larksuite/channel-sdk-go"
	docx "github.com/larksuite/oapi-sdk-go/v3/service/docx/v1"
	drive "github.com/larksuite/oapi-sdk-go/v3/service/drive/v1"
	wiki "github.com/larksuite/oapi-sdk-go/v3/service/wiki/v2"
)

type CommentSource struct {
	FileToken string `json:"fileToken"`
	FileType  string `json:"fileType"`
	CommentID string `json:"commentId"`
	ReplyID   string `json:"replyId,omitempty"`
	Whole     bool   `json:"whole,omitempty"`
}

var documentToken = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func supportedCommentType(fileType string) bool {
	return slices.Contains([]string{"doc", "docx", "sheet", "file"}, fileType)
}

func (l *Lark) receiveComment(ctx context.Context, event *channel.CommentEvent) error {
	if !event.MentionedBot || event.Operator.OpenID == "" || !documentToken.MatchString(event.FileToken) ||
		!documentToken.MatchString(event.CommentID) || (event.ReplyID != "" && !documentToken.MatchString(event.ReplyID)) ||
		(event.FileType != "wiki" && !supportedCommentType(event.FileType)) {
		return nil
	}
	m := Message{
		ID:     "comment:" + stable(event.FileToken, event.CommentID, event.ReplyID),
		ChatID: "document:" + stable(event.FileToken), UserID: event.Operator.OpenID,
		Comment: &CommentSource{FileToken: event.FileToken, FileType: event.FileType, CommentID: event.CommentID, ReplyID: event.ReplyID},
	}
	if !l.Config.TargetAllowed(m, l.Config.Routes[l.Config.CommentRoute], l.Config.CommentRoute) {
		return nil
	}
	if bot := l.Channel.GetBotIdentity(ctx); bot != nil && bot.OpenID == event.Operator.OpenID {
		return nil
	}
	if err := l.Store.Accept(l.Config, m); err != nil {
		l.fail()
		return err
	}
	return nil
}

func (l *Lark) resolveComment(ctx context.Context, m Message) (Message, error) {
	ref := *m.Comment
	data := l.readComment(ctx, ref)
	if data == nil {
		res, err := l.Channel.RawClient().Wiki.V2.Space.GetNode(ctx, wiki.NewGetNodeSpaceReqBuilder().Token(ref.FileToken).Build())
		if err == nil && res != nil && res.Success() && res.Data != nil && res.Data.Node != nil {
			token, kind := value(res.Data.Node.ObjToken), value(res.Data.Node.ObjType)
			if documentToken.MatchString(token) && supportedCommentType(kind) {
				ref.FileToken, ref.FileType = token, kind
				data = l.readComment(ctx, ref)
			}
		}
	}
	if data == nil {
		return m, errors.New("无法读取原评论，请检查飞书应用在此文档的评论权限。")
	}
	ref.Whole = data.IsWhole != nil && *data.IsWhole
	m.Comment = &ref
	var replies []*drive.FileCommentReply
	if data.ReplyList != nil {
		replies = data.ReplyList.Replies
	}
	index := findCommentReply(replies, ref.ReplyID, m.UserID)
	if index < 0 && ref.ReplyID != "" && data.HasMore != nil && *data.HasMore {
		api := l.Channel.RawClient().Drive.V1.FileCommentReply
		for i, cursor := 0, value(data.PageToken); i < 5 && cursor != ""; i++ {
			req := drive.NewListFileCommentReplyReqBuilder().FileToken(ref.FileToken).FileType(ref.FileType).
				CommentId(ref.CommentID).UserIdType("open_id").PageSize(50).PageToken(cursor).Build()
			page, err := api.List(ctx, req)
			if err != nil || page == nil || !page.Success() || page.Data == nil {
				break
			}
			replies = append(replies, page.Data.Items...)
			index = findCommentReply(replies, ref.ReplyID, m.UserID)
			if index >= 0 || page.Data.HasMore == nil || !*page.Data.HasMore {
				break
			}
			cursor = value(page.Data.PageToken)
		}
	}
	if index < 0 {
		// Do not use somebody else's later reply as the question.
		return m, nil
	}
	if ref.ReplyID == "" && len(replies) == 1 && (data.HasMore == nil || !*data.HasMore) {
		ref.ReplyID = value(replies[index].ReplyId)
	}
	m.Text = display(commentReplyText(replies[index]), 5000)
	prior := make([]string, 0, 8)
	for _, reply := range replies[max(0, index-8):index] {
		if text := commentReplyText(reply); text != "" {
			prior = append(prior, display(text, 600))
		}
	}
	m.Quote = display("飞书云文档评论。文档类型："+ref.FileType+"，文档标识："+ref.FileToken+
		"。最终回答会作为文档评论，对有文档访问权的读者可见。只回答已授权的问题，勿披露文档范围之外的信息。"+
		"\n原文选区："+value(data.Quote)+"\n同一评论的前文：\n"+strings.Join(prior, "\n"), 3500)
	if ref.FileType == "docx" {
		body, err := l.Channel.RawClient().Docx.V1.Document.RawContent(ctx, docx.NewRawContentDocumentReqBuilder().DocumentId(ref.FileToken).Build())
		if err == nil && body != nil && body.Success() && body.Data != nil && value(body.Data.Content) != "" {
			m.Quote += "\n文档正文（开头最多 6000 字）：\n" + display(value(body.Data.Content), 6000)
		} else {
			m.Quote += "\n文档正文不可读取，现有资料仅包含评论与选区。"
		}
	}
	return m, nil
}

func (l *Lark) readComment(ctx context.Context, ref CommentSource) *drive.FileComment {
	if !supportedCommentType(ref.FileType) {
		return nil
	}
	api := l.Channel.RawClient().Drive.V1.FileComment
	get, err := api.Get(ctx, drive.NewGetFileCommentReqBuilder().FileToken(ref.FileToken).FileType(ref.FileType).
		CommentId(ref.CommentID).UserIdType("open_id").Build())
	if err == nil && get != nil && get.Success() && get.Data != nil && value(get.Data.CommentId) == ref.CommentID {
		item := drive.FileComment(*get.Data)
		return &item
	}
	query := drive.NewBatchQueryFileCommentReqBuilder().FileToken(ref.FileToken).FileType(ref.FileType).UserIdType("open_id").
		Body(drive.NewBatchQueryFileCommentReqBodyBuilder().CommentIds([]string{ref.CommentID}).Build()).Build()
	r, err := api.BatchQuery(ctx, query)
	if err == nil && r != nil && r.Success() && r.Data != nil {
		for _, item := range r.Data.Items {
			if item != nil && value(item.CommentId) == ref.CommentID {
				return item
			}
		}
	}
	return nil
}

func findCommentReply(replies []*drive.FileCommentReply, id, author string) int {
	for i := len(replies) - 1; i >= 0; i-- {
		reply := replies[i]
		if reply != nil && (value(reply.UserId) == author || (id != "" && value(reply.UserId) == "")) &&
			(id == "" || value(reply.ReplyId) == id) {
			return i
		}
	}
	return -1
}

func commentReplyText(reply *drive.FileCommentReply) string {
	if reply == nil || reply.Content == nil {
		return ""
	}
	var body strings.Builder
	for _, el := range reply.Content.Elements {
		if el == nil {
			continue
		}
		switch value(el.Type) {
		case "text_run":
			if el.TextRun != nil {
				body.WriteString(value(el.TextRun.Text))
			}
		case "docs_link":
			if el.DocsLink != nil {
				body.WriteString(value(el.DocsLink.Url))
			}
		}
	}
	return strings.TrimSpace(body.String())
}

func (l *Lark) replyComment(ctx context.Context, ref CommentSource, text string) (string, error) {
	api := l.Channel.RawClient().Drive.V1
	content := drive.NewReplyContentBuilder().Elements([]*drive.ReplyElement{
		drive.NewReplyElementBuilder().Type("text_run").TextRun(drive.NewTextRunBuilder().Text(display(text, 2000)).Build()).Build(),
	}).Build()
	if !ref.Whole {
		req := drive.NewCreateFileCommentReplyReqBuilder().FileToken(ref.FileToken).FileType(ref.FileType).CommentId(ref.CommentID).
			Body(drive.NewCreateFileCommentReplyReqBodyBuilder().Content(content).Build()).Build()
		r, err := api.FileCommentReply.Create(ctx, req)
		if err == nil && r != nil && r.Success() && r.Data != nil && value(r.Data.ReplyId) != "" {
			return value(r.Data.ReplyId), nil
		}
		if err != nil || r == nil || r.Code != 1069302 {
			return "", errors.New("comment reply was not confirmed")
		}
	}
	// Whole-document comments are a flat list in Feishu.
	body := drive.NewFileCommentBuilder().ReplyList(&drive.ReplyList{Replies: []*drive.FileCommentReply{{Content: content}}}).Build()
	req := drive.NewCreateFileCommentReqBuilder().FileToken(ref.FileToken).FileType(ref.FileType).FileComment(body).Build()
	r, err := api.FileComment.Create(ctx, req)
	if err == nil && r != nil && r.Success() && r.Data != nil && value(r.Data.CommentId) != "" {
		return value(r.Data.CommentId), nil
	}
	return "", errors.New("document comment was not confirmed")
}
