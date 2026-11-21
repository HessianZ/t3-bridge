package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestP2bImplicitEventContinuationQueueRestartAndNewContext(t *testing.T) {
	e, x, l, _ := testEngine(t)
	setupReports(t, e)
	accept(t, e, message("one", "/nas 普通会话"))
	tick(t, e)
	first := onlyTask(t, e)
	x.observation.Status = "completed"
	tick(t, e)
	ev := syntheticEvent("example-nas", "hardware:smart", "fault", e.now().Add(-time.Minute))
	e.Store.update(func(st *State) error { st.EventRecords[ev.ID] = ev; return nil })
	l.message.Quote = "事件：" + ev.ID
	m := message("two", "讨论一下这条告警")
	m.ParentID = "alert"
	m.RootID = first.Source.ID
	accept(t, e, m)
	x.observation.Status = "running"
	tick(t, e)
	second := onlyTask(t, e)
	l.message.Quote = ""
	accept(t, e, message("three", "继续解释证据，不实施修改"))
	tick(t, e)
	if len(e.Store.snapshot().Inbox) != 1 || x.sends != 2 {
		t.Fatal("busy implicit continuation was not queued")
	}
	e.Store.Close()
	s, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e.Store = s
	x.observation.Status = "completed"
	tick(t, e)
	tick(t, e)
	third := onlyTask(t, e)
	if third.ID != first.ID || third.ThreadID != second.ThreadID || third.CardID == second.CardID || third.RequestID == second.RequestID || third.DispatchSource.EventID != ev.ID || !third.DispatchSource.EventInherited || third.Source.EventID != "" || !strings.Contains(x.request, third.RequestID) || strings.Contains(x.request, `"requestId":"`+second.RequestID+`"`) {
		t.Fatal("implicit queued restart lost current-event/new-request/new-card identity")
	}
	queueReport(t, e, third, "investigating", "第三轮实际证据仍不足，需要只读核对。")
	if err := e.refreshReports(context.Background()); err != nil {
		t.Fatal(err)
	}
	current := onlyTask(t, e)
	if current.Report == nil || current.Report.EventID != ev.ID || current.Replies[len(current.Replies)-1].Report != nil {
		t.Fatal("new report updated an old round")
	}
	accept(t, e, message("reset", "/new"))
	tick(t, e)
	l.message.Quote = ""
	accept(t, e, message("four", "/nas 新上下文"))
	tick(t, e)
	var fresh *Task
	for _, task := range s.snapshot().Tasks {
		if task.Source.ID == "four" {
			fresh = task
		}
	}
	if fresh == nil || fresh.ID == first.ID || fresh.DispatchSource.EventID != "" || fresh.ReportInstructions != "" {
		t.Fatal("reset leaked previous incident into a fresh context")
	}
}
func TestP2bReportSubmissionRejectsUnboundSensitiveOrForgedFields(t *testing.T) {
	e, _, l, _ := testEngine(t)
	setupReports(t, e)
	ev := syntheticEvent("example-nas", "hardware:smart", "fault", e.now().Add(-time.Minute))
	e.Store.update(func(st *State) error { st.EventRecords[ev.ID] = ev; return nil })
	l.message.Quote = "事件：" + ev.ID
	m := message("one", "只解释")
	m.ParentID = "alert"
	accept(t, e, m)
	tick(t, e)
	task := onlyTask(t, e)
	for _, mode := range []string{"task", "request", "event", "node", "stage", "empty", "long", "credential", "json-credential", "bearer", "private-key", "bad-json"} {
		input := map[string]any{"taskId": task.ID, "requestId": task.RequestID, "eventId": ev.ID, "stage": "investigating", "summary": "真实只读证据"}
		switch mode {
		case "task":
			input["taskId"] = "outside"
		case "request":
			input["requestId"] = "old-unknown"
		case "event":
			input["eventId"] = "eb1_" + strings.Repeat("f", 32)
		case "node":
			input["node"] = "example-router"
		case "stage":
			input["stage"] = "recovered"
		case "empty":
			input["summary"] = ""
		case "long":
			input["summary"] = strings.Repeat("界", 1201)
		case "credential":
			input["summary"] = "token=synthetic"
		case "json-credential":
			input["summary"] = `{"authorization":"synthetic"}`
		case "bearer":
			input["summary"] = "Bearer synthetic"
		case "private-key":
			input["summary"] = "-----BEGIN PRIVATE KEY-----"
		}
		b, _ := json.Marshal(input)
		if mode == "bad-json" {
			b = []byte("{bad")
		}
		var out bytes.Buffer
		if SubmitReport(e.Config, bytes.NewReader(b), &out) == nil || out.Len() != 0 {
			t.Fatal("bad report accepted or echoed content", mode)
		}
	}
	if files, _ := filepath.Glob(filepath.Join(e.Config.StateDir, "report-inbox", "*.json")); len(files) != 0 {
		t.Fatal("invalid report left a partial receipt")
	}
	id := queueReport(t, e, task, "maintenance", strings.Repeat("界", 1200))
	path := filepath.Join(e.Config.StateDir, "report-inbox", id+".json")
	b, _ := os.ReadFile(path)
	var forged ReportSubmission
	json.Unmarshal(b, &forged)
	forged.Published = true
	b, _ = json.Marshal(forged)
	os.WriteFile(path, b, 0600)
	if e.publishSubmissions(context.Background()) == nil || len(e.Store.snapshot().ReportSubmissions) != 0 {
		t.Fatal("worker trusted model-written publication flags")
	}
}
func TestP2bEventQuoteDoesNotSelectAnotherMachine(t *testing.T) {
	for _, text := range []string{"/mac 检查系统", "请去Mac重启服务"} {
		e, x, l, _ := testEngine(t)
		e.Config.EventChats = []string{"chat"}
		e.Config.EventRoutes = map[string]string{"example-nas": "nas"}
		ev := syntheticEvent("example-nas", "hardware:smart", "fault", e.now().Add(-time.Minute))
		e.Store.update(func(st *State) error { st.EventRecords[ev.ID] = ev; return nil })
		l.message.Quote = "事件：" + ev.ID
		m := message("one", text)
		m.ParentID = "alert"
		accept(t, e, m)
		tick(t, e)
		if x.sends != 0 || onlyTask(t, e).Phase != "clarify" {
			t.Fatal("event quote crossed an explicit machine boundary")
		}
	}
}

func TestP2bImplicitIncidentShortMergeKeepsCurrentRequest(t *testing.T) {
	e, x, l, _ := testEngine(t)
	setupReports(t, e)
	ev := syntheticEvent("example-nas", "hardware:smart", "fault", e.now().Add(-time.Minute))
	e.Store.update(func(st *State) error { st.EventRecords[ev.ID] = ev; return nil })
	l.message.Quote = "事件：" + ev.ID
	m := message("one", "只解释")
	m.ParentID = "alert"
	accept(t, e, m)
	tick(t, e)
	first := onlyTask(t, e)
	x.observation.Status = "completed"
	tick(t, e)
	l.message.Quote = ""
	now := e.now()
	e.Now = func() time.Time { return now.Add(quietWindow + 100*time.Millisecond) }
	for i, id := range []string{"two", "three"} {
		m := message(id, "继续说明")
		m.ReceivedAt = now.Add(time.Duration(i) * 100 * time.Millisecond).UTC().Format(time.RFC3339Nano)
		accept(t, e, m)
	}
	tick(t, e)
	latest := onlyTask(t, e)
	if x.sends != 2 || len(latest.DispatchSource.Parts) != 2 || latest.DispatchSource.EventID != ev.ID || !latest.DispatchSource.EventInherited || latest.CardID == first.CardID || !strings.Contains(x.request, latest.RequestID) {
		t.Fatal("short-message merge erased inherited incident/new-round report identity")
	}
}
func TestP2bFollowupFlushExitContractAndBoundedScheduling(t *testing.T) {
	for _, mode := range []string{"disabled", "empty", "remaining", "business-failure", "foundation", "missing-field", "null-count", "oversized-count", "inconsistent", "stderr"} {
		t.Run(mode, func(t *testing.T) {
			e, x, _, _ := testEngine(t)
			now := time.Now()
			e.Now = func() time.Time { return now }
			dir := t.TempDir()
			count := filepath.Join(dir, "count")
			body := `{"ok":true,"attempted":0,"sent":0,"failed":0,"pending":0}`
			code := 0
			switch mode {
			case "remaining":
				body = `{"ok":true,"attempted":2,"sent":2,"failed":0,"pending":5}`
			case "business-failure":
				body = `{"ok":false,"attempted":2,"sent":1,"failed":1,"pending":4}`
				code = 2
			case "foundation":
				body = ""
				code = 1
			case "missing-field":
				body = `{"ok":true}`
			case "null-count":
				body = `{"ok":true,"attempted":null,"sent":0,"failed":0,"pending":0}`
			case "oversized-count":
				body = `{"ok":true,"attempted":3,"sent":3,"failed":0,"pending":0}`
			case "inconsistent":
				body = `{"ok":true,"attempted":2,"sent":1,"failed":0,"pending":0}`
			}
			script := filepath.Join(dir, "flush")
			text := "#!/bin/sh\nprintf 'call\\n' >> '" + count + "'\n[ \"$1\" = agent-followups-flush ] || exit 99\nprintf '%s' '" + body + "'\n"
			if mode == "stderr" {
				text += "printf 'synthetic diagnostic' >&2\n"
			}
			text += fmt.Sprintf("exit %d\n", code)
			os.WriteFile(script, []byte(text), 0700)
			e.Config.FollowupFlush = FollowupFlushConfig{Enabled: mode != "disabled", Command: script, ConfigFile: filepath.Join(dir, "private.json")}
			if err := e.flushFollowups(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := e.flushFollowups(context.Background()); err != nil {
				t.Fatal(err)
			}
			calls, _ := os.ReadFile(count)
			st := e.Store.snapshot()
			if mode == "disabled" {
				if len(calls) != 0 {
					t.Fatal("default disabled flush invoked a sender")
				}
				return
			}
			if string(calls) != "call\n" || x.sends != 0 || x.stops != 0 {
				t.Fatal("flush ran more than once per schedule or touched T3")
			}
			switch mode {
			case "empty", "remaining":
				if st.FollowupIssue != "" || st.FollowupStatus == "" {
					t.Fatal("valid success rejected")
				}
			case "business-failure":
				if !strings.Contains(st.FollowupIssue, "发送失败") || !strings.Contains(st.FollowupStatus, "剩余4") {
					t.Fatal("business failure hidden")
				}
			default:
				if !strings.Contains(st.FollowupIssue, "基础状态未确认") || st.FollowupStatus != "" {
					t.Fatal("malformed/base failure fabricated delivery")
				}
			}
			now = now.Add(3 * time.Second)
			e.flushFollowups(context.Background())
			calls, _ = os.ReadFile(count)
			want := "call\n"

			if string(calls) != want {
				t.Fatal("remaining backlog/failure retry cadence wrong")
			}
		})
	}
}
