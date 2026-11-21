package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	legacyGroupPageLimit   = 8
	legacyGroupMessageMax  = 128
	legacyGroupContextOpen = "\n\n---\n\n## 飞书桥接上下文（供助手使用）\n\n<bridge_context>\n"
)

type legacyGroupEvidence struct {
	MessageIDs []string
	Revision   string
}

var errLegacyGroupEvidence = errors.New("cannot confirm legacy T3 group history")
var errLegacyGroupAuthor = errors.New("legacy T3 input author differs from selected sender")

// Presence matters for the fields that establish a quiet, complete snapshot.
type legacyGroupPage struct {
	Thread struct {
		threadIdentity
		CreatedBy            string            `json:"createdBy"`
		CreationSource       string            `json:"creationSource"`
		ParentThreadID       json.RawMessage   `json:"parentThreadId"`
		RelationshipToParent json.RawMessage   `json:"relationshipToParent"`
		DelegatedFrom        json.RawMessage   `json:"delegatedFrom"`
		Archived             *bool             `json:"archived"`
		Status               string            `json:"status"`
		ActiveRunID          json.RawMessage   `json:"activeRunId"`
		LatestRunID          string            `json:"latestRunId"`
		UpdatedAt            string            `json:"updatedAt"`
		ItemCount            *int              `json:"itemCount"`
		PendingRequestCount  *int              `json:"pendingRequestCount"`
		PendingBackground    []json.RawMessage `json:"pendingBackgroundTasks"`
		BlockedOnThreadWait  *bool             `json:"blockedOnThreadWait"`
	} `json:"thread"`
	Items        []legacyGroupItem `json:"items"`
	HasMore      *bool             `json:"hasMore"`
	NextPosition *int              `json:"nextPosition"`
}

type legacyGroupItem struct {
	Position       *int            `json:"position"`
	ItemID         string          `json:"itemId"`
	MessageID      string          `json:"messageId"`
	RunID          string          `json:"runId"`
	Type           string          `json:"type"`
	Visibility     string          `json:"visibility"`
	SourceThreadID string          `json:"sourceThreadId"`
	CreatedBy      string          `json:"createdBy"`
	CreationSource string          `json:"creationSource"`
	SenderThreadID json.RawMessage `json:"senderThreadId"`
	Text           string          `json:"text"`
	TextTruncated  *bool           `json:"textTruncated"`
	NextTextOffset *int            `json:"nextTextOffset"`
}

func (item legacyGroupItem) completeLocalMCPInput(threadID string) bool {
	return item.Visibility == "local" && item.SourceThreadID == threadID && item.CreatedBy == "agent" &&
		item.CreationSource == "mcp" && len(item.SenderThreadID) == 0 && strings.TrimSpace(item.RunID) != "" &&
		strings.TrimSpace(item.MessageID) != "" && item.TextTruncated != nil && !*item.TextTruncated && item.NextTextOffset == nil
}

func (item legacyGroupItem) recordPosition(after, itemCount int, itemIDs, messageIDs map[string]bool) bool {
	if item.Position == nil || *item.Position <= after || *item.Position >= itemCount ||
		strings.TrimSpace(item.ItemID) == "" || itemIDs[item.ItemID] || messageIDs[item.MessageID] {
		return false
	}
	itemIDs[item.ItemID] = true
	if item.MessageID != "" {
		messageIDs[item.MessageID] = true
	}
	return true
}

func (t *T3) readLegacyGroupEvidence(ctx context.Context, target Target, threadID string, scope Message, botID string) (legacyGroupEvidence, error) {
	if !validLegacySessionScopeID(botID, "ou_") {
		return legacyGroupEvidence{}, errLegacyGroupEvidence
	}
	evidence, err := t.scanLegacyGroupEvidence(ctx, target, threadID, scope, botID)
	if err != nil {
		return legacyGroupEvidence{}, errLegacyGroupEvidence
	}
	return evidence, nil
}

// An empty botID lets preflight check consistency across T3 records. Dispatch
// goes through readLegacyGroupEvidence with the verified receiving bot ID.
func (t *T3) scanLegacyGroupEvidence(ctx context.Context, target Target, threadID string, scope Message, botID string) (legacyGroupEvidence, error) {
	if scope.ChatType != "group" || !validLegacySessionScopeID(scope.ChatID, "oc_") ||
		!validLegacySessionScopeID(scope.UserID, "ou_") ||
		(scope.TopicID != "" && !validLegacyTopicID(scope.TopicID)) {
		return legacyGroupEvidence{}, errLegacyGroupEvidence
	}
	var evidence legacyGroupEvidence
	itemIDs, messageIDs, feishuIDs := map[string]bool{}, map[string]bool{}, map[string]bool{}
	position := -1
	for range legacyGroupPageLimit {
		page, revision, err := t.readLegacyGroupPage(ctx, target, threadID, position, 100, 20_000)
		if err != nil || (evidence.Revision != "" && evidence.Revision != revision) {
			return legacyGroupEvidence{}, errLegacyGroupEvidence
		}
		evidence.Revision = revision
		for _, item := range page.Items {
			if !item.recordPosition(position, *page.Thread.ItemCount, itemIDs, messageIDs) {
				return legacyGroupEvidence{}, errLegacyGroupEvidence
			}
			position = *item.Position
			switch item.Type {
			case "assistant_message", "proposed_plan":
				continue
			case "user_message":
				if !item.completeLocalMCPInput(threadID) {
					return legacyGroupEvidence{}, errLegacyGroupEvidence
				}
			default:
				return legacyGroupEvidence{}, errLegacyGroupEvidence
			}
			origin, err := legacyGroupOriginal(item.Text, scope, botID)
			if err != nil || len(origin.MessageIDs) > legacyGroupMessageMax-len(evidence.MessageIDs) {
				return legacyGroupEvidence{}, errLegacyGroupEvidence
			}
			botID = origin.BotOpenID
			for _, id := range origin.MessageIDs {
				if !validLegacySessionScopeID(id, "om_") || feishuIDs[id] {
					return legacyGroupEvidence{}, errLegacyGroupEvidence
				}
				feishuIDs[id] = true
				evidence.MessageIDs = append(evidence.MessageIDs, id)
			}
			if origin.SenderID != scope.UserID {
				return legacyGroupEvidence{}, errLegacyGroupAuthor
			}
		}
		if !*page.HasMore {
			if len(evidence.MessageIDs) == 0 {
				return legacyGroupEvidence{}, errLegacyGroupEvidence
			}
			return evidence, nil
		}
	}
	return legacyGroupEvidence{}, errLegacyGroupEvidence
}

// Missing IDs in this bounded scan carry no evidence about past delivery.
func (t *T3) readLegacyReplayEvidence(ctx context.Context, target Target, threadID string, scope Message, botID string) (legacyGroupEvidence, error) {
	if (scope.ChatType != "p2p" && scope.ChatType != "group") || !validLegacySessionScopeID(scope.ChatID, "oc_") ||
		!validLegacySessionScopeID(scope.UserID, "ou_") || !validLegacySessionScopeID(botID, "ou_") ||
		(scope.TopicID != "" && (scope.ChatType != "group" || !validLegacyTopicID(scope.TopicID))) {
		return legacyGroupEvidence{}, errLegacyGroupEvidence
	}
	var evidence legacyGroupEvidence
	itemIDs, messageIDs, feishuIDs := map[string]bool{}, map[string]bool{}, map[string]bool{}
	position := -1
	for range legacyGroupPageLimit {
		page, revision, err := t.readLegacyGroupPage(ctx, target, threadID, position, 100, 20_000)
		if err != nil || (evidence.Revision != "" && evidence.Revision != revision) {
			return legacyGroupEvidence{}, errLegacyGroupEvidence
		}
		evidence.Revision = revision
		for _, item := range page.Items {
			if !item.recordPosition(position, *page.Thread.ItemCount, itemIDs, messageIDs) {
				return legacyGroupEvidence{}, errLegacyGroupEvidence
			}
			position = *item.Position
			if len(evidence.MessageIDs) == legacyGroupMessageMax || item.Type != "user_message" || !item.completeLocalMCPInput(threadID) {
				continue
			}
			// Go Bridge prompts can quote an old envelope before their own machine context.
			if strings.LastIndex(item.Text, "\n\n[飞书-T3 Bridge 机器上下文]\nrequestId: ") > strings.LastIndex(item.Text, legacyGroupContextOpen) {
				continue
			}
			origin, err := legacyGroupOriginal(item.Text, scope, botID)
			if err != nil || origin.SenderID != scope.UserID {
				continue
			}
			for _, id := range origin.MessageIDs {
				if !validLegacySessionScopeID(id, "om_") || feishuIDs[id] {
					continue
				}
				feishuIDs[id] = true
				evidence.MessageIDs = append(evidence.MessageIDs, id)
				if len(evidence.MessageIDs) == legacyGroupMessageMax {
					break
				}
			}
		}
		if !*page.HasMore {
			break
		}
	}
	return evidence, nil
}

func (t *T3) verifyLegacyGroupEvidence(ctx context.Context, target Target, threadID string, revision string) error {
	if revision == "" {
		return errLegacyGroupEvidence
	}
	// The caller has checked the original Feishu IDs. Only the snapshot is needed now.
	_, current, err := t.readLegacyGroupPage(ctx, target, threadID, -1, 1, 1)
	if err != nil || current != revision {
		return errLegacyGroupEvidence
	}
	return nil
}

func (t *T3) readLegacyGroupPage(ctx context.Context, target Target, threadID string, after, limit, maxChars int) (legacyGroupPage, string, error) {
	var page legacyGroupPage
	if threadID == "" {
		return page, "", errLegacyGroupEvidence
	}
	args := map[string]any{
		"environmentId": target.EnvironmentID, "threadId": threadID, "view": "messages", "afterPosition": after,
		"limit": limit, "runLimit": 1, "maxCharsPerItem": maxChars,
	}
	if err := t.Calls.Call(ctx, "t3_thread_read", args, &page); err != nil {
		return legacyGroupPage{}, "", errLegacyGroupEvidence
	}
	p := page.Thread
	if verifyThreadIdentity(target, threadID, p.threadIdentity) != nil ||
		p.CreatedBy != "agent" || p.CreationSource != "mcp" || string(p.ParentThreadID) != "null" ||
		string(p.RelationshipToParent) != "null" || len(p.DelegatedFrom) != 0 || p.Archived == nil || *p.Archived ||
		(p.Status != "idle" && !nativeTerminal(p.Status)) || string(p.ActiveRunID) != "null" ||
		p.PendingRequestCount == nil || *p.PendingRequestCount != 0 || p.PendingBackground == nil || len(p.PendingBackground) != 0 ||
		p.BlockedOnThreadWait == nil || *p.BlockedOnThreadWait || strings.TrimSpace(p.LatestRunID) == "" ||
		p.ItemCount == nil || *p.ItemCount <= 0 || page.HasMore == nil || page.NextPosition == nil ||
		len(page.Items) == 0 || len(page.Items) > limit {
		return legacyGroupPage{}, "", errLegacyGroupEvidence
	}
	last := page.Items[len(page.Items)-1].Position
	if last == nil || *last != *page.NextPosition || *last <= after || *last >= *p.ItemCount ||
		(*page.HasMore && *last+1 == *p.ItemCount) {
		return legacyGroupPage{}, "", errLegacyGroupEvidence
	}
	if _, err := time.Parse(time.RFC3339Nano, p.UpdatedAt); err != nil {
		return legacyGroupPage{}, "", errLegacyGroupEvidence
	}
	revision := stable("legacy-group-v1", target.EnvironmentID, threadID, p.LatestRunID, p.UpdatedAt, strconv.Itoa(*p.ItemCount))
	return page, revision, nil
}

type legacyGroupOrigin struct {
	ChatID     string          `json:"chatId"`
	ChatType   string          `json:"chatType"`
	SenderID   string          `json:"senderId"`
	SenderName json.RawMessage `json:"senderName"`
	SenderType json.RawMessage `json:"senderType"`
	BotOpenID  string          `json:"botOpenId"`
	Mentions   json.RawMessage `json:"mentions"`
	ThreadID   json.RawMessage `json:"threadId"`
	MessageIDs []string        `json:"messageIds"`
	Source     string          `json:"source"`
}

func legacyGroupOriginal(text string, scope Message, botID string) (legacyGroupOrigin, error) {
	// The legacy bridge appends this envelope after the Feishu message, which can quote older envelopes.
	start := strings.LastIndex(text, legacyGroupContextOpen)
	if start < 0 {
		return legacyGroupOrigin{}, errLegacyGroupEvidence
	}
	raw, _, closed := strings.Cut(text[start+len(legacyGroupContextOpen):], "\n</bridge_context>")
	var origin legacyGroupOrigin
	if !closed || decodeConfigJSON([]byte(raw), &origin) != nil || origin.ChatType != scope.ChatType ||
		origin.ChatID != scope.ChatID || origin.Source != "im" || !validLegacySessionScopeID(origin.SenderID, "ou_") ||
		!validLegacySessionScopeID(origin.BotOpenID, "ou_") || (botID != "" && origin.BotOpenID != botID) ||
		(len(origin.SenderType) != 0 && string(origin.SenderType) != `"user"`) || len(origin.MessageIDs) == 0 {
		return legacyGroupOrigin{}, errLegacyGroupEvidence
	}
	topic := ""
	if len(origin.ThreadID) != 0 && (string(origin.ThreadID) == "null" || json.Unmarshal(origin.ThreadID, &topic) != nil) {
		return legacyGroupOrigin{}, errLegacyGroupEvidence
	}
	if topic != scope.TopicID {
		return legacyGroupOrigin{}, errLegacyGroupEvidence
	}
	return origin, nil
}
