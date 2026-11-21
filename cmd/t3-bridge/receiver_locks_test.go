package main

import (
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestReceiverLocksReleasePartialAcquisition(t *testing.T) {
	registry := profileRegistry{root: t.TempDir()}
	accounts := []bridge.Config{{AppID: "cli_active"}}
	release, err := acquireReceiverLocks(registry, accounts)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	other := bridge.Config{AppID: "cli_other"}
	if unlock, err := acquireReceiverLocks(registry, []bridge.Config{other, {AppID: "cli_active", Tenant: "feishu"}}); err == nil {
		unlock()
		t.Fatal("another receiver acquired the same app")
	} else if !strings.Contains(err.Error(), "already has a receiver") {
		t.Fatal(err)
	}
	unlock, err := acquireReceiverLocks(registry, []bridge.Config{other, {AppID: "cli_active", Tenant: "lark"}})
	if err != nil {
		t.Fatalf("partial acquisition leaked a lock or collided across tenants: %v", err)
	}
	defer unlock()
}
