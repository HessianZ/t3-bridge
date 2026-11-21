package bridge

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func captureProcessRequests(t *testing.T, f *accessFixture, reject func(string, replyModeRequest) bool) map[string][]replyModeRequest {
	t.Helper()
	requests := map[string][]replyModeRequest{}
	f.extra = func(w http.ResponseWriter, r *http.Request) bool {
		id, ok := strings.CutPrefix(r.URL.Path, "/open-apis/im/v1/messages/")
		if !ok || (r.Method != http.MethodPatch && !strings.HasSuffix(id, "/reply")) {
			return false
		}
		var body replyModeRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return true
		}
		id = strings.TrimSuffix(id, "/reply")
		requests[id] = append(requests[id], body)
		if reject != nil && reject(id, body) {
			io.WriteString(w, `{"code":230049,"msg":"synthetic missing confirmation"}`)
			return true
		}
		fmt.Fprintf(w, `{"code":0,"data":{"message_id":%q}}`, "reply-"+body.UUID)
		return true
	}
	return requests
}

func TestProcessMessageTextLifecycle(t *testing.T) {
	e, _, ch, x, f := setupAccess(t)
	requests := captureReplyModes(t, f)
	accessCommand(t, e, ch, "default", "user", "chat", "/config process")
	accessCommand(t, e, ch, "denied", "other", "chat", "/config process detailed")
	accessCommand(t, e, ch, "invalid", "admin", "chat", "/config process default")
	if e.Store.runSettings().ProcessMode != "" || !strings.Contains(requests["default"][0].Content, "off") ||
		!strings.Contains(requests["denied"][0].Content, "owner/admin") || x.sends != 0 {
		t.Fatal("process default, validation or command authorization changed")
	}
	accessCommand(t, e, ch, "text", "user", "chat", "/config reply text")
	accessCommand(t, e, ch, "tools-off", "user", "chat", "/config tools off")
	accessCommand(t, e, ch, "progress-off", "user", "chat", "/config progress off")
	accessCommand(t, e, ch, "detailed", "admin", "chat", "/config process detailed")
	now := time.Date(2026, 11, 19, 1, 2, 3, 0, time.UTC)
	e.Now = func() time.Time { return now }
	x.observation.Progress = "synthetic-unclassified-progress"
	activity := &activityExecutor{fakeExecutor: x, tools: []runAction{
		{Label: "命令进行中", At: now}, {Label: "文件搜索已完成", At: now.Add(-time.Second), Finished: true},
	}}
	e.T3 = activity
	accept(t, e, Normalize(groupMessage("one", "user", "chat", "/nas 开始合成任务", true)))
	tick(t, e)
	first := onlyTask(t, e)
	if first.Process == nil || first.Process.MessageID == "" || first.CardID != "" || !first.TextReply || len(requests["one"]) != 1 ||
		requests["one"][0].MsgType != "interactive" || !requests["one"][0].ReplyInThread || requests["one"][0].UUID == cardUUID(first) {
		t.Fatalf("process and text result identities were not independent: %+v", first.Process)
	}
	accessCommand(t, e, ch, "disable", "admin", "chat", "/config process off")
	x.observation.PendingApprovals = 1
	tick(t, e)
	if activity.reads != 1 || len(requests[first.Process.MessageID]) != 0 {
		t.Fatal("process sampling or update exceeded the existing interval")
	}
	now = now.Add(runActivityInterval)
	tick(t, e)
	patches := requests[first.Process.MessageID]
	if len(patches) != 1 || !strings.Contains(patches[0].Content, "等待用户处理 T3 审批") ||
		!strings.Contains(patches[0].Content, "11-19 09:02:02 · 文件搜索已完成") || activity.reads != 2 {
		t.Fatalf("detailed process did not use the same bounded activity sample: %+v", patches)
	}
	now = now.Add(runActivityInterval)
	tick(t, e)
	if len(requests[first.Process.MessageID]) != 1 {
		t.Fatal("unchanged process content produced another patch")
	}
	now = now.Add(runActivityInterval)
	x.observation = Observation{Status: "completed", Tasks: []string{"synthetic-private-child-id"}, Progress: "synthetic-unclassified-progress"}
	tick(t, e)
	if task := onlyTask(t, e); task.Phase != "awaiting_children" || task.CardID != "" || len(requests["one"]) != 1 {
		t.Fatal("parent completion ended the process or text reply while work remained")
	}
	x.observation = Observation{Status: "completed", Summary: "可见最终结论", Progress: "synthetic-unclassified-progress"}
	tick(t, e)
	tick(t, e)
	finished := onlyTask(t, e)
	patches = requests[first.Process.MessageID]
	if !finished.Process.Closed || finished.Process.MessageID != first.Process.MessageID || finished.CardID == first.Process.MessageID ||
		len(patches) != 3 || !strings.Contains(patches[1].Content, "等待异步工作收口") || !strings.Contains(patches[2].Content, "本轮执行已结束") ||
		!strings.Contains(patches[2].Content, "终态未确认") || len(requests["one"]) != 2 || requests["one"][1].MsgType != "text" ||
		!strings.Contains(requests["one"][1].Content, "可见最终结论") || strings.Contains(requests["one"][1].Content, "命令进行中") {
		t.Fatalf("terminal process or text result did not close independently: %+v", requests)
	}
	for _, request := range append(patches, requests["one"][0]) {
		if len(card2Elements(t, request.Content)) != 1 || strings.Contains(request.Content, "synthetic-") || strings.Contains(request.Content, "可见最终结论") {
			t.Fatalf("process message carried buttons or unclassified content: %s", request.Content)
		}
	}
	if e.Store.snapshot().Bindings[key(first.Source, first.Process.MessageID)] != "" {
		t.Fatal("process message was bound as a task reply")
	}
	accept(t, e, message("two", "继续讨论"))
	tick(t, e)
	if second := onlyTask(t, e); second.Process != nil || x.sends != 2 {
		t.Fatal("off setting did not apply to the subsequent round")
	}
}

func TestProcessMessageRestartAndArchive(t *testing.T) {
	e, l, ch, x, f := setupAccess(t)
	restart := func() {
		t.Helper()
		if err := e.Store.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := OpenStore(e.Config.StateDir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { reopened.Close() })
		e.Store, l.Store = reopened, reopened
	}
	createMissing, finalMissing := true, true
	requests := captureProcessRequests(t, f, func(id string, req replyModeRequest) bool {
		return (createMissing && id == "one" && strings.Contains(req.Content, "处理过程")) ||
			(finalMissing && req.UUID == "" && strings.Contains(req.Content, "本轮执行已结束"))
	})
	accessCommand(t, e, ch, "brief", "user", "chat", "/config process brief")
	accessCommand(t, e, ch, "tools", "user", "chat", "/config tools brief")
	now := time.Date(2026, 11, 19, 1, 2, 3, 0, time.UTC)
	e.Now = func() time.Time { return now }
	activity := &activityExecutor{fakeExecutor: x, tools: []runAction{
		{Label: "命令进行中", At: now}, {Label: "文件搜索已完成", At: now.Add(-time.Second), Finished: true},
	}}
	e.T3 = activity
	accept(t, e, message("one", "/nas 合成任务"))
	tick(t, e)
	first := onlyTask(t, e)
	initial := requests["one"][0]
	if first.CardID == "" || first.Process.MessageID != "" || first.Process.CreateAt.IsZero() ||
		len(requests["one"]) != 2 || !strings.Contains(initial.Content, "处理过程") || !strings.Contains(requests["one"][1].Content, "button") {
		t.Fatal("process create uncertainty blocked the original task card or lost its fence")
	}
	restart()
	if e.Store.runSettings().ProcessMode != toolActivityBrief {
		t.Fatal("process mode was lost on restart")
	}
	createMissing = false
	now = now.Add(runActivityInterval)
	tick(t, e)
	confirmed := onlyTask(t, e)
	if len(requests["one"]) != 3 || initial != requests["one"][2] || confirmed.Process.MessageID == "" ||
		confirmed.Process.CreateAt != first.Process.CreateAt || confirmed.CardID != first.CardID || activity.reads != 1 {
		t.Fatal("restart did not reuse the fenced process body and UUID or activity polling")
	}
	now = now.Add(runActivityInterval)
	tick(t, e)
	processID := confirmed.Process.MessageID
	if len(requests[processID]) != 1 || !strings.Contains(requests[processID][0].Content, "命令进行中") ||
		strings.Contains(requests[processID][0].Content, "文件搜索") || strings.Contains(requests[processID][0].Content, "09:02") || activity.reads != 2 {
		t.Fatal("brief mode did not reuse the activity sample or exposed details")
	}
	x.observation = Observation{Status: "completed", Summary: "合成终态结果"}
	tick(t, e)
	first = onlyTask(t, e)
	if first.Process.Closed || first.Process.MessageID != processID || first.NotifyHash == "" ||
		!strings.Contains(requests[first.CardID][len(requests[first.CardID])-1].Content, "合成终态结果") {
		t.Fatal("a failed process patch delayed final task card delivery")
	}
	restart()
	accessCommand(t, e, ch, "input", "user", "chat", "/input")
	form := message("card:form-event", "继续讨论")
	form.InputKind, form.ControlTask, form.ControlCard, form.FormRequestID = "form", first.ID, first.CardID, first.RequestID
	x.observation = Observation{Status: "running", RunID: "run"}
	accept(t, e, form)
	tick(t, e)
	second := onlyTask(t, e)
	if len(second.Replies) != 1 || second.Replies[0].Process == nil || second.Replies[0].Process.MessageID != processID ||
		second.CardID != first.CardID || second.Process == nil || second.Process.ReplyTo != first.CardID || second.Process.MessageID == processID ||
		len(requests["one"]) != 3 || len(requests[form.ID]) != 0 || len(requests[processID]) != 2 {
		t.Fatalf("form discussion lost the reply anchor or pending process close: %+v", second)
	}
	finalMissing = false
	now = now.Add(runActivityInterval)
	tick(t, e)
	archived := onlyTask(t, e).Replies[0].Process
	if !archived.Closed || archived.MessageID != processID || len(requests[processID]) != 3 || requests[processID][1] != requests[processID][2] ||
		strings.Contains(requests[processID][2].Content, "合成终态结果") || x.sends != 2 {
		t.Fatal("archived process did not retry closure against its original identity and body")
	}
}

func TestProcessMessageCreateExpiry(t *testing.T) {
	e, _, ch, x, f := setupAccess(t)
	requests := captureProcessRequests(t, f, func(_ string, req replyModeRequest) bool {
		return strings.Contains(req.Content, "处理过程")
	})
	now := time.Date(2026, 11, 19, 1, 2, 3, 0, time.UTC)
	e.Now = func() time.Time { return now }
	accessCommand(t, e, ch, "brief", "user", "chat", "/config process brief")
	accept(t, e, message("one", "/nas 合成任务"))
	tick(t, e)
	now = now.Add(processCreateWindow)
	x.observation = Observation{Status: "completed", Summary: "已结束"}
	tick(t, e)
	tick(t, e)
	finished := onlyTask(t, e)
	if len(requests["one"]) != 2 || finished.Process.MessageID != "" || !finished.Process.Closed || finished.Process.Issue == "" ||
		finished.NotifyHash == "" || finished.Phase != "completed" {
		t.Fatal("expired unknown create was retried or suppressed the original result")
	}
}

func TestProcessMessageDocumentKeepsCommentDestination(t *testing.T) {
	e, _, ch, x, f := setupAccess(t)
	e.Config.CommentRoute = "mac"
	accessCommand(t, e, ch, "detailed", "user", "chat", "/config process detailed")
	writes := 0
	f.extra = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			return false
		}
		if r.Method != http.MethodPost || r.URL.Path != "/open-apis/drive/v1/files/doc/comments/thread/replies" {
			t.Errorf("unexpected document notification destination: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return true
		}
		writes++
		io.WriteString(w, `{"code":0,"data":{"reply_id":"comment-reply"}}`)
		return true
	}
	m := Message{ID: "event", ChatID: "document:doc", UserID: "user", Text: "文档问题",
		Comment: &CommentSource{FileType: "docx", FileToken: "doc", CommentID: "thread"}}
	if err := e.newTask(m, Decision{Action: "new_task", RouteID: "mac"}, "等待执行"); err != nil {
		t.Fatal(err)
	}
	tick(t, e)
	if task := onlyTask(t, e); task.Process != nil || writes != 0 || task.Phase != "tracking" {
		t.Fatal("document tracking unexpectedly prepared or sent a process message")
	}
	x.observation = Observation{Status: "completed", Summary: "文档结果"}
	tick(t, e)
	if task := onlyTask(t, e); task.CardID != "comment-reply" || writes != 1 {
		t.Fatal("document final result did not reach its original comment")
	}
	for _, m := range []Message{{ID: "auto", InputKind: "automatic"}, {ID: "card:event", InputKind: "form"}} {
		if newProcessMessage(m, toolActivityDetailed) != nil {
			t.Fatalf("unsupported destination prepared a process message: %+v", m)
		}
	}
}
