package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestReceiverReconnectClaimsOnlySelectedAccount(t *testing.T) {
	r, p := testGroupAccessRegistry(t)
	account := newReceiverGroupAccount("lark", "cli_shared")
	other := newReceiverGroupAccount("feishu", "cli_shared")
	ctx := t.Context()
	command := receiverIPCCommand[receiverReconnectRequest]{
		receiverGroupRoute: receiverGroupRoute{Instance: p.process.ID, Account: account, Ticket: strings.Repeat("a", 32)},
		Expires:            time.Now().Add(5 * time.Second),
		Request:            &receiverReconnectRequest{Confirm: true},
	}
	if err := receiverReconnectIPC.enqueue(ctx, r, command); err != nil {
		t.Fatal(err)
	}
	calls := 0
	apply := func(gotCtx context.Context, action bridge.ReconnectAction) (bridge.LocalReconnectView, error) {
		calls++
		if gotCtx != ctx || action != bridge.ReconnectNow {
			t.Fatal("connection took the IPC deadline instead of the worker lifetime")
		}
		return bridge.LocalReconnectView{State: bridge.ReconnectStarted}, nil
	}
	p.serveReconnect(ctx, other, receiverReconnectIPC.pendingPath(p.dir, other), apply)
	if calls != 0 {
		t.Fatal("another tenant claimed the selected App's reconnect")
	}
	pending := receiverReconnectIPC.pendingPath(p.dir, account)
	p.serveReconnect(ctx, account, pending, apply)
	p.serveReconnect(ctx, account, pending, apply)
	if calls != 1 {
		t.Fatalf("connection attempts = %d, want 1", calls)
	}
	file := filepath.Join(receiverReconnectIPC.returnDir(p.dir, command.Ticket), "reply.json")
	got, err := readReceiverIPCFile[receiverIPCReply[bridge.LocalReconnectView]](file, receiverIPCCommandLimit, true)
	if err != nil || got.receiverGroupRoute != command.receiverGroupRoute || got.Failed || got.View == nil || got.View.State != bridge.ReconnectStarted {
		t.Fatalf("reconnect receipt = %+v, %v", got, err)
	}
}

func TestConfigLiveReconnectRequiresConfirmation(t *testing.T) {
	process := strings.Repeat("a", 32)
	args := []string{"-process", process, "-tenant", "lark", "-app-id", "cli_selected"}
	var output bytes.Buffer
	calls := 0
	apply := func(_ context.Context, id, tenant, app string, action bridge.ReconnectAction) (bridge.LocalReconnectView, error) {
		calls++
		if id != process || tenant != "lark" || app != "cli_selected" {
			t.Fatalf("unexpected target: %q %q %q", id, tenant, app)
		}
		state := bridge.ReconnectStarted
		switch action {
		case bridge.ReconnectAfterRuns:
			state = bridge.ReconnectScheduled
		case bridge.ReconnectCancelWait:
			state = bridge.ReconnectCancelled
		}
		return bridge.LocalReconnectView{State: state}, nil
	}
	if err := runConfigLiveReconnect(args, &output, apply); err == nil || calls != 0 {
		t.Fatalf("command without confirmation returned %v, calls %d", err, calls)
	}
	if err := runConfigLiveReconnect(append(args, "-confirm"), &output, apply); err != nil || calls != 1 || output.String() != "{\"state\":\"started\"}\n" {
		t.Fatalf("confirmed command returned %v, calls %d, output %q", err, calls, output.String())
	}
	for _, tc := range []struct{ action, state string }{{"wait", "scheduled"}, {"cancel", "cancelled"}} {
		output.Reset()
		before := calls
		if err := runConfigLiveReconnect(append(args, "-action", tc.action, "-confirm"), &output, apply); err != nil ||
			calls != before+1 || output.String() != "{\"state\":\""+tc.state+"\"}\n" {
			t.Fatalf("confirmed %s returned %v, calls %d, output %q", tc.action, err, calls, output.String())
		}
	}
	before := calls
	if err := runConfigLiveReconnect(append(args, "-action", "typo", "-confirm"), &output, apply); err == nil || calls != before {
		t.Fatal("invalid action reached the worker")
	}
}

func TestReceiverReconnectWaitAndCancelKeepSelectedAccount(t *testing.T) {
	registry, process := testGroupAccessRegistry(t)
	account := newReceiverGroupAccount("lark", "cli_shared")
	other := newReceiverGroupAccount("feishu", "cli_shared")
	pending := receiverReconnectIPC.pendingPath(process.dir, account)
	if raw, err := json.Marshal(receiverReconnectRequest{Confirm: true}); err != nil || string(raw) != `{"confirm":true}` {
		t.Fatalf("legacy immediate reconnect request shape changed: %s, %v", raw, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	calls := make(chan bridge.ReconnectAction, 2)
	go func() {
		defer close(done)
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				process.serveReconnect(ctx, other, receiverReconnectIPC.pendingPath(process.dir, other), func(context.Context, bridge.ReconnectAction) (bridge.LocalReconnectView, error) {
					t.Error("another tenant received the selected App's action")
					return bridge.LocalReconnectView{}, nil
				})
				process.serveReconnect(ctx, account, pending, func(requestCtx context.Context, action bridge.ReconnectAction) (bridge.LocalReconnectView, error) {
					if _, ok := requestCtx.Deadline(); !ok {
						t.Error("local scheduling did not honor the IPC deadline")
					}
					calls <- action
					if action == bridge.ReconnectAfterRuns {
						return bridge.LocalReconnectView{State: bridge.ReconnectScheduled}, nil
					}
					return bridge.LocalReconnectView{State: bridge.ReconnectCancelled}, nil
				})
			}
		}
	}()
	defer func() {
		cancel()
		<-done
	}()
	for _, tc := range []struct {
		action bridge.ReconnectAction
		state  string
	}{{bridge.ReconnectAfterRuns, bridge.ReconnectScheduled}, {bridge.ReconnectCancelWait, bridge.ReconnectCancelled}} {
		view, err := registry.reconnect(ctx, process.process.ID, account.Tenant, account.AppID, tc.action)
		if err != nil || view.State != tc.state || len(calls) != 1 || <-calls != tc.action {
			t.Fatalf("unexpected IPC result for %s: %+v, %v", tc.action, view, err)
		}
	}

	legacy := process.process
	legacy.ReconnectWait = false
	path := filepath.Join(process.dir, "process.json")
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if published, err := localfile.WritePrivateAtomic(path, data, false); err != nil || !published {
		t.Fatalf("could not write legacy capability fixture: %v", err)
	}
	view, err := registry.reconnect(ctx, legacy.ID, account.Tenant, account.AppID, bridge.ReconnectAfterRuns)
	if err == nil || !strings.Contains(err.Error(), "does not support waiting") || view.State != "" || len(calls) != 0 {
		t.Fatalf("legacy receiver accepted a wait: %+v, %v", view, err)
	}
	if _, err := os.Lstat(pending); !os.IsNotExist(err) {
		t.Fatalf("legacy capability rejection left an IPC request: %v", err)
	}
}
