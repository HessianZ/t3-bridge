package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestLegacySessionInventoryMatchesSavedAppAndTenant(t *testing.T) {
	dir := t.TempDir()
	cfg, err := bridge.ParseConfig(tuiTestSource(t).JSON)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AppID, cfg.StateDir = "cli_matching_app", filepath.Join(dir, "new-state")
	target := filepath.Join(dir, "target.json")
	old := filepath.Join(dir, "old.json")
	writeJSON := func(path string, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeJSON(target, cfg)
	oldProfile := func(agent, tenant string) map[string]any {
		return map[string]any{"schemaVersion": 2, "agentKind": agent, "accounts": map[string]any{
			"app": map[string]string{"id": cfg.AppID, "tenant": tenant, "secret": "synthetic-inline-secret"},
		}}
	}
	profiles := map[string]any{
		"Active": oldProfile("codex", "feishu"), "OtherTenant": oldProfile("t3", "lark"),
		"Exact.Case": oldProfile("t3", "feishu"),
	}
	oldConfig := map[string]any{"schemaVersion": 2, "activeProfile": "Active", "profiles": profiles}
	writeJSON(old, oldConfig)
	catalogDir := filepath.Join(dir, "profiles", "Exact.Case")
	if err := os.MkdirAll(catalogDir, 0700); err != nil {
		t.Fatal(err)
	}
	const chat, scope, path = "oc_other", "oc_other:omt_one", "/synthetic/project"
	entry := func(scope, thread string) map[string]any {
		return map[string]any{
			"key": strings.Join([]string{scope, "t3", path, "fingerprint"}, "\x1f"), "scopeId": scope,
			"agentId": "t3", "cwdRealpath": path, "policyFingerprint": "fingerprint", "status": "active",
			"threadId": thread, "lastSummary": "synthetic-private-transcript", "updatedAt": 50,
		}
	}
	writeJSON(filepath.Join(catalogDir, "sessions.json.catalog.json"), []any{entry(scope, "topic-thread"), entry(chat, "chat-thread")})
	args := []string{"-config", target, "-legacy-config", old, "-list"}
	var out bytes.Buffer
	if err := runConfigImportSession(args, &out); err != nil {
		t.Fatal(err)
	}
	var report legacySessionInspection
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.AppID != cfg.AppID || report.LegacyProfile != "Exact.Case" || len(report.Scopes) != 2 ||
		report.Scopes[0].ChatID != chat || report.Scopes[0].TopicID != "" || report.Scopes[1].TopicID != "omt_one" ||
		out.String() == "" || strings.Contains(out.String(), "synthetic-") || strings.Contains(out.String(), "topic-thread") {
		t.Fatalf("unexpected inventory: %s", out.String())
	}
	out.Reset()
	if err := runConfigImportSession(append(args, "-chat-id", chat), &out); err != nil || !strings.Contains(out.String(), "chat-thread") ||
		strings.Contains(out.String(), "topic-thread") {
		t.Fatalf("exact lookup: %s, %v", out.String(), err)
	}
	out.Reset()
	if err := runConfigImportSession(append(args, "-chat-id", chat, "-chat-type", "group", "-topic-id", "omt_one"), &out); err != nil ||
		!strings.Contains(out.String(), "topic-thread") || strings.Contains(out.String(), "chat-thread") {
		t.Fatalf("exact topic lookup: %s, %v", out.String(), err)
	}
	if err := runConfigImportSession(append(args, "-legacy-profile", "OtherTenant"), &out); err == nil {
		t.Fatal("accepted explicitly selected profile for a different tenant")
	}
	profiles["AlsoMatching"] = oldProfile("t3", "feishu")
	writeJSON(old, oldConfig)
	out.Reset()
	if err := runConfigImportSession(args, &out); err == nil || out.Len() != 0 {
		t.Fatalf("resolved ambiguous profiles: %s, %v", out.String(), err)
	}
	if err := runConfigImportSession(append(args, "-legacy-profile", "Exact.Case"), &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg.StateDir); !os.IsNotExist(err) {
		t.Fatalf("read-only inventory accessed destination state: %v", err)
	}
}
