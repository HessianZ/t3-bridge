package bridge

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	im "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type replyModeRequest struct {
	MsgType       string `json:"msg_type"`
	Content       string `json:"content"`
	UUID          string `json:"uuid"`
	ReplyInThread bool   `json:"reply_in_thread"`
}

func captureReplyModes(t *testing.T, f *accessFixture) map[string][]replyModeRequest {
	t.Helper()
	requests := map[string][]replyModeRequest{}
	f.extra = func(w http.ResponseWriter, r *http.Request) bool {
		message, ok := strings.CutPrefix(r.URL.Path, "/open-apis/im/v1/messages/")
		if !ok || (r.Method != http.MethodPatch && !strings.HasSuffix(message, "/reply")) {
			return false
		}
		var body replyModeRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return true
		}
		message = strings.TrimSuffix(message, "/reply")
		requests[message] = append(requests[message], body)
		if f.replyFail {
			io.WriteString(w, `{"code":999,"msg":"synthetic reply failure"}`)
			return true
		}
		fmt.Fprintf(w, `{"code":0,"data":{"message_id":%q}}`, "reply-"+body.UUID)
		return true
	}
	return requests
}

func TestTextReplyTerminalAndReference(t *testing.T) {
	e, _, ch, x, f := setupAccess(t)
	requests := captureReplyModes(t, f)
	accessCommand(t, e, ch, "default", "user", "chat", "/config reply")
	accessCommand(t, e, ch, "denied", "other", "chat", "/config reply text")
	if e.Store.runSettings().ReplyMode != "" || !strings.Contains(requests["default"][0].Content, "card") ||
		!strings.Contains(requests["denied"][0].Content, "owner/admin") || x.sends != 0 {
		t.Fatal("default reply mode or management authorization changed")
	}
	accessCommand(t, e, ch, "set-text", "user", "chat", "/config reply text")
	accessCommand(t, e, ch, "invalid", "admin", "chat", "/config reply invalid")
	receiveAccess(t, ch, groupMessage("one", "user", "chat", "/nas 看执行结果", true))
	tick(t, e)
	if task := onlyTask(t, e); !task.TextReply || task.CardAttemptAt != "" || x.sends != 1 {
		t.Fatal("first round did not persist text mode without an IM send attempt")
	}
	accessCommand(t, e, ch, "input", "user", "chat", "/input")
	if onlyTask(t, e).InputOpen || !strings.Contains(requests["input"][0].Content, "直接发送补充消息") {
		t.Fatal("text mode opened a card form")
	}
	x.observation.PendingApprovals = 1
	accessCommand(t, e, ch, "set-card", "admin", "chat", "/config reply card")
	x.observation = Observation{Status: "completed", Tasks: []string{"async-child"}}
	tick(t, e)
	if onlyTask(t, e).Phase != "awaiting_children" || len(requests["one"]) != 0 {
		t.Fatal("nonterminal approval or asynchronous work produced a reply")
	}
	x.observation = Observation{Status: "completed", Unsettled: true}
	tick(t, e)
	if onlyTask(t, e).Phase != "settling" || len(requests["one"]) != 0 {
		t.Fatal("unsettled run produced a reply")
	}
	x.observation = Observation{Status: "completed", Summary: "可见最终结果\npassword=synthetic-sensitive"}
	tick(t, e)
	tick(t, e)
	first := onlyTask(t, e)
	sent := requests["one"]
	if len(sent) != 1 || sent[0].MsgType != "text" || !sent[0].ReplyInThread || sent[0].UUID != cardUUID(first) ||
		!strings.Contains(sent[0].Content, "completed") || !strings.Contains(sent[0].Content, "可见最终结果") ||
		!strings.Contains(sent[0].Content, "/result") || strings.Contains(sent[0].Content, "synthetic-sensitive") {
		t.Fatalf("unexpected terminal text transport: %+v", sent)
	}

	// The group requires @ for new messages. A reply to the owned text receipt
	// remains a scoped quote and the next turn creates a new reply.
	follow := groupMessage("two", "user", "chat", "继续核对", false)
	follow.RawEvent.(*im.P2MessageReceiveV1).Event.Message.ParentId = &first.CardID
	receiveAccess(t, ch, follow)
	tick(t, e)
	second := onlyTask(t, e)
	if second.TextReply || second.CardID == first.CardID || second.ThreadID != first.ThreadID || x.opens != 1 ||
		len(requests["two"]) != 1 || requests["two"][0].MsgType != "interactive" || len(requests[first.CardID]) != 0 {
		t.Fatal("quoting text lost the session or patched the text reply")
	}
	quote := groupMessage("result", "user", "chat", "/result", false)
	quote.RawEvent.(*im.P2MessageReceiveV1).Event.Message.ParentId = &first.CardID
	receiveAccess(t, ch, quote)
	tick(t, e)
	if result := requests["result"]; len(result) != 1 || !strings.Contains(result[0].Content, "可见最终结果") ||
		strings.Contains(result[0].Content, "synthetic-sensitive") {
		t.Fatal("historical text reply did not select its own redacted full result")
	}

	accessCommand(t, e, ch, "text-again", "user", "chat", "/config reply text")
	patches := len(requests[second.CardID])
	cardQuote := groupMessage("three", "user", "chat", "续接原卡", false)
	cardQuote.RawEvent.(*im.P2MessageReceiveV1).Event.Message.ParentId = &second.CardID
	receiveAccess(t, ch, cardQuote)
	tick(t, e)
	if current := onlyTask(t, e); current.TextReply || current.CardID != second.CardID || len(requests["three"]) != 0 ||
		len(requests[second.CardID]) != patches+1 {
		t.Fatal("explicit card continuation changed the existing card into a new text reply")
	}
}

func TestMarkdownReplyTerminalAndReference(t *testing.T) {
	e, _, ch, x, f := setupAccess(t)
	requests := captureReplyModes(t, f)
	accessCommand(t, e, ch, "markdown", "user", "chat", "/config reply markdown")
	accessCommand(t, e, ch, "stream", "admin", "chat", "/config process stream")
	wallClock := e.Now
	now := time.Date(2026, 11, 19, 3, 0, 0, 0, time.UTC)
	e.Now = func() time.Time { return now }
	x.observation = Observation{RunID: "run", Status: "running", StreamPreview: "过程正文"}
	accept(t, e, Normalize(groupMessage("one", "user", "chat", "/nas 查看结果", true)))
	tick(t, e)
	running := onlyTask(t, e)
	if !running.TextReply || !running.MarkdownReply || running.CardAttemptAt != "" || running.Process == nil || running.Process.MessageID == "" ||
		len(requests["one"]) != 1 || requests["one"][0].MsgType != "interactive" {
		t.Fatal("Markdown round did not use only the independent process card during execution")
	}
	accessCommand(t, e, ch, "card", "admin", "chat", "/config reply card")
	accessCommand(t, e, ch, "process-off", "admin", "chat", "/config process off")
	now = now.Add(processStreamInterval)
	tick(t, e)
	processID := running.Process.MessageID
	if updates := requests[processID]; len(updates) != 1 || !strings.Contains(updates[0].Content, "过程正文") {
		t.Fatalf("independent process card did not show the stage preview: %+v", updates)
	}
	const markdown = "# 结果\n\n- **通过**\n\n```go\nfmt.Println(1)\n```"
	x.observation = Observation{RunID: "run", Status: "completed", Summary: markdown +
		"\n\n<at user_id=all></at> <AT user_id=\"ou_synthetic\">示例</AT>\n\npassword=synthetic-sensitive"}
	tick(t, e)
	tick(t, e)
	first := onlyTask(t, e)
	sent := requests["one"]
	if len(sent) != 2 || sent[1].MsgType != "post" || !sent[1].ReplyInThread || sent[1].UUID != cardUUID(first) ||
		!first.Process.Closed || first.CardID == "" || first.CardID == processID || first.TextReplyBody != "" {
		t.Fatalf("unexpected terminal post receipt: %+v", sent)
	}
	var post map[string]struct {
		Content [][]map[string]string `json:"content"`
	}
	if err := json.Unmarshal([]byte(sent[1].Content), &post); err != nil {
		t.Fatal(err)
	}
	rows := post["zh_cn"].Content
	if len(post) != 1 || len(rows) != 1 || len(rows[0]) != 1 || rows[0][0]["tag"] != "md" {
		t.Fatalf("post does not use the Feishu Markdown element: %+v", post)
	}
	body := rows[0][0]["text"]
	if !strings.Contains(body, "\n\n"+markdown) || !strings.Contains(body, "＜at user_id=all>＜/at> ＜AT user_id=\"ou_synthetic\">示例＜/AT>") ||
		!strings.Contains(body, "[已脱敏]") || strings.Contains(body, "synthetic-sensitive") || !strings.Contains(body, "/result") {
		t.Fatalf("unexpected redacted Markdown: %q", body)
	}
	quote := groupMessage("two", "user", "chat", "继续核对", false)
	quote.RawEvent.(*im.P2MessageReceiveV1).Event.Message.ParentId = &first.CardID
	receiveAccess(t, ch, quote)
	e.Now = wallClock
	tick(t, e)
	second := onlyTask(t, e)
	if second.TextReply || second.MarkdownReply || second.ThreadID != first.ThreadID || second.CardID == first.CardID || x.opens != 1 || x.sends != 2 ||
		len(second.Replies) != 1 || !second.Replies[0].MarkdownReply || second.Replies[0].CardID != first.CardID ||
		len(requests[first.CardID]) != 0 || len(requests["two"]) != 1 || requests["two"][0].MsgType != "interactive" {
		t.Fatalf("quoted post: phase=%s text=%t markdown=%t sameThread=%t sameReceipt=%t opens=%d sends=%d history=%+v oldUpdates=%d newReplies=%+v",
			second.Phase, second.TextReply, second.MarkdownReply, second.ThreadID == first.ThreadID, second.CardID == first.CardID,
			x.opens, x.sends, second.Replies, len(requests[first.CardID]), requests["two"])
	}
}

func TestTextReplyRetryAfterRestart(t *testing.T) {
	testTerminalReplyRetryAfterRestart(t, "text", "text")
}

func TestMarkdownReplyRetryAfterRestart(t *testing.T) {
	testTerminalReplyRetryAfterRestart(t, "markdown", "post")
}

func testTerminalReplyRetryAfterRestart(t *testing.T, mode, msgType string) {
	t.Helper()
	e, l, ch, x, f := setupAccess(t)
	requests := captureReplyModes(t, f)
	accessCommand(t, e, ch, "set-mode", "user", "chat", "/config reply "+mode)
	accept(t, e, message("one", "/nas 开始"))
	tick(t, e)
	x.observation = Observation{Status: "completed", Summary: "最终结果"}
	f.replyFail = true
	tick(t, e)
	failed := onlyTask(t, e)
	if failed.CardAttemptAt == "" || failed.CardID != "" || failed.TextReplyBody == "" || failed.NotificationIssue == "" || len(requests["one"]) != 1 {
		t.Fatal("unconfirmed terminal reply lost its attempt fence")
	}
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	e.Store, l.Store = reopened, reopened
	if saved := onlyTask(t, e); !saved.TextReply || saved.MarkdownReply != (mode == "markdown") || e.Store.runSettings().ReplyMode != mode {
		t.Fatal("reply mode did not survive restart")
	}
	if err := e.Store.update(func(st *State) error {
		st.Tasks[failed.ID].Summary = "发送尝试后更新的状态"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	accept(t, e, message("two", "继续说明"))
	f.replyFail = false
	accessCommand(t, e, ch, "switch-card", "user", "chat", "/config reply card")
	replies := requests["one"]
	confirmed := onlyTask(t, e)
	if len(replies) != 2 || replies[0] != replies[1] || replies[0].MsgType != msgType ||
		x.sends != 1 || len(e.Store.snapshot().Inbox) != 1 || confirmed.CardAttemptAt != failed.CardAttemptAt || confirmed.TextReplyBody != "" {
		t.Fatal("retry changed the original reply or let the queued run replace it")
	}
	tick(t, e)
	if x.sends != 2 || len(requests["one"]) != 2 || len(requests["two"]) != 1 || requests["two"][0].MsgType != "interactive" {
		t.Fatal("queued continuation did not use the new setting after the retry was confirmed")
	}
}

func TestTextReplyCommandStopAndRetryLimit(t *testing.T) {
	e, x, l, _ := testEngine(t)
	if err := e.Store.update(func(st *State) error {
		st.Settings.ReplyMode = "text"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.PollInterval = time.Hour
	accept(t, e, message("one", "/nas 开始"))
	tick(t, e)
	if e.Store.hasImmediateWork(e.Config) || l.notifications != 0 {
		t.Fatal("tracking text round woke the worker just to send a reply")
	}
	stop := message("stop", "/stop")
	stop.ParentID = "one"
	accept(t, e, stop)
	l.fail = true
	tick(t, e)
	task := onlyTask(t, e)
	if x.stops != 1 || task.Phase != "stopped" || l.notifications != 1 || task.NotificationIssue == "" {
		t.Fatal("quoted /stop did not stop the owned text run and attempt the terminal reply")
	}
	if err := e.Store.update(func(st *State) error {
		st.Tasks[task.ID].CardAttemptAt = time.Now().Add(-56 * time.Minute).UTC().Format(time.RFC3339Nano)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	l.fail = false
	e.nextPoll = time.Time{}
	tick(t, e)
	if x.stops != 1 || l.notifications != 1 || !strings.Contains(onlyTask(t, e).NotificationIssue, "超过安全重试窗口") {
		t.Fatal("expired reply or stop was retried")
	}
}
