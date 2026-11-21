package main

import (
	"bufio"
	"cmp"
	"encoding/json"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

const serviceTelemetryMaxLineBytes = 4 << 10

var serviceTelemetryAppID = regexp.MustCompile(`^cli_[A-Za-z0-9_-]{1,124}$`)

type serviceTelemetryRecord struct {
	Type string `json:"type"`
	receiverProcessAccount
	At       time.Time                 `json:"at"`
	Name     string                    `json:"name,omitempty"`
	Value    *int64                    `json:"value,omitempty"`
	Terminal string                    `json:"terminal,omitempty"`
	Step     bridge.CommandFailureStep `json:"step,omitempty"`
	Kind     string                    `json:"kind,omitempty"`
	Count    *int                      `json:"count,omitempty"`
}

func validServiceTelemetryAccount(account receiverProcessAccount) bool {
	return (account.Tenant == "feishu" || account.Tenant == "lark") && serviceTelemetryAppID.MatchString(account.AppID)
}

func (record serviceTelemetryRecord) matchesAccount(account receiverProcessAccount) bool {
	return account == (receiverProcessAccount{}) || record.receiverProcessAccount == account
}

// The workbench filters one bounded snapshot and lists the accounts with valid records.
func serviceTelemetryView(logs string, selected receiverProcessAccount) (string, []receiverProcessAccount) {
	var body strings.Builder
	encoder := json.NewEncoder(&body)
	seen := make(map[receiverProcessAccount]bool)
	var accounts []receiverProcessAccount
	for line := range strings.SplitSeq(logs, "\n") {
		record, ok := parseServiceTelemetry([]byte(line))
		if !ok {
			continue
		}
		account := record.receiverProcessAccount
		if !seen[account] {
			seen[account] = true
			accounts = append(accounts, account)
		}
		if record.matchesAccount(selected) {
			_ = encoder.Encode(record) // Validated fields written to a strings.Builder.
		}
	}
	slices.SortFunc(accounts, func(a, b receiverProcessAccount) int {
		return cmp.Or(cmp.Compare(a.AppID, b.AppID), cmp.Compare(a.Tenant, b.Tenant))
	})
	return body.String(), accounts
}

func writeServiceTelemetry(input io.Reader, output io.Writer, account receiverProcessAccount) error {
	reader := bufio.NewReaderSize(input, serviceTelemetryMaxLineBytes)
	encoder := json.NewEncoder(output)
	for {
		line, err := reader.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			// Drain the rest of this line before checking for another record.
			for err == bufio.ErrBufferFull {
				_, err = reader.ReadSlice('\n')
			}
		} else if (err == nil || err == io.EOF) && len(line) <= serviceTelemetryMaxLineBytes {
			if record, ok := parseServiceTelemetry(line); ok && record.matchesAccount(account) {
				if err := encoder.Encode(record); err != nil {
					return err
				}
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func parseServiceTelemetry(line []byte) (serviceTelemetryRecord, bool) {
	var fields map[string]json.RawMessage
	var record serviceTelemetryRecord
	if json.Unmarshal(line, &fields) != nil ||
		json.Unmarshal(fields["type"], &record.Type) != nil ||
		json.Unmarshal(fields["tenant"], &record.Tenant) != nil ||
		json.Unmarshal(fields["appId"], &record.AppID) != nil ||
		!validServiceTelemetryAccount(record.receiverProcessAccount) ||
		json.Unmarshal(fields["at"], &record.At) != nil || record.At.IsZero() {
		return record, false
	}
	record.At = record.At.UTC()
	if year := record.At.Year(); year < 0 || year > 9999 {
		return record, false
	}

	switch record.Type {
	case "receiver-metric":
		if json.Unmarshal(fields["name"], &record.Name) != nil ||
			json.Unmarshal(fields["value"], &record.Value) != nil ||
			record.Value == nil || *record.Value < 0 {
			return record, false
		}
		switch record.Name {
		case "run_e2e_ms":
			if json.Unmarshal(fields["terminal"], &record.Terminal) != nil {
				return record, false
			}
			return record, record.Terminal == "completed" || record.Terminal == "failed"
		case "ws_reconnect":
			return record, *record.Value == 1
		case "command_fail":
			if *record.Value != 1 || json.Unmarshal(fields["step"], &record.Step) != nil {
				return record, false
			}
			return record, record.Step.Valid()
		case "pool_active", "pool_waiting":
			return record, true
		}
	case "receiver-diagnostic":
		if json.Unmarshal(fields["kind"], &record.Kind) != nil ||
			json.Unmarshal(fields["count"], &record.Count) != nil || record.Count == nil {
			return record, false
		}
		event := receiverDiagnosticEvent{At: record.At, Kind: record.Kind, Count: *record.Count}
		return record, event.valid()
	case "receiver-event":
		if json.Unmarshal(fields["kind"], &record.Kind) != nil {
			return record, false
		}
		switch record.Kind {
		case "run-started", "run-completed", "run-failed", "outbound-sent", "ws-reconnected":
			return record, true
		}
	case "receiver-failure":
		if json.Unmarshal(fields["kind"], &record.Kind) != nil {
			return record, false
		}
		switch record.Kind {
		case "store-open-failed", "account-bind-failed", "initial-connect-failed",
			"inbound-failed", "channel-stopped", "worker-tick-failed":
			return record, true
		}
	}
	return record, false
}
