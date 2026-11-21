package main

import (
	"context"
	"encoding/json"
	"io"
	"time"
)

func writeReceiverDiagnosticLogs(output io.Writer, account receiverProcessAccount, events []receiverDiagnosticEvent) {
	encoder := json.NewEncoder(output)
	for _, event := range events {
		_ = encoder.Encode(struct {
			Type string `json:"type"`
			receiverProcessAccount
			receiverDiagnosticEvent
		}{"receiver-diagnostic", account, event})
	}
}

type receiverFailure struct {
	kind string
	err  error
}

func (e *receiverFailure) Error() string { return e.err.Error() }
func (e *receiverFailure) Unwrap() error { return e.err }

func logReceiverFailure(ctx context.Context, output io.Writer, account receiverProcessAccount, kind string, err error) error {
	if ctx.Err() == nil {
		writeReceiverFailure(output, account, kind)
	}
	return &receiverFailure{kind: kind, err: err}
}

func writeReceiverFailure(output io.Writer, account receiverProcessAccount, kind string) {
	_ = json.NewEncoder(output).Encode(struct {
		Type string `json:"type"`
		receiverProcessAccount
		At   time.Time `json:"at"`
		Kind string    `json:"kind"`
	}{"receiver-failure", account, time.Now().UTC(), kind})
}
