package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

const (
	sharedReceiverRetryDelay = 30 * time.Second
	sharedReceiverMaxRetries = 3
)

func runReceiverAccounts(ctx context.Context, accounts []bridge.Config, retryDelay time.Duration, runAccount func(context.Context, bridge.Config) error, process *receiverProcessRegistration) error {
	retries := 0
	if len(accounts) > 1 {
		retries = sharedReceiverMaxRetries
	}
	type workerResult struct {
		index int
		err   error
	}
	results := make(chan workerResult, len(accounts))
	start := func(index int) {
		go func() {
			err := runReceiverWorker(ctx, accounts[index], retries, retryDelay, runAccount, process)
			results <- workerResult{index: index, err: err}
		}()
	}
	var poll <-chan time.Time
	if process != nil && retries > 0 {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		poll = ticker.C
	}
	failures := make([]error, len(accounts))
	for index := range accounts {
		start(index)
	}
	for running := len(accounts); running > 0; {
		select {
		case result := <-results:
			running--
			failures[result.index] = result.err
		case <-poll:
			if ctx.Err() != nil {
				poll = nil
				continue
			}
			// Claim recovery commands only after the same App's worker has exited.
			for index, err := range failures {
				if err != nil && process.recoverReceiverAccount(ctx, accounts[index]) {
					failures[index] = nil
					running++
					start(index)
				}
			}
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	for index, err := range failures {
		if err != nil {
			return fmt.Errorf("Feishu receiver %s: %w", accounts[index].AppID, err)
		}
	}
	return nil
}

func runReceiverWorker(ctx context.Context, account bridge.Config, retries int, retryDelay time.Duration, runAccount func(context.Context, bridge.Config) error, process *receiverProcessRegistration) error {
	identity := newReceiverGroupAccount(account.Tenant, account.AppID)
	publishStatus := process != nil && retries > 0
	for attempt := 0; ctx.Err() == nil; attempt++ {
		if publishStatus {
			process.writeWorkerStatus(identity, nil)
		}
		err := runAccount(ctx, account)
		if err == nil || ctx.Err() != nil {
			return nil
		}
		exhausted := attempt == retries
		failedAt := time.Now()
		retryAt := failedAt.Add(retryDelay)
		if publishStatus {
			status := receiverWorkerStatus{State: receiverWorkerRetrying, Attempts: attempt + 1, RetryAt: retryAt.UTC()}
			if exhausted {
				status.State, status.RetryAt = receiverWorkerExhausted, time.Time{}
			}
			var failure *receiverFailure
			if errors.As(err, &failure) {
				status.Failure = failure.kind
			}
			process.writeWorkerStatus(identity, &receiverWorkerSample{At: failedAt.UTC(), receiverWorkerStatus: status})
		}
		if exhausted {
			if retries > 0 {
				fmt.Fprintf(os.Stderr, "Feishu receiver %s stopped after %d local retries\n", account.AppID, retries)
			}
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Until(retryAt)):
		}
	}
	return nil
}
