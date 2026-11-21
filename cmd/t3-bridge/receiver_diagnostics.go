package main

import (
	"encoding/json"
	"io"
	"path/filepath"
	"slices"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

const (
	receiverDiagnosticsInterval       = 5 * time.Second
	receiverDiagnosticsMaxAge         = 30 * time.Second
	receiverDiagnosticsLimit          = 4 << 10
	receiverDiagnosticEventLimit      = 12
	receiverDiagnosticEventCountLimit = 9_999
)

const (
	receiverDiagnosticMCPVerified     = "mcp-verified"
	receiverDiagnosticMCPUnverified   = "mcp-unverified"
	receiverDiagnosticFeishuReady     = "feishu-ready"
	receiverDiagnosticFeishuNotReady  = "feishu-not-ready"
	receiverDiagnosticReconcile       = "reconcile"
	receiverDiagnosticDeliveryPending = "delivery-pending"
)

type receiverDiagnosticSample struct {
	At     time.Time                  `json:"at"`
	Data   bridge.ReceiverDiagnostics `json:"diagnostics"`
	Events []receiverDiagnosticEvent  `json:"events,omitempty"`
}

// At records when sampling observed the change. Transitions between samples may be missed.
type receiverDiagnosticEvent struct {
	At    time.Time `json:"at"`
	Kind  string    `json:"kind"`
	Count int       `json:"count"`
}

type receiverDiagnosticHistory struct {
	initialized bool
	previous    bridge.ReceiverDiagnostics
	events      []receiverDiagnosticEvent
}

func (h *receiverDiagnosticHistory) observe(at time.Time, data bridge.ReceiverDiagnostics) []receiverDiagnosticEvent {
	previous := h.previous
	h.previous = data
	if !h.initialized {
		h.initialized = true
		return nil
	}
	var observations []receiverDiagnosticEvent
	if data.MCPVerified != previous.MCPVerified {
		kind := receiverDiagnosticMCPUnverified
		if data.MCPVerified {
			kind = receiverDiagnosticMCPVerified
		}
		observations = append(observations, receiverDiagnosticEvent{At: at, Kind: kind})
	}
	if data.FeishuReady != previous.FeishuReady {
		kind := receiverDiagnosticFeishuNotReady
		if data.FeishuReady {
			kind = receiverDiagnosticFeishuReady
		}
		observations = append(observations, receiverDiagnosticEvent{At: at, Kind: kind})
	}
	for _, count := range []struct {
		kind              string
		previous, current int
	}{
		{receiverDiagnosticReconcile, previous.Reconcile, data.Reconcile},
		{receiverDiagnosticDeliveryPending, previous.DeliveryPending, data.DeliveryPending},
	} {
		if count.current > count.previous || (count.current == 0 && count.previous > 0) {
			observations = append(observations, receiverDiagnosticEvent{
				At: at, Kind: count.kind, Count: min(count.current, receiverDiagnosticEventCountLimit),
			})
		}
	}
	h.events = append(h.events, observations...)
	if len(h.events) > receiverDiagnosticEventLimit {
		copy(h.events, h.events[len(h.events)-receiverDiagnosticEventLimit:])
		h.events = h.events[:receiverDiagnosticEventLimit]
	}
	return observations
}

type receiverDiagnosticFile struct {
	Instance string                    `json:"instance"`
	Account  receiverProcessAccount    `json:"account"`
	At       time.Time                 `json:"at"`
	Data     receiverDiagnosticData    `json:"data,omitzero"`
	Events   []receiverDiagnosticEvent `json:"events,omitempty"`
	Worker   *receiverWorkerStatus     `json:"worker,omitempty"`
}

// A missing value is invalid even when false or zero would be a valid sample.
type receiverDiagnosticData struct {
	MCPVerified      *bool                       `json:"mcpVerified"`
	FeishuReady      *bool                       `json:"feishuReady"`
	Inbox            *int                        `json:"inbox"`
	InFlight         *int                        `json:"inFlight"`
	Reconcile        *int                        `json:"reconcile"`
	DeliveryPending  *int                        `json:"deliveryPending"`
	Capacity         *receiverDiagnosticCapacity `json:"capacity,omitempty"`
	ReconnectWaiting bool                        `json:"reconnectWaiting,omitempty"`
}

type receiverDiagnosticCapacity struct {
	Limit    *int `json:"limit"`
	Occupied *int `json:"occupied"`
	Waiting  *int `json:"waiting"`
}

func receiverDiagnosticsPath(dir string, account receiverProcessAccount) string {
	hash := configRevision([]byte(account.Tenant + "\x00" + account.AppID))
	return filepath.Join(dir, "diagnostics-"+hash+".json")
}

func (p *receiverProcessRegistration) publishDiagnostics(account receiverProcessAccount, engine *bridge.Engine, connections *bridge.Connections, history *receiverDiagnosticHistory, output io.Writer) {
	data, err := engine.LocalDiagnostics(connections)
	if err != nil {
		return
	}
	at := time.Now().UTC()
	previousCapacity := history.previous.Capacity
	observations := history.observe(at, data)
	p.writeDiagnostics(account, receiverDiagnosticSample{At: at, Data: data, Events: history.events})
	writeReceiverCapacityMetrics(output, account, previousCapacity, data.Capacity)
	if len(observations) != 0 {
		writeReceiverDiagnosticLogs(output, account, observations)
	}
}

func (p *receiverProcessRegistration) writeDiagnostics(account receiverProcessAccount, sample receiverDiagnosticSample) {
	data := sample.Data
	var capacity *receiverDiagnosticCapacity
	if c := data.Capacity; c != nil {
		capacity = &receiverDiagnosticCapacity{Limit: &c.Limit, Occupied: &c.Occupied, Waiting: &c.Waiting}
	}
	p.writeDiagnosticFile(receiverDiagnosticFile{
		Instance: p.process.ID,
		Account:  account,
		At:       sample.At,
		Events:   sample.Events,
		Data: receiverDiagnosticData{
			MCPVerified: &data.MCPVerified, FeishuReady: &data.FeishuReady,
			Inbox: &data.Inbox, InFlight: &data.InFlight, Reconcile: &data.Reconcile, DeliveryPending: &data.DeliveryPending,
			Capacity: capacity, ReconnectWaiting: data.ReconnectWaiting,
		},
	})
}

func (p *receiverProcessRegistration) writeDiagnosticFile(file receiverDiagnosticFile) {
	raw, err := json.Marshal(file)
	if err == nil {
		_, _ = localfile.WritePrivateAtomic(receiverDiagnosticsPath(p.dir, file.Account), raw, false)
	}
}

func (r receiverProcessRegistry) receiverSnapshot(process receiverProcess, account receiverProcessAccount) (*receiverDiagnosticSample, *receiverWorkerSample) {
	if !receiverProcessID.MatchString(process.ID) {
		return nil, nil
	}
	guard, err := r.lock(true)
	if err != nil {
		return nil, nil
	}
	defer guard.Close()
	active, err := r.activeProcess(process.ID)
	if err != nil || active.PID != process.PID || !active.StartedAt.Equal(process.StartedAt) ||
		active.ConfigRevision != process.ConfigRevision || !slices.Contains(active.Accounts, account) {
		return nil, nil
	}
	path := receiverDiagnosticsPath(filepath.Join(r.dir, active.ID), account)
	file, err := readReceiverGroupFile[receiverDiagnosticFile](path, receiverDiagnosticsLimit)
	if err != nil || file.Instance != active.ID || file.Account != account {
		return nil, nil
	}
	now := time.Now()
	age := now.Sub(file.At)
	if file.At.Before(active.StartedAt) || age < 0 {
		return nil, nil
	}
	if file.Worker != nil {
		if file.Data != (receiverDiagnosticData{}) || len(file.Events) != 0 || !file.Worker.valid(file.At, now) {
			return nil, nil
		}
		return nil, &receiverWorkerSample{At: file.At, receiverWorkerStatus: *file.Worker}
	}
	if age >= receiverDiagnosticsMaxAge {
		return nil, nil
	}
	data := file.Data
	if data.MCPVerified == nil || data.FeishuReady == nil || data.Inbox == nil || data.InFlight == nil || data.Reconcile == nil || data.DeliveryPending == nil {
		return nil, nil
	}
	if *data.Inbox < 0 || *data.InFlight < 0 || *data.Reconcile < 0 || *data.DeliveryPending < 0 {
		return nil, nil
	}
	var capacity *bridge.ReceiverRunCapacity
	if c := data.Capacity; c != nil {
		if c.Limit == nil || c.Occupied == nil || c.Waiting == nil || *c.Limit < 0 || *c.Occupied < 0 || *c.Waiting < 0 {
			return nil, nil
		}
		capacity = &bridge.ReceiverRunCapacity{Limit: *c.Limit, Occupied: *c.Occupied, Waiting: *c.Waiting}
	}
	if len(file.Events) > receiverDiagnosticEventLimit {
		return nil, nil
	}
	for _, event := range file.Events {
		if !event.valid() {
			return nil, nil
		}
	}
	return &receiverDiagnosticSample{At: file.At, Events: file.Events, Data: bridge.ReceiverDiagnostics{
		MCPVerified: *data.MCPVerified, FeishuReady: *data.FeishuReady,
		Inbox: *data.Inbox, InFlight: *data.InFlight, Reconcile: *data.Reconcile, DeliveryPending: *data.DeliveryPending,
		Capacity: capacity, ReconnectWaiting: data.ReconnectWaiting,
	}}, nil
}

func (e receiverDiagnosticEvent) valid() bool {
	if e.At.IsZero() {
		return false
	}
	switch e.Kind {
	case receiverDiagnosticMCPVerified, receiverDiagnosticMCPUnverified, receiverDiagnosticFeishuReady, receiverDiagnosticFeishuNotReady:
		return e.Count == 0
	case receiverDiagnosticReconcile, receiverDiagnosticDeliveryPending:
		return e.Count >= 0 && e.Count <= receiverDiagnosticEventCountLimit
	default:
		return false
	}
}
