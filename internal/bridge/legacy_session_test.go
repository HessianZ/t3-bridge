package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

type legacyTestExecutor struct {
	*fakeExecutor
	verified  []string
	legacyErr error
}

func (e *legacyTestExecutor) verifyLegacySession(_ context.Context, _ Target, threadID string) error {
	e.verified = append(e.verified, threadID)
	return e.legacyErr
}

func TestInspectLegacySessionsListsOnlyUnambiguousActiveThreads(t *testing.T) {
	const chat, path = "oc_private", "/projects/work"
	entry := func(scope, thread, policy string, updatedAt int64) legacySessionEntry {
		return legacySessionEntry{
			Key: strings.Join([]string{scope, "t3", path, policy}, "\x1f"), Scope: scope, Agent: "t3", Path: path,
			PolicyFingerprint: policy, Status: "active", ThreadID: thread, UpdatedAt: updatedAt, LastSummary: "private legacy content",
		}
	}
	archived := entry(chat, "archived", "p0", 10)
	archived.Status = "archived"
	otherAgent := entry(chat, "recent", "p0", 10)
	otherAgent.Agent = "codex"
	damaged := entry(chat, "damaged", "p0", 10)
	damaged.Key = "damaged"
	claudeID := entry(chat, "with-session-id", "p0", 10)
	claudeID.SessionID = "unexpected"
	raw, err := json.Marshal([]legacySessionEntry{
		entry(chat, "older", "p1", 1), entry(chat, "recent", "p2", 5), entry(chat, "also-recent", "p3", 5),
		entry("oc_other", "other-chat", "p4", 50), entry(chat+":omt_topic", "topic", "p5", 50),
		entry(chat, "ambiguous", "p6", 50), entry("oc_other", "ambiguous", "p7", 50),
		entry(chat+":invalid", "invalid-scope", "p8", 99),
		entry("oc_other:om_topic_root", "topic-root", "p9", 51),
		archived, otherAgent, damaged, claudeID,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []LegacySessionCandidate{{"also-recent", path, 5}, {"recent", path, 5}, {"older", path, 1}}
	if found, err := InspectLegacySessions(raw, chat); err != nil || !slices.Equal(found, want) {
		t.Fatalf("unexpected candidates: %v, %v", found, err)
	}
	if found, err := InspectLegacySessions(raw, "oc_missing"); err != nil || found == nil || len(found) != 0 {
		t.Fatalf("want an empty list for an unknown chat: %v, %v", found, err)
	}
	if _, err := InspectLegacySessions(raw, chat+":omt_topic"); err == nil {
		t.Fatal("accepted a topic in a private chat lookup")
	}
	wantScopes := []LegacySessionScope{
		{ChatID: "oc_other", TopicID: "om_topic_root", ActiveThreads: 1, UpdatedAt: 51},
		{ChatID: "oc_other", ActiveThreads: 1, UpdatedAt: 50},
		{ChatID: chat, TopicID: "omt_topic", ActiveThreads: 1, UpdatedAt: 50},
		{ChatID: chat, ActiveThreads: 3, UpdatedAt: 5},
	}
	if scopes, err := InspectLegacySessionScopes(raw); err != nil || !slices.Equal(scopes, wantScopes) {
		t.Fatalf("unexpected scope inventory: %+v, %v", scopes, err)
	}
}

func TestLegacyPrivateSessionContinuesOnlyTheReviewedThread(t *testing.T) {
	for _, mode := range []string{"new-run", "lost-ack", "verify-refused", "reset", "archived-context"} {
		t.Run(mode, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.Users, cfg.Chats, cfg.Topics = []string{"ou_user"}, []string{"oc_private"}, nil
			cfg.DefaultRoute, cfg.AppID = "mac", "cli_original"
			cfg.access = &accessRuntime{}
			target := cfg.Routes["mac"]
			target.Users, target.ProjectPath = cfg.Users, t.TempDir()
			cfg.Routes = map[string]Target{"mac": target}
			mapping := LegacySessionMapping{ChatID: "oc_private", UserID: "ou_user", ThreadID: "old-thread", RouteID: "mac"}
			catalog, err := json.Marshal([]map[string]any{{
				"key":     strings.Join([]string{mapping.ChatID, "t3", target.ProjectPath, "fingerprint"}, "\x1f"),
				"scopeId": mapping.ChatID, "cwdRealpath": target.ProjectPath, "policyFingerprint": "fingerprint",
				"agentId": "t3", "status": "active", "threadId": mapping.ThreadID, "updatedAt": 1,
				"lastSummary": "synthetic old transcript should never appear in preview",
			}})
			if err != nil {
				t.Fatal(err)
			}
			wrong := mapping
			wrong.ChatID = "oc_other"
			if _, err := ImportLegacySession(cfg, catalog, wrong, nil); err == nil {
				t.Fatal("accepted a different chat")
			}
			before, err := os.ReadDir(cfg.StateDir)
			if err != nil || len(before) != 0 {
				t.Fatal("preview created state")
			}
			plan, err := ImportLegacySession(cfg, catalog, mapping, nil)
			if err != nil || plan.Action != "add" || plan.Applied || plan.ProjectID != target.ProjectID {
				t.Fatalf("unexpected preview %+v, %v", plan, err)
			}
			if mode == "archived-context" {
				archived, err := OpenStore(cfg.StateDir)
				if err != nil {
					t.Fatal(err)
				}
				m := Message{UserID: mapping.UserID, ChatID: mapping.ChatID, ChatType: "p2p"}
				if err := archived.update(func(st *State) error {
					st.Current = map[string]string{sessionKey(m, mapping.RouteID, target): ""}
					return nil
				}); err != nil {
					archived.Close()
					t.Fatal(err)
				}
				if err := archived.Close(); err != nil {
					t.Fatal(err)
				}
				stale := errors.New("review changed")
				if _, err := ImportLegacySession(cfg, catalog, mapping, func(result LegacySessionImportResult) error {
					if result != plan {
						return stale
					}
					return nil
				}); !errors.Is(err, stale) {
					t.Fatalf("stale preview accepted after archive: %v", err)
				}
				fresh, err := ImportLegacySession(cfg, catalog, mapping, nil)
				if err != nil || !fresh.ArchivedContext || fresh.Action != "add" || fresh == plan {
					t.Fatalf("archive requires a distinct, eligible preview: %+v, %v", fresh, err)
				}
				plan = fresh
			}
			applied, err := ImportLegacySession(cfg, catalog, mapping, func(result LegacySessionImportResult) error {
				if result != plan {
					return errors.New("review changed")
				}
				return nil
			})
			if err != nil || !applied.Applied {
				t.Fatalf("apply did not persist the migration: %v", err)
			}
			store, err := OpenStore(cfg.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { store.Close() }()
			first := Message{ID: "om_first", UserID: mapping.UserID, ChatID: mapping.ChatID, ChatType: "p2p", Text: "Continue here"}
			mirror := first
			mirror.ChatType = "group"
			state := store.snapshot()
			groupTask := &Task{Source: mirror, Target: target}
			takeLegacySession(&state, groupTask, sessionKey(mirror, mapping.RouteID, target))
			if groupTask.ThreadID != "" || len(state.LegacySessions) != 1 {
				t.Fatal("group intake consumed a private migration")
			}
			if _, err := ImportLegacySession(cfg, catalog, mapping, func(LegacySessionImportResult) error { return nil }); err == nil {
				t.Fatal("migration wrote through a live store lock")
			}
			fake := &legacyTestExecutor{fakeExecutor: &fakeExecutor{}}
			if mode == "verify-refused" {
				fake.legacyErr = errors.New("legacy thread still active")
			}
			if mode == "lost-ack" {
				fake.sendErr = errors.New("server accepted but response was lost")
			}
			ctx := context.Background()
			e := &Engine{Config: cfg, Store: store, T3: fake, Lark: &fakeMessenger{}}
			if mode == "reset" {
				reset := first
				reset.ID, reset.Text = "om_reset", "/new"
				if err := store.Accept(cfg, reset); err != nil {
					t.Fatal(err)
				}
				if err := e.Tick(ctx); err != nil {
					t.Fatal(err)
				}
			}
			fake.onSend = func(task *Task) {
				expected := mapping.ThreadID
				if mode == "reset" {
					expected = "thread"
				}
				live := store.task(task.ID)
				if task.ThreadID != expected || live.ThreadID != expected || live.Phase != "dispatching" || live.RunID != "" {
					t.Fatal("request escaped its durable thread/dispatch fence")
				}
			}
			if err := store.Accept(cfg, first); err != nil {
				t.Fatal(err)
			}
			if err := e.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			if mode == "reset" {
				if fake.opens != 1 || fake.sends != 1 || len(fake.verified) != 0 {
					t.Fatal("reset did not discard the imported continuation")
				}
				return
			}
			if fake.opens != 0 || !slices.Equal(fake.verified, []string{mapping.ThreadID}) {
				t.Fatal("original thread was not verified or a replacement thread opened")
			}
			if mode == "verify-refused" {
				if fake.sends != 0 || fake.stops != 0 {
					t.Fatal("unverified thread was mutated")
				}
				return
			}
			if fake.sends != 1 {
				t.Fatal("expected exactly one send")
			}
			if mode == "lost-ack" {
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				store, err = OpenStore(cfg.StateDir)
				if err != nil {
					t.Fatal(err)
				}
				e.Store = store
			}
			runID := "run"
			if mode == "lost-ack" {
				runID = "owned-new-run"
			}
			fake.observation = Observation{FoundRequest: true, RunID: runID, Status: "completed", Summary: "Done"}
			if err := e.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			receipt, err := ReadMessageReceipt(cfg.StateDir, cfg.AppID, first.ID)
			if err != nil || receipt.ThreadID != mapping.ThreadID || receipt.RunID != runID || receipt.Phase != "completed" ||
				fake.sends != 1 || fake.opens != 0 || fake.stops != 0 {
				t.Fatalf("receipt did not retain the original thread without replay: %+v %v", receipt, err)
			}
		})
	}
}

func TestVerifyLegacySessionUsesReadOnlyQuietProjectEvidence(t *testing.T) {
	target := testConfig(t).Routes["nas"]
	target.ProjectPath = t.TempDir()
	for _, mode := range []string{"quiet", "active", "project-path", "model-options", "absent-owner"} {
		t.Run(mode, func(t *testing.T) {
			baseline := metadataCaller(t, target, func(_ map[string]any) map[string]any {
				p := metadataPageFor(target)
				thread := p["thread"].(map[string]any)
				if mode == "active" {
					thread["activeRunId"] = "run"
				}
				if mode == "absent-owner" {
					delete(thread, "parentThreadId")
				}
				return p
			})
			caller := scriptedCaller(func(name string, args map[string]any) (any, error) {
				if name == "t3_project_read" {
					root := target.ProjectPath
					if mode == "project-path" {
						root = t.TempDir()
					}
					return map[string]any{"id": target.ProjectID, "workspaceRoot": root}, nil
				}
				if name == "t3_thread_configure" && mode == "model-options" {
					return map[string]any{"threadId": "thread", "modelSelection": map[string]any{"instanceId": target.ProviderID, "model": target.Model}}, nil
				}
				return baseline(name, args)
			})
			client := &T3{Calls: caller}
			if err := client.verifyLegacySession(context.Background(), target, "thread"); (err == nil) != (mode == "quiet") {
				t.Fatalf("verify %q: %v", mode, err)
			}
		})
	}
}

func TestCheckLegacySessionReadsExactConfiguration(t *testing.T) {
	target := testConfig(t).Routes["nas"]
	target.ProjectPath = t.TempDir()
	for _, test := range []struct {
		name, status, field string
	}{
		{"matching", "matched", ""},
		{"plan", "mismatch", "interactionMode"},
		{"options", "mismatch", "options"},
		{"busy", "busy", ""},
		{"drift", "unavailable", ""},
		{"environment", "unavailable", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			actual := target
			if test.name == "plan" {
				actual.InteractionMode = "plan"
			}
			if test.name == "options" {
				actual.Options = map[string]any{"reasoningEffort": false}
			}
			base := metadataCaller(t, actual, func(_ map[string]any) map[string]any {
				page := metadataPageFor(actual)
				if test.name == "busy" {
					page["thread"].(map[string]any)["pendingRequestCount"] = 1
				}
				return page
			})
			reads := 0
			client := &T3{Config: MCPConfig{EnvironmentID: target.EnvironmentID}, Calls: scriptedCaller(func(name string, args map[string]any) (any, error) {
				if name == "t3_environment_read" && len(args) == 0 {
					if test.name == "environment" {
						return map[string]any{"environmentId": "wrong"}, nil
					}
					return map[string]any{"environmentId": target.EnvironmentID}, nil
				}
				if test.name == "environment" {
					t.Fatal("checked thread data on an unexpected environment")
				}
				if name == "t3_thread_read" || name == "t3_thread_configure" {
					if args["threadId"] != "thread" {
						t.Fatal("checked a different thread")
					}
					reads++
				}
				if name == "t3_thread_configure" {
					if len(args) != 2 {
						t.Fatal("configuration check sent arguments beyond the thread and environment")
					}
					if test.name == "drift" {
						return configurationFor(Target{Model: "different-model"}), nil
					}
				}
				if name == "t3_project_read" {
					return map[string]any{"id": target.ProjectID, "workspaceRoot": target.ProjectPath}, nil
				}
				return base(name, args)
			})}
			check := client.checkLegacySession(context.Background(), target, "thread")
			if check.Status != test.status || (reads == 2) != (test.name != "environment") {
				t.Fatalf("unexpected check with %d thread reads: %+v", reads, check)
			}
			if test.field == "" && len(check.Differences) != 0 || test.field != "" && (len(check.Differences) != 1 || check.Differences[0].Field != test.field) {
				t.Fatalf("unexpected configuration differences: %+v", check.Differences)
			}
			if test.name == "plan" && (check.Actual == nil || check.Actual.InteractionMode != "plan" || check.Differences[0].Route != "default") {
				t.Fatalf("missing original plan mode or saved default: %+v", check)
			}
		})
	}
}
