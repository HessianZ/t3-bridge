package bridge

import (
	"context"
	"sync"
	"time"

	im "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type groupMode uint8

const (
	groupModeUnknown groupMode = iota
	groupModeChat
	groupModeTopic
)

type groupModeEntry struct {
	mode       groupMode
	validUntil time.Time
}

type groupModeCache struct {
	mu      sync.Mutex
	entries map[string]groupModeEntry
}

// resolveGroupTopic only backfills scope. The event remains the source of user input.
func (l *Lark) resolveGroupTopic(ctx context.Context, m *Message) bool {
	if m.TopicID != "" {
		l.topics.markTopic(m.ChatID)
		return true
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	mode := l.groupMode(ctx, m.ChatID)
	requireTopic := mode == groupModeTopic || len(l.Config.Topics) != 0
	if mode == groupModeChat && !requireTopic {
		return true
	}

	items, err := l.messageItems(ctx, m.ID)
	if err != nil {
		return !requireTopic
	}
	p := items[0]
	if p == nil || value(p.MessageId) != m.ID || value(p.ChatId) != m.ChatID || (p.Deleted != nil && *p.Deleted) {
		return false
	}
	m.TopicID = value(p.ThreadId)
	if m.TopicID != "" {
		l.topics.markTopic(m.ChatID)
	}
	return m.TopicID != "" || !requireTopic
}

func (l *Lark) groupMode(ctx context.Context, chatID string) groupMode {
	cache := l.topics
	cache.mu.Lock()
	cached := cache.entries[chatID]
	cache.mu.Unlock()
	if time.Now().Before(cached.validUntil) {
		return cached.mode
	}

	r, err := l.Channel.RawClient().Im.V1.Chat.Get(ctx, im.NewGetChatReqBuilder().ChatId(chatID).Build())
	mode := groupModeUnknown
	if err == nil && r != nil && r.Success() && r.Data != nil {
		switch value(r.Data.ChatMode) {
		case "topic":
			mode = groupModeTopic
		case "group":
			mode = groupModeChat
			if value(r.Data.GroupMessageType) == "thread" {
				mode = groupModeTopic
			}
		}
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	current := cache.entries[chatID]
	if mode == groupModeUnknown || current != cached {
		// Keep positive topic evidence through read failures and concurrent events.
		if current.mode == groupModeTopic {
			return groupModeTopic
		}
		return mode
	}
	cache.put(chatID, mode)
	return mode
}

func (c *groupModeCache) markTopic(chatID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries[chatID].mode != groupModeTopic || time.Now().After(c.entries[chatID].validUntil) {
		c.put(chatID, groupModeTopic)
	}
}

// put is called with the cache locked.
func (c *groupModeCache) put(chatID string, mode groupMode) {
	if c.entries == nil || (len(c.entries) >= 256 && c.entries[chatID].mode == groupModeUnknown) {
		c.entries = make(map[string]groupModeEntry)
	}
	c.entries[chatID] = groupModeEntry{mode: mode, validUntil: time.Now().Add(time.Minute)}
}
