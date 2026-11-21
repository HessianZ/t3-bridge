package bridge

import (
	"errors"
	"slices"
)

// ReceiverDiagnostics contains only local connection flags and aggregate counts.
type ReceiverDiagnostics struct {
	MCPVerified bool `json:"mcpVerified"` // Environment verified at connection setup.
	FeishuReady bool `json:"feishuReady"` // Current WebSocket readiness flag.
	Inbox       int  `json:"inbox"`       // Durable inbox entries, including controls.
	InFlight    int  `json:"inFlight"`    // Tasks in any nonterminal phase, including queued tasks.
	Reconcile   int  `json:"reconcile"`   // Tasks with an explicit T3 reconciliation flag or phase.
	// Current and archived replies and process messages that still need delivery.
	DeliveryPending  int                  `json:"deliveryPending"`
	Capacity         *ReceiverRunCapacity `json:"capacity,omitempty"`
	ReconnectWaiting bool                 `json:"reconnectWaiting,omitempty"`
}

// ReceiverRunCapacity describes the run budget for the bound App.
type ReceiverRunCapacity struct {
	Limit    int `json:"limit"` // Zero means unlimited.
	Occupied int `json:"occupied"`
	Waiting  int `json:"waiting"`
}

// LocalDiagnostics runs on the receiver worker that owns Tick and connection changes.
func (e *Engine) LocalDiagnostics(c *Connections) (ReceiverDiagnostics, error) {
	if e == nil || e.Store == nil || c == nil || c.store != e.Store || e.Config.AppID == "" || c.config.AppID != e.Config.AppID {
		return ReceiverDiagnostics{}, errors.New("接收端诊断未绑定到当前应用")
	}

	s := e.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	st := &s.state
	if st.AccountID != e.Config.AppID {
		return ReceiverDiagnostics{}, errors.New("接收端状态未绑定到当前应用")
	}

	active, _ := drainCounts(st)
	d := ReceiverDiagnostics{
		MCPVerified:      c.verified.Load(),
		FeishuReady:      c.ready.Load(),
		Inbox:            len(st.Inbox),
		InFlight:         active,
		Capacity:         &ReceiverRunCapacity{Limit: st.Settings.MaxConcurrent},
		ReconnectWaiting: st.PendingReconnect != "",
	}
	for _, t := range st.Tasks {
		d.Capacity.add(t)
		if t.NeedsReconcile || t.Phase == "needs_reconcile" {
			d.Reconcile++
		}
		d.DeliveryPending += taskDeliveryGapCount(t, nil)
	}
	return d, nil
}

func (c *ReceiverRunCapacity) add(t *Task) {
	if taskOccupiesCapacity(t) {
		c.Occupied++
	}
	if t.Phase == "waiting_capacity" {
		c.Waiting++
	}
}

func taskOccupiesCapacity(t *Task) bool {
	return t.NeedsReconcile || slices.Contains([]string{"opening", "dispatching", "needs_reconcile", "tracking", "awaiting_children", "settling"}, t.Phase)
}
