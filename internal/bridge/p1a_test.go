package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	channel "github.com/larksuite/channel-sdk-go"
)

func TestP1aFileSDKDownloadAndBounds(t *testing.T) {
	for _, mode := range []string{"ok", "json", "large", "error", "missing"} {
		t.Run(mode, func(t *testing.T) {
			e, _, ch, x, f := setupAccess(t)
			e.Config.DefaultRoute = "nas"
			e.Classifier = &fakeClassifier{d: Decision{RouteID: "nas", Action: "new_task"}}
			downloads := 0
			f.extra = func(w http.ResponseWriter, r *http.Request) bool {
				if !strings.Contains(r.URL.Path, "/resources/") {
					return false
				}
				downloads++
				if r.URL.Path != "/open-apis/im/v1/messages/file-msg/resources/file-key" || r.URL.Query().Get("type") != "file" {
					t.Error("lost file identity/type", r.URL)
				}
				if mode == "error" {
					w.WriteHeader(http.StatusBadRequest)
					io.WriteString(w, `{"code":999,"msg":"secret=private-error"}`)
					return true
				}
				switch mode {
				case "json":
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, `["synthetic document"]`)
				case "large":
					w.Header().Set("Content-Type", "application/octet-stream")
					w.Header().Set("Content-Length", fmt.Sprint(maxAttachmentFileBytes+1))
					w.WriteHeader(http.StatusOK)
				default:
					w.Header().Set("Content-Type", "application/octet-stream")
					io.WriteString(w, "synthetic document")
				}
				return true
			}
			n := groupMessage("file-msg", "user", "chat", `<file key="file-key" name="/mac stop.txt"/>`, true)
			n.RawContentType = "file"
			n.Resources = []channel.Resource{{Type: "file", FileKey: "file-key", FileName: "../../report.txt"}}
			if mode == "missing" {
				n.Resources = nil
			}
			m := Normalize(n)
			if mode != "missing" && (len(m.Files) != 1 || m.Files[0].MessageID != n.MessageID || m.Files[0].Name != "../../report.txt" || strings.Contains(routeText(m), "/mac")) {
				t.Fatal("file source or routing boundary lost")
			}
			receiveAccess(t, ch, n)
			tick(t, e)
			task := onlyTask(t, e)
			if mode == "ok" || mode == "json" {
				if downloads != 1 || x.sends != 1 || x.uploads != 1 || !strings.Contains(x.request, "不得自动执行附件") || task.DispatchSource.Files[0].Key != "file-key" {
					t.Fatal("file was dropped", downloads, x.sends)
				}
			} else if x.sends != 0 || (task.Phase != "blocked" && task.Phase != "clarify") || strings.Contains(task.Summary, "private-error") {
				t.Fatal("failed download dispatched or leaked", task.Phase, task.Summary)
			}
			if mode == "missing" && downloads != 0 {
				t.Fatal("missing resource downloaded")
			}
			if mode == "large" && !strings.Contains(task.Summary, "50 MiB") {
				t.Fatal("large attachment did not report its limit", task.Summary)
			}
		})
	}
}

func TestP1aMixedNativeUploadAndUnknownSendFence(t *testing.T) {
	e, l, ch, x, f := setupAccess(t)
	target := e.Config.Routes["nas"]
	var picture bytes.Buffer
	png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	fileContents := strings.Repeat("x", 12<<20)
	downloads, posts, prepares, launches, reads := 0, 0, 0, 0, 0
	f.extra = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.Contains(r.URL.Path, "/resources/") {
			downloads++
			kind, key := "file", "file-key"
			if strings.HasSuffix(r.URL.Path, "/image-key") {
				kind, key = "image", "image-key"
			}
			if r.Method != http.MethodGet || r.URL.Path != "/open-apis/im/v1/messages/mixed/resources/"+key || r.URL.Query().Get("type") != kind {
				t.Error("wrong SDK download request", r.Method, r.URL)
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			if kind == "image" {
				w.Write(picture.Bytes())
			} else {
				w.Header().Set("Content-Length", fmt.Sprint(len(fileContents)))
				io.WriteString(w, fileContents)
			}
			return true
		}
		if r.URL.Path == "/upload" {
			posts++
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer synthetic-mcp" {
				t.Error("wrong upload method or authorization")
			}
			wantBytes := picture.Len()
			if posts == 2 {
				wantBytes = len(fileContents)
			}
			if n, err := io.Copy(io.Discard, r.Body); err != nil || n != int64(wantBytes) {
				t.Errorf("upload read %d bytes, want %d: %v", n, wantBytes, err)
			}
			w.WriteHeader(204)
			return true
		}
		return false
	}
	token := filepath.Join(t.TempDir(), "mcp.secret")
	if err := os.WriteFile(token, []byte("synthetic-mcp"), 0600); err != nil {
		t.Fatal(err)
	}
	api := &T3{Config: MCPConfig{Endpoint: f.baseURL + "/mcp", EnvironmentID: target.EnvironmentID, TokenFile: token}}
	var launchedMessage string
	api.Calls = scriptedCaller(func(name string, a map[string]any) (any, error) {
		switch name {
		case "t3_attachment_prepare_upload":
			prepares++
			u := a["upload"].(map[string]any)
			wantKind, wantName, wantMIME := "image", "image.png", "image/png"
			wantBytes := picture.Len()
			if prepares == 2 {
				wantKind, wantName, wantMIME = "file", "document.txt", "text/plain"
				wantBytes = len(fileContents)
			}
			if u["type"] != wantKind || u["name"] != wantName || u["mimeType"] != wantMIME || u["sizeBytes"] != float64(wantBytes) {
				t.Error("attachment metadata", u)
			}
			return map[string]any{"attachmentId": fmt.Sprint(prepares), "relativeUrl": "/upload"}, nil
		case "t3_thread_launch":
			launches++
			saved := onlyTask(t, e)
			attachments := a["attachments"].([]any)
			if saved.Phase != "dispatching" || saved.ThreadID != "" || saved.AttachmentLaunch == nil || saved.AttachmentLaunch.ExpectedThreadID != attachmentLaunchFixtureID ||
				saved.RequestID == "" || a["clientRequestId"] != saved.RequestID || a["message"] != saved.RequestMessage || len(saved.Attachments) != 2 || len(attachments) != 2 {
				t.Fatal("mixed launch missing durable request or attachment receipt", a)
			}
			for i, attachment := range saved.Attachments {
				if wire := attachments[i].(map[string]any); wire["id"] != fmt.Sprint(i+1) || wire["id"] != attachment.ID || wire["type"] != attachment.Type {
					t.Fatal("launch attachment identity changed", wire, attachment)
				}
			}
			launchedMessage = saved.RequestMessage
			return nil, errors.New("synthetic lost launch acknowledgement")
		case "t3_thread_read":
			reads++
			if a["threadId"] != attachmentLaunchFixtureID || a["view"] != "messages" {
				t.Fatal("recovery read the wrong thread or view", a)
			}
			page, item := attachmentLaunchPage(target, attachmentLaunchFixtureID)
			item["text"] = launchedMessage
			return page, nil
		case "t3_thread_configure":
			if a["threadId"] != attachmentLaunchFixtureID || a["environmentId"] != target.EnvironmentID || len(a) != 2 {
				t.Fatal("recovery did not use a read-only configuration check", a)
			}
			configuration := configurationFor(target)
			configuration["threadId"] = attachmentLaunchFixtureID
			return configuration, nil
		default:
			t.Fatalf("unexpected %s", name)
			return nil, nil
		}
	})
	e.T3 = &p1aNativeLaunchExecutor{p1aNativeExecutor{fakeExecutor: x, api: api}}
	n := groupMessage("mixed", "user", "chat", "/nas 只阅读这两份资料", true)
	n.Resources = []channel.Resource{{Type: "image", FileKey: "image-key", FileName: "image.png"}, {Type: "file", FileKey: "file-key", FileName: `..\document.txt`}}
	receiveAccess(t, ch, n)
	tick(t, e)
	task := onlyTask(t, e)
	if task.Phase != "needs_reconcile" || !task.NeedsReconcile || len(task.Attachments) != 2 ||
		downloads != 2 || posts != 2 || prepares != 2 || launches != 1 || reads != 0 || x.opens != 0 {
		t.Fatalf("mixed launch fence: phase=%s downloads=%d uploads=%d prepares=%d launches=%d reads=%d opens=%d", task.Phase, downloads, posts, prepares, launches, reads, x.opens)
	}
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e.Store = s
	l.Store = s
	x.observation = Observation{Status: "running", FoundRequest: true, RunID: "run"}
	tick(t, e)
	tick(t, e)
	task = onlyTask(t, e)
	if task.Phase != "tracking" || task.NeedsReconcile || task.ThreadID != attachmentLaunchFixtureID || task.RunID != "run" {
		t.Fatalf("original attachment run not recovered after restart: %+v", task)
	}
	if downloads != 2 || posts != 2 || prepares != 2 || launches != 1 || reads != 1 || x.opens != 0 {
		t.Fatalf("attachment launch replayed after restart: downloads=%d uploads=%d prepares=%d launches=%d reads=%d opens=%d", downloads, posts, prepares, launches, reads, x.opens)
	}
	// Cross-environment and invalid bytes must fail before preparation/HTTP.
	for _, tc := range []struct {
		target Target
		data   []byte
		kind   string
	}{
		{e.Config.Routes["mac"], []byte("x"), "file"}, {e.Config.Routes["nas"], nil, "file"}, {e.Config.Routes["nas"], make([]byte, maxAttachmentFileBytes+1), "file"}, {e.Config.Routes["nas"], []byte("not an image"), "image"},
	} {
		if _, err := api.Upload(context.Background(), tc.target, Image{Kind: tc.kind}, tc.data); err == nil {
			t.Fatal("invalid/cross-env upload accepted")
		}
	}
	if prepares != 2 || posts != 2 {
		t.Fatal("bad input reached preparation or upload")
	}
}

type p1aNativeExecutor struct {
	*fakeExecutor
	api *T3
}

func (x *p1aNativeExecutor) Upload(c context.Context, t Target, i Image, b []byte) (Attachment, error) {
	return x.api.Upload(c, t, i, b)
}
func (x *p1aNativeExecutor) Discard(c context.Context, t Target, id string) error {
	return x.api.Discard(c, t, id)
}
func (x *p1aNativeExecutor) Send(c context.Context, t Target, task *Task) (Execution, error) {
	return x.api.Send(c, t, task)
}

type p1aNativeLaunchExecutor struct{ p1aNativeExecutor }

func (*p1aNativeLaunchExecutor) attachmentLaunchThreadID(string) string {
	return attachmentLaunchFixtureID
}
func (x *p1aNativeLaunchExecutor) launchWithAttachments(c context.Context, t Target, task *Task) (Execution, error) {
	return x.api.launchWithAttachments(c, t, task)
}
func (x *p1aNativeLaunchExecutor) recoverAttachmentLaunch(c context.Context, t Target, task *Task) (Execution, error) {
	return x.api.recoverAttachmentLaunch(c, t, task)
}

func TestP1aReplyRoundSDKAndOldCardStop(t *testing.T) {
	e, _, ch, x, f := setupAccess(t)
	cards := map[string]string{}
	cardBodies := map[string]string{}
	sources := map[string]string{}
	patches := []string{}
	f.extra = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/reply") {
			var b map[string]any
			json.NewDecoder(r.Body).Decode(&b)
			id := b["uuid"].(string)
			if cards[id] == "" {
				cards[id] = fmt.Sprintf("card-%d", len(cards)+1)
			}
			cardBodies[cards[id]] = b["content"].(string)
			sources[cards[id]] = r.URL.Path
			fmt.Fprintf(w, `{"code":0,"data":{"message_id":%q}}`, cards[id])
			return true
		}
		if r.Method == "PATCH" {
			var b map[string]string
			json.NewDecoder(r.Body).Decode(&b)
			id := strings.TrimPrefix(r.URL.Path, "/open-apis/im/v1/messages/")
			cardBodies[id] = b["content"]
			patches = append(patches, id)
			io.WriteString(w, `{"code":0,"data":{}}`)
			return true
		}
		return false
	}
	receiveAccess(t, ch, groupMessage("one", "user", "chat", "/nas first", true))
	tick(t, e)
	first := onlyTask(t, e)
	x.observation.Status = "completed"
	x.observation.Summary = "first final"
	tick(t, e)
	oldBody := cardBodies[first.CardID]
	receiveAccess(t, ch, groupMessage("two", "user", "chat", "继续解释", true))
	tick(t, e)
	second := onlyTask(t, e)
	if second.CardID == first.CardID || second.ThreadID != first.ThreadID || cardBodies[first.CardID] != oldBody || sources[second.CardID] != "/open-apis/im/v1/messages/two/reply" {
		t.Fatal("new ordinary round did not preserve old card")
	}
	action := &channel.CardActionEvent{EventID: "stale", MessageID: first.CardID, ChatID: "chat"}
	action.Operator.OpenID = "user"
	action.Action.Value = map[string]any{"action": "stop", "taskId": first.ID, "requestId": first.CardRequestID}
	ch.action(context.Background(), action)
	tick(t, e)
	if onlyTask(t, e).StopRequested || x.stops != 0 {
		t.Fatal("old stop button stopped new run")
	}
	x.observation.Status = "completed"
	x.observation.Summary = "second final"
	tick(t, e)
	// Explicit old-card reference keeps the context and patches precisely that old card.
	m := message("three", "继续第一轮")
	m.ChatType = "group"
	m.ParentID = first.CardID
	accept(t, e, m)
	tick(t, e)
	third := onlyTask(t, e)
	if third.CardID != first.CardID || third.ThreadID != first.ThreadID || x.opens != 1 || patches[len(patches)-1] != first.CardID {
		t.Fatal("explicit old card lost thread/card")
	}
	// Same physical card, old request button must still be rejected.
	action.EventID = "stale-same-card"
	ch.action(context.Background(), action)
	if onlyTask(t, e).StopRequested {
		t.Fatal("stale round callback accepted on reused card")
	}
	action.EventID = "other-user"
	action.Operator.OpenID = "other"
	action.Action.Value["requestId"] = third.CardRequestID
	ch.action(context.Background(), action)
	if onlyTask(t, e).StopRequested {
		t.Fatal("other user borrowed stop")
	}
	action.EventID = "fresh"
	action.Operator.OpenID = "user"
	ch.action(context.Background(), action)
	tick(t, e)
	if x.stops != 1 {
		t.Fatal("current callback no longer stops owned run")
	}
}

func TestP1aLegacyUnknownCardKeepsUUID(t *testing.T) {
	e, _, l, _ := testEngine(t)
	accept(t, e, message("one", "/nas first"))
	tick(t, e)
	task := onlyTask(t, e)
	e.Store.update(func(st *State) error {
		v := st.Tasks[task.ID]
		v.ReplySource = nil
		v.CardRequestID = ""
		v.CardID = ""
		v.CardAttemptAt = time.Now().UTC().Format(time.RFC3339Nano)
		v.Phase = "completed"
		v.NotifyHash = ""
		return nil
	})
	l.fail = true
	accept(t, e, message("two", "继续"))
	tick(t, e)
	pending := onlyTask(t, e)
	if pending.RequestID != task.RequestID || cardUUID(pending) != stable(task.ID, "card") || len(e.Store.snapshot().Inbox) != 1 {
		t.Fatal("unknown legacy card was replaced")
	}
	l.fail = false
	tick(t, e)
	recovered := onlyTask(t, e)
	if recovered.CardRequestID != "" || recovered.CardID == "" || recovered.NotifyHash != "" {
		t.Fatal("legacy attempt lost identity")
	}
	tick(t, e)
	confirmed := onlyTask(t, e)
	if confirmed.CardID != recovered.CardID || confirmed.NotifyHash != notificationHash(confirmed) ||
		confirmed.RequestID != recovered.RequestID || len(e.Store.snapshot().Inbox) != 1 {
		t.Fatal("recovering the ID was treated as acknowledging terminal delivery")
	}
	tick(t, e)
	next := onlyTask(t, e)
	if next.CardID == recovered.CardID || next.ThreadID != task.ThreadID || len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("new ordinary turn didn't wait for legacy resolution")
	}
}

func TestP1aFullResultPagesPrivacyAndRetry(t *testing.T) {
	e, _, ch, x, f := setupAccess(t)
	receiveAccess(t, ch, groupMessage("one", "user", "chat", "/nas 给出长回答", true))
	tick(t, e)
	full := strings.Repeat("正文内容", 1200) + "\nsecret=synthetic-private\n末尾完整标记"
	x.observation.Status = "completed"
	x.observation.Summary = full
	tick(t, e)
	task := onlyTask(t, e)
	if !strings.HasSuffix(task.Result, "末尾完整标记") || strings.Contains(task.Result, "synthetic-private") || len([]rune(task.Summary)) > 1900 || !strings.Contains(card(task), "/result "+shortTaskID(task.RequestID)) {
		t.Fatal("long answer silently truncated or unredacted")
	}
	pages := len(splitResultPages(task.Result))
	var all strings.Builder
	for i := 1; i <= pages; i++ {
		id := fmt.Sprintf("page-%d", i)
		accessCommand(t, e, ch, id, "user", "chat", fmt.Sprintf("/result %s %d", shortTaskID(task.RequestID), i))
		elements := card2Elements(t, f.replies[len(f.replies)-1])
		answer := elements[1].(map[string]any)
		body := answer["content"].(string)
		if answer["tag"] != "markdown" || len([]rune(body)) > 1400 {
			t.Fatal("page incomplete/bounds", answer)
		}
		all.WriteString(body)
	}
	if all.String() != task.Result {
		t.Fatal("paged result differs from full sanitized result")
	}
	for _, m := range []Message{
		{ID: "other-user", UserID: "other", ChatID: "chat", TopicID: "topic", Text: "/result " + shortTaskID(task.RequestID)},
		{ID: "other-chat", UserID: "user", ChatID: "unknown", TopicID: "topic", Text: "/result " + shortTaskID(task.RequestID)},
		{ID: "other-topic", UserID: "user", ChatID: "chat", TopicID: "other", Text: "/result " + shortTaskID(task.RequestID)},
	} {
		st := e.Store.snapshot()
		if reply, isCard := e.resultPage(&st, m, nil); isCard || strings.Contains(reply, "正文内容") {
			t.Fatal("full result leaked scope")
		}
	}
	// Transport failure persists the exact page and fixed UUID, then clears the issue on retry.
	f.replyFail = true
	accessCommand(t, e, ch, "failed-page", "user", "chat", "/result "+shortTaskID(task.RequestID))
	st := e.Store.snapshot()
	cmd := st.Commands[key(message("failed-page", ""), "failed-page")]
	if cmd == nil || !cmd.ReplyCard || !strings.Contains(cmd.Issue, "尚未交付") || len(st.Inbox) != 1 {
		t.Fatal("result delivery falsely acknowledged")
	}
	uuid := f.replyUUIDs[len(f.replyUUIDs)-1]
	reply := cmd.Reply
	status := e.sessionStatus(&st, message("status", "/status"), task, "nas")
	if !strings.Contains(status, "全文页发送未确认") {
		t.Fatal("missing delivery issue in status")
	}
	f.replyFail = false
	tick(t, e)
	cmd = e.Store.snapshot().Commands[key(message("failed-page", ""), "failed-page")]
	if cmd.Issue != "" || !cmd.ReplyCard || cmd.Reply != reply || len(e.Store.snapshot().Inbox) != 0 || f.replyUUIDs[len(f.replyUUIDs)-1] != uuid {
		t.Fatal("page retry changed identity/body")
	}
	// Older round remains readable by its original request token after an ordinary continuation.
	receiveAccess(t, ch, groupMessage("two", "user", "chat", "继续", true))
	tick(t, e)
	st = e.Store.snapshot()
	if reply, isCard := e.resultPage(&st, message("old-result", "/result "+shortTaskID(task.RequestID)), onlyTask(t, e)); !isCard || !strings.Contains(reply, "正文内容") {
		t.Fatal("prior round result lost")
	}
}

func TestP1aResultLimitAndLegacy(t *testing.T) {
	e, _, _, _ := testEngine(t)
	accept(t, e, message("one", "/nas first"))
	tick(t, e)
	task := onlyTask(t, e)
	finishResult(task, strings.Repeat("x", maxResultBytes+1), "")
	if task.Result != "" || task.ResultIssue == "" {
		t.Fatal("oversized full answer falsely saved")
	}
	st := e.Store.snapshot()
	st.Tasks[task.ID] = task
	if reply, isCard := e.resultPage(&st, message("result", "/result"), task); isCard || !strings.Contains(reply, "全文未交付") {
		t.Fatal("limit issue hidden")
	}
	task.ReplySource = nil
	task.Result = ""
	task.ResultIssue = ""
	task.Summary = "旧版完整结论"
	task.Phase = "completed"
	if currentReply(task).Result != "旧版完整结论" {
		t.Fatal("legacy stored answer unavailable")
	}
}

func TestP1aVisibleProgressAndSettlement(t *testing.T) {
	target := testConfig(t).Routes["nas"]
	for _, tc := range []struct {
		name, runID, active, visibility, kind, text string
		want                                        bool
	}{
		{"known-run", "run", "run", "local", "message", "正在核对磁盘健康与容量", true},
		{"null-run", "", "run", "local", "message", "已读取资料，正在核对", true},
		{"unknown-null-run", "", "other", "local", "message", "不属于本次执行", false},
		{"other-run", "other", "run", "local", "message", "其它任务", false},
		{"reasoning", "run", "run", "local", "reasoning", "内部推理", false},
		{"tool", "run", "run", "local", "tool_result", "命令参数", false},
		{"inherited", "run", "run", "inherited", "message", "其它上下文", false},
		{"secret", "run", "run", "local", "message", "token=synthetic-private", false},
		{"command", "run", "run", "local", "message", "sudo /bin/private --secret", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &T3{Calls: scriptedCaller(func(name string, a map[string]any) (any, error) {
				if name == "t3_thread_wait" {
					return map[string]any{"threadId": "thread", "timedOut": true}, nil
				}
				if a["view"] != "messages" {
					t.Fatal("activity requested")
				}
				p := pageFor(target)
				p["thread"].(map[string]any)["activeRunId"] = tc.active
				p["recentRuns"].([]any)[0].(map[string]any)["status"] = "running"
				p["items"] = []any{map[string]any{"position": 0, "createdBy": "user", "runId": "run", "text": "original request"}, map[string]any{"position": 1, "createdBy": "agent", "runId": tc.runID, "visibility": tc.visibility, "type": tc.kind, "text": tc.text}}
				return p, nil
			})}
			o, err := api.Observe(context.Background(), target, &Task{ThreadID: "thread", RunID: "run", RequestMessage: "original request"})
			if err != nil || !o.Unsettled || len(o.Tasks) != 0 || (o.Progress != "") != tc.want {
				t.Fatal("visible progress or fake child regression", o, err)
			}
			if tc.want && o.Progress != tc.text {
				t.Fatal("safe public progress hidden")
			}
		})
	}
	e, x, _, _ := testEngine(t)
	accept(t, e, message("one", "/nas first"))
	tick(t, e)
	for _, tc := range []struct {
		o               Observation
		phase, contains string
	}{
		{Observation{Status: "running", Progress: "正在核对磁盘健康"}, "tracking", "正在核对磁盘健康"},
		{Observation{Status: "completed", Unsettled: true}, "settling", "仍在核对异步工作和原轮次的最终结论"},
		{Observation{Status: "running", Tasks: []string{"actual-child"}}, "awaiting_children", "真实异步工作"},
		{Observation{Status: "running", PendingApprovals: 1}, "tracking", "审批或问题"},
		{Observation{Status: "completed", Summary: "唯一最终结论"}, "completed", "唯一最终结论"},
	} {
		x.observation = tc.o
		tick(t, e)
		task := onlyTask(t, e)
		if task.Phase != tc.phase || !strings.Contains(task.Summary, tc.contains) {
			t.Fatal("incorrect stage", task.Phase, task.Summary)
		}
	}
	if onlyTask(t, e).Result != "唯一最终结论" || strings.Contains(onlyTask(t, e).Summary, "正在核对") {
		t.Fatal("final answer concatenated progress")
	}
}

func TestP1aStatusControlIssuesAndResumePreview(t *testing.T) {
	e, _, l, _ := testEngine(t)
	accept(t, e, message("one", "/nas 检查合成问题 secret=private"))
	tick(t, e)
	st := e.Store.snapshot()
	task := onlyTask(t, e)
	pending := message("pending", "/new chat test")
	st.Inbox = append(st.Inbox, pending)
	st.Commands = map[string]*SessionCommand{}
	st.Commands[key(pending, pending.ID)] = &SessionCommand{Source: pending, Kind: "new_chat", Issue: "确认待核对"}
	other := pending
	other.UserID = "other"
	st.Commands["other"] = &SessionCommand{Source: other, Kind: "new_chat", Issue: "other-private-issue"}
	status := e.sessionStatus(&st, message("status", "/status"), task, "nas")
	if !strings.Contains(status, "建群") || !strings.Contains(status, "确认待核对") || strings.Contains(status, "other-private") {
		t.Fatal("status pending privacy", status)
	}
	accept(t, e, message("resume", "/resume"))
	tick(t, e)
	preview := l.replies[len(l.replies)-1]
	if !strings.Contains(preview, "检查合成问题") || strings.Contains(preview, "secret=private") {
		t.Fatal("resume lacks safe preview", preview)
	}
}

func TestP1aAttachmentLimitAndUploadFailure(t *testing.T) {
	t.Run("combined-count", func(t *testing.T) {
		e, _, ch, x, _ := setupAccess(t)
		n := groupMessage("too-many", "user", "chat", "/nas read", true)
		for i := 0; i <= maxTurnAttachments; i++ {
			kind := "image"
			if i%2 == 1 {
				kind = "file"
			}
			n.Resources = append(n.Resources, channel.Resource{Type: kind, FileKey: fmt.Sprint(i)})
		}
		receiveAccess(t, ch, n)
		tick(t, e)
		if x.sends != 0 || x.uploads != 0 || !strings.Contains(onlyTask(t, e).Summary, "最多10项") {
			t.Fatal("mixed count not enforced")
		}
	})
	for _, mode := range []string{"prepare", "upload"} {
		t.Run(mode, func(t *testing.T) {
			e, _, ch, x, f := setupAccess(t)
			f.extra = func(w http.ResponseWriter, r *http.Request) bool {
				if strings.Contains(r.URL.Path, "/resources/") {
					w.Header().Set("Content-Type", "application/octet-stream")
					io.WriteString(w, "synthetic text")
					return true
				}
				if r.URL.Path == "/upload" {
					w.WriteHeader(500)
					io.WriteString(w, "secret=private-response")
					return true
				}
				return false
			}
			token := filepath.Join(t.TempDir(), "mcp.secret")
			os.WriteFile(token, []byte("synthetic"), 0600)
			api := &T3{Config: MCPConfig{Endpoint: f.baseURL + "/mcp", EnvironmentID: e.Config.Routes["nas"].EnvironmentID, TokenFile: token}, Calls: scriptedCaller(func(name string, a map[string]any) (any, error) {
				if name == "t3_attachment_discard" {
					return nil, nil
				}
				if name != "t3_attachment_prepare_upload" {
					t.Fatal("failure dispatched", name)
				}
				if mode == "prepare" {
					return nil, errors.New("secret=private-error")
				}
				return map[string]string{"attachmentId": "id", "relativeUrl": "/upload"}, nil
			})}
			e.T3 = &p1aNativeExecutor{fakeExecutor: x, api: api}
			n := groupMessage("file", "user", "chat", "/nas 只阅读", true)
			n.Resources = []channel.Resource{{Type: "file", FileKey: "file-key", FileName: "file.txt"}}
			receiveAccess(t, ch, n)
			tick(t, e)
			task := onlyTask(t, e)
			if task.Phase != "blocked" || len(task.Attachments) != 0 || strings.Contains(task.Summary, "private") {
				t.Fatal("upload failure hidden or leaked", task.Phase, task.Summary)
			}
		})
	}
}

func TestP1aFinalTextReadBoundsAndLatestAnswer(t *testing.T) {
	target := testConfig(t).Routes["nas"]
	for _, latestShort := range []bool{false, true} {
		t.Run(fmt.Sprint(latestShort), func(t *testing.T) {
			api := &T3{Calls: scriptedCaller(func(name string, a map[string]any) (any, error) {
				if name == "t3_thread_wait" {
					return map[string]any{"threadId": "thread", "resultRun": map[string]string{"runId": "run", "status": "completed"}}, nil
				}
				p := pageFor(target)
				p["items"] = []any{map[string]any{"position": 1, "createdBy": "agent", "runId": "run", "text": strings.Repeat("x", 20000), "textTruncated": true}}
				if latestShort {
					p["items"] = append(p["items"].([]any), map[string]any{"position": 2, "createdBy": "agent", "runId": "run", "text": "最终短结论"})
				}
				return p, nil
			})}
			o, err := api.Observe(context.Background(), target, &Task{ThreadID: "thread", RunID: "run"})
			if err != nil || (o.ResultIssue == "") != latestShort {
				t.Fatal("truncated full answer or earlier progress poisoned final", o.ResultIssue, err)
			}
			if latestShort && o.Summary != "最终短结论" {
				t.Fatal("not latest answer")
			}
		})
	}
}

func TestP1aPartialUploadCleanupOnlyBeforeSend(t *testing.T) {
	for _, mode := range []string{"download-second", "prepare-second", "upload-second", "discard-failed"} {
		t.Run(mode, func(t *testing.T) {
			e, _, ch, x, f := setupAccess(t)
			uploads, prepares := 0, 0
			discarded := []string{}
			f.extra = func(w http.ResponseWriter, r *http.Request) bool {
				if strings.Contains(r.URL.Path, "/resources/") {
					if mode == "download-second" && strings.HasSuffix(r.URL.Path, "/second") {
						w.WriteHeader(400)
						io.WriteString(w, `{"code":999}`)
						return true
					}
					w.Header().Set("Content-Type", "application/octet-stream")
					io.WriteString(w, "synthetic")
					return true
				}
				if r.URL.Path == "/upload" {
					uploads++
					if uploads == 2 {
						w.WriteHeader(500)
					} else {
						w.WriteHeader(204)
					}
					return true
				}
				return false
			}
			token := filepath.Join(t.TempDir(), "mcp.secret")
			os.WriteFile(token, []byte("synthetic"), 0600)
			api := &T3{Config: MCPConfig{Endpoint: f.baseURL + "/mcp", EnvironmentID: e.Config.Routes["nas"].EnvironmentID, TokenFile: token}, Calls: scriptedCaller(func(name string, a map[string]any) (any, error) {
				switch name {
				case "t3_attachment_prepare_upload":
					prepares++
					if prepares == 2 && mode == "prepare-second" {
						return nil, errors.New("prepare failed")
					}
					return map[string]string{"attachmentId": fmt.Sprintf("pending-%d", prepares), "relativeUrl": "/upload"}, nil
				case "t3_attachment_discard":
					id := a["attachmentId"].(string)
					discarded = append(discarded, id)
					st := e.Store.snapshot()
					for _, task := range st.Tasks {
						if task.Phase != "blocked" || len(task.Attachments) == 0 {
							t.Fatal("pending cleanup identities not durable before discard")
						}
					}
					if mode == "discard-failed" && id == "pending-2" {
						return nil, errors.New("secret=private-discard-error")
					}
					return nil, nil
				default:
					t.Fatal("partial failure reached Send", name)
					return nil, nil
				}
			})}
			e.T3 = &p1aNativeExecutor{fakeExecutor: x, api: api}
			n := groupMessage("mixed-failure", "user", "chat", "/nas 阅读资料", true)
			n.Resources = []channel.Resource{{Type: "file", FileKey: "first", FileName: "a.txt"}, {Type: "file", FileKey: "second", FileName: "b.txt"}}
			receiveAccess(t, ch, n)
			tick(t, e)
			task := onlyTask(t, e)
			want := 2
			if mode == "download-second" || mode == "prepare-second" {
				want = 1
			}
			if task.Phase != "blocked" || len(discarded) != want {
				t.Fatal("unsent attachments leaked", task.Phase, discarded)
			}
			if mode == "discard-failed" {
				if len(task.Attachments) != 1 || task.Attachments[0].ID != "pending-2" || !strings.Contains(task.Summary, "清理未确认") || strings.Contains(task.Summary, "private-discard-error") {
					t.Fatal("cleanup failure identity/issue lost", task)
				}
			} else if len(task.Attachments) != 0 {
				t.Fatal("discarded IDs still pending")
			}
			tick(t, e)
			if len(discarded) != want {
				t.Fatal("cleanup retried automatically")
			}
		})
	}
}
