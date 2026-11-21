package bridge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	im "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func (l *Lark) messageItems(ctx context.Context, id string) ([]*im.Message, error) {
	req := im.NewGetMessageReqBuilder().MessageId(id).CardMsgContentType("user_card_content").Build()
	r, err := l.Channel.RawClient().Im.V1.Message.Get(ctx, req)
	if err != nil || r == nil || !r.Success() || r.Data == nil || len(r.Data.Items) == 0 || len(r.Data.Items) > 20 {
		return nil, errQuotedMessageUnavailable
	}
	return r.Data.Items, nil
}

// Children with omitted scope inherit only through a validated upper_message_id chain.
func messageScope(p *im.Message, m Message, child bool) bool {
	return p != nil && !(p.Deleted != nil && *p.Deleted) && (value(p.ChatId) == m.ChatID || (child && value(p.ChatId) == "")) && (value(p.ThreadId) == m.TopicID || (child && value(p.ThreadId) == ""))
}
func (l *Lark) readVisible(ctx context.Context, m Message, id string, retryAvailable *bool) (string, []Image, error) {
	items, err := l.quotedMessageItems(ctx, id, retryAvailable)
	if err != nil {
		return "", nil, err
	}
	if !messageScope(items[0], m, false) || value(items[0].MessageId) != id {
		return "", nil, errors.New("quoted message outside ACL scope")
	}
	allItems := append([]*im.Message{}, items...)
	seen := map[string]bool{}
	calls, rawBytes, textBytes := 1, 0, 0
	resources := []Image{}
	var walk func(*im.Message, []*im.Message, int) (string, error)
	walk = func(p *im.Message, rows []*im.Message, depth int) (string, error) {
		if !messageScope(p, m, depth > 0) || depth > 3 || seen[value(p.MessageId)] || value(p.MessageId) == "" || len(value(p.MessageId)) > 128 || len(seen) >= 20 {
			return "", errors.New("forward identity/scope/nesting outside bounds")
		}
		seen[value(p.MessageId)] = true
		if p.Body != nil && p.Body.Content != nil && len(*p.Body.Content) > maxQuoteRawBytes {
			return "", errors.New("forward body outside bounds")
		}
		if value(p.MsgType) == "merge_forward" {
			children := []*im.Message{}
			for _, v := range rows {
				if v != nil && value(v.UpperMessageId) == value(p.MessageId) {
					children = append(children, v)
				}
			}
			if len(children) == 0 && depth > 0 && calls < 4 {
				calls++
				extra, err := l.quotedMessageItems(ctx, value(p.MessageId), retryAvailable)
				if err != nil {
					return "", err
				}
				if value(extra[0].MessageId) != value(p.MessageId) || !messageScope(extra[0], m, true) {
					return "", errors.New("nested forward outside scope")
				}
				allItems = append(allItems, extra...)
				rows = extra
				for _, v := range rows {
					if v != nil && value(v.UpperMessageId) == value(p.MessageId) {
						children = append(children, v)
					}
				}
			}
			if len(children) == 0 {
				return "", errors.New("forward children missing")
			}
			parts := []string{}
			for _, v := range children {
				part, err := walk(v, rows, depth+1)
				if err != nil {
					return "", err
				}
				parts = append(parts, part)
			}
			return strings.Join(parts, "\n"), nil
		}
		if p.Body == nil || p.Body.Content == nil {
			return "", errors.New("visible content missing")
		}
		rawBytes += len(*p.Body.Content)
		if rawBytes > 65536 {
			return "", errors.New("forward raw bytes outside bounds")
		}
		text, files, err := quotedContent(value(p.MsgType), *p.Body.Content, value(p.MessageId))
		if err != nil {
			return "", err
		}
		resources = append(resources, files...)
		textBytes += len(text)
		if len(resources) > maxTurnAttachments || textBytes > maxQuoteTextBytes {
			return "", errors.New("forward content outside bounds")
		}
		if depth == 0 {
			return text, nil
		}
		sender := ""
		if p.Sender != nil {
			sender = value(p.Sender.Id)
		}
		return fmt.Sprintf("[转发来源：%s · %s]\n%s", value(p.MessageId), sender, text), nil
	}
	text, err := walk(items[0], items, 0)
	// Reject orphan/partially fetched flat responses rather than silently losing content.
	if err == nil {
		for _, p := range allItems {
			if p == nil || !seen[value(p.MessageId)] {
				err = errors.New("forward response contains unlinked items")
				break
			}
		}
	}
	if len(text) > maxQuoteTextBytes {
		err = errors.New("visible context outside bounds")
	}
	return text, resources, err
}
func (l *Lark) TopicContext(ctx context.Context, m Message) (string, error) {
	req := im.NewListMessageReqBuilder().ContainerIdType("thread").ContainerId(m.TopicID).
		SortType("ByCreateTimeDesc").PageSize(8).CardMsgContentType("user_card_content").Build()
	r, err := l.Channel.RawClient().Im.V1.Message.List(ctx, req)
	if err != nil || r == nil || !r.Success() || r.Data == nil {
		return "", errors.New("topic context unavailable under current permissions")
	}
	if len(r.Data.Items) > 8 {
		return "", errors.New("topic context outside bounds")
	}
	parts := []string{}
	size := 0
	for i := len(r.Data.Items) - 1; i >= 0; i-- {
		p := r.Data.Items[i]
		if !messageScope(p, m, false) {
			return "", errors.New("topic context outside scope")
		}
		id := value(p.MessageId)
		if id == "" || len(id) > 128 {
			return "", errors.New("topic source identity unavailable")
		}
		if id == m.ID || id == m.ParentID || slices.Contains(m.MergedIDs, id) {
			continue
		}
		if p.Body == nil || p.Body.Content == nil {
			return "", errors.New("topic content missing")
		}
		text, files, e := quotedContent(value(p.MsgType), *p.Body.Content, id)
		if e != nil || len(files) > 0 {
			text = "[该前文类型/附件未展开]"
		}
		size += len(text)
		if size > 6000 {
			return "", errors.New("topic context outside bounds")
		}
		parts = append(parts, "[话题来源："+id+"]\n"+text)
	}
	return "仅最近最多8条可见话题文字（非完整历史，附件未自动读取）：\n" + strings.Join(parts, "\n"), nil
}
func (e *Engine) initialHistory(ctx context.Context, st *State, m Message, target Target) string {
	if m.TopicID == "" {
		return ""
	}
	for _, t := range st.Tasks {
		if userScope(t.Source) == userScope(m) && targetIdentity(t.Target) == targetIdentity(target) && t.ThreadID != "" {
			return ""
		}
	}
	if api, ok := e.Lark.(interface {
		TopicContext(context.Context, Message) (string, error)
	}); ok {
		if history, err := api.TopicContext(ctx, m); err == nil {
			return history
		}
		// Locally received material is not a substitute for unseen group history.
		parts := []string{}
		tasks := []*Task{}
		for _, t := range st.Tasks {
			tasks = append(tasks, t)
		}
		slices.SortFunc(tasks, func(a, b *Task) int {
			if n := strings.Compare(a.Updated, b.Updated); n != 0 {
				return n
			}
			return strings.Compare(a.ID, b.ID)
		})
		for _, t := range tasks {
			if userScope(t.Source) != userScope(m) || targetIdentity(t.Target) != targetIdentity(target) {
				continue
			}
			for _, r := range append(t.Replies, currentReply(t)) {
				if r.InputID == m.ID || r.InputID == m.ParentID || slices.Contains(m.MergedIDs, r.InputID) {
					continue
				}
				parts = append(parts, display(r.Source.Text, 400))
			}
		}
		if len(parts) > 8 {
			parts = parts[len(parts)-8:]
		}
		return "话题前文未取得（现有权限或可见范围不足）；当前输入仍可处理。以下仅本地已接收文字，不含未见历史：\n" + strings.Join(parts, "\n")
	}
	return ""
}
