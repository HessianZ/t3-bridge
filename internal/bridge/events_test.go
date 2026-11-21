package bridge

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type manifest struct {
	Now     string                                   `json:"now"`
	Floor   string                                   `json:"recovery_floor"`
	Key     string                                   `json:"test_only_peer_key_utf8"`
	Sources []struct{ File, Node, Signature string } `json:"sources"`
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/homeops/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func loadManifest(t *testing.T) (manifest, time.Time, time.Time) {
	t.Helper()
	var m manifest
	if err := json.Unmarshal(fixture(t, "manifest.json"), &m); err != nil {
		t.Fatal(err)
	}
	now, err := time.Parse(time.RFC3339Nano, m.Now)
	if err != nil {
		t.Fatal(err)
	}
	floor, err := time.Parse(time.RFC3339Nano, m.Floor)
	if err != nil {
		t.Fatal(err)
	}
	return m, now, floor
}
func signed(b []byte, key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(b)
	return hex.EncodeToString(mac.Sum(nil))
}
func TestHomeOpsGoldenFullEvents(t *testing.T) {
	m, now, floor := loadManifest(t)
	got := map[string]Event{}
	for _, source := range m.Sources {
		events, _, err := ParseHealth(fixture(t, source.File), source.Signature, []byte(m.Key), source.Node, now, floor)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			got[e.ID] = e
		}
	}
	var want []Event
	if err := json.Unmarshal(fixture(t, "events.json"), &want); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || len(want) != 3 {
		t.Fatal("expected three events")
	}
	for _, event := range want {
		if !reflect.DeepEqual(event, got[event.ID]) {
			t.Errorf("event %s full payload mismatch\nwant %+v\ngot %+v", event.ID, event, got[event.ID])
		}
	}
}
func TestHealthFailureBoundaries(t *testing.T) {
	m, now, floor := loadManifest(t)
	src := m.Sources[0]
	original := fixture(t, src.File)
	for _, name := range []string{"signature", "whitespace", "node", "stale", "future", "collector", "type", "oversized", "invalid_order"} {
		t.Run(name, func(t *testing.T) {
			b := append([]byte{}, original...)
			sig := src.Signature
			node := src.Node
			var p map[string]any
			json.Unmarshal(b, &p)
			switch name {
			case "signature":
				sig = "00"
			case "whitespace":
				b = append(b, ' ')
			case "node":
				node = "example-router"
			case "stale":
				p["sampled"] = now.Add(-151 * time.Second).Format(time.RFC3339Nano)
			case "future":
				p["sampled"] = now.Add(31 * time.Second).Format(time.RFC3339Nano)
			case "collector":
				p["collector_ok"] = false
			case "type":
				p["active_incidents"] = 42
			case "oversized":
				b = make([]byte, 32769)
				sig = signed(b, m.Key)
			case "invalid_order":
				p["incident_starts"] = map[string]any{"hardware:smart": now.Add(time.Second).Format(time.RFC3339Nano)}
			}
			if name != "signature" && name != "whitespace" && name != "node" && name != "oversized" {
				b, _ = json.Marshal(p)
				sig = signed(b, m.Key)
			}
			if _, _, err := ParseHealth(b, sig, []byte(m.Key), node, now, floor); err == nil {
				t.Fatal("accepted coverage gap")
			}
		})
	}
}
func TestHealthMissingNullAndFirstActivation(t *testing.T) {
	m, now, floor := loadManifest(t)
	var p map[string]any
	json.Unmarshal(fixture(t, m.Sources[0].File), &p)
	for _, null := range []bool{true, false} {
		for _, k := range []string{"active_incidents", "incident_starts", "deliveries"} {
			if null {
				p[k] = nil
			} else {
				delete(p, k)
			}
		}
		b, _ := json.Marshal(p)
		events, _, err := ParseHealth(b, signed(b, m.Key), []byte(m.Key), "example-nas", now, floor)
		if err != nil || len(events) != 0 {
			t.Fatal("missing/null collections must be empty")
		}
	}
	b := fixture(t, m.Sources[0].File)
	events, first, err := ParseHealth(b, m.Sources[0].Signature, []byte(m.Key), "example-nas", now, time.Time{})
	if err != nil || len(events) != 1 || first.IsZero() {
		t.Fatal("first activation must suppress old recovery")
	}
}
func TestMaintenanceGoldenExpiryAndNamespaces(t *testing.T) {
	b := fixture(t, "maintenance-status.json")
	for _, check := range []string{"hardware:smart", "example-router:hardware:smart"} {
		busy, err := MaintenanceBusy(b, []string{check})
		if err != nil || !busy {
			t.Fatal("expired registration or namespace bypass")
		}
	}
	busy, err := MaintenanceBusy(b, []string{"service:gitea"})
	if err != nil || busy {
		t.Fatal("unrelated resource blocked")
	}
	if _, err := MaintenanceBusy([]byte(`{"ok":false,"operations":[]}`), []string{"hardware:smart"}); err == nil {
		t.Fatal("status failure treated as free")
	}
}
func TestAtomicReportsWhitelistAndFailure(t *testing.T) {
	e, _, _, _ := testEngine(t)
	f := setupReports(t, e)
	old := seedOldReports(t, e, f, 1)
	if err := e.refreshReports(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := e.Config.ReportOutputs["example-nas"]
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []AgentReport
	if decodeStrict(before, &rows) != nil || len(rows) != 1 || rows[0].Summary != old[0].Summary {
		t.Fatal("actual source report was replaced")
	}
	reportSQL(t, f.db, []any{`UPDATE analysis_reports SET summary=?`, []any{"token=synthetic-secret"}})
	e.nextReportCheck = time.Time{}
	if e.refreshReports(context.Background()) == nil {
		t.Fatal("credential assignment accepted")
	}
	after, _ := os.ReadFile(path)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("bad source damaged old report")
	}
	e.Config.ReportOutputs["example-nas"] = filepath.Join(path, "missing")
	if e.writeReports() == nil {
		t.Fatal("expected export failure")
	}
	after, _ = os.ReadFile(path)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed output damaged previous report")
	}
}
func TestTrustedAlertLinkNoAutomaticExecution(t *testing.T) {
	e, x, l, _ := testEngine(t)
	e.Config.EventChats = []string{"chat"}
	e.Config.EventRoutes = map[string]string{"example-nas": "nas"}
	var events []Event
	json.Unmarshal(fixture(t, "events.json"), &events)
	ev := events[0]
	e.Store.update(func(st *State) error { st.EventRecords[ev.ID] = ev; return nil })
	tick(t, e)
	if x.sends != 0 {
		t.Fatal("health event auto executed")
	}
	l.message.Quote = "合成告警 " + ev.ID
	m := message("one", "排查这个告警")
	m.ParentID = "alert"
	accept(t, e, m)
	tick(t, e)
	task := onlyTask(t, e)
	if task.Source.EventID != ev.ID || x.sends != 1 {
		t.Fatal("alert not linked")
	}
	x.observation.Status = "completed"
	tick(t, e)
	reply := message("two", "继续调查")
	reply.ParentID = "alert"
	accept(t, e, reply)
	tick(t, e)
	if x.opens != 1 || x.sends != 2 || len(e.Store.snapshot().Tasks) != 1 {
		t.Fatal("same incident did not continue same investigation")
	}
}

func TestSharedAgentReportFixturesAndSeparateSilenceContract(t *testing.T) {
	_, now, _ := loadManifest(t)
	for file, node := range map[string]string{"nas-agent-reports.json": "example-nas", "router-agent-reports.json": "example-router"} {
		t.Run(file, func(t *testing.T) {
			var reports []AgentReport
			b := fixture(t, file)
			if err := decodeStrict(b, &reports); err != nil || len(b) > 32768 || len(reports) > 8 {
				t.Fatal("report shape", err)
			}
			for _, r := range reports {
				if r.Node != node || r.Check == "" || len(r.Check) > 128 || len([]rune(r.Summary)) < 1 || len([]rune(r.Summary)) > 1200 || len(r.EventID) > 128 {
					t.Fatal("report boundary")
				}
				switch r.Stage {
				case "investigating", "maintenance", "resolved", "blocked":
				default:
					t.Fatal("stage")
				}
				updated, err := time.Parse(time.RFC3339Nano, r.Updated)
				if err != nil || updated.After(now.Add(60*time.Second)) {
					t.Fatal("report time")
				}
			}
		})
	}
	if _, err := MaintenanceBusy(fixture(t, "monitor-maintenance.json"), []string{"service:gitea"}); err == nil {
		t.Fatal("silence record treated as ownership status")
	}
}
