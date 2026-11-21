package bridge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func syntheticEvent(node, check, phase string, start time.Time) Event {
	ev := Event{Schema: "homeops.eventbridge/v1", Node: node, Check: check, Phase: phase, FailureStart: normalized(start), Observed: normalized(start.Add(time.Second)), SourceSampled: normalized(start.Add(time.Second))}
	ev.ID = eventID(node, check, ev.FailureStart, phase)
	return ev
}
func TestP2bAutomaticBaselineSurvivesEmptyMapsRestart(t *testing.T) {
	for _, oldFault := range []bool{false, true} {
		t.Run(map[bool]string{false: "healthy-baseline", true: "old-fault-baseline"}[oldFault], func(t *testing.T) {
			e, _, _, _ := testEngine(t)
			e.Config.Topics = nil
			now := time.Now().UTC()
			e.Now = func() time.Time { return now }
			e.Config.EventRoutes = map[string]string{"example-nas": "nas"}
			e.Config.EventChats = []string{"chat"}
			e.Config.AutoInvestigations = AutoInvestigations{Enabled: true, LegacyDispatcherStopped: true, UserID: "user", ChatID: "chat", ChatType: "p2p"}
			old := syntheticEvent("example-nas", "service:gitea", "fault", now.Add(-time.Hour))
			events := []Event{}
			if oldFault {
				events = append(events, old)
			}
			if err := e.Store.update(func(st *State) error {
				for _, ev := range events {
					st.EventRecords[ev.ID] = ev
				}
				return e.enqueueAutomatic(st, "example-nas", events)
			}); err != nil {
				t.Fatal(err)
			}
			if len(e.Store.snapshot().Inbox) != 0 {
				t.Fatal("baseline replayed old fault")
			}
			e.Store.Close()
			s, err := OpenStore(e.Config.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			e.Store = s
			now = now.Add(time.Minute)
			fresh := syntheticEvent("example-nas", "service:gitea", "fault", now)
			for i := 0; i < 2; i++ {
				if err := s.update(func(st *State) error {
					st.EventRecords[fresh.ID] = fresh
					batch := []Event{fresh}
					if oldFault {
						batch = append(batch, old)
					}
					return e.enqueueAutomatic(st, "example-nas", batch)
				}); err != nil {
					t.Fatal(err)
				}
			}
			st := s.snapshot()
			if len(st.Inbox) != 1 || st.Inbox[0].EventID != fresh.ID || st.AutoIncidents[incidentKey(fresh)] == "" || (oldFault && st.AutoEvents[old.ID] != "baseline-only") {
				t.Fatal("restart lost cursor/maps or replayed an incident")
			}
		})
	}
}

func TestP2bGeneralVisibleEventContextPreservesTextAndSource(t *testing.T) {
	for _, text := range []string{"这条为什么会出现？给我讲原理", "不要执行命令，只解释这条告警", "先讨论备选方案，不实施", "收到", "请比较这次和上次的证据"} {
		t.Run(text, func(t *testing.T) {
			e, x, l, _ := testEngine(t)
			e.Config.EventChats = []string{"chat"}
			e.Config.EventRoutes = map[string]string{"example-nas": "nas"}
			accept(t, e, message("one", "/nas 普通咨询"))
			tick(t, e)
			source := onlyTask(t, e).Source
			x.observation.Status = "completed"
			tick(t, e)
			ev := syntheticEvent("example-nas", "hardware:smart", "fault", time.Now().Add(-time.Minute))
			e.Store.update(func(st *State) error { st.EventRecords[ev.ID] = ev; return nil })
			l.message.Quote = "事件：" + ev.ID
			m := message("next", text)
			m.ParentID = "alert"
			m.RootID = source.ID
			accept(t, e, m)
			tick(t, e)
			task := onlyTask(t, e)
			if x.sends != 2 || !reflect.DeepEqual(task.Source, source) || task.DispatchSource.EventID != ev.ID || !strings.HasPrefix(x.request, text) || !strings.Contains(x.request, "仅获只读调查授权") {
				t.Fatal("generic quoted discussion was swallowed or overwrote source")
			}
			if len(task.Replies) != 1 || task.Replies[0].Source.EventID != "" {
				t.Fatal("event identity retroactively changed a plain turn")
			}
		})
	}
}

func TestP2bMaintenanceAllowsDiscussionWithRealHeldCheckShape(t *testing.T) {
	e, x, _, _ := testEngine(t)
	e.Config.DefaultRoute = "nas"
	e.Classifier = &fakeClassifier{d: Decision{"nas", "new_task"}}
	target := e.Config.Routes["nas"]
	target.Checks = []string{"service:gitea", "service:nextcloud", "security:audit", "hardware:smart"}
	e.Config.Routes["nas"] = target
	status := map[string]any{"ok": true, "operations": []any{map[string]any{"check": "service:gitea", "owner_thread": "t3-thread://v2/nas/another", "expired": true, "repair_blocked": true}, map[string]any{"check": "service:nextcloud", "owner_thread": "t3-thread://v2/nas/another", "expired": false, "repair_blocked": true}, map[string]any{"check": "security:audit", "owner_thread": "t3-thread://v2/nas/another", "expired": false, "repair_blocked": true}}}
	b, _ := json.Marshal(status)
	file := filepath.Join(t.TempDir(), "status.json")
	os.WriteFile(file, b, 0600)
	script := filepath.Join(t.TempDir(), "coordination")
	os.WriteFile(script, []byte("#!/bin/sh\nif [ \"$1\" = status ]; then cat '"+file+"'; else exit 75; fi\n"), 0700)
	e.Maintenance = Coordination{Command: script}
	accept(t, e, message("one", "列一下你可用的mcp"))
	tick(t, e)
	if x.sends != 1 || onlyTask(t, e).Phase != "tracking" || !strings.Contains(x.request, "其它owner或过期登记重叠") && !strings.Contains(x.request, "其它owner或过期登记") || !strings.Contains(x.request, "不是OS权限沙箱") || !strings.Contains(x.request, "实际受影响checks") {
		t.Fatal("maintenance blocked discussion or lost the write guard boundary")
	}
	if busy, err := (Coordination{Command: script}).Busy(context.Background(), target); err != nil || !busy {
		t.Fatal("discussion incorrectly released maintenance ownership")
	}
	x.observation.Status = "completed"
	tick(t, e)
	review := target
	review.Model = "codebuddy-direct/hy4free"
	review.Options = map[string]any{"variant": "high"}
	e.Config.Routes["nas-review"] = review
	e.Config.ReviewRoute = "nas-review"
	accept(t, e, message("review", "/review 请只读复核"))
	tick(t, e)
	if x.sends != 2 || !strings.Contains(x.request, "仅获只读讨论授权") || !strings.Contains(x.request, "配置checks存在维护登记") {
		t.Fatal("maintenance still blocks explicit Hy4 review")
	}
	for _, mode := range []string{"bad-status", "failed-status"} {
		if mode == "bad-status" {
			os.WriteFile(file, []byte(`{"ok":false,"operations":[]}`), 0600)
		} else {
			os.Remove(file)
		}
		notice := maintenanceNotice(false, context.DeadlineExceeded)
		if !strings.Contains(notice, "不得推断空闲或生产写入") {
			t.Fatal("failed maintenance read opened write authority")
		}
		if _, err := (Coordination{Command: script}).Busy(context.Background(), target); err == nil {
			t.Fatal("bad status was accepted as free")
		}
	}
}
