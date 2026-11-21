package bridge

import (
	"context"
	"time"
)

const (
	groupMessageCheckCommand      = "/config mention check"
	groupMessageAuthorizeCommand  = "/config mention authorize"
	groupMessageGrantFallback     = "授权卡片不可用。请在飞书开放平台为当前应用申请 im:message.group_msg，再发送 " + groupMessageCheckCommand + "。"
	groupMessageGrantPollInterval = 15 * time.Second
)

func groupMessageCommand(words []string) bool {
	return len(words) >= 3 && words[0] == "/config" && words[1] == "mention" && (words[2] == "check" || words[2] == "authorize")
}

func (e *Engine) groupMessageControl(ctx context.Context, m Message, cmd *SessionCommand, action string) {
	if !e.privateAdminAllowed(m) {
		cmd.Reply = "请由所有者或管理员在已授权私聊检查或申请群消息权限。"
		return
	}
	cmd.Kind, cmd.RouteID, cmd.Target = action, "", Target{}
	scope := readGroupMessageScope(ctx, e.Lark)
	cmd.Reply = groupMessageScopeNote(scope)
	if scope != GroupMessageScopeMissing {
		return
	}
	if action == groupMessageCheckCommand {
		cmd.Card = e.groupMessageAuthorizeCard()
		return
	}
	api, ok := e.Lark.(interface {
		beginGroupMessageGrant(context.Context) (groupMessageGrant, error)
	})
	if !ok {
		cmd.Card = nil
		cmd.Reply = "请在飞书开放平台为本应用申请 im:message.group_msg，再复查权限。"
		return
	}
	grant, err := api.beginGroupMessageGrant(ctx)
	if err != nil {
		cmd.Card = nil
		cmd.Reply = "未能生成补授权链接。请重试，或在飞书开放平台为本应用申请 im:message.group_msg。"
		return
	}
	cmd.GroupMessageGrant = &grant
	cmd.Reply = "请在飞书确认本应用的 im:message.group_msg 增量授权。本卡会自动核对权限，也可点「复查权限」。"
	cmd.Card = &CommandCard{
		AppID:   e.Config.AppID,
		Buttons: []CommandButton{{Label: "复查权限", Command: groupMessageCheckCommand}},
	}
}

// The card receipt is enough to resume the check after a restart. The URL is discarded on delivery.
func (e *Engine) pollGroupMessageGrants(ctx context.Context, st State) error {
	now := e.now()
	patcher, ok := e.Lark.(interface {
		patchCard(context.Context, string, string) error
	})
	if !ok || st.AccountID != e.Config.AppID || now.Before(e.nextGroupMessageGrantPoll) {
		return nil
	}
	e.nextGroupMessageGrantPoll = now.Add(groupMessageGrantPollInterval)

	pending := map[string]*SessionCommand{}
	updates := map[string]*SessionCommand{}
	for id, cmd := range st.Commands {
		if cmd == nil || cmd.GroupMessageGrantUntil.IsZero() {
			continue
		}
		sentAt, err := time.Parse(time.RFC3339Nano, cmd.ReplyAt)
		if err != nil || !now.Before(cmd.GroupMessageGrantUntil) ||
			cmd.Kind != groupMessageAuthorizeCommand || cmd.Card == nil || cmd.Card.AppID != st.AccountID ||
			cmd.Card.ReplyID == "" || !e.privateAdminAllowed(cmd.Source) {
			cmd.GroupMessageGrantUntil = time.Time{}
			updates[id] = cmd
		} else if !now.Before(sentAt.Add(groupMessageGrantPollInterval)) {
			pending[id] = cmd
		}
	}
	if len(pending) > 0 {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if readGroupMessageScope(ctx, e.Lark) == GroupMessageScopeGranted {
			for id, cmd := range pending {
				if ctx.Err() != nil {
					break
				}
				confirmed := *cmd
				card := *cmd.Card
				card.Buttons = nil
				confirmed.Card = &card
				confirmed.GroupMessageGrant = nil
				confirmed.GroupMessageGrantUntil = time.Time{}
				confirmed.Reply = groupMessageScopeNote(GroupMessageScopeGranted)
				if patcher.patchCard(ctx, card.ReplyID, commandCardJSON(&confirmed)) == nil {
					updates[id] = &confirmed
				}
			}
		}
	}
	if len(updates) == 0 {
		return nil
	}
	return e.Store.update(func(st *State) error {
		for id, cmd := range updates {
			saveCommandState(st, id, cmd)
		}
		return nil
	})
}

func (e *Engine) groupMessageAuthorizeCard() *CommandCard {
	return &CommandCard{
		AppID:   e.Config.AppID,
		Buttons: []CommandButton{{Label: "申请群消息权限", Command: groupMessageAuthorizeCommand}},
	}
}

// The setting is already committed. Persist the scope result with the first reply attempt.
func (e *Engine) finishGroupMessageScopeCheck(ctx context.Context, cmd *SessionCommand) {
	cmd.GroupMessageScopePending = false
	groups, _ := e.Store.groupAccess()
	if groups.defaultRequiresMention() {
		return
	}
	scope := readGroupMessageScope(ctx, e.Lark)
	cmd.Reply += "\n" + groupMessageScopeNote(scope)
	if scope == GroupMessageScopeMissing {
		cmd.Card = e.groupMessageAuthorizeCard()
	}
}

func readGroupMessageScope(ctx context.Context, messenger any) GroupMessageScope {
	if reader, ok := messenger.(interface {
		groupMessageScope(context.Context) GroupMessageScope
	}); ok {
		return reader.groupMessageScope(ctx)
	}
	return GroupMessageScopeUnknown
}

func groupMessageScopeNote(scope GroupMessageScope) string {
	switch scope {
	case GroupMessageScopeGranted:
		return "飞书已授予 im:message.group_msg。请用一条新的无 @ 群消息核对订阅和投递。"
	case GroupMessageScopeMissing:
		return "飞书尚未授予 im:message.group_msg，无 @ 群消息无法投递。请在管理员私聊发送 " + groupMessageAuthorizeCommand + " 补授权。"
	default:
		return "暂无法确认飞书的 im:message.group_msg。请在管理员私聊发送 " + groupMessageCheckCommand + " 复查。"
	}
}
