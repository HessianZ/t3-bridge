package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestWorkspaceImportRequiresCurrentReviewAndOfflineState(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)
	stateDir, projectPath := filepath.Join(dir, "state"), filepath.Join(dir, "project")
	path := filepath.Join(dir, "config.json")
	raw, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	edits, err := json.Marshal([]legacyImportEdit{
		{[]string{"appId"}, "cli_migrate"},
		{[]string{"stateDir"}, stateDir},
		{[]string{"users"}, []string{"ou_migrate"}},
		{[]string{"routes", "work", "users"}, []string{"ou_migrate"}},
		{[]string{"routes", "work", "projectId"}, "project"},
		{[]string{"routes", "work", "projectPath"}, projectPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = bridge.EditConfigJSON(raw, edits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localfile.WritePrivateAtomic(path, raw, true); err != nil {
		t.Fatal(err)
	}
	legacy, err := json.Marshal(map[string]any{"named": map[string]string{"Alpha": projectPath, "Beta": projectPath, "Unmapped": projectPath}})
	if err != nil {
		t.Fatal(err)
	}
	oldConfig := []byte(`{"schemaVersion":2,"activeProfile":"other","profiles":{
		"other":{"schemaVersion":2,"accounts":{"app":{"id":"cli_migrate","tenant":"lark"}}},
		"work":{"schemaVersion":2,"accounts":{"app":{"id":"cli_migrate","tenant":"feishu"}}}
	}}`)
	makeOldRoot := func(root string) string {
		t.Helper()
		workspacePath := filepath.Join(root, "profiles", "work", "workspaces.json")
		if err := localfile.MkdirAllPrivate(filepath.Dir(workspacePath)); err != nil {
			t.Fatal(err)
		}
		for file, data := range map[string][]byte{filepath.Join(root, "config.json"): oldConfig, workspacePath: legacy} {
			if _, err := localfile.WritePrivateAtomic(file, data, true); err != nil {
				t.Fatal(err)
			}
		}
		canonical, err := filepath.EvalSymlinks(workspacePath)
		if err != nil {
			t.Fatal(err)
		}
		return canonical
	}
	oldRoot := filepath.Join(dir, "old")
	t.Setenv("LARK_CHANNEL_HOME", oldRoot)
	legacyPath := makeOldRoot(oldRoot)
	const alpha = `{"sourceKey":"Alpha","userId":"ou_migrate","chatId":"oc_direct","chatType":"p2p","routeId":"work"}`
	beta := strings.Replace(alpha, "Alpha", "Beta", 1)
	source := webSource{Path: path}
	request := workspaceImportRequest{AppID: "cli_migrate", MappingJSON: "[" + alpha + "," + beta + "]"}
	var listing bytes.Buffer
	listArgs := []string{"-config", path, "-app-id", request.AppID, "-list"}
	if err := runConfigImportWorkspaces(listArgs, &listing); err != nil {
		t.Fatal(err)
	}
	var found workspaceImportInspection
	if err := json.Unmarshal(listing.Bytes(), &found); err != nil || found.LegacyPath != legacyPath || found.AppID != request.AppID ||
		len(found.Aliases) != 3 || found.Aliases[0].SourceKey != "Alpha" || len(found.Routes) != 1 || found.Routes[0] != "work" {
		t.Fatalf("unexpected source listing: %s, %v", &listing, err)
	}
	if _, err := os.Lstat(stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("listing created destination state: %v", err)
	}
	listing.Reset()
	if err := runConfigImportWorkspaces(append(listArgs, "-apply"), &listing); err == nil || listing.Len() != 0 {
		t.Fatalf("list accepted a write flag: %s, %v", &listing, err)
	}
	preview, review, err := previewWorkspaceImport(source, request)
	if err != nil || review == "" || preview.LegacyPath != legacyPath || preview.Applied || preview.Unmapped != 1 ||
		len(preview.Aliases) != 2 || preview.Aliases[0].Action != "add" || preview.Aliases[0].ProjectID != "project" {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
	if _, err := applyWorkspaceImport(source, request, ""); err == nil {
		t.Fatal("write without preview was accepted")
	}
	changed := request
	changed.MappingJSON = "[" + beta + "]"
	if _, err := applyWorkspaceImport(source, changed, review); !errors.Is(err, errWorkspaceImportChanged) {
		t.Fatalf("write after mapping changed: %v", err)
	}
	t.Run("source path is part of the review", func(t *testing.T) {
		other := filepath.Join(dir, "other")
		makeOldRoot(other)
		t.Setenv("LARK_CHANNEL_HOME", other)
		if _, err := applyWorkspaceImport(source, request, review); !errors.Is(err, errWorkspaceImportChanged) {
			t.Fatalf("write after discovered path changed: %v", err)
		}
	})
	t.Run("ambiguous source requires explicit path", func(t *testing.T) {
		ambiguous := t.TempDir()
		old := bytes.Replace(oldConfig, []byte(`"tenant":"lark"`), []byte(`"tenant":"feishu"`), 1)
		if _, err := localfile.WritePrivateAtomic(filepath.Join(ambiguous, "config.json"), old, true); err != nil {
			t.Fatal(err)
		}
		t.Setenv("LARK_CHANNEL_HOME", ambiguous)
		if _, _, err := previewWorkspaceImport(source, request); err == nil || !strings.Contains(err.Error(), "多个旧 profile") {
			t.Fatalf("ambiguous profile should require a file: %v", err)
		}
		explicit := request
		explicit.LegacyPath = legacyPath
		if got, _, err := previewWorkspaceImport(source, explicit); err != nil || got.LegacyPath != legacyPath {
			t.Fatalf("explicit source failed: %v", err)
		}
	})
	t.Run("single account layout", func(t *testing.T) {
		root := t.TempDir()
		old := []byte(`{"app":{"id":"cli_migrate","tenant":"feishu"}}`)
		for name, data := range map[string][]byte{"config.json": old, "workspaces.json": legacy} {
			if _, err := localfile.WritePrivateAtomic(filepath.Join(root, name), data, true); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("LARK_CHANNEL_HOME", root)
		path, err := filepath.EvalSymlinks(filepath.Join(root, "workspaces.json"))
		if err != nil {
			t.Fatal(err)
		}
		if got, _, err := previewWorkspaceImport(source, request); err != nil || got.LegacyPath != path {
			t.Fatalf("single account source failed: %v", err)
		}
	})
	for file, original := range map[string][]byte{path: raw, legacyPath: legacy} {
		if err := os.WriteFile(file, append(bytes.Clone(original), '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := applyWorkspaceImport(source, request, review); !errors.Is(err, errWorkspaceImportChanged) {
			t.Fatalf("write after %s changed: %v", filepath.Base(file), err)
		}
		if err := os.WriteFile(file, original, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Lstat(stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preview or rejected write created state: %v", err)
	}
	store, err := bridge.OpenStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	_, lockedErr := applyWorkspaceImport(source, request, review)
	store.Close()
	if lockedErr == nil || !strings.Contains(lockedErr.Error(), "already opened") {
		t.Fatalf("write while the receiver holds the state lock: %v", lockedErr)
	}
	account, err := bridge.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.ImportLegacyWorkspaces(account, legacy, []byte("["+alpha+"]"), true); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(stateDir, "state.json")
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyWorkspaceImport(source, request, review); !errors.Is(err, errWorkspaceImportChanged) {
		t.Fatalf("write after plan changed: %v", err)
	}
	if after, err := os.ReadFile(statePath); err != nil || !bytes.Equal(before, after) {
		t.Fatalf("rejected write changed state: %v", err)
	}
	preview, review, err = previewWorkspaceImport(source, request)
	if err != nil || len(preview.Aliases) != 2 || preview.Aliases[0].Action != "unchanged" || preview.Aliases[1].Action != "add" {
		t.Fatalf("refreshed preview = %+v, %v", preview, err)
	}
	if saved, err := applyWorkspaceImport(source, request, review); err != nil || !saved.Applied {
		t.Fatalf("apply = %+v, %v", saved, err)
	}
	readback, _, err := previewWorkspaceImport(source, request)
	if err != nil || len(readback.Aliases) != 2 || readback.Aliases[0].Action != "unchanged" || readback.Aliases[1].Action != "unchanged" {
		t.Fatalf("readback = %+v, %v", readback, err)
	}
	mappingFile := filepath.Join(dir, "mapping.json")
	if err := os.WriteFile(mappingFile, []byte(request.MappingJSON), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"-config", path, "-app-id", request.AppID, "-mapping", mappingFile}
	var cliOut bytes.Buffer
	if err := runConfigImportWorkspaces(append(args, "-apply"), &cliOut); err == nil || cliOut.Len() != 0 {
		t.Fatalf("allowed direct apply with discovered source: %v", err)
	}
	if err := runConfigImportWorkspaces(args, &cliOut); err != nil {
		t.Fatal(err)
	}
	var cliPreview struct {
		LegacyPath, Review string
	}
	if err := json.Unmarshal(cliOut.Bytes(), &cliPreview); err != nil || cliPreview.LegacyPath != legacyPath || cliPreview.Review == "" {
		t.Fatalf("CLI preview omitted the path or review: %s, %v", &cliOut, err)
	}
	cliOut.Reset()
	if err := runConfigImportWorkspaces(append(args, "-confirm", cliPreview.Review), &cliOut); err != nil ||
		!bytes.Contains(cliOut.Bytes(), []byte(`"applied": true`)) {
		t.Fatalf("CLI review did not confirm: %s, %v", &cliOut, err)
	}
	cliOut.Reset()
	if err := runConfigImportWorkspaces(append(args, "-legacy-workspaces", legacyPath, "-apply"), &cliOut); err != nil ||
		!bytes.Contains(cliOut.Bytes(), []byte(`"applied": true`)) {
		t.Fatalf("explicit source direct apply failed: %s, %v", &cliOut, err)
	}
	for file, want := range map[string][]byte{path: raw, legacyPath: legacy} {
		if after, err := os.ReadFile(file); err != nil || !bytes.Equal(want, after) {
			t.Fatalf("migration modified input %s: %v", file, err)
		}
	}
}

func TestAppendWorkspaceImportMappingPreservesExistingRows(t *testing.T) {
	const raw = `[{"sourceKey":"Existing","projectId":"projectA","customField":"keep for validation"}]`
	entry := workspaceImportDraftEntry{
		SourceKey: "dev\x1fowner\x1foc_chat:omt_topic\x1fScoped",
		UserID:    "ou_example", ChatID: "oc_chat", ChatType: "group", TopicID: "omt_topic", RouteID: "work",
	}
	got, err := appendWorkspaceImportMapping(raw, entry)
	var rows []json.RawMessage
	if err != nil || json.Unmarshal([]byte(got), &rows) != nil || len(rows) != 2 ||
		!bytes.Contains(rows[0], []byte(`"customField": "keep for validation"`)) {
		t.Fatalf("existing draft changed unexpectedly: %s, %v", got, err)
	}
	var appended workspaceImportDraftEntry
	if err := json.Unmarshal(rows[1], &appended); err != nil || appended != entry {
		t.Fatalf("appended mapping lost fields or the exact key: %+v, %v", appended, err)
	}
	if got, err := appendWorkspaceImportMapping("null", entry); err == nil || got != "" {
		t.Fatal("adding a row accepted a non-array draft")
	}
}
