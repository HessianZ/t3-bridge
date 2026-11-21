package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	channel "github.com/larksuite/channel-sdk-go"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	application "github.com/larksuite/oapi-sdk-go/v3/service/application/v6"
)

// Overrides include false so removing a statically configured group survives restart.
type GroupAccess struct {
	Chats          map[string]bool `json:"chats,omitempty"`
	Mentions       map[string]bool `json:"mentions,omitempty"`
	RequireMention *bool           `json:"requireMention,omitempty"`
}

func (g GroupAccess) defaultRequiresMention() bool {
	return g.RequireMention == nil || *g.RequireMention
}

func (g GroupAccess) allows(chats []string, chat string) bool {
	if allowed, ok := g.Chats[chat]; ok {
		return allowed
	}
	return slices.Contains(chats, chat)
}

func (g GroupAccess) requiresMention(chat string) bool {
	if required, ok := g.Mentions[chat]; ok {
		return required
	}
	return g.defaultRequiresMention()
}

func (g *GroupAccess) setAllowed(chat string, allowed bool) {
	if g.Chats == nil {
		g.Chats = map[string]bool{}
	}
	g.Chats[chat] = allowed
}

func (g *GroupAccess) setMention(chat string, required bool) {
	if g.Mentions == nil {
		g.Mentions = map[string]bool{}
	}
	g.Mentions[chat] = required
}

type accessRuntime struct {
	mu               sync.RWMutex
	owner            string
	nextOwnerRefresh time.Time
	groups           GroupAccess
	policy           AccessPolicy
}

func (c Config) isAdmin(user string) bool {
	if user == "" {
		return false
	}
	if c.access != nil {
		c.access.mu.RLock()
		defer c.access.mu.RUnlock()
		if c.access.owner == user {
			return true
		}
		if allowed, known := c.access.policy.Admins[user]; known {
			return allowed
		}
	}
	return slices.Contains(c.Admins, user)
}

func (c Config) groupAllowed(chat string) bool {
	c.access.mu.RLock()
	defer c.access.mu.RUnlock()
	return c.access.groups.allows(c.Chats, chat)
}
func (c Config) requireMention(chat string) bool {
	c.access.mu.RLock()
	defer c.access.mu.RUnlock()
	return c.access.groups.requiresMention(chat)
}

func (a *accessRuntime) publishGroups(groups GroupAccess) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.groups = groups
}

// Refresh only the current app's owner_id. Never infer management rights from Users or creator_id.
func (l *Lark) RefreshOwner(ctx context.Context) {
	a := l.Config.access
	a.mu.RLock()
	due := time.Now().After(a.nextOwnerRefresh)
	a.mu.RUnlock()
	if !due {
		return
	}
	owner := l.lookupAppOwner(ctx)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.owner = owner
	delay := 30 * time.Minute
	if owner == "" {
		delay = time.Minute
	}
	a.nextOwnerRefresh = time.Now().Add(delay)
}

func (l *Lark) lookupAppOwner(ctx context.Context) string {
	return lookupFeishuAppOwner(ctx, l.Channel.RawClient(), l.Config.AppID)
}

func lookupFeishuAppOwner(ctx context.Context, client *lark.Client, appID string, options ...larkcore.RequestOptionFunc) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r, err := client.Application.V6.Application.Get(ctx, application.NewGetApplicationReqBuilder().AppId(appID).Lang("zh_cn").UserIdType("open_id").Build(), options...)
	if err == nil && r != nil && r.Success() && r.Data != nil && r.Data.App != nil && r.Data.App.Owner != nil {
		return value(r.Data.App.Owner.OwnerId)
	}
	return ""
}

func (l *Lark) receive(ctx context.Context, n *channel.NormalizedMessage) error {
	if !slices.Contains([]string{"text", "post", "image", "file", "merge_forward"}, n.RawContentType) {
		return nil
	}
	m := Normalize(n)
	m.ReceivedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if m.ChatType == "group" {
		if !n.MentionedBot && m.ParentID == "" && l.Config.requireMention(m.ChatID) {
			return nil
		}
		// Check sender and group access before doing any message lookup. The full
		// topic ACL below uses the resolved scope.
		coarse := l.Config
		coarse.Topics = nil
		if coarse.Allowed(m) && !l.resolveGroupTopic(ctx, &m) {
			return nil
		}
	}
	if len(m.Images)+len(m.Files) > maxTurnAttachments {
		m.AttachmentIssue = fmt.Sprintf("附件合计最多%d项，请分批发送。未执行。", maxTurnAttachments)
		m.Images = nil
		m.Files = nil
	}
	if !l.Config.Allowed(m) {
		if m.ChatType == "group" && n.MentionedBot && !l.Config.groupAllowed(m.ChatID) {
			fresh := false
			err := l.Store.update(func(st *State) error {
				k := key(m, m.ID)
				if !st.Seen[k] {
					st.Seen[k] = true
					fresh = true
				}
				return nil
			})
			if err != nil {
				l.fail()
				return err
			}
			if fresh {
				return l.replyAccess(ctx, m, "本群尚未授权，请应用所有者或管理员 @机器人发送 /invite group。")
			}
		}
		return nil
	}
	if m.ChatType == "group" && !n.MentionedBot && l.Config.requireMention(m.ChatID) {
		st := l.Store.snapshot()
		t := boundTask(&st, m)
		if t == nil || t.CardID == "" || !knownReplyReference(t, m.ParentID) || t.Source.UserID != m.UserID || t.Source.ChatID != m.ChatID || t.Source.TopicID != m.TopicID || !l.Config.TargetAllowed(m, l.Config.Routes[t.RouteID], t.RouteID) {
			return nil
		}
	}
	if err := l.Store.Accept(l.Config, m); err != nil {
		l.fail()
		return err
	}
	return nil
}

// Commands run in the durable worker, before any binding or classifier processing.
func (l *Lark) HandleControl(ctx context.Context, m Message, failed func(CommandFailureStep)) (bool, error) {
	words := strings.Fields(routeText(m))
	if len(words) == 0 || !slices.Contains([]string{"/invite", "/remove", "/config"}, words[0]) {
		return false, nil
	}
	id := key(m, m.ID)
	st := l.Store.snapshot()
	cmd := st.Commands[id]
	var reply string
	if cmd == nil {
		var err error
		reply, err = l.prepareAccessReply(ctx, m, words, st, failed)
		if err != nil {
			if l.replyAccess(ctx, m, "保存失败，未确认生效。请核对私有状态及 /config。") != nil {
				failed(CommandFailureReply)
			}
			return true, err
		}
	} else {
		reply = cmd.Reply
		if reply == "" {
			// Older receipts were written after applying the command but omitted its reply.
			reply = "此前命令已处理，原回复待核对。请用 /config 查看当前设置。"
		}
	}
	if err := l.replyAccess(ctx, m, reply); err != nil {
		if cmd == nil {
			failed(CommandFailureReply)
		}
		return true, nil
	}
	return true, l.Store.update(func(st *State) error {
		delete(st.Commands, id)
		removeInbox(st, m)
		return nil
	})
}

const mentionConfigHelp = "/config mention on|off|default · /config mention all [on|off]"

func (l *Lark) prepareAccessReply(ctx context.Context, m Message, words []string, st State, failed func(CommandFailureStep)) (string, error) {
	reply := "仅应用所有者或明确配置的管理员可管理群授权。"
	var chats []string
	var allow, mention, defaultMention *bool
	var clearMention bool
	if l.Config.isAdmin(m.UserID) {
		switch strings.Join(words, " ") {
		case "/config", "/config access":
			l.Config.access.mu.RLock()
			ownerOK := l.Config.access.owner != ""
			l.Config.access.mu.RUnlock()
			scope := "群授权和单群@设置请在目标群配置。"
			if m.ChatType == "group" {
				scope = fmt.Sprintf("当前群授权：%t · 需要@：%t", l.Config.groupAllowed(m.ChatID), l.Config.requireMention(m.ChatID))
			}
			reply = fmt.Sprintf("Owner已解析：%t · 管理员：%d\n本应用群聊默认需要@：%t\n%s\n/invite group · /remove group · /invite all group\n/config group on|off · %s\n群使用权不增加route.users或生产写入授权。", ownerOK, len(l.Config.Admins), st.GroupAccess.defaultRequiresMention(), scope, mentionConfigHelp)
			policy := st.AccessPolicy
			reply += "\n管理员可在私聊发送 /config groups 管理本应用所在群，或发送 /config run 打开运行设置表单。"
			reply += "\n私聊用 /config access user、/config access admin 或 /config access route <路由ID> 查看名单。"
			reply += fmt.Sprintf("\n持久授权覆盖：user=%d · admin=%d · route=%d（各自独立）", len(policy.Users), len(policy.Admins), len(policy.Routes))
			settings := st.Settings
			reply += fmt.Sprintf("\n/config concurrency 0..%d · /config progress on|off\n/invite|/remove user|admin <真实@或open_id>\n/invite|/remove route <routeId> <真实@或open_id>\n并发预算=%d（0不限）；可见进度=%t；超时用 /timeout 显式设置。", MaxRunConcurrency, settings.MaxConcurrent, settings.Progress == nil || *settings.Progress)
		case "/invite all group":
			var err error
			chats, err = l.joinedGroups(ctx)
			if err != nil {
				failed(CommandFailureGroupRead)
				reply = "读取所在群失败，未改变任何群授权。"
			} else {
				on := true
				allow = &on
				reply = fmt.Sprintf("已授权机器人所在的%d个群；目标用户权限保持不变。", len(chats))
			}
		case "/invite group", "/config group on", "/remove group", "/config group off":
			if m.ChatType != "group" {
				reply = "请在目标群中 @机器人发送此命令。"
				break
			}
			chats = []string{m.ChatID}
			on := words[len(words)-1] != "off" && words[0] != "/remove"
			allow = &on
			reply = fmt.Sprintf("本群授权：%t；目标用户权限保持不变。", on)
		case "/config mention on", "/config mention off", "/config mention default":
			if m.ChatType != "group" {
				reply = "请在目标群中 @机器人发送此命令。"
				break
			}
			if words[2] == "default" {
				clearMention = true
				reply = fmt.Sprintf("本群已跟随本应用默认设置。当前需要@：%t。", st.GroupAccess.defaultRequiresMention())
			} else {
				on := words[2] == "on"
				mention = &on
				reply = fmt.Sprintf("本群需要@：%t（实际入站仍受应用已有权限限制）。", on)
			}
		case "/config mention all", "/config mention all on", "/config mention all off":
			if m.ChatType != "group" && m.ChatType != "p2p" {
				reply = "请在群聊或私聊中发送此命令。"
				break
			}
			on := st.GroupAccess.defaultRequiresMention()
			if len(words) == 4 {
				on = words[3] == "on"
				defaultMention = &on
			}
			reply = fmt.Sprintf("本应用群聊默认需要@：%t。单独设置的群沿用本群设置。", on)
		default:
			reply = "群管理：/invite group · /remove group · /invite all group · /config · /config group on|off · " + mentionConfigHelp
		}
	}
	if (mention != nil && !*mention) || (defaultMention != nil && !*defaultMention) || (clearMention && !st.GroupAccess.defaultRequiresMention()) {
		reply += "\n" + groupMessageScopeNote(l.groupMessageScope(ctx))
	}
	// Save the reply with the policy change so a delivery retry cannot reapply it.
	published, err := l.Store.updatePublished(func(st *State) error {
		if allow != nil {
			for _, chat := range chats {
				st.GroupAccess.setAllowed(chat, *allow)
			}
		}
		if mention != nil {
			st.GroupAccess.setMention(m.ChatID, *mention)
		}
		if clearMention {
			delete(st.GroupAccess.Mentions, m.ChatID)
		}
		if defaultMention != nil {
			st.GroupAccess.RequireMention = defaultMention
		}
		saveCommandState(st, key(m, m.ID), &SessionCommand{Source: m, Kind: words[0], Reply: reply, Issue: "管理回复未确认，保留Inbox和原UUID重试"})
		return nil
	})
	if published {
		groups, _ := l.Store.groupAccess()
		l.Config.access.publishGroups(groups)
	}
	if err != nil {
		return "", err
	}
	return reply, nil
}
func (l *Lark) replyAccess(ctx context.Context, m Message, text string) error {
	b, _ := json.Marshal(map[string]string{"text": text})
	return l.replyWithUUID(ctx, m, "text", string(b))
}
func (l *Lark) replyWithUUID(ctx context.Context, m Message, msgType, content string) error {
	r, err := l.replyMessage(ctx, m, m.ID, msgType, content)
	if err != nil || r == nil || !r.Success() {
		return errors.New("access reply failed")
	}
	return nil
}
func (l *Lark) joinedGroups(ctx context.Context) ([]string, error) {
	groups, err := listFeishuGroups(ctx, l.Channel.RawClient())
	if err != nil {
		return nil, err
	}
	chats := make([]string, len(groups))
	for i, group := range groups {
		chats[i] = group.ID
	}
	return chats, nil
}
