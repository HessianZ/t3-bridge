package bridge

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func doctorFixture(t *testing.T) (*Engine, *fakeExecutor, *fakeMessenger) {
	t.Helper()
	e, x, l, _ := testEngine(t)
	e.Config.Admins = []string{"user"}
	e.Config.Topics = nil
	e.Config.DefaultRoute = "nas"
	safe := e.Config.Routes["nas"]
	safe.InteractionMode = "plan"
	safe.ProjectID = "synthetic-diagnostic-project"
	e.Config.Routes["diagnostic"] = safe
	return e, x, l
}

func doctorMessage(id, command string) Message {
	m := message(id, command)
	m.ChatType, m.TopicID = "p2p", ""
	return m
}

func TestDoctorEchoKeepsConversationAndDeliversOnlyVerdict(t *testing.T) {
	e, x, l := doctorFixture(t)
	chat := doctorMessage("normal", "ordinary task")
	accept(t, e, chat)
	tick(t, e)
	ordinary := onlyTask(t, e)
	x.observation = Observation{Status: "completed", Summary: "ordinary answer"}
	tick(t, e)

	m := doctorMessage("echo", "/doctor --echo diagnostic")
	m.Quote, m.History = "synthetic quoted detail", "synthetic history"
	m.Images = []Image{{Key: "synthetic attachment"}}
	x.observation = Observation{Status: "running", Progress: "synthetic progress"}
	accept(t, e, m)
	accept(t, e, m)
	tick(t, e)
	state := e.Store.snapshot()
	diag := state.Tasks[stable(m.ChatID, m.TopicID, m.ID)]
	if diag == nil || !diag.TextReply || diag.CardID != "" || diag.Process != nil || diag.Target.ProjectID != "synthetic-diagnostic-project" || x.opens != 2 || x.sends != 2 || x.uploads != 0 {
		t.Fatalf("diagnostic did not start one isolated text task: %+v", diag)
	}
	if x.request != doctorEchoPrompt+"\nBridge request: "+diag.RequestID {
		t.Fatal("diagnostic sent data beyond the fixed prompt and receipt ID")
	}
	if got := currentTask(e.Config, &state, chat, ""); got == nil || got.ID != ordinary.ID {
		t.Fatal("diagnostic replaced the current workspace or conversation")
	}
	if got := sessionHistory(e.Config, &state, m, "diagnostic"); len(got) != 0 {
		t.Fatal("diagnostic thread appeared in resumable history")
	}
	var results []string
	l.onNotify = func(task *Task) {
		if task.ID == diag.ID {
			results = append(results, task.TextReplyBody)
		}
	}
	x.observation = Observation{Status: "completed", Summary: " OK\n", Unsettled: true}
	tick(t, e)
	if len(results) > 0 {
		t.Fatal("reported a result before native work settled")
	}
	x.observation.Unsettled = false
	tick(t, e)
	tick(t, e)
	state = e.Store.snapshot()
	diag = state.Tasks[diag.ID]
	if len(results) != 1 || !strings.Contains(results[0], "模型回声通过（OK）") || state.Bindings[key(m, m.ID)] != "" || state.Bindings[key(m, diag.CardID)] != "" {
		t.Fatalf("diagnostic verdict did not remain a one-off reply: %v", results)
	}
}

func TestDoctorEchoGatesAndRateLimit(t *testing.T) {
	e, x, l := doctorFixture(t)
	var failures []CommandFailureStep
	e.OnCommandFailed = func(step CommandFailureStep) { failures = append(failures, step) }
	now := e.now()
	e.Now = func() time.Time { return now }
	for _, test := range []struct{ cmd, user, chat, reply string }{
		{"/doctor", "other", "p2p", "配置已解析"},
		{"/doctor --echo diagnostic", "other", "p2p", "管理员"},
		{"/doctor --echo diagnostic", "user", "group", "私聊"},
		{"/doctor --echo nas", "user", "p2p", "plan / approval-required"},
		{"/doctor --echo absent", "user", "p2p", "诊断路由"},
		{"/doctor --echo diagnostic extra", "user", "p2p", "用法"},
	} {
		m := doctorMessage(stable(test.cmd, test.user, test.chat), test.cmd)
		m.UserID, m.ChatType = test.user, test.chat
		accept(t, e, m)
		tick(t, e)
		if x.opens != 0 || x.preflights != 0 || !strings.Contains(l.replies[len(l.replies)-1], test.reply) {
			t.Fatalf("%q crossed the diagnostic gate: %v", test.cmd, l.replies)
		}
	}
	accept(t, e, doctorMessage("start", "/doctor --echo diagnostic"))
	tick(t, e)
	accept(t, e, doctorMessage("busy", "/doctor --echo diagnostic"))
	tick(t, e)
	if x.sends != 1 || !strings.Contains(l.replies[len(l.replies)-1], "待收口") {
		t.Fatal("opened a second diagnostic while one was active")
	}
	x.observation = Observation{Status: "completed", Summary: "synthetic unexpected model output"}
	var verdict string
	l.onNotify = func(task *Task) { verdict = task.TextReplyBody }
	tick(t, e)
	if !strings.Contains(verdict, "未收到预期") || strings.Contains(verdict, "synthetic unexpected") || onlyTask(t, e).Result != "" {
		t.Fatal("unexpected model output was delivered or persisted")
	}
	accept(t, e, doctorMessage("cooldown", "/doctor --echo diagnostic"))
	tick(t, e)
	if x.sends != 1 || !strings.Contains(l.replies[len(l.replies)-1], "30 秒") {
		t.Fatal("operator cooldown did not apply after completion")
	}
	now = now.Add(31 * time.Second)
	accept(t, e, doctorMessage("after-cooldown", "/doctor --echo diagnostic"))
	tick(t, e)
	if x.sends != 2 {
		t.Fatal("operator cooldown did not release")
	}
	if len(failures) != 1 || failures[0] != CommandFailureDoctorRun {
		t.Fatalf("diagnostic declines or later ticks produced failure samples: %v", failures)
	}
}

func TestDoctorEchoTimeoutRecoversOriginalRequest(t *testing.T) {
	e, x, l := doctorFixture(t)
	now := e.now()
	e.Now = func() time.Time { return now }
	x.sendErr = errors.New("synthetic lost Send receipt")
	accept(t, e, doctorMessage("lost", "/doctor --echo diagnostic"))
	tick(t, e)
	before := onlyTask(t, e)
	if before.Phase != "needs_reconcile" || before.RequestMessage == "" {
		t.Fatal("send without a response did not keep the durable echo receipt")
	}
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	e.Store = s
	t.Cleanup(func() { s.Close() })
	now = now.Add(doctorEchoTimeout)
	tick(t, e)
	tick(t, e)
	after := onlyTask(t, e)
	if !after.TimeoutExpired || after.Phase != "stopped" || !after.NeedsReconcile || after.RequestID != before.RequestID || after.RunID != "run" || x.opens != 1 || x.sends != 1 || x.stops != 1 {
		t.Fatalf("restart did not stop only the original native run: %+v", after)
	}
	accept(t, e, doctorMessage("after-stop-ack", "/doctor --echo diagnostic"))
	tick(t, e)
	if x.sends != 1 || !strings.Contains(l.replies[len(l.replies)-1], "待收口") {
		t.Fatal("stop acknowledgement was treated as a quiet run")
	}
	x.observation = Observation{FoundRequest: true, RunID: "run", Status: "interrupted", OriginalRunSettled: true}
	tick(t, e)
	if err := e.Store.DrainCheck(e.Config.AppID); err != nil {
		t.Fatalf("confirmed echo still blocks a clean drain: %v", err)
	}
	accept(t, e, doctorMessage("after-native-stop", "/doctor --echo diagnostic"))
	x.sendErr = nil
	tick(t, e)
	if x.sends != 2 || x.stops != 1 {
		t.Fatal("native settlement did not release the gate or retried Stop")
	}
}
