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

func TestConfigDrainReturnsAllAccountsAndNonzeroForBusy(t *testing.T) {
	root := t.TempDir()
	template, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	config, err := bridge.ParseConfig(template)
	if err != nil {
		t.Fatal(err)
	}
	config.AppID = "cli_fixture_primary"
	config.StateDir = filepath.Join(root, "primary")
	config.AppSecretFile = filepath.Join(root, "secret-unused")
	config.MCP.TokenFile = filepath.Join(root, "token-unused")
	extra := config
	extra.AppID, extra.StateDir, extra.ReceiverAuthorized = "cli_fixture_extra", filepath.Join(root, "extra"), false
	config.AdditionalAccounts = []bridge.Config{extra}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	if _, err := localfile.WritePrivateAtomic(path, raw, true); err != nil {
		t.Fatal(err)
	}
	if _, err := checkSavedDrain(path, configRevision(template)); !errors.Is(err, errWorkspaceServiceConfigChanged) {
		t.Fatalf("stale revision returned %v", err)
	}
	for _, account := range config.Accounts() {
		store, err := bridge.OpenStore(account.StateDir)
		if err != nil {
			t.Fatal(err)
		}
		bindErr := store.BindAccount(account.AppID)
		if err := errors.Join(bindErr, store.Close()); err != nil {
			t.Fatal(err)
		}
	}
	held, err := bridge.OpenStore(extra.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	args := []string{"drain", "-config", path}
	var output bytes.Buffer
	checkJSON := func(drained bool, extraStatus string) {
		t.Helper()
		var report bridge.DrainReport
		if err := json.Unmarshal(output.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		if report.Drained != drained || len(report.Accounts) != 2 ||
			report.Accounts[0].AppID != config.AppID || report.Accounts[0].Status != "drained" ||
			report.Accounts[1].AppID != extra.AppID || report.Accounts[1].Status != extraStatus {
			t.Fatalf("unexpected drain report: %s", output.String())
		}
		if strings.Contains(output.String(), "secret-unused") || strings.Contains(output.String(), "token-unused") {
			t.Fatalf("drain exposed a credential reference: %s", output.String())
		}
	}
	if err := runConfig(args, nil, &output); err == nil {
		t.Fatal("busy account was accepted")
	}
	checkJSON(false, "busy")
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := runConfig(args, nil, &output); err != nil {
		t.Fatal(err)
	}
	checkJSON(true, "drained")
}
