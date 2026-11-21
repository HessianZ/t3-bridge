package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestConfigTelemetryArchive(t *testing.T) {
	dir := t.TempDir()
	raw, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	config, err := bridge.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	config.Tenant, config.AppID = "feishu", "cli_archive_primary"
	config.StateDir = filepath.Join(dir, "primary")
	config.AppSecretFile = filepath.Join(dir, "primary-secret-unused")
	config.MCP.TokenFile = filepath.Join(dir, "primary-token-unused")
	extra := config
	extra.Tenant, extra.AppID = "lark", "cli_archive_extra"
	extra.StateDir = filepath.Join(dir, "extra")
	extra.AppSecretFile = filepath.Join(dir, "extra-secret-unused")
	extra.MCP.TokenFile = filepath.Join(dir, "extra-token-unused")
	config.AdditionalAccounts = []bridge.Config{extra}
	raw, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "saved.json")
	if _, err := localfile.WritePrivateAtomic(configPath, raw, true); err != nil {
		t.Fatal(err)
	}
	const day = "2026-02-20"
	archive := receiverTelemetryArchiveDir(extra.StateDir)
	if err := localfile.MkdirAllPrivate(archive); err != nil {
		t.Fatal(err)
	}
	record := func(tenant, appID, at, kind string) string {
		return `{"type":"receiver-event","tenant":"` + tenant + `","appId":"` + appID + `","at":"` + at + `","kind":"` + kind + `","content":"private-fixture"}`
	}
	early := record("lark", extra.AppID, day+"T00:00:00Z", "run-started")
	latest := record("lark", extra.AppID, day+"T01:00:00Z", "run-completed")
	fileData := strings.Join([]string{
		strings.Repeat("x", serviceLogMaxBytes), early, latest,
		record("lark", extra.AppID, "2026-02-21T00:00:00Z", "run-failed"),
		record("feishu", extra.AppID, day+"T00:00:00Z", "outbound-sent"),
		record("lark", extra.AppID+"suffix", day+"T00:00:00Z", "outbound-sent"),
		record("lark", extra.AppID, day+"T02:00:00Z", "outbound-sent"), // Missing newline.
	}, "\n")
	if _, err := localfile.WritePrivateAtomic(filepath.Join(archive, receiverTelemetryArchiveName(day)), []byte(fileData), true); err != nil {
		t.Fatal(err)
	}
	args := []string{"telemetry-archive", "-config", configPath, "-date", day}
	var output bytes.Buffer
	if err := runConfig(args, nil, &output); err != nil || output.Len() != 0 {
		t.Fatalf("primary with no archive = %v, %q", err, output.String())
	}
	if err := runConfig(append(args, "-app-id", extra.AppID, "-lines", "1"), nil, &output); err != nil {
		t.Fatal(err)
	}
	want := `{"type":"receiver-event","tenant":"lark","appId":"cli_archive_extra","at":"2026-02-20T01:00:00Z","kind":"run-completed"}` + "\n"
	if output.String() != want {
		t.Fatalf("archive = %q, want %q", output.String(), want)
	}
	displayDay, err := parseReceiverTelemetryArchiveDate(day)
	if err != nil {
		t.Fatal(err)
	}
	revision := configRevision(raw)
	data, err := lookupWorkspaceTelemetryArchive(configPath, extra.AppID, revision, displayDay)
	if err != nil || bytes.Count(data, []byte{'\n'}) != 2 || !bytes.HasSuffix(data, []byte(want)) {
		t.Fatalf("saved archive lookup = %q, %v", data, err)
	}
	if _, err := localfile.WritePrivateAtomic(configPath, append(raw, '\n'), false); err != nil {
		t.Fatal(err)
	}
	if data, err := lookupWorkspaceTelemetryArchive(configPath, extra.AppID, revision, displayDay); len(data) != 0 || !errors.Is(err, errWorkspaceServiceConfigChanged) {
		t.Fatalf("stale saved archive lookup = %q, %v", data, err)
	}
	for _, flags := range [][]string{{"-app-id", "cli_unknown"}, {"-date", "2026-02-30"}, {"-date", "../2026-02-20"}, {"-lines", "0"}, {"-lines", "1001"}} {
		output.Reset()
		if err := runConfig(append(args, flags...), nil, &output); err == nil || output.Len() != 0 {
			t.Errorf("flags %v = %v, %q", flags, err, output.String())
		}
	}
}

func TestReceiverTelemetryRetentionDays(t *testing.T) {
	for raw, want := range map[string]int{"": 30, "foo": 30, "0": 30, "-2": 30, "1": 1, "3650": 3650, "3651": 30} {
		if got := receiverTelemetryRetentionDays(raw); got != want {
			t.Errorf("retention %q = %d, want %d", raw, got, want)
		}
	}
}
