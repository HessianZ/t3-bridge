package bridge

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestReceiverProcessStopRequiresConfirmedReplyAndInstance(t *testing.T) {
	e, _, lark, _ := testEngine(t)
	var failures []CommandFailureStep
	e.OnCommandFailed = func(step CommandFailureStep) { failures = append(failures, step) }
	e.Config.Admins = []string{"user"}
	e.Config.Topics = nil
	instanceID := "00112233445566778899aabbccddeeff"
	stop := message("stop", "/exit "+instanceID)
	stop.TopicID, stop.ChatType = "", "p2p"
	stops := 0
	e.Receiver = &ReceiverProcess{
		ID: instanceID, PID: 12, StartedAt: time.Unix(1, 0).UTC(),
		Stop: func() error {
			stops++
			st := e.Store.snapshot()
			if receipt := st.Commands[key(stop, stop.ID)]; receipt == nil || receipt.ReplyAt == "" || receipt.Issue != "" {
				t.Fatalf("shutdown was requested before a confirmed durable reply: %+v", receipt)
			}
			if _, pending := queuedPresent(&st, stop); pending {
				t.Fatal("shutdown started before the command was removed from the inbox")
			}
			return nil
		},
	}
	status := stop
	status.ID, status.Text = "status", "/ps"
	accept(t, e, status)
	tick(t, e)
	if !strings.Contains(lark.replies[0], "/exit "+instanceID) {
		t.Fatalf("process status did not give administrators the current ID: %q", lark.replies[0])
	}

	lark.replyErr = errors.New("synthetic unconfirmed reply")
	accept(t, e, stop)
	tick(t, e)
	if stops != 0 {
		t.Fatal("shutdown occurred without a confirmed reply")
	}
	lark.replyErr = nil
	tick(t, e)
	accept(t, e, stop)
	tick(t, e)
	if stops != 1 {
		t.Fatalf("shutdown requests = %d, want 1", stops)
	}

	for _, tc := range []struct {
		name, user, kind, processID string
		shared                      bool
	}{
		{"group", "user", "group", instanceID, false},
		{"member", "other", "p2p", instanceID, false},
		{"shared", "user", "p2p", instanceID, true},
		{"stale", "user", "p2p", "ffeeddccbbaa99887766554433221100", false},
	} {
		m := stop
		m.ID, m.UserID, m.ChatType = tc.name, tc.user, tc.kind
		m.Text = "/exit " + tc.processID
		e.Receiver.Shared = tc.shared
		accept(t, e, m)
		tick(t, e)
		if stops != 1 {
			t.Fatalf("%s requested shutdown without permission", tc.name)
		}
	}
	e.Receiver.Stop = func() error { return errors.New("synthetic private registry failure") }
	failed := stop
	failed.ID = "failed-stop"
	accept(t, e, failed)
	tick(t, e)
	if len(failures) != 2 || failures[0] != CommandFailureReply || failures[1] != CommandFailureReceiverStop {
		t.Fatalf("unexpected receipt and receiver failure samples: %v", failures)
	}
}
