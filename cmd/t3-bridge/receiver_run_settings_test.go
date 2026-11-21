package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestReceiverRunSettingsExchange(t *testing.T) {
	r, process := testGroupAccessRegistry(t)
	account := newReceiverGroupAccount("lark", "cli_shared")
	pending := receiverRunSettingsIPC.pendingPath(process.dir, account)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	type result struct {
		view bridge.RunSettingsView
		err  error
	}
	done := make(chan result, 1)
	go func() {
		off, disabled := false, 0
		view, err := r.runSettings(ctx, process.process.ID, account.Tenant, account.AppID, bridge.RunSettingsPatch{
			Progress: &off, IdleTimeoutMinutes: &disabled,
		})
		done <- result{view, err}
	}()
	want := bridge.RunSettingsView{MaxConcurrent: 2, ReplyMode: "text", ToolActivity: "brief", ProcessMode: "off"}
	apply := func(_ context.Context, patch bridge.RunSettingsPatch) (bridge.RunSettingsView, error) {
		if patch.Progress == nil || *patch.Progress || patch.IdleTimeoutMinutes == nil || *patch.IdleTimeoutMinutes != 0 ||
			patch.MaxConcurrent != nil || patch.ReplyMode != nil || patch.ToolActivity != nil || patch.ProcessMode != nil {
			t.Errorf("received unexpected patch: %+v", patch)
		}
		return want, nil
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case got := <-done:
			if got.err != nil || got.view != want {
				t.Fatalf("run settings returned %+v, %v", got.view, got.err)
			}
			return
		case <-ticker.C:
			receiverRunSettingsIPC.serve(ctx, process, account, pending, apply)
		case <-ctx.Done():
			t.Fatal("run settings exchange did not complete")
		}
	}
}

func TestReceiverRunSettingsRejectMalformedAndWrongAccount(t *testing.T) {
	_, process := testGroupAccessRegistry(t)
	account := newReceiverGroupAccount("lark", "cli_shared")
	pending := receiverRunSettingsIPC.pendingPath(process.dir, account)
	const ticket = "00000000000000000000000000000001"
	returnDir := receiverRunSettingsIPC.returnDir(process.dir, ticket)
	if err := localfile.MkdirAllPrivate(returnDir); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, request, tenant string
	}{
		{"null request", `null`, "lark"},
		{"malformed field", `{"progress":false,"idleTimeoutMinutes":null}`, "lark"},
		{"duplicate field", `{"progress":false,"progress":true}`, "lark"},
		{"wrong tenant", `{"progress":false}`, "feishu"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := json.RawMessage(tc.request)
			writeGroupAccessTestFile(t, pending, receiverIPCCommand[json.RawMessage]{
				receiverGroupRoute: receiverGroupRoute{Instance: process.process.ID, Account: newReceiverGroupAccount(tc.tenant, account.AppID), Ticket: ticket},
				Expires:            time.Now().Add(time.Minute),
				Request:            &request,
			})
			receiverRunSettingsIPC.serve(t.Context(), process, account, pending, func(context.Context, bridge.RunSettingsPatch) (bridge.RunSettingsView, error) {
				t.Fatal("worker applied an invalid settings request")
				return bridge.RunSettingsView{}, nil
			})
			if _, err := os.Lstat(pending); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid request was left pending: %v", err)
			}
		})
	}
}

func TestReceiverRunSettingsRejectLegacyRegistration(t *testing.T) {
	r, process := testGroupAccessRegistry(t)
	legacy := process.process
	legacy.RunSettings = false
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localfile.WritePrivateAtomic(filepath.Join(process.dir, "process.json"), data, false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.runSettings(t.Context(), legacy.ID, "", "cli_shared", bridge.RunSettingsPatch{}); !errors.Is(err, errReceiverRunSettingsUnsupported) {
		t.Fatalf("legacy receiver returned %v", err)
	}
}
