package bridge

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestInitialRuntimeConfig(t *testing.T) {
	c := testConfig(t)
	c.Chats = []string{"oc_Group", "invalid"}
	base, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, seed string
		valid      bool
	}{
		{"explicit group", `{"settings":{"maxConcurrent":0,"processMode":"card"},"requireMention":false,"groupMentions":{"oc_Group":false}}`, true},
		{"invalid settings", `{"settings":{"maxConcurrent":51}}`, false},
		{"invalid tool display", `{"settings":{"toolActivity":"card"}}`, false},
		{"invalid group ID", `{"groupMentions":{"invalid":true}}`, false},
		{"unknown group", `{"groupMentions":{"oc_group":true}}`, false},
		{"unknown field", `{"groupMentions":{},"chats":["oc_Group"]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loaded, err := ParseConfig(append([]byte(`{"initialRuntime":`+tc.seed+`,`), base[1:]...))
			if (err == nil) != tc.valid {
				t.Fatalf("ParseConfig() error = %v", err)
			}
			if tc.valid {
				seed := loaded.InitialRuntime
				if seed == nil || seed.Settings.MaxConcurrent == nil || *seed.Settings.MaxConcurrent != 0 ||
					seed.Settings.ProcessMode == nil || *seed.Settings.ProcessMode != "card" ||
					seed.RequireMention == nil || *seed.RequireMention || !reflect.DeepEqual(seed.GroupMentions, map[string]bool{"oc_Group": false}) {
					t.Fatalf("explicit settings, false or case lost: %+v", seed)
				}
			}
		})
	}
}

func TestBindReceiverInitialRuntime(t *testing.T) {
	open := func(t *testing.T, c Config) *Store {
		t.Helper()
		store, err := OpenStore(c.StateDir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.Close() })
		return store
	}
	reopen := func(t *testing.T, c Config, store *Store) *Store {
		t.Helper()
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		return open(t, c)
	}
	concurrency, required := 3, false
	seed := &InitialRuntimeConfig{
		Settings: RunSettingsPatch{MaxConcurrent: &concurrency}, RequireMention: &required,
		GroupMentions: map[string]bool{"oc_group": true},
	}
	checkSeed := func(t *testing.T, st State, c Config) {
		t.Helper()
		if st.AccountID != c.AppID || !st.ReceiverStarted || st.Settings.MaxConcurrent != concurrency ||
			st.GroupAccess.RequireMention == nil || *st.GroupAccess.RequireMention ||
			!reflect.DeepEqual(st.GroupAccess.Mentions, seed.GroupMentions) || len(st.GroupAccess.Chats) != 0 {
			t.Fatalf("account, startup or initial runtime values are incorrect: %+v", st)
		}
	}

	t.Run("fresh and atomic", func(t *testing.T) {
		c := workspaceImportFixture(t)
		c.InitialRuntime = seed
		store := open(t, c)
		writeErr := errors.New("synthetic write failure")
		store.writeState = func(string, []byte, bool) (bool, error) { return false, writeErr }
		before := store.snapshot()
		if err := store.BindReceiver(c); !errors.Is(err, writeErr) || !reflect.DeepEqual(store.snapshot(), before) {
			t.Fatalf("failed write changed the account or runtime state: %v", err)
		}
		store.writeState = nil
		if err := store.BindReceiver(c); err != nil {
			t.Fatal(err)
		}
		store = reopen(t, c, store)
		checkSeed(t, store.snapshot(), c)
	})

	for _, mode := range []string{"workspace", "session"} {
		t.Run("prebound by "+mode+" import", func(t *testing.T) {
			c := workspaceImportFixture(t)
			c.InitialRuntime = seed
			if mode == "workspace" {
				legacy := []byte(`{"named":{"Alias":"/t3/base"}}`)
				mapping := []byte(`[{"sourceKey":"Alias","userId":"ou_target","chatId":"oc_group","chatType":"group","routeId":"Work","projectId":"base"}]`)
				if _, err := ImportLegacyWorkspaces(c, legacy, mapping, true); err != nil {
					t.Fatal(err)
				}
			} else {
				catalog := []byte(`[{"key":"oc_direct\u001ft3\u001f/t3/base\u001ffp","scopeId":"oc_direct","agentId":"t3","cwdRealpath":"/t3/base","policyFingerprint":"fp","status":"active","threadId":"old-thread"}]`)
				mapping := LegacySessionMapping{ThreadID: "old-thread", ChatID: "oc_direct", UserID: "ou_target", RouteID: "Work"}
				if _, err := ImportLegacySession(c, catalog, mapping, func(LegacySessionImportResult) error { return nil }); err != nil {
					t.Fatal(err)
				}
			}
			store := open(t, c)
			before := store.snapshot()
			if before.AccountID != c.AppID || before.ReceiverStarted || before.Settings != (RunSettings{}) {
				t.Fatal("offline import changed receiver startup state")
			}
			if err := store.BindReceiver(c); err != nil {
				t.Fatal(err)
			}
			after := store.snapshot()
			checkSeed(t, after, c)
			if !reflect.DeepEqual(after.WorkspaceNames, before.WorkspaceNames) || !reflect.DeepEqual(after.LegacySessions, before.LegacySessions) {
				t.Fatal("startup changed the imported records")
			}

			e := Engine{Config: c, Store: store}
			e.Config.access = &accessRuntime{}
			zero := 0
			if _, err := e.LocalRunSettings(t.Context(), RunSettingsPatch{MaxConcurrent: &zero}); err != nil {
				t.Fatal(err)
			}
			for _, chat := range []string{"", "oc_group"} {
				if _, err := e.LocalGroupAccess(t.Context(), GroupAccessRequest{ChatID: chat, Mention: "inherit"}); err != nil {
					t.Fatal(err)
				}
			}
			before.AccountID, before.ReceiverStarted = c.AppID, true
			store = reopen(t, c, store)
			if err := store.BindReceiver(c); err != nil || !reflect.DeepEqual(store.snapshot(), before) {
				t.Fatalf("restart failed to retain reset values: %v", err)
			}
		})
	}

	for _, tc := range []struct {
		name, bindErr string
		prepare       func(*State, string)
	}{
		{"older bound receiver", "", func(st *State, app string) { st.AccountID = app }},
		{"existing settings and history", "", func(st *State, app string) {
			st.AccountID = app
			st.Settings.ReplyMode = "text"
			st.GroupAccess.setAllowed("oc_group", false)
			st.GroupAccess.setMention("oc_group", false)
			st.Seen["previous"] = true
		}},
		{"unbound legacy settings", "", func(st *State, _ string) { st.Settings.ReplyMode = "text" }},
		{"unadopted legacy history", "legacy state has no app identity", func(st *State, _ string) { st.Seen["previous"] = true }},
		{"prebound with runtime history", "", func(st *State, app string) {
			st.AccountID = app
			st.LegacySessions = map[string]legacySessionSeed{"scope": {ThreadID: "old-thread"}}
			st.Seen["previous"] = true
		}},
		{"foreign account", "state belongs to another app", func(st *State, _ string) { st.AccountID = "foreign-app" }},
		{"foreign task", "task belongs to another app", func(st *State, app string) {
			st.AccountID = app
			st.Tasks["foreign"] = &Task{ReceiverAppID: "foreign-app"}
		}},
		{"first start without seed", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := workspaceImportFixture(t)
			store := open(t, c)
			if tc.prepare == nil {
				if err := store.BindReceiver(c); err != nil {
					t.Fatal(err)
				}
			} else if err := store.update(func(st *State) error { tc.prepare(st, c.AppID); return nil }); err != nil {
				t.Fatal(err)
			}
			store = reopen(t, c, store)
			before := store.snapshot()
			c.InitialRuntime = seed
			err := store.BindReceiver(c)
			if tc.bindErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.bindErr) {
					t.Fatalf("BindReceiver() error = %v, want %q", err, tc.bindErr)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				before.AccountID, before.ReceiverStarted = c.AppID, true
			}
			if !reflect.DeepEqual(store.snapshot(), before) {
				t.Fatal("startup changed existing runtime state")
			}
		})
	}
}
