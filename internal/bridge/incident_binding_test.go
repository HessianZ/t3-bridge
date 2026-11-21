package bridge

import (
	"reflect"
	"strings"
	"testing"
)

func TestClarifiedAlertKeepsIncidentBindingAndReportIdentity(t *testing.T) {
	e, x, l, _ := testEngine(t)
	e.Config.Topics = nil
	e.Config.EventChats = []string{"chat"}
	e.Config.EventRoutes = map[string]string{"example-nas": "nas"}
	fault := Event{Node: "example-nas", Check: "hardware:smart", FailureStart: "2026-11-17T11:00:00Z", Phase: "fault"}
	fault.ID = eventID(fault.Node, fault.Check, fault.FailureStart, fault.Phase)
	recovery := fault
	recovery.Phase = "recovery"
	recovery.ID = eventID(recovery.Node, recovery.Check, recovery.FailureStart, recovery.Phase)
	if err := e.Store.update(func(st *State) error {
		st.EventRecords[fault.ID] = fault
		st.EventRecords[recovery.ID] = recovery
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	copied := message("copied-alert", "[HomeOps] 硬盘告警\n事件："+fault.ID)
	copied.TopicID = ""
	accept(t, e, copied)
	tick(t, e)
	initial := onlyTask(t, e)
	if initial.Phase != "clarify" || initial.RouteID != "" || initial.Source.EventID != "" || x.sends != 0 {
		t.Fatal("copied alert acquired authorization")
	}
	l.message.Quote = copied.Text
	investigate := message("investigate", "查一下")
	investigate.TopicID = ""
	investigate.ParentID = copied.ID
	accept(t, e, investigate)
	tick(t, e)
	task := onlyTask(t, e)
	wantSource := initial.Source
	if !reflect.DeepEqual(task.Source, wantSource) || task.ID != initial.ID || task.CardID != initial.CardID || task.DispatchSource.EventID != fault.ID || x.sends != 1 {
		t.Fatal("clarify conversion lost original identity or event metadata")
	}
	if e.Store.snapshot().Events[incidentKey(fault)] != task.ID {
		t.Fatal("incident binding lost when clarification became investigation")
	}
	// A busy event continuation must remain an explicit investigation/card update after pin/restart.
	l.message.Quote = "[HomeOps] 恢复\n事件：" + recovery.ID
	follow := message("recovery-follow", "查一下")
	follow.TopicID = ""
	follow.ParentID = "recovery-alert"
	accept(t, e, follow)
	tick(t, e)
	if len(e.Store.snapshot().Inbox) != 1 || x.sends != 1 {
		t.Fatal("busy event reply was not queued")
	}
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	e.Store = reopened
	x.observation.Status = "completed"
	tick(t, e)
	tick(t, e)
	task = onlyTask(t, e)
	if task.ID != initial.ID || task.CardID != initial.CardID || x.opens != 1 || x.sends != 2 || e.Store.snapshot().Events[recovery.ID] != task.ID {
		t.Fatal("recovery forked original investigation after restart")
	}
	x.observation.Status = "completed"
	tick(t, e)
	l.onResolve = func() { t.Fatal("own task card should keep fast path") }
	own := message("own-follow", "继续核对")
	own.TopicID = ""
	own.ParentID = task.CardID
	accept(t, e, own)
	tick(t, e)
	if x.sends != 3 || x.opens != 1 || !strings.Contains(x.request, "仅获只读调查授权") {
		t.Fatal("own-card continuation lost read-only scope")
	}
	task = onlyTask(t, e)
	if !reflect.DeepEqual(task.Source, wantSource) || task.CardID != initial.CardID {
		t.Fatal("continuation replaced source identity")
	}
	if task.DispatchSource.EventID != recovery.ID || len(task.Replies) < 2 || task.Replies[len(task.Replies)-2].Source.EventID != fault.ID {
		t.Fatal("event/report identity lost from turn sources")
	}
}

func TestBoundInvestigationRejectsDifferentIncidentOrUnknownReference(t *testing.T) {
	for _, kind := range []string{"different_check", "different_start", "legacy_dispatch_incident", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			e, x, l, _ := testEngine(t)
			e.Config.EventChats = []string{"chat"}
			e.Config.EventRoutes = map[string]string{"example-nas": "nas"}
			originalEvent := Event{Node: "example-nas", Check: "hardware:smart", FailureStart: "2026-11-17T11:00:00Z", Phase: "fault"}
			originalEvent.ID = eventID(originalEvent.Node, originalEvent.Check, originalEvent.FailureStart, originalEvent.Phase)
			next := originalEvent
			switch kind {
			case "different_check":
				next.Check = "service:gitea"
			case "different_start", "legacy_dispatch_incident":
				next.FailureStart = "2026-11-17T12:00:00Z"
			}
			next.ID = eventID(next.Node, next.Check, next.FailureStart, next.Phase)
			if err := e.Store.update(func(st *State) error {
				st.EventRecords[originalEvent.ID] = originalEvent
				if kind != "unknown" {
					st.EventRecords[next.ID] = next
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			copied := message("copied", "[HomeOps] 告警\n事件："+originalEvent.ID)
			accept(t, e, copied)
			tick(t, e)
			initial := onlyTask(t, e)
			if kind != "unknown" {
				l.message.Quote = copied.Text
				reply := message("first", "查一下")
				reply.ParentID = copied.ID
				accept(t, e, reply)
				tick(t, e)
				x.observation.Status = "completed"
				tick(t, e)
			}
			if kind == "legacy_dispatch_incident" {
				if err := e.Store.update(func(st *State) error {
					source := &st.Tasks[initial.ID].Source
					source.EventID = ""
					source.EventNode = ""
					source.EventCheck = ""
					source.EventRoute = ""
					delete(st.Events, incidentKey(originalEvent))
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			before := e.Store.snapshot().Tasks[initial.ID]
			if kind == "unknown" {
				l.message.Quote = "事件：eb1_" + strings.Repeat("f", 32)
			} else {
				l.message.Quote = "事件：" + next.ID
			}
			reply := message("next", "查一下")
			reply.ParentID = "other-alert"
			reply.RootID = copied.ID
			accept(t, e, reply)
			tick(t, e)
			after := e.Store.snapshot().Tasks[initial.ID]
			wantSends := 1
			if kind == "unknown" {
				wantSends = 0
			}
			if x.sends != wantSends || !reflect.DeepEqual(before.Source, after.Source) || before.RequestID != after.RequestID {
				t.Fatal("new quote overwrote bound investigation or acquired authorization")
			}
			if kind != "unknown" && e.Store.snapshot().Events[incidentKey(next)] != "" {
				t.Fatal("different incident rebound to old investigation")
			}
			if len(e.Store.snapshot().Tasks) != 2 {
				t.Fatal("conflicting reference did not clarify")
			}
		})
	}
}
