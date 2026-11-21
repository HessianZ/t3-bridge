package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestReceiverGroupAccessClaimsAccountRequestOnce(t *testing.T) {
	r, process := testGroupAccessRegistry(t)
	account := newReceiverGroupAccount("lark", "cli_shared")
	pending := receiverGroupPendingPath(process.dir, account)
	request := bridge.GroupAccessRequest{ChatIDs: []string{"oc_synthetic", "oc_other"}, Access: "allow"}
	type outcome struct {
		view bridge.GroupAccessView
		err  error
	}
	result := make(chan outcome, 1)
	go func() {
		view, err := r.groupAccess(t.Context(), process.process.ID, account.Tenant, account.AppID, request)
		result <- outcome{view, err}
	}()
	command := waitGroupAccessPending(t, r, pending)
	if _, err := r.groupAccess(t.Context(), process.process.ID, account.Tenant, account.AppID, request); !errors.Is(err, errReceiverGroupBusy) {
		t.Fatalf("second request while the first is pending: %v", err)
	}
	if _, err := r.groupAccess(t.Context(), process.process.ID, account.Tenant, "cli_other", request); err == nil {
		t.Fatal("accepted an account outside the receiver registration")
	}
	calls := 0
	apply := func(_ context.Context, got bridge.GroupAccessRequest) (bridge.GroupAccessView, error) {
		calls++
		want := request
		want.IncludeMessageEvent = true
		if !reflect.DeepEqual(got, want) {
			t.Errorf("request = %+v, want %+v", got, want)
		}
		if _, err := os.Lstat(pending); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("worker called engine before removing pending command: %v", err)
		}
		return bridge.GroupAccessView{JoinedAvailable: true, MessageEvent: "subscribed"}, nil
	}
	other := newReceiverGroupAccount("feishu", account.AppID)
	process.serveGroupAccess(t.Context(), other, receiverGroupPendingPath(process.dir, other), apply)
	process.serveGroupAccess(t.Context(), account, pending, apply)
	process.serveGroupAccess(t.Context(), account, pending, apply)
	got := <-result
	if got.err != nil || !got.view.JoinedAvailable || got.view.MessageEvent != "subscribed" || calls != 1 {
		t.Fatalf("result = %+v, engine calls = %d", got, calls)
	}
	if _, err := os.Lstat(command.returnDir(process.dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("caller did not remove reply directory: %v", err)
	}
}

func TestReceiverGroupAccessCancellationAndExpiration(t *testing.T) {
	r, process := testGroupAccessRegistry(t)
	account := newReceiverGroupAccount("", "cli_shared")
	pending := receiverGroupPendingPath(process.dir, account)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := r.groupAccess(ctx, process.process.ID, account.Tenant, account.AppID, bridge.GroupAccessRequest{})
		result <- err
	}()
	command := waitGroupAccessPending(t, r, pending)
	cancel()
	if err := <-result; !errors.Is(err, errReceiverGroupUnknown) || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request returned %v", err)
	}
	apply := func(context.Context, bridge.GroupAccessRequest) (bridge.GroupAccessView, error) {
		t.Error("worker executed a canceled or expired request")
		return bridge.GroupAccessView{}, nil
	}
	process.serveGroupAccess(t.Context(), account, pending, apply)
	if err := localfile.MkdirAllPrivate(command.returnDir(process.dir)); err != nil {
		t.Fatal(err)
	}
	command.Expires = time.Now().Add(-time.Second)
	writeGroupAccessTestFile(t, pending, command)
	process.serveGroupAccess(t.Context(), account, pending, apply)
	if _, err := os.Lstat(pending); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired request was left pending: %v", err)
	}
}

func TestReceiverGroupAccessRejectsLegacyAndStaleRegistrations(t *testing.T) {
	r, process := testGroupAccessRegistry(t)
	registration := process.process
	metadataPath := filepath.Join(process.dir, "process.json")
	rewriteRegistration := func() {
		t.Helper()
		data, err := json.Marshal(registration)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := localfile.WritePrivateAtomic(metadataPath, data, false); err != nil {
			t.Fatal(err)
		}
	}
	registration.GroupBatchGrant = false
	rewriteRegistration()
	batch := bridge.GroupAccessRequest{ChatIDs: []string{"oc_synthetic", "oc_other"}, Access: "allow"}
	if _, err := r.groupAccess(t.Context(), registration.ID, "", "cli_shared", batch); !errors.Is(err, errReceiverGroupBatchUnsupported) {
		t.Fatalf("receiver without batch capability returned %v", err)
	}
	pending := receiverGroupPendingPath(process.dir, newReceiverGroupAccount("", "cli_shared"))
	if _, err := os.Lstat(pending); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsupported batch left a pending request: %v", err)
	}
	registration.GroupMessageEvent = false
	rewriteRegistration()
	account := newReceiverGroupAccount("", "cli_shared")
	result := make(chan error, 1)
	go func() {
		view, err := r.groupAccess(t.Context(), registration.ID, account.Tenant, account.AppID, bridge.GroupAccessRequest{})
		if err == nil && view.MessageEvent != "" {
			t.Error("legacy worker must omit the event state")
		}
		result <- err
	}()
	waitGroupAccessPending(t, r, pending)
	process.serveGroupAccess(t.Context(), account, pending, func(_ context.Context, request bridge.GroupAccessRequest) (bridge.GroupAccessView, error) {
		if request.IncludeMessageEvent {
			t.Error("legacy worker received an unknown request field")
		}
		return bridge.GroupAccessView{}, nil
	})
	if err := <-result; err != nil {
		t.Fatalf("legacy group query failed: %v", err)
	}
	registration.GroupAccess = false
	rewriteRegistration()
	if _, err := r.groupAccess(t.Context(), registration.ID, "", "cli_shared", bridge.GroupAccessRequest{}); !errors.Is(err, errReceiverGroupUnsupported) {
		t.Fatalf("legacy receiver returned %v", err)
	}
	// Release the registration lock without deleting its files to simulate process exit.
	if err := process.lock.Close(); err != nil {
		t.Fatal(err)
	}
	current, err := r.register([]bridge.Config{{AppID: "cli_shared"}})
	if err != nil {
		t.Fatal(err)
	}
	defer current.close()
	if _, err := r.groupAccess(t.Context(), registration.ID, "", "cli_shared", bridge.GroupAccessRequest{}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old process ID accepted after starting its replacement: %v", err)
	}
}

func TestReceiverGroupAccessReplyValidation(t *testing.T) {
	for _, name := range []string{"instance", "account", "ticket", "oversized", "unknown field", "duplicate field", "wrong field case", "trailing value", "engine error"} {
		t.Run(name, func(t *testing.T) {
			r, process := testGroupAccessRegistry(t)
			account := newReceiverGroupAccount("", "cli_shared")
			pending := receiverGroupPendingPath(process.dir, account)
			result := make(chan error, 1)
			go func() {
				_, err := r.groupAccess(t.Context(), process.process.ID, account.Tenant, account.AppID, bridge.GroupAccessRequest{})
				result <- err
			}()
			command := waitGroupAccessPending(t, r, pending)
			if name == "engine error" {
				process.serveGroupAccess(t.Context(), account, pending, func(context.Context, bridge.GroupAccessRequest) (bridge.GroupAccessView, error) {
					return bridge.GroupAccessView{}, errors.New("private synthetic error")
				})
				if err := <-result; !errors.Is(err, errReceiverGroupFailed) || strings.Contains(err.Error(), "private") {
					t.Fatalf("engine error returned %v", err)
				}
				return
			}
			reply := receiverGroupReply{receiverGroupRoute: command.receiverGroupRoute, View: &bridge.GroupAccessView{}}
			switch name {
			case "instance":
				reply.Instance = strings.Repeat("0", 32)
			case "account":
				reply.Account.AppID = "cli_other"
			case "ticket":
				reply.Ticket = strings.Repeat("0", 32)
			}
			data, err := json.Marshal(reply)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "oversized":
				data = append(data, strings.Repeat(" ", receiverGroupLimit)...)
			case "trailing value":
				data = append(data, []byte(" {}")...)
			case "unknown field":
				data = append(data[:len(data)-1], []byte(`,"unexpected":true}`)...)
			case "duplicate field":
				data = append(data[:len(data)-1], []byte(`,"ticket":"`+command.Ticket+`"}`)...)
			case "wrong field case":
				data = []byte(strings.Replace(string(data), `"ticket":`, `"Ticket":`, 1))
			}
			if _, err := localfile.WritePrivateAtomic(filepath.Join(command.returnDir(process.dir), "reply.json"), data, true); err != nil {
				t.Fatal(err)
			}
			if err := <-result; !errors.Is(err, errReceiverGroupUnknown) {
				t.Fatalf("invalid reply returned %v", err)
			}
		})
	}
}

func testGroupAccessRegistry(t *testing.T) (receiverProcessRegistry, *receiverProcessRegistration) {
	t.Helper()
	r := localReceiverProcesses(profileRegistry{root: t.TempDir()})
	process, err := r.register([]bridge.Config{{AppID: "cli_shared"}, {Tenant: "lark", AppID: "cli_shared"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = process.close()
	})
	return r, process
}

func waitGroupAccessPending(t *testing.T, r receiverProcessRegistry, path string) receiverGroupCommand {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()
	for {
		guard, err := r.lock(true)
		if err != nil {
			t.Fatal(err)
		}
		command, err := readReceiverGroupFile[receiverGroupCommand](path, receiverGroupCommandLimit)
		_ = guard.Close()
		if err == nil {
			return command
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-timeout.C:
			t.Fatal("request was not published")
		case <-ticker.C:
		}
	}
}

func writeGroupAccessTestFile(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localfile.WritePrivateAtomic(path, data, true); err != nil {
		t.Fatal(err)
	}
}
