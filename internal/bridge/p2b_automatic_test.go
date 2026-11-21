package bridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestP2bSignedAutomaticInvestigationRestartAndRecovery(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-incident-recovery", true: "stopped-never-revived"}[stop], func(t *testing.T) {
			e, x, _, _ := testEngine(t)
			setupReports(t, e)
			e.Config.Topics = nil
			now := time.Now().UTC()
			e.Now = func() time.Time { return now }
			e.Config.AutoInvestigations = AutoInvestigations{Enabled: true, LegacyDispatcherStopped: true, UserID: "user", ChatID: "chat", ChatType: "p2p"}
			key := strings.Repeat("a", 32)
			keyFile := filepath.Join(t.TempDir(), "health.secret")
			os.WriteFile(keyFile, []byte(key), 0600)
			var mu sync.Mutex
			payload := map[string]any{}
			badSignature := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				b, _ := json.Marshal(payload)
				sig := signed(b, key)
				if badSignature {
					sig = "00"
				}
				w.Header().Set("X-HomeOps-Signature", sig)
				w.Write(b)
			}))
			defer server.Close()
			e.Config.Health = []HealthSource{{server.URL + "/healthz", "example-nas", "127.0.0.1", keyFile}}
			set := func(active []string, starts map[string]string, deliveries []any) {
				mu.Lock()
				defer mu.Unlock()
				payload = map[string]any{"node": "example-nas", "sampled": now.Format(time.RFC3339Nano), "collector_ok": true, "active_incidents": active, "incident_starts": starts, "deliveries": deliveries}
			}
			set([]string{}, map[string]string{}, []any{})
			tick(t, e)
			if x.sends != 0 || e.Store.snapshot().AutoNodes["example-nas"] == "" {
				t.Fatal("first healthy sample dispatched historical work")
			}
			e.Store.Close()
			s, err := OpenStore(e.Config.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			e.Store = s
			now = now.Add(31 * time.Second)
			start := normalized(now.Add(-time.Second))
			set([]string{"hardware:smart"}, map[string]string{"hardware:smart": start}, []any{})
			tick(t, e)
			faultTask := onlyTask(t, e)
			if x.sends != 1 || faultTask.Source.InputKind != "automatic" || faultTask.DispatchSource.EventID == "" || !strings.Contains(x.request, "仅获只读调查授权") || len(s.snapshot().Current) != 0 {
				t.Fatal("automatic fault lost identity/read-only scope or stole current conversation")
			}
			now = now.Add(31 * time.Second)
			set([]string{"hardware:smart"}, map[string]string{"hardware:smart": start}, []any{})
			tick(t, e)
			if x.sends != 1 {
				t.Fatal("duplicate signed fault re-dispatched")
			}
			// Absence from the active set is not a recovery receipt.
			x.observation.Status = "completed"
			now = now.Add(31 * time.Second)
			set([]string{}, map[string]string{}, []any{})
			tick(t, e)
			if len(s.snapshot().EventRecords) != 1 || x.sends != 1 {
				t.Fatal("set disappearance or completed run invented recovery")
			}
			if stop {
				m := Message{ID: "stop", UserID: "user", ChatID: "chat", ChatType: "p2p", Text: "拒绝", ParentID: faultTask.CardID}
				accept(t, e, m)
				tick(t, e)
			}
			originalCursor := s.snapshot().AutoNodes["example-nas"]
			now = now.Add(31 * time.Second)
			set([]string{}, map[string]string{}, []any{map[string]string{"check": "hardware:smart", "kind": "recovery", "failure_start": start, "observed": normalized(now.Add(-time.Second)), "delivered": normalized(now)}})
			mu.Lock()
			badSignature = true
			mu.Unlock()
			tick(t, e)
			if len(s.snapshot().EventRecords) != 1 || s.snapshot().AutoNodes["example-nas"] != originalCursor {
				t.Fatal("signature gap advanced recovery/cursor")
			}
			mu.Lock()
			badSignature = false
			mu.Unlock()
			now = now.Add(31 * time.Second)
			set([]string{}, map[string]string{}, []any{map[string]string{"check": "hardware:smart", "kind": "recovery", "failure_start": start, "observed": normalized(now.Add(-time.Second)), "delivered": normalized(now)}})
			s.Close()
			s, err = OpenStore(e.Config.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			e.Store = s
			tick(t, e)
			latest := onlyTask(t, e)
			if stop {
				if x.sends != 1 || !latest.StopRequested {
					t.Fatal("trusted recovery revived refused investigation")
				}
			} else if x.sends != 2 || x.opens != 1 || latest.ID != faultTask.ID || latest.CardID != faultTask.CardID || latest.DispatchSource.EventID == faultTask.DispatchSource.EventID || latest.RequestID == faultTask.RequestID || latest.Source.EventID != faultTask.Source.EventID {
				t.Fatal("recovery forked/overwrote the original investigation")
			}
		})
	}
}
func TestP2bAutomaticNotificationUsesStableCreateThenPatch(t *testing.T) {
	e, l, _, _, fixture := setupAccess(t)
	creates, patches := 0, 0
	uuids := []string{}
	fixture.extra = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/open-apis/im/v1/messages" && r.Method == "POST" {
			var b struct {
				ReceiveID string `json:"receive_id"`
				UUID      string `json:"uuid"`
				Content   string `json:"content"`
				Type      string `json:"msg_type"`
			}
			json.NewDecoder(r.Body).Decode(&b)
			if b.ReceiveID != "chat" || r.URL.Query().Get("receive_id_type") != "chat_id" || b.Type != "interactive" {
				t.Error("wrong automatic notification scope")
			}
			creates++
			uuids = append(uuids, b.UUID)
			io.WriteString(w, `{"code":0,"data":{"message_id":"auto-card"}}`)
			return true
		}
		if r.Method == "PATCH" {
			patches++
			io.WriteString(w, `{"code":0,"data":{}}`)
			return true
		}
		return false
	}
	m := Message{ID: "auto:synthetic", InputKind: "automatic", ChatID: "chat", ChatType: "group", UserID: "user"}
	task := &Task{ID: "synthetic", RequestID: "request", CardRequestID: "request", Source: m, ReplySource: &m, DispatchSource: m, Phase: "tracking", RouteID: "nas", Target: e.Config.Routes["nas"], Summary: "仅只读自动调查"}
	for i := 0; i < 2; i++ {
		card, err := l.Notify(context.Background(), task)
		if err != nil || card != "auto-card" {
			t.Fatal(err)
		}
	}
	task.CardID = "auto-card"
	if _, err := l.Notify(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if creates != 2 || patches != 1 || uuids[0] == "" || uuids[0] != uuids[1] {
		t.Fatal("automatic first-card retry changed UUID or patch identity")
	}
}
func TestP2bAutomaticConfigurationAndOfflineBaselineGate(t *testing.T) {
	e, _, _, _ := testEngine(t)
	setupReports(t, e)
	e.Config.Topics = nil
	e.Config.Health = []HealthSource{{"http://127.0.0.1/healthz", "example-nas", "127.0.0.1", "/synthetic/key"}}
	e.Config.AutoInvestigations = AutoInvestigations{Enabled: true, LegacyDispatcherStopped: true, UserID: "user", ChatID: "chat", ChatType: "p2p"}
	if err := e.Config.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"legacy-active", "foreign-actor", "foreign-chat", "topic", "missing-source", "wrong-node-path"} {
		c := e.Config
		c.AutoInvestigations = e.Config.AutoInvestigations
		switch mode {
		case "legacy-active":
			c.AutoInvestigations.LegacyDispatcherStopped = false
		case "foreign-actor":
			c.AutoInvestigations.UserID = "unknown"
		case "foreign-chat":
			c.AutoInvestigations.ChatID = "unknown"
		case "topic":
			c.AutoInvestigations.TopicID = "outside"
		case "missing-source":
			c.ReportSources = nil
		case "wrong-node-path":
			c.ReportOutputs = map[string]string{"example-nas": "/same", "example-router": "/same"}
			c.ReportSources = append(append([]ReportSource{}, c.ReportSources...), ReportSource{"example-router", "/source", "/config"})
		}
		if c.Validate() == nil {
			t.Fatal("unsafe automatic configuration", mode)
		}
	}
	e.Store.update(func(st *State) error {
		st.AutoNodes = map[string]string{"example-nas": "before"}
		st.AutoEvents = map[string]string{"stopped-event": "stopped-no-revival"}
		st.AutoIncidents = map[string]string{"old": "task"}
		return nil
	})
	if err := e.Store.PrepareAutoBaseline("foreign-app"); err == nil {
		t.Fatal("baseline changed another account")
	}
	if err := e.Store.PrepareAutoBaseline(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	st := e.Store.snapshot()
	if len(st.AutoNodes) != 0 || st.AutoEvents["stopped-event"] != "stopped-no-revival" || st.AutoIncidents["old"] != "task" {
		t.Fatal("re-arming erased stop/dedup history")
	}
	accept(t, e, message("pending", "/nas input"))
	if e.Store.PrepareAutoBaseline(e.Config.AppID) == nil {
		t.Fatal("baseline moved while inbox was not drained")
	}
}

func TestP2bAutomaticTaskIsNotAdoptedByLegacyConversationFallback(t *testing.T) {
	e, x, _, _ := testEngine(t)
	e.Config.Topics = nil
	e.Config.DefaultRoute = "nas"
	e.Config.EventRoutes = map[string]string{"example-nas": "nas"}
	ev := syntheticEvent("example-nas", "hardware:smart", "fault", e.now().Add(-time.Minute))
	m := Message{ID: "auto:" + ev.ID, InputKind: "automatic", UserID: "user", ChatID: "chat", ChatType: "p2p", EventID: ev.ID, EventNode: ev.Node, EventCheck: ev.Check, EventRoute: "nas", Text: "自动只读调查"}
	e.Store.update(func(st *State) error { st.EventRecords[ev.ID] = ev; return nil })
	if err := e.newTask(m, Decision{"nas", "new_task"}, "自动调查"); err != nil {
		t.Fatal(err)
	}
	tick(t, e)
	automatic := onlyTask(t, e)
	ordinary := Message{ID: "user", UserID: "user", ChatID: "chat", ChatType: "p2p", Text: "/nas 普通咨询"}
	st := e.Store.snapshot()
	if currentTask(e.Config, &st, ordinary, "nas") != nil {
		t.Fatal("background automatic task was silently adopted as current conversation")
	}
	accept(t, e, ordinary)
	tick(t, e)
	if len(e.Store.snapshot().Tasks) != 2 || x.sends != 2 {
		t.Fatal("ordinary question queued into automatic investigation")
	}
	e.Store.update(func(st *State) error {
		st.Inbox = append(st.Inbox, Message{ID: "auto-recovery", InputKind: "automatic", UserID: "user", ChatID: "chat", ChatType: "p2p", SessionTask: automatic.ID, RouteAtReceive: "nas"})
		cancelQueued(e.Config, st, ordinary, nil)
		return nil
	})
	for _, q := range e.Store.snapshot().Inbox {
		if q.ID == "auto-recovery" && q.Cancelled {
			t.Fatal("unbound human reset cancelled an unrelated automatic continuation")
		}
	}
}
