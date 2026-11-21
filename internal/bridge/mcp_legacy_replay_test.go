package bridge

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestReadLegacyReplayEvidenceScopes(t *testing.T) {
	for _, tc := range []struct{ chatType, topic string }{
		{"p2p", ""}, {"group", ""}, {"group", "omt_topic"}, {"group", "om_topic_root"},
	} {
		t.Run(tc.chatType+"/"+tc.topic, func(t *testing.T) {
			scope := legacyGroupFixtureScope
			scope.ChatType, scope.TopicID = tc.chatType, tc.topic
			otherScope := scope
			otherScope.TopicID = "omt_other"
			other := legacyGroupFixtureItem(0, legacyGroupFixtureOrigin(otherScope, "om_other_topic"))
			origin := legacyGroupFixtureOrigin(scope, "om_one", "om_two", "om_one")
			delete(origin, "senderType") // The old bridge could omit an unknown sender type.
			first := legacyGroupFixtureItem(2, origin)
			first["text"] = legacyGroupFixtureText(legacyGroupFixtureOrigin(scope, "om_quoted")) + first["text"].(string)
			last := legacyGroupFixtureItem(5, legacyGroupFixtureOrigin(scope, "om_two", "om_three"))
			api := legacyGroupFixtureAPI(t,
				legacyGroupReadStep{after: -1, page: legacyGroupFixturePage(true, other, first)},
				legacyGroupReadStep{after: 2, page: legacyGroupFixturePage(false, last)},
				legacyGroupReadStep{after: -1, page: legacyGroupFixturePage(true, first), snapshot: true},
			)
			ctx := context.Background()
			got, err := api.readLegacyReplayEvidence(ctx, legacyGroupFixtureTarget, "thread", scope, legacyGroupFixtureBot)
			if err != nil || !slices.Equal(got.MessageIDs, []string{"om_one", "om_two", "om_three"}) || got.Revision == "" {
				t.Fatalf("unexpected evidence %#v, error %v", got, err)
			}
			if err := api.verifyLegacyGroupEvidence(ctx, legacyGroupFixtureTarget, "thread", got.Revision); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReadLegacyReplayEvidenceSkipsUnprovenInputs(t *testing.T) {
	scope := legacyGroupFixtureScope
	scope.ChatType = "p2p"
	items := make([]map[string]any, 0)
	for _, tc := range []struct {
		field string
		value any
	}{
		{"chatType", "group"}, {"chatId", "oc_other"}, {"senderId", "ou_other"}, {"botOpenId", "ou_other"},
		{"botOpenId", ""}, {"source", "card"}, {"source", "comment"}, {"senderType", "bot"}, {"threadId", nil},
		{"messageIds", []string{"omt_topic", "om_", "om_ invalid"}},
	} {
		origin := legacyGroupFixtureOrigin(scope, "om_wrong_origin")
		origin[tc.field] = tc.value
		items = append(items, legacyGroupFixtureItem(len(items), origin))
	}
	for _, tc := range []struct {
		field string
		value any
	}{
		{"type", "assistant_message"}, {"type", "proposed_plan"}, {"type", "other"},
		{"visibility", "inherited"}, {"sourceThreadId", "other"}, {"senderThreadId", "other"},
		{"createdBy", "user"}, {"creationSource", "desktop"}, {"runId", ""}, {"messageId", nil},
		{"textTruncated", true}, {"textTruncated", nil}, {"nextTextOffset", 10}, {"text", "other MCP input"},
	} {
		item := legacyGroupFixtureItem(len(items), legacyGroupFixtureOrigin(scope, "om_wrong_item"))
		item[tc.field] = tc.value
		items = append(items, item)
	}
	goInput := legacyGroupFixtureItem(len(items), legacyGroupFixtureOrigin(scope, "om_go_quote"))
	goInput["text"] = prompt(&Task{DispatchSource: Message{Text: "current Go input", Quote: goInput["text"].(string)}})
	items = append(items, goInput)
	malformed := legacyGroupFixtureItem(len(items), legacyGroupFixtureOrigin(scope, "om_malformed"))
	malformed["text"] = strings.Replace(malformed["text"].(string), `"senderId":`, `"senderId":"ou_other","senderId":`, 1)
	items = append(items, malformed)
	ctx := context.Background()
	api := legacyGroupFixtureAPI(t, legacyGroupReadStep{after: -1, page: legacyGroupFixturePage(false, items...)})
	got, err := api.readLegacyReplayEvidence(ctx, legacyGroupFixtureTarget, "thread", scope, legacyGroupFixtureBot)
	if err != nil || len(got.MessageIDs) != 0 || got.Revision == "" {
		t.Fatalf("unexpected evidence %#v, error %v", got, err)
	}

	last := legacyGroupFixtureItem(len(items), legacyGroupFixtureOrigin(scope, "om_verified"))
	api = legacyGroupFixtureAPI(t, legacyGroupReadStep{after: -1, page: legacyGroupFixturePage(false, append(items, last)...)})
	got, err = api.readLegacyReplayEvidence(ctx, legacyGroupFixtureTarget, "thread", scope, legacyGroupFixtureBot)
	if err != nil || !slices.Equal(got.MessageIDs, []string{"om_verified"}) {
		t.Fatalf("unexpected evidence %#v, error %v", got, err)
	}
}

func TestReadLegacyReplayEvidenceRejectsInvalidScope(t *testing.T) {
	for _, tc := range []struct{ chatType, topic, bot string }{
		{"p2p", "omt_topic", legacyGroupFixtureBot}, {"group", "other", legacyGroupFixtureBot},
		{"private", "", legacyGroupFixtureBot}, {"p2p", "", ""},
	} {
		scope := legacyGroupFixtureScope
		scope.ChatType, scope.TopicID = tc.chatType, tc.topic
		api := legacyGroupFixtureAPI(t)
		got, err := api.readLegacyReplayEvidence(context.Background(), legacyGroupFixtureTarget, "thread", scope, tc.bot)
		if err != errLegacyGroupEvidence || len(got.MessageIDs) != 0 || got.Revision != "" {
			t.Fatalf("invalid scope %+v, bot %q: evidence %#v, error %v", scope, tc.bot, got, err)
		}
	}
}

func TestReadLegacyReplayEvidenceDiscardsUnstableScan(t *testing.T) {
	for _, name := range []string{"revision changed", "bad cursor", "out of order position", "repeated item", "read failed"} {
		t.Run(name, func(t *testing.T) {
			scope := legacyGroupFixtureScope
			ids := make([]string, 128)
			for i := range ids {
				ids[i] = fmt.Sprintf("om_%d", i)
			}
			first := legacyGroupFixtureItem(0, legacyGroupFixtureOrigin(scope, ids...))
			middle := legacyGroupFixtureItem(2, legacyGroupFixtureOrigin(scope, "om_middle"))
			last := legacyGroupFixtureItem(5, legacyGroupFixtureOrigin(scope, "om_last"))
			changed := legacyGroupFixturePage(false, middle, last)
			switch name {
			case "revision changed":
				changed["thread"].(map[string]any)["latestRunId"] = "other-run"
			case "bad cursor":
				changed["nextPosition"] = 7
			case "out of order position":
				middle["position"] = 0
			case "repeated item":
				middle["itemId"] = first["itemId"]
			case "read failed":
				changed = nil
			}
			api := legacyGroupFixtureAPI(t,
				legacyGroupReadStep{after: -1, page: legacyGroupFixturePage(true, first)},
				legacyGroupReadStep{after: 0, page: changed},
			)
			got, err := api.readLegacyReplayEvidence(context.Background(), legacyGroupFixtureTarget, "thread", scope, legacyGroupFixtureBot)
			if err != errLegacyGroupEvidence || len(got.MessageIDs) != 0 || got.Revision != "" {
				t.Fatalf("unstable scan published evidence %#v, error %v", got, err)
			}
		})
	}
}

func TestReadLegacyReplayEvidenceBounds(t *testing.T) {
	scope := legacyGroupFixtureScope
	t.Run("eight pages with more history", func(t *testing.T) {
		steps := make([]legacyGroupReadStep, 8)
		var want []string
		for i := range steps {
			id := fmt.Sprintf("om_%d", i)
			want = append(want, id)
			item := legacyGroupFixtureItem(i, legacyGroupFixtureOrigin(scope, id))
			steps[i] = legacyGroupReadStep{after: i - 1, page: legacyGroupFixturePage(true, item)}
		}
		api := legacyGroupFixtureAPI(t, steps...)
		got, err := api.readLegacyReplayEvidence(context.Background(), legacyGroupFixtureTarget, "thread", scope, legacyGroupFixtureBot)
		if err != nil || !slices.Equal(got.MessageIDs, want) || got.Revision == "" {
			t.Fatalf("eight-page scan: got %#v, error %v", got, err)
		}
	})
	t.Run("128 distinct IDs", func(t *testing.T) {
		ids := make([]string, 130)
		for i := range ids {
			ids[i] = fmt.Sprintf("om_%d", i)
		}
		first := legacyGroupFixtureItem(0, legacyGroupFixtureOrigin(scope, ids[:64]...))
		last := legacyGroupFixtureItem(2, legacyGroupFixtureOrigin(scope, ids[63:]...))
		api := legacyGroupFixtureAPI(t,
			legacyGroupReadStep{after: -1, page: legacyGroupFixturePage(true, first)},
			legacyGroupReadStep{after: 0, page: legacyGroupFixturePage(false, last)},
		)
		got, err := api.readLegacyReplayEvidence(context.Background(), legacyGroupFixtureTarget, "thread", scope, legacyGroupFixtureBot)
		if err != nil || !slices.Equal(got.MessageIDs, ids[:128]) || got.Revision == "" {
			t.Fatalf("128-ID scan: got %d IDs, error %v", len(got.MessageIDs), err)
		}
	})
}
