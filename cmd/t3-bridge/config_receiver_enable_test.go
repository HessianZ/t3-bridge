package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestPrepareReceiverEnableScopesCheckAndEdit(t *testing.T) {
	config, err := bridge.ParseConfig(tuiTestSource(t).JSON)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	config.AppID, config.StateDir = "cli_primary", filepath.Join(dir, "primary-state")
	extra := config
	extra.AppID, extra.StateDir = "cli_additional", filepath.Join(dir, "additional-state")
	config.AdditionalAccounts = []bridge.Config{extra}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "bridge.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	revision := configRevision(raw)
	passing := bridge.ReceiverPreflight{AllAccounts: &bridge.ReceiverPreflightOutcome{Passed: true}}
	selectAccount := func(want string) func(context.Context, bridge.Config, bool) bridge.ReceiverPreflight {
		return func(_ context.Context, account bridge.Config, includeDisabled bool) bridge.ReceiverPreflight {
			if account.AppID != want || account.ReceiverAuthorized || len(account.AdditionalAccounts) != 0 || !includeDisabled {
				t.Fatalf("unexpected preflight scope: %s, additional=%d, enabled=%t, includeDisabled=%t",
					account.AppID, len(account.AdditionalAccounts), account.ReceiverAuthorized, includeDisabled)
			}
			return passing
		}
	}
	for _, id := range []string{"", extra.AppID} {
		want := config
		want.AdditionalAccounts = []bridge.Config{extra}
		selected := config.AppID
		if id == "" {
			want.ReceiverAuthorized = true
		} else {
			selected = id
			want.AdditionalAccounts[0].ReceiverAuthorized = true
		}
		result, err := prepareReceiverEnableWithCheck(t.Context(), path, revision, id, selectAccount(selected))
		if err != nil {
			t.Fatal(err)
		}
		got, err := bridge.ParseConfig(result.Candidate)
		if err != nil {
			t.Fatal(err)
		}
		wantedBytes, _ := json.Marshal(want)
		actualBytes, _ := json.Marshal(got)
		if !bytes.Equal(wantedBytes, actualBytes) || !reflect.DeepEqual(result.Report, passing) {
			t.Fatal("the plan changed fields outside the selected App's receive flag")
		}
	}
	notCalled := func(context.Context, bridge.Config, bool) bridge.ReceiverPreflight {
		t.Fatal("an invalid request performed a preflight")
		return bridge.ReceiverPreflight{}
	}
	if _, err := prepareReceiverEnableWithCheck(t.Context(), path, "stale", "", notCalled); !errors.Is(err, errWorkspaceServiceConfigChanged) {
		t.Fatalf("stale revision accepted: %v", err)
	}
	if _, err := prepareReceiverEnableWithCheck(t.Context(), path, revision, "cli_other", notCalled); err == nil {
		t.Fatal("unknown App accepted")
	}
	blocked := bridge.ReceiverPreflight{AllAccounts: &bridge.ReceiverPreflightOutcome{Passed: false}}
	result, err := prepareReceiverEnableWithCheck(t.Context(), path, revision, "",
		func(context.Context, bridge.Config, bool) bridge.ReceiverPreflight { return blocked })
	if err != nil || result.Candidate != nil || !reflect.DeepEqual(result.Report, blocked) {
		t.Fatalf("failed preflight produced a draft: %+v, %v", result, err)
	}
	saved, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(saved, raw) {
		t.Fatalf("preview modified the saved configuration: %v", err)
	}
	result, err = prepareReceiverEnableWithCheck(t.Context(), path, revision, "",
		func(context.Context, bridge.Config, bool) bridge.ReceiverPreflight {
			if _, err := saveConfigCandidate(path, append(bytes.Clone(raw), '\n'), revision); err != nil {
				t.Fatal(err)
			}
			return passing
		})
	if !errors.Is(err, errWorkspaceServiceConfigChanged) || result.Candidate != nil {
		t.Fatalf("configuration replaced during probe still produced a candidate: %v", err)
	}
}
