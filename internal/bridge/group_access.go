package bridge

import (
	"context"
	"errors"
	"maps"
	"regexp"
	"slices"
)

type GroupAccessRequest struct {
	ChatID              string   `json:"chatId,omitempty"`
	ChatIDs             []string `json:"chatIds,omitempty"`
	Access              string   `json:"access,omitempty"`
	Mention             string   `json:"mention,omitempty"`
	IncludeMessageEvent bool     `json:"includeMessageEvent,omitempty"`
}

// Keep a batch within the existing 4 KiB local request limit.
const MaxGroupGrantBatch = 32

type GroupAccessView struct {
	DefaultRequiresMention bool               `json:"defaultRequiresMention"`
	JoinedAvailable        bool               `json:"joinedAvailable"`
	MessageScope           GroupMessageScope  `json:"messageScope,omitempty"`
	MessageEvent           string             `json:"messageEvent,omitempty"`
	Groups                 []GroupAccessEntry `json:"groups"`
}

type GroupAccessEntry struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Joined          bool   `json:"joined"`
	Allowed         bool   `json:"allowed"`
	AccessOverride  string `json:"accessOverride"`
	RequiresMention bool   `json:"requiresMention"`
	MentionOverride string `json:"mentionOverride"`
}

var groupChatIDPattern = regexp.MustCompile(`^oc_[A-Za-z0-9_-]{1,100}$`)

// LocalGroupAccess runs on the same receiver worker as Tick and connection changes.
func (e *Engine) LocalGroupAccess(ctx context.Context, request GroupAccessRequest) (GroupAccessView, error) {
	if err := request.validate(); err != nil {
		return GroupAccessView{}, err
	}
	if e.Store == nil || e.Config.AppID == "" {
		return GroupAccessView{}, errors.New("接收端没有绑定应用状态")
	}
	groups, appID := e.Store.groupAccess()
	if appID != e.Config.AppID {
		return GroupAccessView{}, errors.New("接收端状态未绑定到当前应用")
	}
	var joined []FeishuGroup
	var joinedAvailable bool
	var platform groupMessagePlatformStatus
	batch := len(request.ChatIDs) > 0
	if request.Access != "" || request.Mention != "" {
		if e.Config.access == nil {
			return GroupAccessView{}, errors.New("接收端群策略尚未初始化")
		}
		if batch {
			joined, joinedAvailable, platform = e.liveGroupDirectory(ctx, appID, request.IncludeMessageEvent)
			if !joinedAvailable {
				return GroupAccessView{}, errors.New("无法核验机器人所在群")
			}
			ids := make(map[string]bool, len(joined))
			for _, group := range joined {
				ids[group.ID] = true
			}
			for _, id := range request.ChatIDs {
				if !ids[id] {
					return GroupAccessView{}, errors.New("所选群包含机器人未加入的群")
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return GroupAccessView{}, err
		}
		published, err := e.Store.updatePublished(func(st *State) error {
			if batch {
				for _, id := range request.ChatIDs {
					if !st.GroupAccess.allows(e.Config.Chats, id) {
						st.GroupAccess.setAllowed(id, true)
					}
				}
			} else {
				request.apply(&st.GroupAccess)
			}
			return nil
		})
		if published {
			groups, _ = e.Store.groupAccess()
			e.Config.access.publishGroups(groups)
		}
		if err != nil {
			return GroupAccessView{}, err
		}
	}

	if !batch {
		joined, joinedAvailable, platform = e.liveGroupDirectory(ctx, appID, request.IncludeMessageEvent)
	}
	view := groups.view(e.Config.Chats, joined, joinedAvailable)
	view.MessageScope = platform.scope
	view.MessageEvent = platform.event
	return view, nil
}

func (e *Engine) liveGroupDirectory(ctx context.Context, appID string, includeMessageEvent bool) ([]FeishuGroup, bool, groupMessagePlatformStatus) {
	l, ok := e.Lark.(*Lark)
	if !ok || l == nil || l.Channel == nil || l.Store != e.Store || l.Config.AppID != appID {
		return nil, false, unknownGroupMessagePlatformStatus(includeMessageEvent)
	}
	platform := make(chan groupMessagePlatformStatus, 1)
	go func() { platform <- l.groupMessagePlatformStatus(ctx, includeMessageEvent) }()
	joined, err := listFeishuGroups(ctx, l.Channel.RawClient())
	return joined, err == nil, <-platform
}

func (r GroupAccessRequest) validate() error {
	if r.ChatIDs != nil {
		return r.ValidateBatchGrant()
	}
	if r.ChatID == "" {
		if r.Access != "" || !slices.Contains([]string{"", "on", "off", "inherit"}, r.Mention) {
			return errors.New("应用级群策略只支持默认 @ 模式 on、off 或 inherit")
		}
		return nil
	}
	if !groupChatIDPattern.MatchString(r.ChatID) {
		return errors.New("群 ID 必须为有效的 oc_ 标识")
	}
	if r.Access == "" && r.Mention == "" {
		return errors.New("请指定群授权或群 @ 模式")
	}
	if !slices.Contains([]string{"", "allow", "deny", "inherit"}, r.Access) {
		return errors.New("群授权模式必须为 allow、deny 或 inherit")
	}
	if !slices.Contains([]string{"", "on", "off", "inherit"}, r.Mention) {
		return errors.New("群 @ 模式必须为 on、off 或 inherit")
	}
	return nil
}

func (r GroupAccessRequest) ValidateBatchGrant() error {
	if r.ChatID != "" || r.Access != "allow" || r.Mention != "" {
		return errors.New("批量操作只支持群授权")
	}
	if len(r.ChatIDs) == 0 || len(r.ChatIDs) > MaxGroupGrantBatch {
		return errors.New("批量授权每次可选择 1 至 32 个群")
	}
	seen := make(map[string]bool, len(r.ChatIDs))
	for _, id := range r.ChatIDs {
		if !groupChatIDPattern.MatchString(id) || seen[id] {
			return errors.New("批量授权需要互不重复的完整 oc_ 群 ID")
		}
		seen[id] = true
	}
	return nil
}

func (r GroupAccessRequest) apply(groups *GroupAccess) {
	if r.ChatID == "" {
		if r.Mention == "inherit" {
			groups.RequireMention = nil
			return
		}
		required := r.Mention == "on"
		groups.RequireMention = &required
		return
	}
	switch r.Access {
	case "allow", "deny":
		groups.setAllowed(r.ChatID, r.Access == "allow")
	case "inherit":
		delete(groups.Chats, r.ChatID)
	}
	switch r.Mention {
	case "on", "off":
		groups.setMention(r.ChatID, r.Mention == "on")
	case "inherit":
		delete(groups.Mentions, r.ChatID)
	}
}

func (s *Store) groupAccess() (GroupAccess, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	groups := s.state.GroupAccess
	groups.Chats = maps.Clone(groups.Chats)
	groups.Mentions = maps.Clone(groups.Mentions)
	return groups, s.state.AccountID
}

func (g GroupAccess) view(chats []string, joined []FeishuGroup, joinedAvailable bool) GroupAccessView {
	memberships := make(map[string]string, len(joined))
	ids := make(map[string]bool, len(chats)+len(joined))
	for _, chat := range chats {
		ids[chat] = true
	}
	for chat := range g.Chats {
		ids[chat] = true
	}
	for chat := range g.Mentions {
		ids[chat] = true
	}
	for _, group := range joined {
		memberships[group.ID] = group.Name
		ids[group.ID] = true
	}
	view := GroupAccessView{
		DefaultRequiresMention: g.defaultRequiresMention(),
		JoinedAvailable:        joinedAvailable,
		Groups:                 make([]GroupAccessEntry, 0, len(ids)),
	}
	for _, chat := range slices.Sorted(maps.Keys(ids)) {
		name, member := memberships[chat]
		entry := GroupAccessEntry{
			ID: chat, Name: name, Joined: member,
			Allowed: g.allows(chats, chat), AccessOverride: "inherit",
			RequiresMention: g.requiresMention(chat), MentionOverride: "inherit",
		}
		if allowed, ok := g.Chats[chat]; ok {
			entry.AccessOverride = "deny"
			if allowed {
				entry.AccessOverride = "allow"
			}
		}
		if required, ok := g.Mentions[chat]; ok {
			entry.MentionOverride = "off"
			if required {
				entry.MentionOverride = "on"
			}
		}
		view.Groups = append(view.Groups, entry)
	}
	return view
}
