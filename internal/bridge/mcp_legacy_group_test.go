package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
)

const legacyGroupFixtureBot = "ou_legacy_bot"

var legacyGroupFixtureScope = Message{ChatType: "group", ChatID: "oc_legacy_group", UserID: "ou_legacy_user"}

var legacyGroupFixtureTarget = Target{
	EnvironmentID: "environment", ProjectID: "project", ProviderID: "provider", Model: "model",
	RuntimeMode: "approval-required", InteractionMode: "default",
}

type legacyGroupReadStep struct {
	after    int
	page     map[string]any
	snapshot bool
}

func legacyGroupFixtureAPI(t *testing.T, steps ...legacyGroupReadStep) *T3 {
	t.Helper()
	next := 0
	t.Cleanup(func() {
		if next != len(steps) {
			t.Errorf("used %d of %d expected T3 reads", next, len(steps))
		}
	})
	return &T3{Calls: scriptedCaller(func(name string, args map[string]any) (any, error) {
		if next >= len(steps) {
			t.Fatalf("unexpected T3 call %q", name)
		}
		step := steps[next]
		next++
		limit, chars := float64(100), float64(20000)
		if step.snapshot {
			limit, chars = 1, 1
		}
		want := map[string]any{
			"environmentId": legacyGroupFixtureTarget.EnvironmentID, "threadId": "thread", "view": "messages",
			"afterPosition": float64(step.after), "runLimit": float64(1), "limit": limit, "maxCharsPerItem": chars,
		}
		if name != "t3_thread_read" || !maps.Equal(args, want) {
			t.Fatalf("T3 call %s %v, want t3_thread_read %v", name, args, want)
		}
		if step.page == nil {
			return nil, errors.New("synthetic transport failure with private body")
		}
		return step.page, nil
	})}
}

func legacyGroupFixturePage(hasMore bool, items ...map[string]any) map[string]any {
	var cursor any
	if len(items) > 0 {
		cursor = items[len(items)-1]["position"]
	}
	target := legacyGroupFixtureTarget
	return map[string]any{
		"thread": map[string]any{
			"threadId": "thread", "projectId": target.ProjectID, "providerInstanceId": target.ProviderID,
			"model": target.Model, "runtimeMode": target.RuntimeMode, "interactionMode": target.InteractionMode,
			"createdBy": "agent", "creationSource": "mcp", "parentThreadId": nil, "relationshipToParent": nil,
			"archived": false, "status": "completed", "activeRunId": nil, "latestRunId": "run",
			"updatedAt": "2026-11-19T00:00:00.000Z", "itemCount": 40, "pendingRequestCount": 0,
			"pendingBackgroundTasks": []any{}, "blockedOnThreadWait": false,
		},
		"items": items, "hasMore": hasMore, "nextPosition": cursor,
	}
}

func legacyGroupFixtureOrigin(scope Message, ids ...string) map[string]any {
	origin := map[string]any{
		"chatId": scope.ChatID, "chatType": scope.ChatType, "senderId": scope.UserID, "senderType": "user",
		"botOpenId": legacyGroupFixtureBot, "source": "im", "messageIds": ids,
		"senderName": "Synthetic user", "mentions": []any{map[string]any{"openId": legacyGroupFixtureBot, "isBot": true}},
	}
	if scope.TopicID != "" {
		origin["threadId"] = scope.TopicID
	}
	return origin
}

const legacyGroupFixtureMarker = "\n\n---\n\n## 飞书桥接上下文（供助手使用）\n\n<bridge_context>\n"

func legacyGroupFixtureText(origin map[string]any) string {
	encoded, _ := json.Marshal(origin)
	return "synthetic original body" + legacyGroupFixtureMarker + string(encoded) + "\n</bridge_context>\n\nsynthetic instructions"
}

func legacyGroupFixtureItem(position int, origin map[string]any) map[string]any {
	return map[string]any{
		"position": position, "itemId": fmt.Sprintf("item-%d", position), "messageId": fmt.Sprintf("message-%d", position),
		"runId": "run", "type": "user_message", "visibility": "local", "sourceThreadId": "thread",
		"createdBy": "agent", "creationSource": "mcp", "text": legacyGroupFixtureText(origin),
		"textTruncated": false, "nextTextOffset": nil,
	}
}

func TestReadLegacyGroupEvidenceNormalAndTopicScopes(t *testing.T) {
	for _, topic := range []string{"", "omt_topic", "om_topic_root"} {
		name := topic
		if name == "" {
			name = "normal group"
		}
		t.Run(name, func(t *testing.T) {
			scope := legacyGroupFixtureScope
			scope.TopicID = topic
			firstOrigin := legacyGroupFixtureOrigin(scope, "om_one", "om_two")
			delete(firstOrigin, "senderType") // The old fork omitted unknown sender types.
			first := legacyGroupFixtureItem(0, firstOrigin)
			quoted := legacyGroupFixtureOrigin(scope, "om_quoted")
			quoted["senderId"] = "ou_someone_else"
			first["text"] = legacyGroupFixtureText(quoted) + first["text"].(string)
			assistant := map[string]any{"position": 3, "type": "assistant_message", "itemId": "assistant", "messageId": "assistant-message"}
			plan := map[string]any{"position": 5, "type": "proposed_plan", "itemId": "plan", "messageId": nil}
			last := legacyGroupFixtureItem(8, legacyGroupFixtureOrigin(scope, "om_three"))
			snapshotItem := maps.Clone(first)
			snapshotItem["text"], snapshotItem["textTruncated"], snapshotItem["nextTextOffset"] = "s", true, 1
			firstPage := legacyGroupReadStep{after: -1, page: legacyGroupFixturePage(true, first, assistant)}
			lastPage := legacyGroupReadStep{after: 3, page: legacyGroupFixturePage(false, plan, last)}
			snapshot := legacyGroupReadStep{after: -1, page: legacyGroupFixturePage(true, snapshotItem), snapshot: true}
			api := legacyGroupFixtureAPI(t, firstPage, lastPage, snapshot)
			ctx := context.Background()
			got, err := api.readLegacyGroupEvidence(ctx, legacyGroupFixtureTarget, "thread", scope, legacyGroupFixtureBot)
			if err != nil || !slices.Equal(got.MessageIDs, []string{"om_one", "om_two", "om_three"}) || got.Revision == "" {
				t.Fatalf("unexpected evidence %#v, error %v", got, err)
			}
			if err := api.verifyLegacyGroupEvidence(ctx, legacyGroupFixtureTarget, "thread", got.Revision); err != nil {
				t.Fatal(err)
			}

			preview, err := legacyGroupFixtureAPI(t, firstPage, lastPage).scanLegacyGroupEvidence(ctx, legacyGroupFixtureTarget, "thread", scope, "")
			if err != nil || !slices.Equal(preview.MessageIDs, got.MessageIDs) || preview.Revision != got.Revision {
				t.Fatalf("unexpected preview evidence %#v, error %v", preview, err)
			}
			missingBot, err := legacyGroupFixtureAPI(t).readLegacyGroupEvidence(ctx, legacyGroupFixtureTarget, "thread", scope, "")
			if err != errLegacyGroupEvidence || len(missingBot.MessageIDs) != 0 || missingBot.Revision != "" {
				t.Fatalf("expected empty runtime evidence and generic error without receiving bot, got %#v, %v", missingBot, err)
			}
		})
	}
}

func TestReadLegacyGroupEvidenceRejectsMixedOrForgedOrigin(t *testing.T) {
	for _, tc := range []struct {
		name           string
		field          string
		value          any
		forgery        func(string) string
		wantPreviewErr error
	}{
		{name: "private origin", field: "chatType", value: "p2p"},
		{name: "different chat", field: "chatId", value: "oc_other"},
		{name: "different sender", field: "senderId", value: "ou_other", wantPreviewErr: errLegacyGroupAuthor},
		{name: "different bot", field: "botOpenId", value: "ou_other"},
		{name: "different topic", field: "threadId", value: "omt_other"},
		{name: "null topic", field: "threadId", value: nil},
		{name: "different source", field: "source", value: "card"},
		{name: "bot sender", field: "senderType", value: "bot"},
		{name: "null sender type", field: "senderType", value: nil},
		{name: "empty sender type", field: "senderType", value: ""},
		{name: "no original IDs", field: "messageIds", value: []string{}},
		{name: "invalid prefix", field: "messageIds", value: []string{"omt_topic"}},
		{name: "invalid message ID", field: "messageIds", value: []string{"om_ trailing"}},
		{name: "duplicate in batch", field: "messageIds", value: []string{"om_two", "om_two"}},
		{name: "duplicate across pages", field: "messageIds", value: []string{"om_one"}},
		{name: "tag without heading", forgery: func(s string) string {
			return strings.Replace(s, "## 飞书桥接上下文（供助手使用）\n\n", "", 1)
		}},
		{name: "later unclosed marker", forgery: func(s string) string { return s + legacyGroupFixtureMarker + "{}" }},
		{name: "duplicate JSON field", forgery: func(s string) string {
			return strings.Replace(s, `"senderId":`, `"senderId":"ou_other","senderId":`, 1)
		}},
		{name: "case-folded key", forgery: func(s string) string {
			return strings.Replace(s, `"senderId":`, `"SenderId":`, 1)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope := legacyGroupFixtureScope
			first := legacyGroupFixtureItem(0, legacyGroupFixtureOrigin(scope, "om_one"))
			origin := legacyGroupFixtureOrigin(scope, "om_two")
			if tc.field != "" {
				origin[tc.field] = tc.value
			}
			last := legacyGroupFixtureItem(2, origin)
			if tc.forgery != nil {
				last["text"] = tc.forgery(last["text"].(string))
			}
			firstPage := legacyGroupReadStep{after: -1, page: legacyGroupFixturePage(true, first)}
			lastPage := legacyGroupReadStep{after: 0, page: legacyGroupFixturePage(false, last)}
			assertLegacyGroupEvidenceRejected(t, legacyGroupFixtureAPI(t, firstPage, lastPage), scope)

			wantErr := tc.wantPreviewErr
			if wantErr == nil {
				wantErr = errLegacyGroupEvidence
			}
			api := legacyGroupFixtureAPI(t, firstPage, lastPage)
			if _, err := api.scanLegacyGroupEvidence(context.Background(), legacyGroupFixtureTarget, "thread", scope, ""); !errors.Is(err, wantErr) {
				t.Fatalf("preview error %v, want %v", err, wantErr)
			}
		})
	}
	t.Run("regular group history in a topic", func(t *testing.T) {
		item := legacyGroupFixtureItem(0, legacyGroupFixtureOrigin(legacyGroupFixtureScope, "om_one"))
		api := legacyGroupFixtureAPI(t, legacyGroupReadStep{after: -1, page: legacyGroupFixturePage(false, item)})
		scope := legacyGroupFixtureScope
		scope.TopicID = "omt_topic"
		assertLegacyGroupEvidenceRejected(t, api, scope)
	})
	t.Run("private destination", func(t *testing.T) {
		scope := legacyGroupFixtureScope
		scope.ChatType = "p2p"
		assertLegacyGroupEvidenceRejected(t, legacyGroupFixtureAPI(t), scope)
	})
}

func assertLegacyGroupEvidenceRejected(t *testing.T, api *T3, scope Message) {
	t.Helper()
	evidence, err := api.readLegacyGroupEvidence(context.Background(), legacyGroupFixtureTarget, "thread", scope, legacyGroupFixtureBot)
	if err != errLegacyGroupEvidence || len(evidence.MessageIDs) != 0 || evidence.Revision != "" {
		t.Fatalf("expected empty evidence and generic error, got %#v, %v", evidence, err)
	}
}

func TestReadLegacyGroupEvidenceRejectsIncompleteTimeline(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(page, item, later map[string]any)
	}{
		{"inherited input", func(_, item, _ map[string]any) { item["visibility"] = "inherited" }},
		{"wrong source thread", func(_, item, _ map[string]any) { item["sourceThreadId"] = "other" }},
		{"thread sender", func(_, item, _ map[string]any) { item["senderThreadId"] = "other" }},
		{"user-authored input", func(_, item, _ map[string]any) { item["createdBy"] = "user" }},
		{"non-MCP input", func(_, item, _ map[string]any) { item["creationSource"] = "desktop" }},
		{"missing run", func(_, item, _ map[string]any) { delete(item, "runId") }},
		{"missing message ID", func(_, item, _ map[string]any) { item["messageId"] = nil }},
		{"missing item ID", func(_, item, _ map[string]any) { item["itemId"] = "" }},
		{"missing text", func(_, item, _ map[string]any) { item["text"] = nil }},
		{"truncated text", func(_, item, _ map[string]any) { item["textTruncated"] = true }},
		{"absent truncation flag", func(_, item, _ map[string]any) { delete(item, "textTruncated") }},
		{"text continuation", func(_, item, _ map[string]any) { item["nextTextOffset"] = 10 }},
		{"unsupported item type", func(_, _, later map[string]any) { later["type"] = "unknown" }},
		{"duplicate item ID", func(_, item, later map[string]any) { later["itemId"] = item["itemId"] }},
		{"duplicate message ID", func(_, item, later map[string]any) { later["messageId"] = item["messageId"] }},
		{"duplicate position", func(_, item, later map[string]any) { item["position"] = later["position"] }},
		{"missing position", func(_, item, _ map[string]any) { delete(item, "position") }},
		{"missing hasMore", func(page, _, _ map[string]any) { delete(page, "hasMore") }},
		{"missing nextPosition", func(page, _, _ map[string]any) { delete(page, "nextPosition") }},
		{"null nextPosition", func(page, _, _ map[string]any) { page["nextPosition"] = nil }},
		{"cursor skips rows", func(page, _, _ map[string]any) { page["nextPosition"] = 9 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope := legacyGroupFixtureScope
			first := legacyGroupFixtureItem(0, legacyGroupFixtureOrigin(scope, "om_one"))
			last := legacyGroupFixtureItem(3, legacyGroupFixtureOrigin(scope, "om_two"))
			page := legacyGroupFixturePage(false, first, last)
			tc.mutate(page, first, last)
			assertLegacyGroupEvidenceRejected(t, legacyGroupFixtureAPI(t, legacyGroupReadStep{after: -1, page: page}), scope)
		})
	}
	for _, tc := range []struct {
		name string
		page map[string]any
	}{
		{"empty page", legacyGroupFixturePage(false)},
		{"assistant-only history", legacyGroupFixturePage(false, map[string]any{
			"position": 2, "type": "assistant_message", "itemId": "assistant", "messageId": "assistant-message",
		})},
		{"failed read", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertLegacyGroupEvidenceRejected(t, legacyGroupFixtureAPI(t, legacyGroupReadStep{after: -1, page: tc.page}), legacyGroupFixtureScope)
		})
	}
	t.Run("non-advancing cursor across pages", func(t *testing.T) {
		scope := legacyGroupFixtureScope
		first := legacyGroupFixtureItem(0, legacyGroupFixtureOrigin(scope, "om_one"))
		last := legacyGroupFixtureItem(0, legacyGroupFixtureOrigin(scope, "om_two"))
		api := legacyGroupFixtureAPI(t,
			legacyGroupReadStep{after: -1, page: legacyGroupFixturePage(true, first)},
			legacyGroupReadStep{after: 0, page: legacyGroupFixturePage(false, last)},
		)
		assertLegacyGroupEvidenceRejected(t, api, scope)
	})
}

func TestLegacyGroupEvidenceRejectsSnapshotDrift(t *testing.T) {
	for _, tc := range []struct {
		field string
		value any
	}{
		{"latestRunId", "new-run"},
		{"updatedAt", "2026-11-19T00:00:01.000Z"},
		{"itemCount", 41},
		{"createdBy", "user"},
		{"creationSource", "desktop"},
		{"projectId", "another-project"},
		{"providerInstanceId", "another-provider"},
		{"threadId", "other-thread"},
		{"parentThreadId", "parent"},
		{"relationshipToParent", "fork"},
		{"delegatedFrom", map[string]any{"threadId": "parent"}},
		{"archived", true},
		{"activeRunId", "run"},
		{"status", "queued"},
		{"pendingRequestCount", 1},
		{"pendingBackgroundTasks", []any{map[string]any{"taskId": "pending"}}},
		{"blockedOnThreadWait", true},
	} {
		scope := legacyGroupFixtureScope
		first := legacyGroupFixtureItem(0, legacyGroupFixtureOrigin(scope, "om_one"))
		last := legacyGroupFixtureItem(3, legacyGroupFixtureOrigin(scope, "om_two"))
		t.Run(tc.field+" between pages", func(t *testing.T) {
			changed := legacyGroupFixturePage(false, last)
			changed["thread"].(map[string]any)[tc.field] = tc.value
			api := legacyGroupFixtureAPI(t,
				legacyGroupReadStep{after: -1, page: legacyGroupFixturePage(true, first)},
				legacyGroupReadStep{after: 0, page: changed},
			)
			assertLegacyGroupEvidenceRejected(t, api, scope)
		})
		t.Run(tc.field+" before dispatch", func(t *testing.T) {
			changed := legacyGroupFixturePage(false, first)
			changed["thread"].(map[string]any)[tc.field] = tc.value
			api := legacyGroupFixtureAPI(t,
				legacyGroupReadStep{after: -1, page: legacyGroupFixturePage(false, first)},
				legacyGroupReadStep{after: -1, page: changed, snapshot: true},
			)
			ctx := context.Background()
			evidence, err := api.readLegacyGroupEvidence(ctx, legacyGroupFixtureTarget, "thread", scope, legacyGroupFixtureBot)
			if err != nil {
				t.Fatal(err)
			}
			if err := api.verifyLegacyGroupEvidence(ctx, legacyGroupFixtureTarget, "thread", evidence.Revision); err != errLegacyGroupEvidence {
				t.Fatalf("changed snapshot was accepted or leaked details: %v", err)
			}
		})
	}
}

func TestReadLegacyGroupEvidenceBounds(t *testing.T) {
	scope := legacyGroupFixtureScope
	for _, hasNinth := range []bool{false, true} {
		t.Run(fmt.Sprintf("ninth page=%t", hasNinth), func(t *testing.T) {
			steps := make([]legacyGroupReadStep, 0, 8)
			for i := range 8 {
				origin := legacyGroupFixtureOrigin(scope, fmt.Sprintf("om_%d", i))
				steps = append(steps, legacyGroupReadStep{
					after: i - 1, page: legacyGroupFixturePage(i < 7 || hasNinth, legacyGroupFixtureItem(i, origin)),
				})
			}
			api := legacyGroupFixtureAPI(t, steps...)
			if hasNinth {
				assertLegacyGroupEvidenceRejected(t, api, scope)
				return
			}
			evidence, err := api.readLegacyGroupEvidence(context.Background(), legacyGroupFixtureTarget, "thread", scope, legacyGroupFixtureBot)
			if err != nil || len(evidence.MessageIDs) != 8 {
				t.Fatalf("eight-page history: got %d IDs, error %v", len(evidence.MessageIDs), err)
			}
		})
	}
	for _, n := range []int{128, 129} {
		t.Run(fmt.Sprintf("%d original IDs", n), func(t *testing.T) {
			ids := make([]string, n)
			for i := range ids {
				ids[i] = fmt.Sprintf("om_%d", i)
			}
			first := legacyGroupFixtureItem(0, legacyGroupFixtureOrigin(scope, ids[:64]...))
			last := legacyGroupFixtureItem(2, legacyGroupFixtureOrigin(scope, ids[64:]...))
			api := legacyGroupFixtureAPI(t,
				legacyGroupReadStep{after: -1, page: legacyGroupFixturePage(true, first)},
				legacyGroupReadStep{after: 0, page: legacyGroupFixturePage(false, last)},
			)
			if n > 128 {
				assertLegacyGroupEvidenceRejected(t, api, scope)
				return
			}
			evidence, err := api.readLegacyGroupEvidence(context.Background(), legacyGroupFixtureTarget, "thread", scope, legacyGroupFixtureBot)
			if err != nil || !slices.Equal(evidence.MessageIDs, ids) {
				t.Fatalf("128-message history: got %d IDs, error %v", len(evidence.MessageIDs), err)
			}
		})
	}
}
