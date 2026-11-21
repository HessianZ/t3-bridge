package bridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectLegacyWorkspacesUsesExactKeys(t *testing.T) {
	raw := []byte(`{"chats":{},"named":{
		"Zeta":"/home/Z",
		"dev\u001fowner\u001foc_group:omt_topic\u001fAlpha":"/home/A",
		"BadPath":"relative",
		"Bad\u001fkey":"/home/h"
	}}`)
	aliases, invalid, err := InspectLegacyWorkspaces(raw)
	if err != nil || invalid != 2 || len(aliases) != 2 ||
		aliases[0].Name != "Zeta" || aliases[0].TopicRequired ||
		aliases[1].SourceKey != "dev\x1fowner\x1foc_group:omt_topic\x1fAlpha" ||
		aliases[1].Name != "Alpha" || aliases[1].Path != "/home/A" || !aliases[1].TopicRequired {
		t.Fatalf("unexpected aliases %+v, skipped=%d, error=%v", aliases, invalid, err)
	}
}

func TestImportLegacyWorkspacesScopesAndReadback(t *testing.T) {
	c := workspaceImportFixture(t)
	c.Routes["Duplicate"] = Target{EnvironmentID: "env1", ProjectID: "project", ProjectPath: "/t3/project/."}
	legacy := []byte(`{
		"chats":{"oc_old":{"cwd":"/legacy/default"}},
		"named":{
			"codex\u001fou_old_owner\u001foc_old:omt_old\u001fProject Alias":"/t3/project/",
			"codex\u001fou_old_owner\u001foc_old\u001fProject Alias":"/t3/base",
			"Global":"/t3/project", "Unmapped":"/t3/base"
		}
	}`)
	mapping := []byte(`[
		{"sourceKey":"codex\u001fou_old_owner\u001foc_old:omt_old\u001fProject Alias","userId":"ou_target","chatId":"oc_group","chatType":"group","topicId":"omt_target","routeId":"Work"},
		{"sourceKey":"codex\u001fou_old_owner\u001foc_old\u001fProject Alias","userId":"ou_target","chatId":"oc_group","chatType":"group","routeId":"Work","projectId":"base"},
		{"sourceKey":"Global","userId":"ou_target","chatId":"oc_direct","chatType":"p2p","routeId":"Work","projectId":"project"}
	]`)
	preview, err := ImportLegacyWorkspaces(c, legacy, mapping, false)
	if err != nil || preview.Applied || preview.Unmapped != 1 || len(preview.Aliases) != 3 ||
		preview.Aliases[0].Action != "add" || preview.Aliases[0].ProjectID != "project" {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
	if _, err := os.Stat(c.StateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preview touched destination directory: %v", err)
	}
	applied, err := ImportLegacyWorkspaces(c, legacy, mapping, true)
	if err != nil || !applied.Applied {
		t.Fatalf("apply = %+v, %v", applied, err)
	}

	store, err := OpenStore(c.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state := store.snapshot()
	if state.AccountID != c.AppID || len(state.WorkspaceNames) != 3 || len(state.Workspaces) != 0 ||
		len(state.Tasks) != 0 || len(state.Current) != 0 || len(state.Selected) != 0 {
		t.Fatal("expected only account binding and three isolated alias scopes")
	}
	for _, want := range []struct {
		chat, topic, name string
		workspace         Workspace
	}{
		{"oc_group", "omt_target", "Project Alias", Workspace{"project", "Project", "/t3/project/"}},
		{"oc_group", "", "Project Alias", Workspace{"base", "Base", "/t3/base"}},
		{"oc_direct", "", "Global", Workspace{"project", "Project", "/t3/project"}},
	} {
		key := workspaceKey(c, Message{UserID: "ou_target", ChatID: want.chat, TopicID: want.topic}, "Work")
		if got := state.WorkspaceNames[key]; len(got) != 1 || got[want.name] != want.workspace {
			t.Fatalf("aliases for %s/%s = %v", want.chat, want.topic, got)
		}
	}
	// Preview reads the durable snapshot even while the receiver holds the lock.
	replay, err := ImportLegacyWorkspaces(c, legacy, mapping, false)
	if err != nil || len(replay.Aliases) != 3 || replay.Aliases[0].Action != "unchanged" {
		t.Fatalf("replay preview = %+v, %v", replay, err)
	}
	if _, err := ImportLegacyWorkspaces(c, legacy, mapping, true); err == nil || !strings.Contains(err.Error(), "already opened") {
		t.Fatalf("apply while Store is locked = %v", err)
	}
}

func TestImportLegacyWorkspacesProjectInference(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cwd       string
		extra     Target
		projectID string
		wantError string
	}{
		{"no match in destination environment", "/t3/extra", Target{EnvironmentID: "env2", ProjectID: "other", ProjectPath: "/t3/extra"}, "", "no configured project"},
		{"ambiguous", "/t3/project", Target{EnvironmentID: "env1", ProjectID: "other", ProjectPath: "/t3/project"}, "", "multiple configured projects"},
		{"explicit override", "/t3/project", Target{EnvironmentID: "env1", ProjectID: "other", ProjectPath: "/t3/project"}, "other", ""},
		{"conflicting path", "/t3/project", Target{EnvironmentID: "env1", ProjectID: "project", ProjectPath: "/t3/extra"}, "", "configured path different from legacy cwd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := workspaceImportFixture(t)
			c.Routes["Extra"] = tc.extra
			mapping := workspaceImportMapping{
				SourceKey: "Test", UserID: "ou_target", ChatID: "oc_direct", ChatType: "p2p", RouteID: "Work", ProjectID: tc.projectID,
			}
			preview, _, err := planWorkspaceImport(c, State{}, map[string]string{"Test": tc.cwd}, []workspaceImportMapping{mapping})
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("preview error = %v, want %q", err, tc.wantError)
				}
				return
			}
			if err != nil || len(preview.Aliases) != 1 || preview.Aliases[0].ProjectID != tc.projectID {
				t.Fatalf("preview = %+v, %v", preview, err)
			}
		})
	}
}

func TestImportLegacyWorkspacesConflictsLeaveStateUntouched(t *testing.T) {
	c := workspaceImportFixture(t)
	legacy := []byte(`{"chats":{},"named":{"Keep":"/t3/base","New":"/t3/project","Work\u001fou_owner\u001foc_old:omt_old\u001fKeep":"/t3/project"}}`)
	base := workspaceImportMapping{
		SourceKey: "Keep", UserID: "ou_target", ChatID: "oc_group", ChatType: "group", TopicID: "omt_target", RouteID: "Work", ProjectID: "base",
	}
	mapping, err := json.Marshal([]workspaceImportMapping{base})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ImportLegacyWorkspaces(c, legacy, mapping, true); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(c.StateDir, "state.json")
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}

	add := base
	add.SourceKey, add.ProjectID = "New", "project"
	conflict := add
	conflict.SourceKey = "Work\x1fou_owner\x1foc_old:omt_old\x1fKeep"
	wrongUser := add
	wrongUser.UserID = "ou_owner"
	wrongTopic := conflict
	wrongTopic.TopicID = ""
	wrongProject := add
	wrongProject.ProjectID = "wrong-environment"
	for _, tc := range []struct {
		name string
		row  workspaceImportMapping
		want string
	}{
		{"existing alias", conflict, "conflict for \"Keep\""},
		{"duplicate destination", add, "multiple mappings target the same alias"},
		{"owner is not user", wrongUser, "must be authorized"},
		{"topic cannot be implicit", wrongTopic, "requires an explicit destination topicId"},
		{"project environment", wrongProject, "destination route's T3 environment"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mapping, err := json.Marshal([]workspaceImportMapping{add, tc.row})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ImportLegacyWorkspaces(c, legacy, mapping, true); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("apply error = %v", err)
			}
			after, err := os.ReadFile(statePath)
			if err != nil || !bytes.Equal(after, before) {
				t.Fatalf("destination state changed after failed import: %v", err)
			}
		})
	}
	foreign := c
	foreign.AppID = "cli_other"
	if _, err := ImportLegacyWorkspaces(foreign, legacy, mapping, true); err == nil || !strings.Contains(err.Error(), "state belongs to another app") {
		t.Fatalf("foreign state error = %v", err)
	}
}

func workspaceImportFixture(t *testing.T) Config {
	return Config{
		AppID: "cli_destination", StateDir: filepath.Join(t.TempDir(), "state"),
		Users: []string{"ou_target"}, Chats: []string{"oc_group"}, DirectChats: &[]string{"oc_direct"},
		Routes: map[string]Target{
			"Work":   {EnvironmentID: "env1", ProjectID: "base", ProjectName: "Base", ProjectPath: "/t3/base", Users: []string{"ou_target"}},
			"Known":  {EnvironmentID: "env1", ProjectID: "project", ProjectName: "Project", ProjectPath: "/t3/project"},
			"Remote": {EnvironmentID: "env2", ProjectID: "wrong-environment", ProjectPath: "/t3/project"},
		},
	}
}
