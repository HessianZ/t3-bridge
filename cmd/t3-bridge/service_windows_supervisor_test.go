//go:build windows

package main

import (
	"errors"
	"testing"
	"time"
)

func TestWindowsReceiverRecovery(t *testing.T) {
	for _, test := range []struct {
		name      string
		exitCodes []int
		exhausted bool
	}{
		{name: "clean exit", exitCodes: []int{0}},
		{name: "recovered on last retry", exitCodes: []int{1, 1, 1, 0}},
		{name: "retries exhausted", exitCodes: []int{1, 1, 1, 1}, exhausted: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			launches, waits := 0, 0
			err := retryWindowsReceiver(func() (int, error) {
				if launches >= len(test.exitCodes) {
					t.Fatal("unexpected receiver launch")
				}
				code := test.exitCodes[launches]
				launches++
				return code, nil
			}, func(delay time.Duration) {
				if delay != time.Minute {
					t.Fatalf("retry delay = %v, want one minute", delay)
				}
				waits++
			})
			if (err != nil) != test.exhausted || launches != len(test.exitCodes) || waits != launches-1 {
				t.Fatalf("recovery returned %v after %d launches and %d waits", err, launches, waits)
			}
		})
	}

	startErr := errors.New("receiver could not start")
	err := retryWindowsReceiver(func() (int, error) { return 0, startErr }, func(time.Duration) {
		t.Fatal("process creation failure scheduled a retry")
	})
	if !errors.Is(err, startErr) {
		t.Fatalf("recovery error = %v, want %v", err, startErr)
	}
}
