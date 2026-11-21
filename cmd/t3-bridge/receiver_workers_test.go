package main

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

const receiverWorkerTestDelay = time.Millisecond

func TestRunReceiverAccountsRecoversAfterBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		accounts := []bridge.Config{{AppID: "cli_flaky"}, {AppID: "cli_healthy"}}
		failFirst := make(chan struct{})
		finish := make(chan struct{})
		flakyAttempts := make(chan context.Context, 4)
		healthyStarted := make(chan context.Context, 1)
		var attempts atomic.Int32
		done := make(chan error, 1)
		go func() {
			done <- runReceiverAccounts(ctx, accounts, receiverWorkerTestDelay, func(workerCtx context.Context, account bridge.Config) error {
				if account.AppID == "cli_healthy" {
					healthyStarted <- workerCtx
				} else {
					attempt := attempts.Add(1)
					flakyAttempts <- workerCtx
					if attempt == 1 {
						select {
						case <-failFirst:
							return errors.New("temporary receive failure")
						case <-workerCtx.Done():
							return nil
						}
					}
				}
				select {
				case <-finish:
				case <-workerCtx.Done():
				}
				return nil
			}, nil)
		}()

		synctest.Wait()
		if len(flakyAttempts) != 1 || len(healthyStarted) != 1 {
			t.Fatal("both Apps should start while the first attempt is active")
		}
		firstCtx, healthyCtx := <-flakyAttempts, <-healthyStarted
		time.Sleep(receiverWorkerTestDelay)
		synctest.Wait()
		if len(flakyAttempts) != 0 {
			t.Fatal("retry overlapped the first attempt")
		}
		close(failFirst)
		synctest.Wait()
		if len(flakyAttempts) != 0 {
			t.Fatal("retried before the backoff elapsed")
		}
		time.Sleep(receiverWorkerTestDelay)
		synctest.Wait()
		if len(flakyAttempts) != 1 {
			t.Fatalf("retry attempts after backoff = %d, want 1", len(flakyAttempts))
		}
		if retryCtx := <-flakyAttempts; retryCtx != firstCtx {
			t.Fatal("retry received a different worker context")
		}
		if healthyCtx.Err() != nil || len(healthyStarted) != 0 || len(done) != 0 {
			t.Fatal("recovery interrupted the healthy receiver or ended the group")
		}
		close(finish)
		if err := <-done; err != nil {
			t.Fatalf("workers completed after recovery with error: %v", err)
		}
	})
}

func TestRunReceiverAccountsKeepsHealthyWorkerAfterRetryExhaustion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		accounts := []bridge.Config{{AppID: "cli_failed"}, {AppID: "cli_healthy"}}
		healthyWork := make(chan struct{})
		healthyWorked := make(chan struct{}, 1)
		failure := errors.New("permanent receive failure")
		var failedAttempts atomic.Int32
		done := make(chan error, 1)
		go func() {
			done <- runReceiverAccounts(ctx, accounts, receiverWorkerTestDelay, func(workerCtx context.Context, account bridge.Config) error {
				if account.AppID == "cli_failed" {
					failedAttempts.Add(1)
					return failure
				}
				select {
				case <-healthyWork:
					healthyWorked <- struct{}{}
				case <-workerCtx.Done():
				}
				<-workerCtx.Done()
				return workerCtx.Err()
			}, nil)
		}()

		synctest.Wait()
		// Include the interval in which a prohibited fourth retry would occur.
		time.Sleep(4 * receiverWorkerTestDelay)
		synctest.Wait()
		if got := failedAttempts.Load(); got != 4 {
			t.Fatalf("failed App attempts = %d, want 4", got)
		}
		close(healthyWork)
		synctest.Wait()
		if len(healthyWorked) != 1 || len(done) != 0 {
			t.Fatal("healthy App did not keep working after the failed App exhausted its retries")
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("shutdown after another App exited with an error: %v", err)
		}
	})
}

func TestRunReceiverAccountsReturnsAppFailureAfterWorkersExit(t *testing.T) {
	for _, tc := range []struct {
		name string
		apps int
		want int32
	}{
		{name: "single App", apps: 1, want: 1},
		{name: "all Apps", apps: 2, want: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				accounts := []bridge.Config{{AppID: "cli_first"}, {AppID: "cli_second"}}[:tc.apps]
				failures := []error{errors.New("first receive failure"), errors.New("second receive failure")}
				var attempts [2]atomic.Int32
				err := runReceiverAccounts(t.Context(), accounts, receiverWorkerTestDelay, func(_ context.Context, account bridge.Config) error {
					for i, candidate := range accounts {
						if account.AppID == candidate.AppID {
							if attempts[i].Add(1) > tc.want {
								return nil
							}
							return failures[i]
						}
					}
					t.Errorf("unexpected worker App %q", account.AppID)
					return nil
				}, nil)
				matches := 0
				for i, account := range accounts {
					if got := attempts[i].Load(); got != tc.want {
						t.Errorf("%s attempts = %d, want %d", account.AppID, got, tc.want)
					}
					if errors.Is(err, failures[i]) && strings.Contains(err.Error(), account.AppID) {
						matches++
					}
				}
				if matches != 1 {
					t.Fatalf("error = %v, want one App ID wrapping its receive failure", err)
				}
			})
		})
	}
}

func TestRunReceiverAccountsCancelsDuringBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		accounts := []bridge.Config{{AppID: "cli_backoff"}, {AppID: "cli_other"}}
		var failedAttempts atomic.Int32
		failure := errors.New("receive failure before shutdown")
		done := make(chan error, 1)
		go func() {
			done <- runReceiverAccounts(ctx, accounts, receiverWorkerTestDelay, func(workerCtx context.Context, account bridge.Config) error {
				if account.AppID == "cli_backoff" {
					failedAttempts.Add(1)
					return failure
				}
				<-workerCtx.Done()
				return nil
			}, nil)
		}()

		synctest.Wait()
		if got := failedAttempts.Load(); got != 1 || len(done) != 0 {
			t.Fatalf("before shutdown: attempts = %d, completed results = %d", got, len(done))
		}
		cancel()
		synctest.Wait()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("shutdown during backoff: %v", err)
			}
		default:
			t.Fatal("shutdown is still waiting for the retry timer")
		}
		if got := failedAttempts.Load(); got != 1 {
			t.Fatalf("attempts after shutdown = %d, want 1", got)
		}
	})
}
