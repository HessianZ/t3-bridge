package bridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeExecutor struct {
	opens, sends, stops, uploads, preflights  int
	preflightErr, verifyErr, sendErr, stopErr error
	observation                               Observation
	onSend                                    func(*Task)
	onPreflight                               func()
	onObserve                                 func()
	request                                   string
	sendLink                                  string
}

func (f *fakeExecutor) Preflight(context.Context, Target) error {
	f.preflights++
	if f.onPreflight != nil {
		f.onPreflight()
	}
	return f.preflightErr
}
func (f *fakeExecutor) Open(_ context.Context, target Target, _, _ string) (Execution, error) {
	f.opens++
	return Execution{ThreadID: "thread", Link: "t3-thread://v2/" + target.EnvironmentID + "/thread"}, nil
}
func (f *fakeExecutor) Verify(context.Context, Target, string) error { return f.verifyErr }
func (f *fakeExecutor) Send(_ context.Context, _ Target, t *Task) (Execution, error) {
	f.sends++
	f.request = t.RequestMessage
	if f.onSend != nil {
		f.onSend(t)
	}
	return Execution{ThreadID: "thread", RunID: "run", Link: f.sendLink}, f.sendErr
}
func (f *fakeExecutor) Observe(context.Context, Target, *Task) (Observation, error) {
	if f.onObserve != nil {
		f.onObserve()
	}
	return f.observation, nil
}
func (f *fakeExecutor) Stop(context.Context, Target, *Task) error     { f.stops++; return f.stopErr }
func (f *fakeExecutor) Discard(context.Context, Target, string) error { return nil }
func (f *fakeExecutor) Upload(context.Context, Target, Image, []byte) (Attachment, error) {
	f.uploads++
	return Attachment{ID: "image", Type: "image", MIME: "image/png", Size: 3}, nil
}

type fakeMessenger struct {
	replies       []string
	replyErr      error
	cards         map[string]string
	sentCards     map[string]string
	resolveErr    error
	notifications int
	fail          bool
	message       Message
	onResolve     func()
	onNotify      func(*Task)
}

func (f *fakeMessenger) ReplyControl(_ context.Context, _ Message, text string, _ bool) error {
	f.replies = append(f.replies, text)
	return f.replyErr
}
func (f *fakeMessenger) Resolve(_ context.Context, m Message) (Message, error) {
	if f.onResolve != nil {
		f.onResolve()
	}
	if f.message.Quote != "" {
		m.Quote = f.message.Quote
	}
	return m, f.resolveErr
}
func (f *fakeMessenger) Image(context.Context, Message, Image, int) ([]byte, error) {
	return []byte("png"), nil
}
func (f *fakeMessenger) Notify(_ context.Context, t *Task) (string, error) {
	if f.onNotify != nil {
		f.onNotify(t)
	}
	f.notifications++
	if f.fail {
		return "", errors.New("lost notification")
	}
	if f.sentCards == nil {
		f.sentCards = map[string]string{}
	}
	if t.CardID != "" {
		f.sentCards[t.CardID] = card(t)
		return t.CardID, nil
	}
	if f.cards == nil {
		f.cards = map[string]string{}
	}
	id := cardUUID(t)
	if f.cards[id] == "" {
		name := "card"
		if len(f.cards) > 0 {
			name = fmt.Sprintf("card-%d", len(f.cards)+1)
		}
		f.cards[id] = name
	}
	f.sentCards[f.cards[id]] = card(t)
	return f.cards[id], nil
}

type fakeMaintenance struct {
	busy  bool
	err   error
	calls int
}

func (f *fakeMaintenance) Busy(context.Context, Target) (bool, error) {
	f.calls++
	return f.busy, f.err
}

type fakeClassifier struct {
	d            Decision
	err          error
	calls        int
	message      Message
	routes       []string
	defaultRoute string
}

func (f *fakeClassifier) Classify(_ context.Context, m Message, routes []string, defaultRoute string) (Decision, error) {
	f.calls++
	f.message, f.routes, f.defaultRoute = m, routes, defaultRoute
	return f.d, f.err
}
func testConfig(t *testing.T) Config {
	t.Helper()
	nas := Target{Kind: "nas", EnvironmentID: "nas-env", ProjectID: "homeops", Driver: "opencode", ProviderID: "official", Model: "deepseek-v4.1-flash", Options: map[string]any{"effort": "high"}, RuntimeMode: "approval-required", InteractionMode: "default", Users: []string{"user", "other"}, Checks: []string{"service:gitea"}}
	mac := nas
	mac.Kind = "mac"
	mac.EnvironmentID = "mac-env"
	mac.Driver = "codex"
	mac.Model = "gpt-6.1-sol"
	mac.Checks = nil
	return Config{ReceiverMachine: "synthetic-nas", ReceiverAuthorized: true, StateDir: t.TempDir(), AppID: "fake-app", AppSecretFile: "/private/fake.secret", Users: []string{"user", "other"}, Chats: []string{"chat"}, Topics: []string{"topic"}, MCP: MCPConfig{Endpoint: "http://127.0.0.1:3773/mcp", TokenFile: "/private/mcp.secret", EnvironmentID: "nas-env"}, CoordinationCommand: "/private/coordination", Routes: map[string]Target{"nas": nas, "mac": mac}}
}
func testEngine(t *testing.T) (*Engine, *fakeExecutor, *fakeMessenger, *fakeMaintenance) {
	t.Helper()
	c := testConfig(t)
	s, err := OpenStore(c.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	x := &fakeExecutor{observation: Observation{Status: "running", RunID: "run", FoundRequest: true}}
	l := &fakeMessenger{}
	m := &fakeMaintenance{}
	return &Engine{Now: func() time.Time { return time.Now().Add(3 * time.Second) }, Config: c, Store: s, T3: x, Lark: l, Maintenance: m}, x, l, m
}
func message(id, text string) Message {
	return Message{ID: id, Text: text, UserID: "user", ChatID: "chat", TopicID: "topic"}
}
func accept(t *testing.T, e *Engine, m Message) {
	t.Helper()
	if err := e.Store.Accept(e.Config, m); err != nil {
		t.Fatal(err)
	}
}
func tick(t *testing.T, e *Engine) {
	t.Helper()
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func onlyTask(t *testing.T, e *Engine) *Task {
	t.Helper()
	st := e.Store.snapshot()
	if len(st.Tasks) != 1 {
		t.Fatalf("tasks=%d", len(st.Tasks))
	}
	for _, v := range st.Tasks {
		return v
	}
	return nil
}
func TestLifecycleDedupContinuationSameCard(t *testing.T) {
	e, x, l, _ := testEngine(t)
	m := message("one", "NAS检查磁盘")
	m.Images = []Image{{Key: "test-image"}}
	accept(t, e, m)
	accept(t, e, m)
	tick(t, e)
	task := onlyTask(t, e)
	if x.sends != 1 || x.uploads != 1 || task.Phase != "tracking" {
		t.Fatalf("unexpected lifecycle: %+v", task)
	}
	if !strings.HasPrefix(x.request, m.Text+"\n\n") {
		t.Fatal("user original must be first")
	}
	if !strings.Contains(x.request, "opencode / official / deepseek-v4.1-flash") {
		t.Fatal("missing explicit executor")
	}
	x.observation.Status = "completed"
	x.observation.Summary = "合成结论"
	tick(t, e)
	task = onlyTask(t, e)
	if task.Phase != "completed" || !strings.Contains(task.Summary, "业务结果请核对") {
		t.Fatal(task.Phase)
	}
	n := l.notifications
	tick(t, e)
	if l.notifications != n {
		t.Fatal("unchanged notification repeated")
	}
	reply := message("two", "继续核对")
	reply.ParentID = "card"
	accept(t, e, reply)
	tick(t, e)
	if x.sends != 2 || x.opens != 1 || onlyTask(t, e).CardID != "card" {
		t.Fatal("continuation lost task/card binding")
	}
}
func TestMaintenanceReadOnlyAndNextTurnRecheck(t *testing.T) {
	e, x, _, m := testEngine(t)
	m.busy = true
	accept(t, e, message("one", "NAS升级Gitea"))
	tick(t, e)
	if onlyTask(t, e).Phase != "tracking" || x.sends != 1 || !strings.Contains(x.request, "配置checks存在维护登记") || !strings.Contains(x.request, "禁止写入") {
		t.Fatal("maintenance discussion or write boundary lost")
	}
	x.observation.Status = "completed"
	tick(t, e)
	m.busy = false
	next := message("two", "只解释后续方案")
	next.ParentID = onlyTask(t, e).CardID
	accept(t, e, next)
	tick(t, e)
	if x.sends != 2 || m.calls != 2 || x.preflights != 2 || !strings.Contains(x.request, "不产生写入授权") {
		t.Fatal("missing next-turn maintenance recheck")
	}
}
func TestPreflightFailuresNeverExecute(t *testing.T) {
	for _, name := range []string{"provider", "permission", "linked access", "identity", "maintenance"} {
		t.Run(name, func(t *testing.T) {
			e, x, _, m := testEngine(t)
			switch name {
			case "provider", "permission":
				x.preflightErr = errors.New(name)
			case "linked access":
				x.preflightErr = ErrRemoteAccessInsufficient
			case "identity":
				x.verifyErr = errors.New(name)
			case "maintenance":
				m.err = errors.New(name)
			}
			accept(t, e, message("one", "NAS重启Gitea"))
			tick(t, e)
			if name == "maintenance" {
				if x.sends != 1 || !strings.Contains(x.request, "不得推断空闲或生产写入") {
					t.Fatal("maintenance read failure blocked discussion or granted writes")
				}
			} else if onlyTask(t, e).Phase != "blocked" || x.sends != 0 {
				t.Fatal("failed identity preflight executed")
			}
			if name == "linked access" && (x.opens != 0 || !strings.Contains(onlyTask(t, e).Summary, "关联权限不足")) {
				t.Fatal("linked access failure opened a thread or lost the actionable diagnosis")
			}
		})
	}
}
func TestStopNoRetryIncludingRestart(t *testing.T) {
	for _, before := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "waiting"}[before], func(t *testing.T) {
			e, x, _, m := testEngine(t)
			m.busy = before
			accept(t, e, message("one", "NAS升级Gitea"))
			tick(t, e)
			stop := message("stop", "不要执行")
			stop.ParentID = "card"
			accept(t, e, stop)
			x.stopErr = errors.New("stop reply lost")
			tick(t, e)
			task := onlyTask(t, e)
			if task.Phase != "stopped" || !task.StopRequested {
				t.Fatal("stop lost")
			}
			sends, stops := x.sends, x.stops
			tick(t, e)
			if x.sends != sends || x.stops != stops {
				t.Fatal("stop retried")
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
			tick(t, e)
			if x.sends != sends || x.stops != stops {
				t.Fatal("restart replayed stop/execution")
			}
		})
	}
}
func TestStoppedRunRetainsBackgroundUntilReadBack(t *testing.T) {
	e, x, _, _ := testEngine(t)
	accept(t, e, message("one", "NAS检查磁盘"))
	tick(t, e)
	stop := message("stop", "不要执行")
	stop.ParentID = "card"
	accept(t, e, stop)
	x.stopErr = errStopBackgroundPending
	tick(t, e)
	task := onlyTask(t, e)
	if task.Phase != "stopped" || !task.StopBackgroundPending || !task.NeedsReconcile || !taskOccupiesCapacity(task) {
		t.Fatal("background work did not keep the original stop pending")
	}
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e.Store = store
	x.observation = Observation{RunID: task.RunID, FoundRequest: true, Status: "interrupted", Tasks: []string{"child"}}
	tick(t, e)
	if !onlyTask(t, e).NeedsReconcile {
		t.Fatal("main run termination cleared unresolved background work")
	}
	x.observation.Tasks = nil
	tick(t, e)
	if !onlyTask(t, e).NeedsReconcile {
		t.Fatal("a terminal result alone did not prove the original run settled")
	}
	x.observation.OriginalRunSettled = true
	tick(t, e)
	after := onlyTask(t, e)
	if after.Phase != "stopped" || after.StopBackgroundPending || after.NeedsReconcile || after.StopSettledRequest != task.RequestID || after.RunID != task.RunID || x.stops != 1 || x.sends != 1 {
		t.Fatal("stop did not settle by readback, or a write was replayed")
	}
}

func TestLostSendReconcileWithoutReplay(t *testing.T) {
	e, x, _, _ := testEngine(t)
	x.sendErr = errors.New("reply lost")
	accept(t, e, message("one", "NAS检查磁盘"))
	tick(t, e)
	task := onlyTask(t, e)
	if task.Phase != "needs_reconcile" {
		t.Fatal(task.Phase)
	}
	e.Store.Close()
	s, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e.Store = s
	x.observation = Observation{RunID: "run", FoundRequest: false}
	tick(t, e)
	if x.sends != 1 {
		t.Fatal("replayed ambiguous send")
	}
	x.observation = Observation{RunID: "run", FoundRequest: true, Status: "completed", Summary: "原请求结果"}
	tick(t, e)
	if onlyTask(t, e).Phase != "completed" || x.sends != 1 {
		t.Fatal("did not reconcile original request")
	}
}
func TestAsyncChildrenAndNotificationRetry(t *testing.T) {
	e, x, l, _ := testEngine(t)
	l.fail = true
	accept(t, e, message("one", "NAS检查磁盘"))
	tick(t, e)
	if onlyTask(t, e).NotifyHash != "" {
		t.Fatal("failed delivery marked delivered")
	}
	l.fail = false
	x.observation = Observation{RunID: "run", Status: "completed", Tasks: []string{"child"}, ChildThreads: []string{"child-thread"}}
	tick(t, e)
	task := onlyTask(t, e)
	if task.Phase != "awaiting_children" || len(task.ChildThreads) != 1 {
		t.Fatal("parent completed prematurely")
	}
	x.observation.Tasks = nil
	tick(t, e)
	if onlyTask(t, e).Phase != "completed" {
		t.Fatal("did not settle")
	}
}

func TestFailedObservationWaitsForDelegatedSettlement(t *testing.T) {
	e, x, _, _ := testEngine(t)
	accept(t, e, message("one", "NAS检查磁盘"))
	tick(t, e)
	before := onlyTask(t, e)
	x.observation = Observation{RunID: before.RunID, Status: "failed", Unsettled: true, Summary: "早期错误"}
	tick(t, e)
	if task := onlyTask(t, e); task.Phase != "settling" || task.Result != "" || task.ResultRunID != "" {
		t.Fatal("unsettled failure was committed as a terminal reply")
	}
	x.observation.Unsettled, x.observation.Summary, x.observation.ResultRunID = false, "异步收口后的结论", "wake"
	tick(t, e)
	if after := onlyTask(t, e); after.Phase != "failed" || after.RunID != before.RunID || after.RequestID != before.RequestID ||
		after.Result != "异步收口后的结论" || after.ResultRunID != "wake" || currentReply(after).ResultRunID != "wake" || x.sends != 1 {
		t.Fatal("settled failure lost its original run or the attributed conclusion")
	}
}

func TestACLAndScopedBindings(t *testing.T) {
	for _, name := range []string{"user", "chat", "topic"} {
		t.Run(name, func(t *testing.T) {
			e, x, _, _ := testEngine(t)
			m := message("one", "NAS重启Gitea")
			switch name {
			case "user":
				m.UserID = "intruder"
			case "chat":
				m.ChatID = "other"
			case "topic":
				m.TopicID = "other"
			}
			if e.Store.Accept(e.Config, m) == nil {
				t.Fatal("ACL accepted")
			}
			tick(t, e)
			if x.sends != 0 {
				t.Fatal("ACL bypass")
			}
		})
	}
	e, x, _, _ := testEngine(t)
	accept(t, e, message("one", "NAS检查磁盘"))
	tick(t, e)
	stop := message("two", "停止")
	stop.UserID = "other"
	stop.ParentID = "card"
	accept(t, e, stop)
	tick(t, e)
	if onlyTask(t, e).StopRequested || x.stops != 0 {
		t.Fatal("another user stopped owned task")
	}
}
func TestStopDuringNetworkDoesNotBlockInbound(t *testing.T) {
	e, x, _, _ := testEngine(t)
	accept(t, e, message("one", "NAS升级Gitea"))
	x.onSend = func(task *Task) { m := message("stop", "/stop"); m.ParentID = "one"; accept(t, e, m) }
	tick(t, e)
	if !onlyTask(t, e).StopRequested {
		t.Fatal("worker overwrote concurrent stop")
	}
	tick(t, e)
	if x.stops != 1 {
		t.Fatal("stop not forwarded")
	}
}
func TestStoreLockRollbackAndConcurrentAccept(t *testing.T) {
	e, _, _, _ := testEngine(t)
	if other, err := OpenStore(e.Config.StateDir); err == nil {
		other.Close()
		t.Fatal("missing single-process lock")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.Store.Accept(e.Config, message("one", "NAS检查磁盘")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(e.Store.snapshot().Inbox) != 1 {
		t.Fatal("concurrent dedup failed")
	}
	path := e.Store.dir
	e.Store.dir = filepath.Join(path, "missing")
	if err := e.Store.Accept(e.Config, message("two", "NAS检查磁盘")); err == nil {
		t.Fatal("expected disk error")
	}
	e.Store.dir = path
	if len(e.Store.snapshot().Inbox) != 1 || e.Store.snapshot().Seen[key(message("two", ""), "two")] {
		t.Fatal("failed persistence remained in memory")
	}
	info, err := os.Stat(filepath.Join(path, "state.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("state not private")
	}
}
func TestSaveFencePreventsSend(t *testing.T) {
	e, x, _, _ := testEngine(t)
	accept(t, e, message("one", "NAS检查磁盘"))
	x.onPreflight = func() { e.Store.dir = filepath.Join(e.Store.dir, "missing") }
	if e.Tick(context.Background()) == nil || x.sends != 0 {
		t.Fatal("side effect after durable fence failure")
	}
}

func TestClarificationResolvesOnSameTaskCard(t *testing.T) {
	e, x, _, _ := testEngine(t)
	accept(t, e, message("one", "检查它"))
	tick(t, e)
	if onlyTask(t, e).Phase != "clarify" {
		t.Fatal("expected clarification")
	}
	reply := message("two", "NAS检查磁盘")
	reply.ParentID = "card"
	accept(t, e, reply)
	tick(t, e)
	if len(e.Store.snapshot().Tasks) != 1 || x.sends != 1 || x.opens != 1 || onlyTask(t, e).CardID != "card" {
		t.Fatal("clarification reply lost task/card")
	}
}

func TestBoundNaturalRefusalStopsBeforeRelease(t *testing.T) {
	e, x, _, m := testEngine(t)
	m.busy = true
	original := message("one", "NAS升级Gitea")
	accept(t, e, original)
	if err := e.consume(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	reply := message("two", "先别升级NAS，等我确认")
	reply.ParentID = original.ID
	accept(t, e, reply)
	m.busy = false
	tick(t, e)
	if x.sends != 0 || onlyTask(t, e).Phase != "stopped" {
		t.Fatal("natural refusal bypassed")
	}
}

func TestExplicitNASReplyPreservesOriginalThroughDispatch(t *testing.T) {
	for _, text := range []string{`/nas 只回复“已接通”，不要执行命令`, `/nas 不要执行命令，只回复“已接通”`} {
		t.Run(text, func(t *testing.T) {
			e, x, _, _ := testEngine(t)
			e.Classifier = &fakeClassifier{err: errors.New("classifier unavailable")}
			accept(t, e, message("one", text))
			tick(t, e)
			task := onlyTask(t, e)
			if x.sends != 1 || task.RouteID != "nas" || task.Phase != "tracking" || !strings.HasPrefix(x.request, text+"\n\n") {
				t.Fatalf("dispatch failed: sends=%d task=%+v", x.sends, task)
			}
		})
	}
}

func TestExplicitNASControlStopsWaitingTask(t *testing.T) {
	for _, body := range []string{"停止", "拒绝", "不要执行"} {
		t.Run(body, func(t *testing.T) {
			e, x, _, maintenance := testEngine(t)
			maintenance.busy = true
			original := message("one", "NAS升级Gitea")
			accept(t, e, original)
			if err := e.consume(context.Background(), original); err != nil {
				t.Fatal(err)
			}
			reply := message("two", "/nas "+body)
			reply.ParentID = original.ID
			accept(t, e, reply)
			if !onlyTask(t, e).StopRequested {
				t.Fatal("prefixed control not persisted at ingress")
			}
			maintenance.busy = false
			tick(t, e)
			if x.sends != 0 || onlyTask(t, e).Phase != "stopped" {
				t.Fatal("prefixed stop did not prevent execution")
			}
		})
	}
}

func TestExplicitRouteKeepsTargetACLAndStopOwnership(t *testing.T) {
	t.Run("target_acl", func(t *testing.T) {
		e, x, _, _ := testEngine(t)
		target := e.Config.Routes["nas"]
		target.Users = []string{"other"}
		e.Config.Routes["nas"] = target
		accept(t, e, message("one", `/nas 只回复“已接通”，不要执行命令`))
		tick(t, e)
		if x.sends != 0 || onlyTask(t, e).Phase != "clarify" {
			t.Fatal("explicit prefix bypassed target ACL")
		}
	})
	t.Run("stop_owner", func(t *testing.T) {
		e, x, _, maintenance := testEngine(t)
		maintenance.busy = true
		accept(t, e, message("one", "NAS检查磁盘"))
		tick(t, e)
		reply := message("two", "/nas 停止")
		reply.ParentID = "card"
		reply.UserID = "other"
		accept(t, e, reply)
		tick(t, e)
		if onlyTask(t, e).StopRequested || x.stops != 0 {
			t.Fatal("another user stopped task with explicit prefix")
		}
	})
}

func TestBoundOwnInteractiveCardContinuationSkipsResolve(t *testing.T) {
	e, x, l, _ := testEngine(t)
	e.Lark = cacheFollowupFixture{l}
	target := e.Config.Routes["nas"]
	target.CacheFollowupAttachments = true
	e.Config.Routes["nas"] = target
	accept(t, e, message("one", "NAS检查磁盘"))
	tick(t, e)
	x.observation.Status = "completed"
	tick(t, e)
	original := onlyTask(t, e)
	resolves := 0
	l.onResolve = func() { resolves++ }
	l.resolveErr = errors.New("unsupported quote type; require text or image")
	reply := message("two", "继续核对这张图片")
	reply.ParentID = original.CardID
	reply.Images = []Image{{Key: "current-user-image"}}
	accept(t, e, reply)
	tick(t, e)
	task := onlyTask(t, e)
	if resolves != 0 || x.sends != 2 || x.opens != 1 || x.uploads != 0 || task.ID != original.ID || task.ThreadID != original.ThreadID || task.CardID != original.CardID || task.Phase != "tracking" {
		t.Fatalf("own card continuation failed: resolves=%d sends=%d task=%+v", resolves, x.sends, task)
	}
	if task.DispatchSource.ID != reply.ID || !strings.HasPrefix(x.request, reply.Text+"\n\n") || len(task.DispatchSource.Images) != 1 {
		t.Fatal("current user message or image lost")
	}
}

func TestOwnCardReplyKeepsScopeAndOtherQuoteBoundaries(t *testing.T) {
	for _, kind := range []string{"unknown_card", "root_only_binding", "original_message", "other_user", "other_chat", "other_topic", "target_acl"} {
		t.Run(kind, func(t *testing.T) {
			e, x, l, _ := testEngine(t)
			accept(t, e, message("one", "NAS检查磁盘"))
			tick(t, e)
			x.observation.Status = "completed"
			tick(t, e)
			original := onlyTask(t, e)
			resolves := 0
			l.onResolve = func() { resolves++ }
			l.resolveErr = errors.New("unsupported quote type; require text or image")
			reply := message("two", "继续核对")
			reply.ParentID = original.CardID
			wantResolves := 0
			switch kind {
			case "unknown_card":
				reply.ParentID = "unknown-card"
				wantResolves = 1
			case "root_only_binding":
				reply.ParentID = "unknown-card"
				reply.RootID = "one"
				wantResolves = 1
			case "original_message":
				reply.ParentID = "one"
				wantResolves = 1
			case "other_user":
				reply.UserID = "other"
			case "other_chat":
				reply.ChatID = "other-chat"
				e.Config.Chats = append(e.Config.Chats, reply.ChatID)
				wantResolves = 1
			case "other_topic":
				reply.TopicID = "other-topic"
				e.Config.Topics = append(e.Config.Topics, reply.TopicID)
				wantResolves = 1
			case "target_acl":
				if err := e.Store.update(func(st *State) error { st.Tasks[original.ID].Target.Users = []string{"other"}; return nil }); err != nil {
					t.Fatal(err)
				}
			}
			accept(t, e, reply)
			tick(t, e)
			current := e.Store.snapshot().Tasks[original.ID]
			if resolves != wantResolves || x.sends != 1 || x.opens != 1 || current.RequestID != original.RequestID || current.Phase != "completed" {
				t.Fatalf("quote boundary bypassed: resolves=%d sends=%d task=%+v", resolves, x.sends, current)
			}
		})
	}
}

func TestOwnCardReplyWaitsForRunAndHonorsStop(t *testing.T) {
	e, x, l, _ := testEngine(t)
	accept(t, e, message("one", "NAS检查磁盘"))
	tick(t, e)
	resolves := 0
	l.onResolve = func() { resolves++ }
	l.resolveErr = errors.New("unsupported quote type; require text or image")
	reply := message("two", "继续核对")
	reply.ParentID = "card"
	accept(t, e, reply)
	tick(t, e)
	if x.sends != 1 || resolves != 0 || len(e.Store.snapshot().Inbox) != 1 {
		t.Fatal("running task did not retain queued continuation")
	}
	x.observation.Status = "completed"
	tick(t, e)
	tick(t, e)
	if x.sends != 2 || resolves != 0 || onlyTask(t, e).CardID != "card" {
		t.Fatal("queued continuation did not resume same card")
	}
	stop := message("stop", "/nas 停止")
	stop.ParentID = "card"
	accept(t, e, stop)
	tick(t, e)
	if x.stops != 1 || x.sends != 2 || resolves != 0 || onlyTask(t, e).Phase != "stopped" {
		t.Fatal("own card fast path bypassed stop")
	}
}
