package bridge

import (
	"encoding/json"
	"errors"
	im "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSessionContinuousQueueAndRestart(t *testing.T) {
	e, x, l, _ := testEngine(t)
	accept(t, e, message("one", "/nas 列出工具"))
	tick(t, e)
	first := onlyTask(t, e)
	accept(t, e, message("two", "继续解释"))
	tick(t, e)
	accept(t, e, message("three", "不要执行命令，只告诉我当前可用工具"))
	tick(t, e)
	if len(e.Store.snapshot().Inbox) != 2 || x.sends != 1 || e.Store.snapshot().Tasks[first.ID].StopRequested {
		t.Fatal("active messages lost, executed early or restriction became stop")
	}
	for _, m := range e.Store.snapshot().Inbox {
		if m.SessionTask != first.ID {
			t.Fatal("queued message lost destination")
		}
	}
	e.Store.Close()
	s, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e.Store = s
	x.observation.Status = "completed"
	x.observation.Summary = "第一轮最终结论，必须保留"
	tick(t, e)
	oldCard := l.sentCards[first.CardID]
	x.observation.Summary = "后续轮次结论"
	tick(t, e)
	tick(t, e)
	tick(t, e)
	task := onlyTask(t, e)
	if !strings.Contains(oldCard, "第一轮最终结论") || l.sentCards[first.CardID] != oldCard || len(task.Replies) != 2 || task.Replies[0].Result != "第一轮最终结论，必须保留" {
		t.Fatal("queued ordinary turn overwrote prior answer/card or lost archived result")
	}

	if x.opens != 1 || x.sends != 3 || task.ThreadID != first.ThreadID || task.CardID == first.CardID || len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("continuous queue/restart lost thread or overwrote previous round card", x.sends, task.Phase)
	}
	link := "t3-thread://v2/" + task.Target.EnvironmentID + "/" + task.ThreadID
	if err := e.Store.update(func(st *State) error {
		st.Tasks[task.ID].Link = link
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	accept(t, e, message("status", "/status"))
	tick(t, e)
	if !strings.Contains(l.replies[len(l.replies)-1], "排队：0") || !strings.Contains(l.replies[len(l.replies)-1], "会话："+link) {
		t.Fatal("status missing current conversation")
	}
}

func TestSessionIsolationAndControlledRouteSwitch(t *testing.T) {
	e, x, _, _ := testEngine(t)
	e.Config.Topics = nil
	for _, m := range []Message{message("user-a", "/nas hi"), {ID: "user-b", UserID: "other", ChatID: "chat", TopicID: "topic", Text: "/nas hi"}, {ID: "topic-b", UserID: "user", ChatID: "chat", TopicID: "other-topic", Text: "/nas hi"}} {
		accept(t, e, m)
		tick(t, e)
	}
	if len(e.Store.snapshot().Tasks) != 3 || x.opens != 3 {
		t.Fatal("users/topics shared context")
	}
	x.observation.Status = "completed"
	tick(t, e)
	accept(t, e, message("switch", "/mac 查看工具"))
	tick(t, e)
	if len(e.Store.snapshot().Tasks) != 4 || x.opens != 4 {
		t.Fatal("explicit authorized other target did not create isolated context")
	}
	tick(t, e)
	accept(t, e, message("switch-back", "/nas 继续核对"))
	tick(t, e)
	if len(e.Store.snapshot().Tasks) != 4 || x.opens != 4 || x.sends != 5 {
		t.Fatal("controlled route switch lost original NAS context")
	}
	// Explicit reference to another user's task must never borrow its context.
	var foreign *Task
	for _, task := range e.Store.snapshot().Tasks {
		if task.Source.UserID == "other" {
			foreign = task
		}
	}
	m := message("cross-user", "继续")
	m.ParentID = foreign.Source.ID
	accept(t, e, m)
	tick(t, e)
	if x.sends != 5 {
		t.Fatal("borrowed another user's task")
	}
}

func TestSessionImplicitClassifierAndExplicitQuotes(t *testing.T) {
	e, x, l, _ := testEngine(t)
	e.Config.DefaultRoute = "nas"
	f := &fakeClassifier{d: Decision{"nas", "new_task"}}
	e.Classifier = f
	accept(t, e, message("one", "你能做什么"))
	tick(t, e)
	x.observation.Status = "completed"
	tick(t, e)
	accept(t, e, message("compare", "比较Mac和NAS的方案，不执行"))
	tick(t, e)
	if x.opens != 1 || x.sends != 2 || f.defaultRoute != "nas" {
		t.Fatal("ordinary comparison did not continue controlled context")
	}
	tick(t, e)
	l.message.Quote = "引用文章：Mac重启"
	m := message("quote", "解释这个引用里的“Mac重启”")
	m.ParentID = "external-reference"
	// An unbound external quote is context, never the previous conversation's authorization.
	accept(t, e, m)
	tick(t, e)
	if x.opens != 2 {
		t.Fatal("explicit unknown reference silently borrowed current task")
	}
	tick(t, e)
	m = message("own", "先讨论一下“Mac与NAS”的方案")
	m.ParentID = onlyOwnedTask(e, "one").CardID
	oldCalls := f.calls
	accept(t, e, m)
	tick(t, e)
	if f.calls != oldCalls || x.opens != 2 {
		t.Fatal("scoped card discussion was keyword-blocked or reclassified")
	}
}
func onlyOwnedTask(e *Engine, source string) *Task {
	for _, t := range e.Store.snapshot().Tasks {
		if t.Source.ID == source {
			return t
		}
	}
	return nil
}

func TestSessionResetStopResumeAndReplyRetry(t *testing.T) {
	e, x, l, _ := testEngine(t)
	accept(t, e, message("first", "/nas first"))
	tick(t, e)
	x.observation.Status = "completed"
	tick(t, e)
	first := onlyTask(t, e)
	l.replyErr = errors.New("reply lost")
	accept(t, e, message("reset", "/reset"))
	tick(t, e)
	if len(e.Store.snapshot().Inbox) != 1 || x.stops != 0 {
		t.Fatal("reset interrupted completed/manual work or lost failed ack")
	}
	l.replyErr = nil
	tick(t, e)
	accept(t, e, message("second", "/nas second"))
	tick(t, e)
	if x.opens != 2 {
		t.Fatal("reset reused archived context")
	}
	second := onlyOwnedTask(e, "second")
	x.stopErr = errors.New("unknown stop")
	accept(t, e, message("stop", "/stop"))
	if !e.Store.snapshot().Tasks[second.ID].StopRequested {
		t.Fatal("unreferenced stop not durable at intake")
	}
	tick(t, e)
	tick(t, e)
	if x.stops != 1 || !strings.Contains(e.Store.snapshot().Tasks[second.ID].Summary, "待核对") {
		t.Fatal("unknown stop retried or claimed success")
	}
	accept(t, e, message("resume-list", "/resume"))
	tick(t, e)
	if !strings.Contains(l.replies[len(l.replies)-1], shortTaskID(first.ID)) || strings.Contains(l.replies[len(l.replies)-1], shortTaskID(second.ID)) {
		t.Fatal("history omitted owned context or offered stopped task")
	}
	accept(t, e, message("resume", "/resume "+shortTaskID(first.ID)))
	tick(t, e)
	accept(t, e, message("follow", "继续"))
	tick(t, e)
	if x.opens != 2 || x.sends != 3 {
		t.Fatal("resume did not reuse owned, matching original thread")
	}
}

func TestSessionResetRunningDoesNotReleaseQueuedOldWork(t *testing.T) {
	e, x, _, _ := testEngine(t)
	accept(t, e, message("first", "/nas first"))
	tick(t, e)
	accept(t, e, message("queued", "下一步"))
	tick(t, e)
	accept(t, e, message("new", "/new"))
	tick(t, e)
	tick(t, e)
	if x.sends != 1 || x.stops != 1 {
		t.Fatal("reset released queued work or retried stop")
	}
	accept(t, e, message("next", "/nas new context"))
	tick(t, e)
	if x.sends != 2 || x.opens != 2 {
		t.Fatal("new context not independent after stopping old run")
	}
}

func TestSessionLegacyPrivacyPolicyAndCommands(t *testing.T) {
	e, x, l, _ := testEngine(t)
	accept(t, e, message("old", "/nas old"))
	tick(t, e)
	x.observation.Status = "completed"
	tick(t, e)
	old := onlyTask(t, e)
	if err := e.Store.update(func(st *State) error { st.Current = nil; st.Selected = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	accept(t, e, message("legacy-follow", "继续"))
	tick(t, e)
	if x.opens != 1 {
		t.Fatal("unique legacy task was not adopted")
	}
	tick(t, e)
	for i, text := range []string{"/help", "/unsupported", "/resume deadbeef"} {
		accept(t, e, message(string(rune('a'+i)), text))
		tick(t, e)
	}
	if x.sends != 2 || x.opens != 1 || !strings.Contains(l.replies[len(l.replies)-1], "不属于") {
		t.Fatal("commands reached agent or arbitrary resume accepted")
	}
	target := e.Config.Routes["nas"]
	target.RuntimeMode = "full-access"
	e.Config.Routes["nas"] = target
	accept(t, e, message("changed", "/resume "+shortTaskID(old.ID)))
	tick(t, e)
	if !strings.Contains(l.replies[len(l.replies)-1], "不属于") {
		t.Fatal("resume crossed permission identity")
	}
	for _, path := range []string{"/home/operator/file.go", "/workspace/project", "/tmp/", "/", "/tmp", "/etc"} {
		if slashWord(e.Config, path) != "" {
			t.Fatal("path became unsupported command", path)
		}
	}
	for _, text := range []string{"/NAS 正文", "/nas 正文"} {
		if slashWord(e.Config, text) != "" {
			t.Fatal("controlled route prefix became unknown command")
		}
	}
}

func TestSessionNewChatOfficialIdempotencyAndPrivacy(t *testing.T) {
	e, _, ch, x, f := setupAccess(t)
	var failures []CommandFailureStep
	e.OnCommandFailed = func(step CommandFailureStep) { failures = append(failures, step) }
	e.Config.Topics = nil
	e.Lark.(*Lark).Config.Topics = nil
	e.Config.DefaultRoute = "nas"
	creates := 0
	welcomes := 0
	uuid := ""
	welcomeUUID := ""
	f.extra = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodGet && r.URL.Path == "/open-apis/im/v1/chats/new-private-group" {
			io.WriteString(w, `{"code":0,"data":{"chat_mode":"group","group_message_type":"chat"}}`)
			return true
		}
		if r.Method == "POST" && r.URL.Path == "/open-apis/im/v1/chats" {
			creates++
			if uuid != "" && uuid != r.URL.Query().Get("uuid") {
				t.Error("changed creation UUID")
			}
			uuid = r.URL.Query().Get("uuid")
			var b map[string]any
			json.NewDecoder(r.Body).Decode(&b)
			users := b["user_id_list"].([]any)
			if len(users) != 1 || users[0] != "user" || b["chat_type"] != "private" || b["chat_mode"] != "group" || b["bot_id_list"] != nil || b["owner_id"] != nil {
				t.Error("created non-private group or invited others", b)
			}
			if creates == 1 {
				w.WriteHeader(503)
				io.WriteString(w, "synthetic lost response")
				return true
			}
			io.WriteString(w, `{"code":0,"data":{"chat_id":"new-private-group"}}`)
			return true
		}
		if r.Method == "POST" && r.URL.Path == "/open-apis/im/v1/messages" {
			welcomes++
			var b map[string]any
			json.NewDecoder(r.Body).Decode(&b)
			if b["receive_id"] != "new-private-group" || r.URL.Query().Get("receive_id_type") != "chat_id" {
				t.Error("welcome wrong target")
			}
			if welcomeUUID != "" && welcomeUUID != b["uuid"].(string) {
				t.Error("welcome not idempotent")
			}
			welcomeUUID = b["uuid"].(string)
			if welcomes == 1 {
				io.WriteString(w, `{"code":999,"msg":"synthetic unknown"}`)
				return true
			}
			io.WriteString(w, `{"code":0,"data":{"message_id":"welcome"}}`)
			return true
		}
		return false
	}
	e.Classifier = &fakeClassifier{d: Decision{"nas", "new_task"}}
	accessCommand(t, e, ch, "new-chat", "user", "chat", "/new chat 合成讨论组")
	if creates != 1 || len(e.Store.snapshot().Inbox) != 1 || x.opens != 0 {
		t.Fatal("uncertain create acknowledged or executed")
	}
	tick(t, e)
	if creates != 2 || welcomes != 1 || len(e.Store.snapshot().Inbox) != 1 {
		t.Fatal("failed welcome lost known group")
	}
	// Simulate restart of the worker against the same durable state, retaining the fake SDK only.
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e.Store = s
	e.Lark.(*Lark).Store = s
	f.replyFail = true
	tick(t, e)
	if len(e.Store.snapshot().Inbox) != 1 {
		t.Fatal("failed confirmation lost command")
	}
	f.replyFail = false
	tick(t, e)
	if len(f.replyUUIDs) < 2 || f.replyUUIDs[len(f.replyUUIDs)-1] != f.replyUUIDs[len(f.replyUUIDs)-2] {
		t.Fatal("confirmation UUID changed")
	}
	if creates != 2 || welcomes != 2 || len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("restart recreated group or lost welcome/confirmation")
	}
	n := groupMessage("group-follow", "user", "new-private-group", "/nas 你好", true)
	n.RawEvent.(*im.P2MessageReceiveV1).Event.Message.ThreadId = nil
	receiveAccess(t, ch, n)
	tick(t, e)
	if x.sends != 1 || !e.Config.groupAllowed("new-private-group") {
		t.Fatal("new group's inherited controlled context unusable")
	}
	if !slices.Equal(failures, []CommandFailureStep{CommandFailureChatCreate, CommandFailureChatWelcome, CommandFailureReply}) {
		t.Fatalf("unexpected new chat telemetry: %v", failures)
	}
}

func TestSessionNewChatUnauthorizedAndExpiredFence(t *testing.T) {
	e, _, ch, x, f := setupAccess(t)
	creates := 0
	f.extra = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == "POST" && r.URL.Path == "/open-apis/im/v1/chats" {
			creates++
			w.WriteHeader(503)
			return true
		}
		return false
	}
	accessCommand(t, e, ch, "denied-chat", "stranger", "chat", "/new chat private")
	if creates != 0 || x.opens != 0 {
		t.Fatal("unauthorized user created group")
	}
	// Select an explicitly authorized route, then exercise the persisted uncertain fence.
	accept(t, e, message("select", "/nas hi"))
	tick(t, e)
	accessCommand(t, e, ch, "unknown-chat", "user", "chat", "/new chat private")
	if creates != 1 {
		t.Fatal("did not attempt authorized creation")
	}
	if err := e.Store.update(func(st *State) error {
		for _, cmd := range st.Commands {
			if cmd.Kind == "new_chat" {
				cmd.CreateAt = time.Now().Add(-10 * time.Hour).UTC().Format(time.RFC3339Nano)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	tick(t, e)
	if creates != 1 || len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("expired uncertain creation blindly retried")
	}
	b, err := os.ReadFile(filepath.Join(e.Config.StateDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "超") {
		t.Fatal("uncertain result not retained")
	}
}

func TestSessionHistoryStatusScopeAndBusyQueue(t *testing.T) {
	e, x, l, _ := testEngine(t)
	e.Config.Topics = nil
	accept(t, e, message("mine", "/nas mine"))
	tick(t, e)
	mine := onlyTask(t, e)
	other := message("other", "/nas other")
	other.UserID = "other"
	accept(t, e, other)
	tick(t, e)
	foreign := onlyOwnedTask(e, "other")
	topic := message("topic", "/nas topic")
	topic.TopicID = "another-topic"
	accept(t, e, topic)
	tick(t, e)
	unrelated := onlyOwnedTask(e, "topic")
	queued := message("queued", "继续检查")
	queued.RootID = foreign.Source.ID
	accept(t, e, queued)
	tick(t, e)
	if len(e.Store.snapshot().Inbox) != 1 || e.Store.snapshot().Inbox[0].SessionTask != mine.ID {
		t.Fatal("shared topic root borrowed another user's context")
	}
	accept(t, e, message("status-scope", "/status"))
	tick(t, e)
	if !strings.Contains(l.replies[len(l.replies)-1], "排队：1") {
		t.Fatal("busy queue not visible")
	}
	accept(t, e, message("history-scope", "/resume"))
	tick(t, e)
	reply := l.replies[len(l.replies)-1]
	if !strings.Contains(reply, shortTaskID(mine.ID)) || strings.Contains(reply, shortTaskID(foreign.ID)) || strings.Contains(reply, shortTaskID(unrelated.ID)) {
		t.Fatal("history crossed user/topic")
	}
	accept(t, e, message("cannot-borrow", "/resume "+shortTaskID(foreign.ID)))
	tick(t, e)
	if !strings.Contains(l.replies[len(l.replies)-1], "不属于") || x.sends != 3 {
		t.Fatal("resume borrowed other identity or queued work prematurely")
	}
}

func TestSessionNewChatDefiniteRejectionAndReplyFence(t *testing.T) {
	e, _, ch, _, f := setupAccess(t)
	var failures []CommandFailureStep
	e.OnCommandFailed = func(step CommandFailureStep) { failures = append(failures, step) }
	e.Config.DefaultRoute = "nas"
	calls := 0
	f.extra = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == "POST" && r.URL.Path == "/open-apis/im/v1/chats" {
			calls++
			io.WriteString(w, `{"code":999,"msg":"private failure bearer synthetic"}`)
			return true
		}
		return false
	}
	accessCommand(t, e, ch, "rejected", "user", "chat", "/new chat 合成")
	tick(t, e)
	if calls != 1 || len(e.Store.snapshot().Inbox) != 0 || strings.Contains(f.replies[len(f.replies)-1], "private failure") {
		t.Fatal("definite rejection retried or leaked response")
	}
	f.replyFail = true
	accessCommand(t, e, ch, "help-reply", "user", "chat", "/help")
	if err := e.Store.update(func(st *State) error {
		st.Commands[key(Normalize(groupMessage("help-reply", "user", "chat", "/help", true)), "help-reply")].ReplyAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := len(f.replies)
	tick(t, e)
	if len(f.replies) != before || len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("expired reply UUID window blindly resent")
	}
	if !slices.Equal(failures, []CommandFailureStep{CommandFailureChatCreate, CommandFailureReply, CommandFailureReplyFallback, CommandFailureReplyExpired}) {
		t.Fatalf("unexpected rejection and expiry samples: %v", failures)
	}
}

func TestSessionManagementReferencePriority(t *testing.T) {
	e, x, l, _ := testEngine(t)
	accept(t, e, message("nas", "/nas nas"))
	tick(t, e)
	x.observation.Status = "completed"
	tick(t, e)
	nas := onlyTask(t, e)
	accept(t, e, message("mac", "/mac mac"))
	tick(t, e)
	reset := message("reset-nas", "/new")
	reset.ParentID = nas.Source.ID
	accept(t, e, reset)
	tick(t, e)
	if x.stops != 0 || e.Store.snapshot().Selected[userScope(reset)] != "nas" {
		t.Fatal("referenced reset affected unrelated current Mac run")
	}
	m := message("reset-unknown", "/reset")
	m.ParentID = "unknown"
	accept(t, e, m)
	tick(t, e)
	if !strings.Contains(l.replies[len(l.replies)-1], "引用任务不明") || x.stops != 0 {
		t.Fatal("unknown reference authorized resetting current run")
	}
	accept(t, e, message("nas-next", "/nas next"))
	tick(t, e)
	if x.opens != 3 {
		t.Fatal("referenced reset did not archive NAS mapping")
	}
}

func TestSessionResetAcknowledgementDoesNotResetAgain(t *testing.T) {
	e, x, l, _ := testEngine(t)
	accept(t, e, message("first", "/nas old"))
	tick(t, e)
	x.observation.Status = "completed"
	tick(t, e)
	l.replyErr = errors.New("unknown acknowledgement")
	accept(t, e, message("new-once", "/new"))
	tick(t, e)
	accept(t, e, message("new-message", "/nas fresh"))
	tick(t, e)
	fresh := onlyOwnedTask(e, "new-message")
	if fresh == nil || x.opens != 2 {
		t.Fatal("new context did not start while reset ack pending")
	}
	l.replyErr = nil
	tick(t, e)
	state := e.Store.snapshot()
	if state.Current[sessionKey(fresh.Source, "nas", fresh.Target)] != fresh.ID || state.Tasks[fresh.ID].StopRequested || x.stops != 0 {
		t.Fatal("acknowledgement retry repeated reset side effect")
	}
}

func TestSessionLegacyWaitingTaskKeepsOriginalRequest(t *testing.T) {
	e, x, _, maintenance := testEngine(t)
	maintenance.busy = true
	accept(t, e, message("old-waiting", "/nas 合成等待"))
	tick(t, e)
	old := onlyTask(t, e)
	if err := e.Store.update(func(st *State) error { st.Current = nil; st.Selected = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	accept(t, e, message("old-queue", "继续解释"))
	tick(t, e)
	current := onlyTask(t, e)
	if current.ID != old.ID || current.RequestID != old.RequestID || current.CardID != old.CardID || x.sends != 1 || len(e.Store.snapshot().Inbox) != 1 {
		t.Fatal("legacy pending task forked or replaced original request")
	}
	maintenance.busy = false
	tick(t, e)
	x.observation.Status = "completed"
	tick(t, e)
	tick(t, e)
	if x.opens != 1 || x.sends != 2 || onlyTask(t, e).ID != old.ID {
		t.Fatal("legacy pending conversation did not settle then continue")
	}
}
