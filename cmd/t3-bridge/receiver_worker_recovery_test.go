package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestReceiverWorkerRecoveryViaLocalReconnect(t *testing.T) {
	failed := bridge.Config{
		Tenant: "lark", AppID: "cli_failed", ReceiverAuthorized: true,
		AppSecretFile: "/synthetic/failed.secret", StateDir: "/synthetic/failed-state",
	}
	healthy := bridge.Config{Tenant: "feishu", AppID: "cli_healthy"}
	accounts := []bridge.Config{failed, healthy}
	registry := localReceiverProcesses(profileRegistry{root: t.TempDir()})
	process, err := registry.register(accounts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.close() })

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	finish := make(chan struct{})
	healthyWork := make(chan chan struct{})
	recovered := make(chan struct{}, 1)
	var failedAttempts, healthyStarts atomic.Int32
	var workerErr error
	done := make(chan struct{})
	failure := errors.New("synthetic receiver failure")
	go func() {
		defer close(done)
		workerErr = runReceiverAccounts(ctx, accounts, time.Millisecond, func(workerCtx context.Context, cfg bridge.Config) error {
			if workerCtx != ctx {
				t.Error("worker did not receive the process context")
			}
			switch cfg.AppID {
			case failed.AppID:
				if !reflect.DeepEqual(cfg, failed) {
					t.Error("worker did not receive the original App configuration")
				}
				// The first round exhausts all four attempts. The next round
				// needs its full retry budget to keep this attempt alive.
				if failedAttempts.Add(1) != 8 {
					return failure
				}
				recovered <- struct{}{}
				select {
				case <-finish:
				case <-workerCtx.Done():
				}
				return nil
			case healthy.AppID:
				healthyStarts.Add(1)
				for {
					select {
					case ack := <-healthyWork:
						close(ack)
					case <-finish:
						return nil
					case <-workerCtx.Done():
						return nil
					}
				}
			default:
				t.Errorf("unexpected App started: %q", cfg.AppID)
				return nil
			}
		}, process)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("workers did not exit after process cancellation")
		}
	}()
	checkHealthyWork := func() {
		t.Helper()
		ack := make(chan struct{})
		select {
		case healthyWork <- ack:
		case <-ctx.Done():
			t.Fatal("healthy App stopped accepting work")
		}
		select {
		case <-ack:
		case <-ctx.Done():
			t.Fatal("healthy App stopped processing work")
		}
		if got := healthyStarts.Load(); got != 1 {
			t.Fatalf("healthy App starts = %d, want 1", got)
		}
	}
	checkHealthyWork()

	id := process.process.ID
	account := newReceiverGroupAccount(failed.Tenant, failed.AppID)
	args := []string{"-process", id, "-tenant", account.Tenant, "-app-id", account.AppID, "-confirm"}
	var output bytes.Buffer
	for _, action := range []bridge.ReconnectAction{bridge.ReconnectAfterRuns, bridge.ReconnectCancelWait} {
		output.Reset()
		err := runConfigLiveReconnect(append(args, "-action", string(action)), &output, registry.reconnect)
		if err == nil || !strings.Contains(err.Error(), "Use -action now") || output.String() != "{\"state\":\"worker-offline\"}\n" {
			t.Fatalf("offline %s returned %v, output %q", action, err, output.String())
		}
		if got := failedAttempts.Load(); got != 4 {
			t.Fatalf("App attempts after offline %s = %d, want 4", action, got)
		}
	}
	if sample, worker := registry.receiverSnapshot(process.process, account); sample != nil || worker == nil ||
		worker.State != receiverWorkerExhausted || worker.Attempts != 4 || !worker.RetryAt.IsZero() {
		t.Fatalf("exhausted snapshot = %+v, %+v", sample, worker)
	}
	for _, target := range []receiverProcessAccount{
		newReceiverGroupAccount(healthy.Tenant, failed.AppID),
		newReceiverGroupAccount(failed.Tenant, "cli_unknown"),
	} {
		view, err := registry.reconnect(ctx, id, target.Tenant, target.AppID, bridge.ReconnectNow)
		if err == nil || !strings.Contains(err.Error(), "does not serve this account") || view.State != "" {
			t.Fatalf("reconnect for %s/%s returned %+v, %v", target.Tenant, target.AppID, view, err)
		}
	}
	view, err := receiverReconnectIPC.exchange(ctx, registry, id, account, receiverReconnectRequest{})
	if err != errReceiverReconnectUnknown || view.State != "" {
		t.Fatalf("unconfirmed IPC request returned %+v, %v", view, err)
	}

	expired := receiverIPCCommand[receiverReconnectRequest]{
		receiverGroupRoute: receiverGroupRoute{Instance: id, Account: account, Ticket: strings.Repeat("a", 32)},
		Expires:            time.Now().Add(-time.Second),
		Request:            &receiverReconnectRequest{Confirm: true},
	}
	if err := receiverReconnectIPC.enqueue(ctx, registry, expired); err != nil {
		t.Fatal(err)
	}
	pending := receiverReconnectIPC.pendingPath(process.dir, account)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Lstat(pending); errors.Is(err, os.ErrNotExist) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal("receiver left the expired reconnect request pending")
		}
	}
	view, err = registry.reconnect(ctx, id, account.Tenant, account.AppID, bridge.ReconnectAfterRuns)
	if err != nil || view.State != bridge.ReconnectWorkerOffline || failedAttempts.Load() != 4 {
		t.Fatalf("after rejected requests: view %+v, error %v, attempts %d", view, err, failedAttempts.Load())
	}
	if _, err := os.Lstat(filepath.Join(receiverReconnectIPC.returnDir(process.dir, expired.Ticket), "reply.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired reconnect request produced a reply: %v", err)
	}

	output.Reset()
	if err := runConfigLiveReconnect(args, &output, registry.reconnect); err != nil || output.String() != "{\"state\":\"recovery-requested\"}\n" {
		t.Fatalf("recovery command returned %v, output %q", err, output.String())
	}
	select {
	case <-recovered:
	case <-ctx.Done():
		t.Fatalf("App did not reach its fourth attempt in the recovery round, total attempts %d", failedAttempts.Load())
	}
	if _, worker := registry.receiverSnapshot(process.process, account); worker != nil {
		t.Fatalf("recovered App retained an old retry snapshot: %+v", worker)
	}
	checkHealthyWork()
	close(finish)
	select {
	case <-done:
		if workerErr != nil || failedAttempts.Load() != 8 || healthyStarts.Load() != 1 {
			t.Fatalf("worker result = %v, recovered App attempts = %d, healthy App starts = %d", workerErr, failedAttempts.Load(), healthyStarts.Load())
		}
	case <-ctx.Done():
		t.Fatal("worker group did not finish")
	}
}
