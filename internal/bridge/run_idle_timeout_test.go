package bridge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunIdleTimeoutCommands(t *testing.T) {
	e, x, l, _ := testEngine(t)
	e.Config.Admins = []string{"user"}
	e.Config.DefaultRoute = "nas"
	run := func(id, user, text string) {
		t.Helper()
		m := message(id, text)
		m.UserID = user
		accept(t, e, m)
		tick(t, e)
	}
	run("first", "user", "/nas 合成任务")
	if onlyTask(t, e).IdleTimeoutMinutes != 0 {
		t.Fatal("idle timeout enabled by default")
	}
	run("denied", "other", "/config idle-timeout 3")
	if e.Store.runSettings().IdleTimeoutMinutes != 0 || !strings.Contains(l.replies[len(l.replies)-1], "owner/admin") {
		t.Fatal("ordinary user could set the default")
	}
	run("default", "user", "/config idle-timeout 3")
	if e.Store.runSettings().IdleTimeoutMinutes != 3 || onlyTask(t, e).IdleTimeoutMinutes != 0 {
		t.Fatal("default affected an existing round or was not saved")
	}
	run("override", "user", "/timeout idle 2")
	run("absolute", "user", "/timeout 86400")
	run("invalid", "user", "/timeout idle 121")
	task := onlyTask(t, e)
	if task.IdleTimeoutMinutes != 2 || task.TimeoutSeconds != 86400 || task.DeadlineAt == "" {
		t.Fatal("idle override and absolute deadline were not independent")
	}
	run("off", "user", "/timeout idle off")
	scope := sessionKey(task.Source, task.RouteID, task.Target)
	st := e.Store.snapshot()
	if n, ok := st.IdleTimeouts[scope]; !ok || n != 0 || onlyTask(t, e).IdleTimeoutMinutes != 0 {
		t.Fatal("explicit off did not override the saved default")
	}
	run("inherit", "user", "/timeout idle default")
	st = e.Store.snapshot()
	if _, ok := st.IdleTimeouts[scope]; ok || onlyTask(t, e).IdleTimeoutMinutes != 3 {
		t.Fatal("inheritance did not clear the override")
	}
	run("disable-default", "user", "/config idle-timeout off")
	if onlyTask(t, e).IdleTimeoutMinutes != 3 {
		t.Fatal("changing the default edited an existing active round")
	}
	x.observation.Status = "completed"
	tick(t, e)
	run("next", "user", "/nas 同一会话下一轮")
	if round := onlyTask(t, e); round.IdleTimeoutMinutes != 0 || round.IdleTimeoutExpired || round.TimeoutSeconds != 86400 || x.sends != 2 {
		t.Fatal("new round did not snapshot the updated default independently of the absolute timeout")
	}
}

type idleTestExecutor struct {
	*activityExecutor
	observeErr, prepareErr error
	beforeStop             func(*Task)
}

func (x *idleTestExecutor) Observe(ctx context.Context, target Target, task *Task) (Observation, error) {
	if x.observeErr != nil {
		return Observation{}, x.observeErr
	}
	return x.fakeExecutor.Observe(ctx, target, task)
}

func (x *idleTestExecutor) prepareIdleStop(ctx context.Context, task *Task, _ string, _ *int) (func() error, error) {
	if x.prepareErr != nil {
		return nil, x.prepareErr
	}
	return func() error {
		x.beforeStop(task)
		return x.Stop(ctx, task.Target, task)
	}, nil
}

func TestRunIdleTimeoutRequiresConsecutiveEvidence(t *testing.T) {
	for _, tc := range []string{"activity", "activity-unavailable", "observe-unavailable", "approval", "child", "queued", "waiting", "unknown", "other-run", "new-round", "restart", "poll-gap", "final-recheck"} {
		t.Run(tc, func(t *testing.T) {
			e, x, _, _ := testEngine(t)
			now := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
			e.Now = func() time.Time { return now }
			api := &idleTestExecutor{activityExecutor: &activityExecutor{fakeExecutor: x, marker: "initial"}}
			api.beforeStop = func(task *Task) {
				t.Helper()
				saved := e.Store.task(task.ID)
				if !saved.StopRequested || !saved.StopSent || !saved.IdleTimeoutExpired || !saved.NeedsReconcile || saved.RunID != "run" {
					t.Fatalf("missing original run stop receipt before call: %+v", saved)
				}
			}
			e.T3 = api
			if err := e.Store.update(func(st *State) error {
				off := false
				st.Settings = RunSettings{Progress: &off, ReplyMode: "text", IdleTimeoutMinutes: 1}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			accept(t, e, message("first", "/nas 合成任务"))
			tick(t, e)
			sample := func(gap time.Duration) {
				t.Helper()
				now = now.Add(gap)
				tick(t, e)
			}
			for i := 0; i < 4; i++ {
				sample(runActivityInterval)
			}
			running := x.observation
			wantSends := 1
			switch tc {
			case "activity":
				api.marker = "updated"
			case "activity-unavailable":
				api.err = errors.New("synthetic activity read failure")
			case "observe-unavailable":
				api.observeErr = errors.New("synthetic observe failure")
			case "approval":
				x.observation.PendingApprovals = 1
			case "child":
				x.observation.Tasks = []string{"synthetic-child"}
			case "queued", "waiting", "unknown":
				x.observation.Status = tc
			case "other-run":
				x.observation.RunID = "other"
			case "new-round":
				x.observation.Status = "completed"
				tick(t, e)
				x.observation = running
				accept(t, e, message("next", "/nas 下一轮合成任务"))
				tick(t, e)
				wantSends = 2
			case "restart":
				task := onlyTask(t, e)
				if err := e.Store.Close(); err != nil {
					t.Fatal(err)
				}
				s, err := OpenStore(e.Config.StateDir)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				e = &Engine{Config: e.Config, Store: s, Now: e.Now, T3: api, Lark: e.Lark}
				if restored := e.Store.task(task.ID); restored.IdleTimeoutMinutes != 1 || restored.RunID != task.RunID {
					t.Fatal("lost idle configuration or run identity across recovery")
				}
			case "poll-gap":
				now = now.Add(time.Minute)
			case "final-recheck":
				api.prepareErr = errors.New("activity became unsafe before stopping")
			}
			sample(runActivityInterval)
			if x.stops != 0 || onlyTask(t, e).StopRequested {
				t.Fatal("stopped without a continuous, eligible window")
			}
			x.observation = running
			api.err, api.observeErr, api.prepareErr = nil, nil, nil
			sample(time.Minute)
			for i := 0; i < 3; i++ {
				sample(runActivityInterval)
			}
			if x.stops != 0 {
				t.Fatal("inherited idle duration across the pause")
			}
			x.stopErr = errors.New("synthetic uncertain interrupt")
			sample(runActivityInterval)
			sample(runActivityInterval)
			task := onlyTask(t, e)
			if x.stops != 1 || x.sends != wantSends || task.Phase != "stopped" || !task.NeedsReconcile || task.TimeoutExpired || !task.IdleTimeoutExpired {
				t.Fatalf("unexpected idle stop receipt: %+v, sends=%d stops=%d", task, x.sends, x.stops)
			}
			if api.reads == 0 || task.RunActivity != nil {
				t.Fatal("idle sampling depended on card progress or exposed activity in a text round")
			}
		})
	}
}

func TestRunIdlePublicActivityEvidence(t *testing.T) {
	target := testConfig(t).Routes["nas"]
	task := &Task{ID: "owned", RequestID: "request", ThreadID: "thread", RunID: "run", Target: target}
	newPage := func() map[string]any {
		p := pageFor(target)
		thread := p["thread"].(map[string]any)
		thread["activeRunId"], thread["status"] = "run", "running"
		p["recentRuns"].([]any)[0].(map[string]any)["status"] = "running"
		return p
	}
	item := func(id, kind, status, run, source, visibility, at string) map[string]any {
		return map[string]any{"itemId": id, "type": kind, "status": status, "runId": run, "sourceThreadId": source, "visibility": visibility, "updatedAt": at, "createdBy": "agent", "text": "private payload"}
	}
	const old = "2000-01-01T12:00:00Z"
	page := newPage()
	page["items"] = []any{
		item("assistant", "assistant_message", "running", "run", "thread", "local", old),
		item("thinking", "reasoning", "running", "run", "thread", "local", "invalid"),
		item("old-run", "command_execution", "running", "old", "thread", "local", "invalid"),
		item("child", "command_execution", "running", "run", "child", "local", "invalid"),
		item("context", "command_execution", "running", "run", "thread", "inherited", "invalid"),
	}
	var older map[string]any
	readFailed, interrupted := false, false
	api := &T3{Calls: scriptedCaller(func(name string, args map[string]any) (any, error) {
		switch name {
		case "t3_thread_read":
			if args["view"] != "activity" {
				return newPage(), nil
			}
			if args["maxCharsPerItem"] != float64(1) || args["threadId"] != task.ThreadID || args["environmentId"] != target.EnvironmentID {
				t.Fatalf("unexpected activity arguments: %v", args)
			}
			if readFailed {
				return nil, errors.New("activity unavailable")
			}
			if args["beforePosition"] == float64(10) && older != nil {
				return older, nil
			}
			return page, nil
		case "t3_thread_configure":
			return configurationFor(target), nil
		case "t3_thread_interrupt":
			if args["threadId"] != task.ThreadID || args["runId"] != task.RunID || args["clientRequestId"] != stable(task.ID, task.RequestID, "stop") {
				t.Fatalf("wrong interrupt identity: %v", args)
			}
			if _, hasStop := args["stop"]; hasStop {
				t.Fatalf("unexpected stop field in interrupt: %v", args)
			}
			interrupted = true
			return map[string]any{"threadId": task.ThreadID, "runId": task.RunID, "status": "interrupt_requested"}, nil
		default:
			t.Fatalf("unexpected action %s", name)
			return nil, nil
		}
	})}
	ctx := context.Background()
	trusted, err := api.readRunActivity(ctx, task, true, nil)
	if err != nil || trusted.IdleMarker == "" || trusted.Action.Label != "回复进行中" {
		t.Fatalf("missing trusted public activity: %+v %v", trusted, err)
	}
	checkBlocked := func() {
		t.Helper()
		stop, err := api.prepareIdleStop(ctx, task, trusted.IdleMarker, nil)
		if stop != nil || err == nil || interrupted {
			t.Fatal("idle interrupt was prepared despite ambiguous or changed public activity")
		}
	}
	thread := page["thread"].(map[string]any)
	thread["activeRunId"] = "other"
	checkBlocked()
	thread["activeRunId"] = "run"
	thread["pendingRequestCount"] = 1
	checkBlocked()
	delete(thread, "pendingRequestCount")
	checkBlocked()
	thread["pendingRequestCount"] = 0
	readFailed = true
	checkBlocked()
	readFailed = false

	page["hasMore"] = true
	checkBlocked()
	page["earlierPosition"] = 10
	older = newPage()
	older["items"] = []any{item("long-tool", "mcp_tool_call", "running", "run", "thread", "local", old)}
	checkBlocked()
	older["items"] = []any{item("tool", "mcp_tool_call", "completed", "run", "thread", "local", "invalid")}
	checkBlocked()
	boundary := item("request", "user_message", "completed", "run", "thread", "local", old)
	boundary["position"] = 5
	older["items"], older["hasMore"] = []any{boundary}, true
	checkBlocked()
	position := 5
	anchored, err := api.readRunActivity(ctx, task, true, &position)
	if err != nil || anchored.IdleMarker != trusted.IdleMarker {
		t.Fatal("verified input did not bound the run's activity history", err)
	}
	page["hasMore"] = false
	page["items"].([]any)[0].(map[string]any)["updatedAt"] = "2000-01-01T12:00:01Z"
	checkBlocked()
	page["items"].([]any)[0].(map[string]any)["updatedAt"] = old
	stop, err := api.prepareIdleStop(ctx, task, trusted.IdleMarker, nil)
	if err != nil || stop == nil || interrupted {
		t.Fatalf("expected a verified, unsent stop action: %v", err)
	}
	if err := stop(); err != nil || !interrupted {
		t.Fatalf("expected interrupt of the original run: %v", err)
	}
}
