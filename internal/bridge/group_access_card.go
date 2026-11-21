package bridge

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

const groupAccessCardCommand = "/config groups"
const groupAccessCardPageSize = 6
const groupAccessCardStale = "群设置或卡片已变化，本次未保存。请重新发送 /config groups 查看。"

type groupAccessCardButton struct {
	Page      int                     `json:"page,omitempty"`
	ChatID    string                  `json:"chatId,omitempty"`
	Operation string                  `json:"operation,omitempty"`
	Snapshot  groupAccessCardSnapshot `json:"snapshot,omitzero"`
}

type groupAccessCardSnapshot struct {
	Allowed                bool   `json:"allowed"`
	AccessOverride         string `json:"accessOverride"`
	DefaultAllowed         bool   `json:"defaultAllowed"`
	RequiresMention        bool   `json:"requiresMention"`
	MentionOverride        string `json:"mentionOverride"`
	DefaultRequiresMention bool   `json:"defaultRequiresMention"`
}

func (e *Engine) groupAccessCardLark(accountID string) *Lark {
	l, ok := e.Lark.(*Lark)
	if !ok || l == nil || l.Channel == nil || e.Store == nil || l.Store != e.Store || e.Config.access == nil ||
		e.Config.AppID == "" || accountID != e.Config.AppID || l.Config.AppID != accountID ||
		cmp.Or(l.Config.Tenant, "feishu") != cmp.Or(e.Config.Tenant, "feishu") {
		return nil
	}
	return l
}

func (e *Engine) prepareGroupAccessCard(ctx context.Context, st *State, m Message, cmd *SessionCommand, origin *SessionCommand, button CommandButton) *groupAccessCardButton {
	cmd.Kind, cmd.RouteID, cmd.Target, cmd.BaseTargetID = groupAccessCardCommand, "", Target{}, ""
	cmd.Card = nil
	if !e.privateAdminAllowed(m) {
		cmd.Reply = "请由当前应用的所有者或管理员在已授权私聊发送 /config groups。"
		return nil
	}
	selection := groupAccessCardButton{Page: 1}
	if m.CommandAction != "" {
		if origin == nil || origin.Card == nil || origin.Kind != groupAccessCardCommand ||
			userScope(origin.Source) != userScope(m) || button.Command != groupAccessCardCommand || button.GroupAccess == nil ||
			(origin.Card.Used && button.GroupAccess.Operation != "") {
			cmd.Reply = groupAccessCardStale
			return nil
		}
		selection = *button.GroupAccess
	} else {
		if m.ParentID != "" || m.ControlTask != "" || m.SessionTask != "" {
			cmd.Reply = "请直接发送 /config groups 查看群授权。"
			return nil
		}
		const usage = "请发送 /config groups、/config groups <页码> 或 /config groups <完整群 ID>。"
		words := strings.Fields(routeText(m))
		if len(words) < 2 || len(words) > 3 || words[0] != "/config" || words[1] != "groups" {
			cmd.Reply = usage
			return nil
		}
		if len(words) == 3 {
			arg := words[2]
			if groupChatIDPattern.MatchString(arg) {
				selection.ChatID = arg
			} else {
				page, err := strconv.Atoi(arg)
				if err != nil || page < 1 || strings.HasPrefix(arg, "+") {
					cmd.Reply = usage
					return nil
				}
				selection.Page = page
			}
		}
	}
	l := e.groupAccessCardLark(st.AccountID)
	if l == nil {
		cmd.Reply = "接收端未绑定到当前飞书应用，无法管理群授权。"
		return nil
	}
	const readFailed = "无法核验机器人所在群。请重新发送 /config groups。"
	client := l.Channel.RawClient()
	if client == nil {
		cmd.Reply = readFailed
		return nil
	}
	groups, err := listFeishuGroups(ctx, client)
	if err != nil || ctx.Err() != nil {
		cmd.Reply = readFailed
		return nil
	}
	for _, group := range groups {
		if !groupChatIDPattern.MatchString(group.ID) {
			cmd.Reply = readFailed
			return nil
		}
	}
	if selection.ChatID == "" && selection.Operation == "" {
		e.groupAccessCardList(st, cmd, groups, max(1, selection.Page), m.CommandAction != "")
		return nil
	}
	index := slices.IndexFunc(groups, func(group FeishuGroup) bool { return group.ID == selection.ChatID })
	if index < 0 {
		cmd.Reply = "机器人当前未加入所选群。请重新发送 /config groups 查看。"
		return nil
	}
	if selection.Operation != "" {
		return &selection
	}
	e.groupAccessCardDetail(st, cmd, groups[index], max(1, selection.Page))
	return nil
}

func (e *Engine) groupAccessCardList(st *State, cmd *SessionCommand, groups []FeishuGroup, page int, fromCard bool) {
	if len(groups) == 0 {
		cmd.Reply = "机器人当前没有加入任何群。"
		return
	}
	pages := (len(groups) + groupAccessCardPageSize - 1) / groupAccessCardPageSize
	if page > pages && !fromCard {
		cmd.Reply = fmt.Sprintf("页码超出范围，共 %d 页。请发送 /config groups 查看。", pages)
		return
	}
	page = min(page, pages)
	slices.SortFunc(groups, func(a, b FeishuGroup) int { return strings.Compare(a.ID, b.ID) })
	cmd.Card = &CommandCard{AppID: e.Config.AppID}
	var reply strings.Builder
	fmt.Fprintf(&reply, "机器人所在群 · 第 %d/%d 页", page, pages)
	for i, group := range groups[(page-1)*groupAccessCardPageSize : min(page*groupAccessCardPageSize, len(groups))] {
		name := groupAccessCardName(group)
		allowed := st.GroupAccess.allows(e.Config.Chats, group.ID)
		fmt.Fprintf(&reply, "\n%d. %s · %s · %s", i+1, name, group.ID, groupAccessCardAccessLabel(allowed))
		cmd.Card.Buttons = append(cmd.Card.Buttons, CommandButton{
			Label: fmt.Sprintf("查看 %d · %s", i+1, name), Command: groupAccessCardCommand,
			GroupAccess: &groupAccessCardButton{Page: page, ChatID: group.ID},
		})
	}
	reply.WriteString("\n群详情 · /config groups <完整群 ID>")
	if page > 1 {
		fmt.Fprintf(&reply, "\n上一页 · %s %d", groupAccessCardCommand, page-1)
		cmd.Card.Buttons = append(cmd.Card.Buttons, CommandButton{
			Label: "上一页", Command: groupAccessCardCommand, GroupAccess: &groupAccessCardButton{Page: page - 1},
		})
	}
	if page < pages {
		fmt.Fprintf(&reply, "\n下一页 · %s %d", groupAccessCardCommand, page+1)
		cmd.Card.Buttons = append(cmd.Card.Buttons, CommandButton{
			Label: "下一页", Command: groupAccessCardCommand, GroupAccess: &groupAccessCardButton{Page: page + 1},
		})
	}
	cmd.Reply = reply.String()
}

func (e *Engine) groupAccessCardDetail(st *State, cmd *SessionCommand, group FeishuGroup, page int) {
	name := groupAccessCardName(group)
	snapshot := e.groupAccessCardSnapshot(st, group.ID)
	cmd.Reply = fmt.Sprintf("%s · %s\n群授权 · %s（覆盖 %s）\n需要 @ · %s（覆盖 %s）", name, group.ID,
		groupAccessCardAccessLabel(snapshot.Allowed), snapshot.AccessOverride, runSettingsOnOff(snapshot.RequiresMention), snapshot.MentionOverride)
	cmd.Reply += fmt.Sprintf("\n配置默认群授权 · %s\n应用默认 @ · %s", groupAccessCardAccessLabel(snapshot.DefaultAllowed), runSettingsOnOff(snapshot.DefaultRequiresMention))
	cmd.Reply += "\n文字操作请到目标群 @机器人发送 /invite group、/remove group、/config mention on、/config mention off 或 /config mention default。"
	cmd.Reply += fmt.Sprintf("\n返回列表 · %s %d", groupAccessCardCommand, page)
	cmd.Card = &CommandCard{AppID: e.Config.AppID}
	for _, action := range []struct{ operation, label string }{
		{"allow", "授权"}, {"deny", "撤销授权"}, {"inherit", "恢复配置默认"},
		{"mention on", "必须 @"}, {"mention off", "允许无 @"}, {"mention inherit", "@ 跟随应用默认"},
	} {
		cmd.Card.Buttons = append(cmd.Card.Buttons, CommandButton{
			Label: action.label + "「" + name + "」", Command: groupAccessCardCommand,
			GroupAccess: &groupAccessCardButton{ChatID: group.ID, Operation: action.operation, Snapshot: snapshot},
		})
	}
	cmd.Card.Buttons = append(cmd.Card.Buttons, CommandButton{
		Label: "返回列表", Command: groupAccessCardCommand, GroupAccess: &groupAccessCardButton{Page: page},
	})
}

func groupAccessCardName(group FeishuGroup) string {
	return display(cmp.Or(strings.Join(strings.Fields(group.Name), " "), group.ID), 36)
}

func groupAccessCardAccessLabel(allowed bool) string {
	if allowed {
		return "已授权"
	}
	return "未授权"
}

func (e *Engine) groupAccessCardSnapshot(st *State, chatID string) groupAccessCardSnapshot {
	groups := st.GroupAccess
	snapshot := groupAccessCardSnapshot{
		Allowed: groups.allows(e.Config.Chats, chatID), AccessOverride: "inherit",
		RequiresMention: groups.requiresMention(chatID), MentionOverride: "inherit",
		DefaultAllowed: slices.Contains(e.Config.Chats, chatID), DefaultRequiresMention: groups.defaultRequiresMention(),
	}
	if allowed, ok := groups.Chats[chatID]; ok {
		snapshot.AccessOverride = "deny"
		if allowed {
			snapshot.AccessOverride = "allow"
		}
	}
	if required, ok := groups.Mentions[chatID]; ok {
		snapshot.MentionOverride = runSettingsOnOff(required)
	}
	return snapshot
}

// The joined-group lookup ran in prepare. Revalidate the card and policy in the save transaction.
func (e *Engine) applyGroupAccessCard(st *State, m Message, cmd *SessionCommand, change *groupAccessCardButton) bool {
	cmd.Reply = groupAccessCardStale
	if change == nil || change.ChatID == "" || change.Operation == "" || m.CommandAction == "" ||
		e.groupAccessCardLark(st.AccountID) == nil || !e.privateAdminAllowed(m) {
		return false
	}
	origin, button := commandCardAction(e.Config, st, m)
	if origin == nil || origin.Card.Used || origin.Kind != groupAccessCardCommand || userScope(origin.Source) != userScope(m) ||
		button.Command != groupAccessCardCommand || button.GroupAccess == nil || *button.GroupAccess != *change ||
		e.groupAccessCardSnapshot(st, change.ChatID) != change.Snapshot {
		return false
	}
	request := GroupAccessRequest{ChatID: change.ChatID}
	if mention, ok := strings.CutPrefix(change.Operation, "mention "); ok {
		request.Mention = mention
	} else {
		request.Access = change.Operation
	}
	if request.validate() != nil {
		return false
	}
	request.apply(&st.GroupAccess)
	origin.Card.Used = true
	cmd.Reply = "已保存群设置。请重新发送 /config groups 查看。"
	if request.Mention != "" && !st.GroupAccess.requiresMention(request.ChatID) {
		cmd.Reply += "\n可发送 /config mention check 核对飞书无 @ 群消息权限。"
	}
	return true
}
