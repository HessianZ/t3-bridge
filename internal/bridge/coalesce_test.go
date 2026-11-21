package bridge

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func shortMessage(id, text string, at time.Time) Message {
	m := message(id, text)
	m.ReceivedAt = at.UTC().Format(time.RFC3339Nano)
	return m
}

func TestGroupReceiptDeduplicatesAfterTopicMetadataChanges(t *testing.T) {
	e, _, _, _ := testEngine(t)
	e.Config.Topics = nil
	unscoped := message("without-topic", "先以群消息入队")
	unscoped.ChatType, unscoped.TopicID = "group", ""
	scoped := message("with-topic", "先以话题消息入队")
	scoped.ChatType = "group"
	accept(t, e, unscoped)
	accept(t, e, scoped)
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	e.Store = store
	unscoped.TopicID, scoped.TopicID = scoped.TopicID, ""
	accept(t, e, unscoped)
	accept(t, e, scoped)
	inbox := store.snapshot().Inbox
	if len(inbox) != 2 || inbox[0].TopicID != "" || inbox[1].TopicID != unscoped.TopicID {
		t.Fatal("replayed message was queued again or original scope changed", inbox)
	}
}

func TestCoalesceQuietWindowRestartAndPerRoundReply(t *testing.T) {
	e, x, l, _ := testEngine(t)
	e.Config.DefaultRoute = "nas"
	e.Classifier = &fakeClassifier{d: Decision{RouteID: "nas", Action: "new_task"}}
	start := time.Now().UTC()
	now := start
	e.Now = func() time.Time { return now }
	a := shortMessage("one", "/nas 第一段", start)
	accept(t, e, a)
	tick(t, e)
	if len(e.Store.snapshot().Tasks) != 0 {
		t.Fatal("quiet window bypassed")
	}
	b := shortMessage("two", "补充第二段", start.Add(300*time.Millisecond))
	accept(t, e, b)
	now = start.Add(700 * time.Millisecond)
	tick(t, e)
	if x.sends != 0 {
		t.Fatal("did not wait for quiet")
	}
	e.Store.Close()
	s, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e.Store = s
	now = start.Add(time.Second)
	tick(t, e)
	task := onlyTask(t, e)
	if x.sends != 1 || len(task.DispatchSource.Parts) != 2 || task.DispatchSource.ID != "two" || replySource(task).ID != "two" || strings.Index(x.request, "第一段") > strings.Index(x.request, "第二段") {
		t.Fatal("merge source/order/reply lost")
	}
	st := e.Store.snapshot()
	for _, id := range []string{"one", "two"} {
		if !st.Seen[key(a, id)] || st.Bindings[key(a, id)] != task.ID {
			t.Fatal("original inbound identity missing")
		}
	}
	x.observation.Status = "completed"
	x.observation.Summary = "第一轮最终结论"
	tick(t, e)
	old := l.sentCards[task.CardID]
	x.observation.Status = "running"
	c := shortMessage("three", "继续一", now)
	d := shortMessage("four", "继续二", now.Add(200*time.Millisecond))
	accept(t, e, c)
	accept(t, e, d)
	now = now.Add(time.Second)
	tick(t, e)
	second := onlyTask(t, e)
	if second.ThreadID != task.ThreadID || second.CardID == task.CardID || x.sends != 2 || l.sentCards[task.CardID] != old || len(second.DispatchSource.Parts) != 2 {
		t.Fatal("implicit merged turn overwrote old answer")
	}
	// Busy messages pin to the Task but retain ordinary/new-card identity after restart.
	c = shortMessage("five", "忙时一", now)
	d = shortMessage("six", "忙时二", now.Add(200*time.Millisecond))
	accept(t, e, c)
	accept(t, e, d)
	now = now.Add(time.Second)
	tick(t, e)
	if len(e.Store.snapshot().Inbox) != 2 || x.sends != 2 {
		t.Fatal("busy batch not queued")
	}
	e.Store.Close()
	s, err = OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e.Store = s
	x.observation.Status = "completed"
	x.observation.Summary = "第二轮最终结论"
	tick(t, e)
	secondFinal := l.sentCards[second.CardID]
	tick(t, e)
	third := onlyTask(t, e)
	if !currentStopReference(third, Message{ParentID: "five"}) || currentStopReference(third, Message{ParentID: "one"}) {
		t.Fatal("merged source stop scope incorrect")
	}
	oldResult := message("result", "/result")
	oldResult.ParentID = "one"
	state := e.Store.snapshot()
	if reply, isCard := e.resultPage(&state, oldResult, third); !isCard || !strings.Contains(reply, "第一轮最终结论") {
		t.Fatal("merged first input lost archived full result")
	}
	copy := *third
	follow := message("old-follow", "继续")
	follow.ParentID = "one"
	beginReply(&copy, follow, false, RunSettings{})
	if copy.CardID != task.CardID {
		t.Fatal("merged first input lost original reply card")
	}
	if x.sends != 3 || third.CardID == second.CardID || third.ThreadID != task.ThreadID || l.sentCards[second.CardID] != secondFinal || len(third.DispatchSource.Parts) != 2 {
		t.Fatal("busy merged turn lost new card/fence")
	}
}
func TestCoalesceIsolationLimitsAndControlPriority(t *testing.T) {
	for _, mode := range []string{"user", "topic", "route", "reference", "event", "files", "max-window", "stop", "reset", "unknown-send"} {
		t.Run(mode, func(t *testing.T) {
			e, x, _, _ := testEngine(t)
			e.Config.DefaultRoute = "nas"
			e.Config.Topics = nil
			e.Classifier = &fakeClassifier{d: Decision{RouteID: "nas", Action: "new_task"}}
			start := time.Now().UTC()
			now := start.Add(3 * time.Second)
			e.Now = func() time.Time { return now }
			a := shortMessage("a", "/nas 第一段", start)
			b := shortMessage("b", "/nas 第二段", start.Add(200*time.Millisecond))
			switch mode {
			case "user":
				b.UserID = "other"
			case "topic":
				b.TopicID = "other"
			case "route":
				b.Text = "/mac 解释"
			case "reference":
				b.ParentID = "other-card"
			case "event":
				b.Text = "资料 eb1_0123456789abcdef0123456789abcdef"
			case "files":
				a.Images = make([]Image, maxNativeAttachments-1)
				b.Files = []Image{{Kind: "file", Key: "f"}, {Kind: "file", Key: "g"}}
			case "max-window":
				b.ReceivedAt = start.Add(2100 * time.Millisecond).Format(time.RFC3339Nano)
			case "unknown-send":
				x.sendErr = errors.New("lost")
			}
			accept(t, e, a)
			accept(t, e, b)
			if mode == "stop" || mode == "reset" {
				text := "/stop"
				if mode == "reset" {
					text = "/reset"
				}
				accept(t, e, message("command", text))
			}
			tick(t, e)
			if mode == "stop" || mode == "reset" {
				if x.sends != 0 || x.opens != 0 {
					t.Fatal("command waited behind batch or revived cancelled input")
				}
				return
			}
			if mode == "unknown-send" {
				if x.sends != 1 || onlyTask(t, e).Phase != "needs_reconcile" {
					t.Fatal("merged send fence lost")
				}
				tick(t, e)
				if x.sends != 1 {
					t.Fatal("unknown merged send replayed")
				}
				return
			}
			for _, task := range e.Store.snapshot().Tasks {
				if len(task.DispatchSource.Parts) != 0 {
					t.Fatal("incompatible inputs merged", mode)
				}
			}
			if mode == "files" && x.uploads != maxNativeAttachments-1 {
				t.Fatal("mixed total resource bound lost", x.uploads)
			}
		})
	}
}
func TestCoalesceBatchUpperBoundAndSourceFiles(t *testing.T) {
	e, x, _, _ := testEngine(t)
	e.Config.DefaultRoute = "nas"
	e.Classifier = &fakeClassifier{d: Decision{RouteID: "nas", Action: "new_task"}}
	start := time.Now().UTC()
	now := start.Add(2 * time.Second)
	e.Now = func() time.Time { return now }
	for i := 0; i < 4; i++ {
		m := shortMessage(string(rune('a'+i)), "/nas 只读资料", start.Add(time.Duration(i)*500*time.Millisecond))
		m.Files = []Image{{Kind: "file", Key: m.ID + "1"}, {Kind: "file", Key: m.ID + "2"}}
		accept(t, e, m)
	}
	tick(t, e)
	task := onlyTask(t, e)
	if x.sends != 1 || x.uploads != maxNativeAttachments || len(task.DispatchSource.Parts) != 4 {
		t.Fatal("upper window didn't flush full compatible group")
	}
	for i, r := range task.DispatchSource.Files {
		if r.MessageID != string(rune('a'+i/2)) {
			t.Fatal("mixed attachment lost original message source")
		}
	}
}
func TestWorkspaceQueuedInputRetainsReceivedProject(t *testing.T) {
	e, x, l, _, _ := workspaceFixture(t)
	e.Config.DefaultRoute = "nas"
	workspaceCommand(t, e, l, "select", "/cd /srv/alpha")
	now := time.Now().UTC()
	e.Now = func() time.Time { return now }
	m := shortMessage("waiting", "/nas 原项目请求", now)
	accept(t, e, m)
	// Simulate another control selection after receipt; old pending input must keep alpha.
	e.Store.update(func(st *State) error {
		target := e.Config.Routes["nas"]
		target.ProjectID = "beta"
		rememberWorkspace(e.Config, st, message("x", ""), "nas", target)
		return nil
	})
	now = now.Add(time.Second)
	tick(t, e)
	if onlyTask(t, e).Target.ProjectID != "alpha" || x.sends != 1 {
		t.Fatal("pending work migrated to later selected project")
	}
}

func TestCoalesceUnknownStopReferenceDoesNotCancelOwnQueue(t *testing.T) {
	e, x, _, _ := testEngine(t)
	e.Config.DefaultRoute = "nas"
	start := time.Now().UTC()
	now := start
	e.Now = func() time.Time { return now }
	accept(t, e, shortMessage("queued", "/nas 只讨论", start))
	stop := message("unbound-stop", "/stop")
	stop.ParentID = "unknown-card"
	accept(t, e, stop)
	for _, q := range e.Store.snapshot().Inbox {
		if q.ID == "queued" && q.Cancelled {
			t.Fatal("unknown reference cancelled current queue")
		}
	}
	now = now.Add(time.Second)
	tick(t, e)
	if x.sends != 1 {
		t.Fatal("unknown stop suppressed unrelated own input")
	}
}

func TestWorkspaceStopDoesNotCancelOtherProjectQueue(t *testing.T) {
	e, _, l, _, _ := workspaceFixture(t)
	e.Config.DefaultRoute = "nas"
	workspaceCommand(t, e, l, "alpha", "/cd /srv/alpha")
	accept(t, e, message("run-alpha", "/nas alpha"))
	tick(t, e)
	alpha := onlyTask(t, e)
	e.Store.update(func(st *State) error {
		b := e.Config.Routes["nas"]
		b.ProjectID = "beta"
		rememberWorkspace(e.Config, st, message("x", ""), "nas", b)
		return nil
	})
	now := time.Now().UTC()
	e.Now = func() time.Time { return now }
	accept(t, e, shortMessage("beta-queued", "/nas beta", now))
	stop := message("stop-alpha", "/stop")
	stop.ParentID = alpha.CardID
	accept(t, e, stop)
	for _, q := range e.Store.snapshot().Inbox {
		if q.ID == "beta-queued" && q.Cancelled {
			t.Fatal("old project reference cancelled new project queue")
		}
	}
}
