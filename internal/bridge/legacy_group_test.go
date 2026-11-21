package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

type legacyGroupTestExecutor struct {
	*legacyTestExecutor
	onEvidence func(Target, string, Message, string) (legacyGroupEvidence, error)
	onRecheck  func(string, string) error
}

func (f *legacyGroupTestExecutor) readLegacyGroupEvidence(_ context.Context, target Target, id string, scope Message, bot string) (legacyGroupEvidence, error) {
	return f.onEvidence(target, id, scope, bot)
}

func (f *legacyGroupTestExecutor) verifyLegacyGroupEvidence(_ context.Context, _ Target, id, revision string) error {
	return f.onRecheck(id, revision)
}

func TestLegacyGroupSessionRequiresEveryOriginalFeishuAuthor(t *testing.T) {
	for _, scenario := range []string{"topic", "group", "mixed", "wrong-topic", "deleted", "changed-thread"} {
		t.Run(scenario, func(t *testing.T) {
			e, _, _, executor, fixture := setupAccess(t)
			mapping := LegacySessionMapping{
				ChatType: "group", ChatID: "oc_old_group", TopicID: "omt_original", UserID: "ou_original", ThreadID: "old-t3-thread", RouteID: "mac",
			}
			prefix := mapping.ChatID + ":" + mapping.TopicID
			if scenario == "group" {
				mapping.TopicID = ""
				prefix = mapping.ChatID
			}
			config := e.Config
			config.Chats, config.Topics, config.Users = []string{mapping.ChatID}, nil, []string{mapping.UserID}
			config.DefaultRoute = mapping.RouteID
			target := config.Routes[mapping.RouteID]
			target.Users, target.ProjectPath = config.Users, t.TempDir()
			config.Routes = map[string]Target{mapping.RouteID: target}
			old := legacySessionEntry{
				Scope: prefix, ThreadID: mapping.ThreadID, Agent: "t3", Path: target.ProjectPath, PolicyFingerprint: "old-policy", Status: "active",
				Key: strings.Join([]string{prefix, "t3", target.ProjectPath, "old-policy"}, "\x1f"),
			}
			catalog, err := json.Marshal([]legacySessionEntry{old})
			if err != nil {
				t.Fatal(err)
			}
			if err := e.Store.Close(); err != nil {
				t.Fatal(err)
			}
			plan, err := ImportLegacySession(config, catalog, mapping, nil)
			if err != nil || plan.Applied || plan.ChatType != "group" || plan.TopicID != mapping.TopicID {
				t.Fatalf("bad group preview: %+v, %v", plan, err)
			}
			applied, err := ImportLegacySession(config, catalog, mapping, func(current LegacySessionImportResult) error {
				if current != plan {
					return errors.New("preview changed")
				}
				return nil
			})
			if err != nil || !applied.Applied {
				t.Fatalf("group registration failed: %v", err)
			}
			store, err := OpenStore(config.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			e.Config, e.Store = config, store
			if lark, ok := e.Lark.(*Lark); ok {
				lark.Config, lark.Store = config, store
			}
			actor := Message{ID: "om_next", UserID: mapping.UserID, ChatID: mapping.ChatID, TopicID: mapping.TopicID, ChatType: "group", Text: "继续核对"}
			if scenario == "topic" {
				actor.ParentID = "om_topic_root"
			}
			pending := store.snapshot()
			wrongUser := actor
			wrongUser.UserID = "ou_other"
			outside := &Task{Source: wrongUser}
			takeLegacySession(&pending, outside, sessionKey(wrongUser, mapping.RouteID, target))
			if outside.LegacyGroupSeed != nil || len(pending.LegacySessions) != 1 {
				t.Fatal("another group actor consumed the old thread")
			}
			if mapping.TopicID != "" {
				otherTopic := actor
				otherTopic.TopicID = "omt_other"
				outside.Source = otherTopic
				takeLegacySession(&pending, outside, sessionKey(otherTopic, mapping.RouteID, target))
				if outside.LegacyGroupSeed != nil || len(pending.LegacySessions) != 1 {
					t.Fatal("another topic consumed the old thread")
				}
			}
			originIDs := []string{"om_older", "om_later"}
			var checked []string
			fixture.extra = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path == "/open-apis/bot/v3/info" {
					io.WriteString(w, `{"code":0,"bot":{"open_id":"ou_old_bot","app_name":"Fixture"}}`)
					return true
				}
				id, message := strings.CutPrefix(r.URL.Path, "/open-apis/im/v1/messages/")
				if r.Method == "GET" && message && id == actor.ParentID {
					json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"items": []any{map[string]any{
						"message_id": id, "chat_id": mapping.ChatID, "thread_id": mapping.TopicID, "msg_type": "text",
						"body": map[string]string{"content": `{"text":"synthetic topic root"}`},
					}}}})
					return true
				}
				if r.Method != "GET" || !message || !slices.Contains(originIDs, id) {
					return false
				}
				checked = append(checked, id)
				author, topicID, deleted := mapping.UserID, mapping.TopicID, false
				if id == "om_later" {
					switch scenario {
					case "mixed":
						author = "ou_other"
					case "wrong-topic":
						topicID = "omt_other"
					case "deleted":
						deleted = true
					}
				}
				json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"items": []any{map[string]any{
					"message_id": id, "chat_id": mapping.ChatID, "thread_id": topicID, "deleted": deleted,
					"sender": map[string]string{"sender_type": "user", "id_type": "open_id", "id": author},
					"body":   map[string]string{"content": "synthetic-private-legacy-body"},
				}}}})
				return true
			}
			rechecks := 0
			probe := &legacyGroupTestExecutor{legacyTestExecutor: &legacyTestExecutor{fakeExecutor: executor}}
			probe.onEvidence = func(got Target, thread string, scope Message, bot string) (legacyGroupEvidence, error) {
				if got.EnvironmentID != target.EnvironmentID || thread != mapping.ThreadID || scope.TopicID != actor.TopicID ||
					scope.UserID != actor.UserID || scope.ChatID != actor.ChatID || bot != "ou_old_bot" {
					t.Fatal("evidence escaped exact scope")
				}
				task := onlyTask(t, e)
				if task.ThreadID != "" || task.LegacyGroupSeed == nil {
					t.Fatal("thread was adopted before proof")
				}
				return legacyGroupEvidence{MessageIDs: originIDs, Revision: "sealed-revision"}, nil
			}
			probe.onRecheck = func(id, rev string) error {
				rechecks++
				if id != mapping.ThreadID || rev != "sealed-revision" || !slices.Equal(checked, originIDs) {
					t.Fatal("invalid final thread read before dispatch")
				}
				if scenario == "changed-thread" {
					return errors.New("thread changed during Feishu reads")
				}
				return nil
			}
			e.T3 = probe
			executor.onSend = func(task *Task) {
				saved := store.task(task.ID)
				if rechecks != 1 || task.ThreadID != mapping.ThreadID || saved.ThreadID != mapping.ThreadID ||
					saved.Phase != "dispatching" || saved.LegacyGroupSeed != nil {
					t.Fatal("dispatch was not fenced on the verified original thread")
				}
			}
			accept(t, e, actor)
			tick(t, e)
			task := onlyTask(t, e)
			good := scenario == "group" || scenario == "topic"
			if good && (executor.opens != 0 || executor.sends != 1 || task.ThreadID != mapping.ThreadID) {
				t.Fatalf("old group thread did not continue: %s, opens=%d sends=%d", task.Summary, executor.opens, executor.sends)
			}
			if !good && (executor.opens != 0 || executor.sends != 0 || task.ThreadID != "" || task.Phase != "stopped") {
				t.Fatalf("unverified group history dispatched: %s, opens=%d sends=%d", task.Summary, executor.opens, executor.sends)
			}
			if strings.Contains(task.Summary, "synthetic-private-legacy-body") ||
				strings.Contains(task.RequestMessage, "synthetic-private-legacy-body") ||
				strings.Contains(strings.Join(fixture.replies, "\n"), "synthetic-private-legacy-body") {
				t.Fatal("old message content escaped verification")
			}
		})
	}
}
