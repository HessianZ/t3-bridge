package main

import (
	"encoding/json"
	"io"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

type receiverMetric struct {
	Name     string                    `json:"name"`
	Value    int64                     `json:"value"`
	Terminal string                    `json:"terminal,omitempty"`
	Step     bridge.CommandFailureStep `json:"step,omitempty"`
}

func writeReceiverCapacityMetrics(output io.Writer, account receiverProcessAccount, previous, current *bridge.ReceiverRunCapacity) {
	if current == nil {
		return
	}
	if previous == nil || current.Occupied != previous.Occupied {
		writeReceiverMetric(output, account, "pool_active", int64(current.Occupied), "")
	}
	if previous == nil || current.Waiting != previous.Waiting {
		writeReceiverMetric(output, account, "pool_waiting", int64(current.Waiting), "")
	}
}

func writeReceiverMetric(output io.Writer, account receiverProcessAccount, name string, value int64, terminal string) {
	receiverMetric{Name: name, Value: value, Terminal: terminal}.write(output, account)
}

func receiverCommandFailureMetric(output io.Writer, account receiverProcessAccount) func(bridge.CommandFailureStep) {
	return func(step bridge.CommandFailureStep) {
		receiverMetric{Name: "command_fail", Value: 1, Step: step}.write(output, account)
	}
}

func (metric receiverMetric) write(output io.Writer, account receiverProcessAccount) {
	_ = json.NewEncoder(output).Encode(struct {
		Type string `json:"type"`
		receiverProcessAccount
		At time.Time `json:"at"`
		receiverMetric
	}{"receiver-metric", account, time.Now().UTC(), metric})
}

func writeReceiverEvent(output io.Writer, account receiverProcessAccount, kind string) {
	_ = json.NewEncoder(output).Encode(struct {
		Type string `json:"type"`
		receiverProcessAccount
		At   time.Time `json:"at"`
		Kind string    `json:"kind"`
	}{"receiver-event", account, time.Now().UTC(), kind})
}
