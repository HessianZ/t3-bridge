package bridge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestLatestRunActionUsesOnlyOwnedActivityMetadata(t *testing.T) {
	target := testConfig(t).Routes["nas"]
	page := pageFor(target)
	item := func(kind, run, source, visibility, at, status string) map[string]any {
		return map[string]any{
			"type": kind, "runId": run, "sourceThreadId": source, "visibility": visibility,
			"updatedAt": at, "status": status, "title": "private title", "text": "private command/output",
		}
	}
	page["items"] = []any{
		item("command_execution", "run", "thread", "local", "2026-11-18T12:27:03.333Z", "running"),
		item("command_execution", "run", "thread", "local", "2026-11-18T12:27:02Z", "completed"),
		item("command_execution", "other", "thread", "local", "2026-11-18T12:27:04Z", "completed"),
		item("command_execution", "", "thread", "local", "2026-11-18T12:27:04Z", "completed"),
		item("command_execution", "run", "child", "local", "2026-11-18T12:27:04Z", "completed"),
		item("command_execution", "run", "thread", "inherited", "2026-11-18T12:27:04Z", "completed"),
		item("reasoning", "run", "thread", "local", "2026-11-18T12:27:04Z", "completed"),
		item("reasoning_summary", "run", "thread", "local", "2026-11-18T12:27:04Z", "completed"),
		item("secret_request", "run", "thread", "local", "2026-11-18T12:27:04Z", "completed"),
		item("tool_call", "run", "thread", "local", "invalid", "completed"),
		item("dynamic_tool", "run", "thread", "local", "2026-11-18T12:27:06Z", "failed"),
		item("file_search", "run", "thread", "local", "2026-11-18T12:27:05Z", "completed"),
		item("web_search", "run", "thread", "local", "2026-11-18T12:27:04Z", "cancelled"),
		item("dynamic_tool", "run", "thread", "local", "2026-11-18T12:27:01Z", "completed"),
	}
	api := &T3{Calls: scriptedCaller(func(name string, args map[string]any) (any, error) {
		if name != "t3_thread_read" || args["view"] != "activity" || args["beforePosition"] != float64(1<<31-1) || args["limit"] != float64(50) || args["maxCharsPerItem"] != float64(1) || args["environmentId"] != target.EnvironmentID {
			t.Fatalf("expected bounded newest activity read, got %s %v", name, args)
		}
		return page, nil
	})}
	task := &Task{ThreadID: "thread", RunID: "run", Target: target}
	sample, err := api.readRunActivity(context.Background(), task, false, nil)
	action := sample.Action
	if err != nil || action == nil || action.Label != "命令进行中" || action.At.Format(time.RFC3339Nano) != "2026-11-18T12:27:03.333Z" {
		t.Fatalf("unexpected activity: %+v, %v", action, err)
	}
	tools := sample.Tools
	if len(tools) != 5 || tools[0].Label != "工具调用失败" || !tools[0].Finished ||
		tools[1].Label != "文件搜索已完成" || tools[2].Label != "网页搜索已中止" ||
		tools[3].Label != "命令进行中" || tools[3].Finished || tools[4].Label != "命令已完成" {
		t.Fatalf("unexpected tool metadata: %+v", tools)
	}
	page["thread"].(map[string]any)["projectId"] = "other-project"
	if sample, err := api.readRunActivity(context.Background(), task, false, nil); err == nil || sample.Action != nil {
		t.Fatal("accepted activity from an unverified thread")
	}
}

type activityExecutor struct {
	*fakeExecutor
	action *runAction
	tools  []runAction
	marker string
	err    error
	reads  int
}

func (x *activityExecutor) readRunActivity(_ context.Context, _ *Task, watchIdle bool, _ *int) (runActivitySample, error) {
	x.reads++
	sample := runActivitySample{Action: x.action, Tools: x.tools}
	if watchIdle {
		sample.IdleMarker = x.marker
	}
	return sample, x.err
}

func TestToolActivityModeUpdatesCardAndReadsTerminalMetadata(t *testing.T) {
	e, x, l, _ := testEngine(t)
	e.Config.Admins = []string{"user"}
	now := time.Date(2026, 11, 18, 12, 27, 10, 0, time.UTC)
	e.Now = func() time.Time { return now }
	x.observation.Progress = "合成阶段正文"
	api := &activityExecutor{fakeExecutor: x, tools: []runAction{
		{Label: "工具调用进行中", At: now},
		{Label: "文件搜索已完成", At: now.Add(-time.Second), Finished: true},
	}}
	e.T3 = api
	command := func(id, text string) {
		t.Helper()
		accept(t, e, message(id, "/config "+text))
		tick(t, e)
	}
	command("progress", "progress off")
	command("brief", "tools brief")
	accept(t, e, message("run", "/nas 合成任务"))
	tick(t, e)
	tick(t, e)
	id := onlyTask(t, e).CardID
	assertCard := func(want, absent string) {
		t.Helper()
		if body := l.sentCards[id]; len(l.cards) != 1 || !strings.Contains(body, want) ||
			(absent != "" && strings.Contains(body, absent)) || strings.Contains(body, "合成阶段正文") {
			t.Fatalf("unexpected original card: %s", body)
		}
	}
	assertCard("工具调用进行中", "文件搜索已完成")
	command("detailed", "tools detailed")
	assertCard("11-18 20:27:09 · 文件搜索已完成", "")
	command("invalid", "tools invalid")
	if e.Store.runSettings().ToolActivity != toolActivityDetailed || api.reads != 1 {
		t.Fatal("mode changes should reuse the sample and reject unsupported values")
	}
	command("off", "tools off")
	assertCard("主run执行中", "最近工具活动")
	command("show-progress", "progress on")
	if body := l.sentCards[id]; !strings.Contains(body, "合成阶段正文") || strings.Contains(body, "最近工具活动") {
		t.Fatalf("off mode affected assistant progress: %s", body)
	}
	command("hide-progress", "progress off")
	if api.reads != 1 {
		t.Fatal("off mode unnecessarily polled activity")
	}
	command("restore", "tools detailed")
	x.observation = Observation{Status: "failed", Summary: "合成结束说明"}
	api.tools = []runAction{
		{Label: "工具调用失败", At: now, Finished: true},
		{Label: "命令进行中", At: now.Add(-time.Second)},
	}
	reads := api.reads
	tick(t, e)
	assertCard("工具调用失败", "工具调用进行中")
	assertCard("命令进行中（终态未确认）", "")
	assertCard("合成结束说明", "最近工具活动")
	if api.reads != reads+1 || x.sends != 1 {
		t.Fatal("terminal transition did not take one fresh sample of the original run")
	}
}

func TestToolActivityModeInTerminalTextWithoutLiveReply(t *testing.T) {
	e, x, l, _ := testEngine(t)
	if err := e.Store.update(func(st *State) error {
		st.Settings = RunSettings{ReplyMode: "text", ToolActivity: toolActivityBrief}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	api := &activityExecutor{fakeExecutor: x, tools: []runAction{
		{Label: "命令已完成", Finished: true},
	}}
	e.T3 = api
	var body string
	l.onNotify = func(task *Task) { body = task.TextReplyBody }
	accept(t, e, message("one", "/nas 合成任务"))
	tick(t, e)
	tick(t, e)
	if api.reads != 0 || l.notifications != 0 {
		t.Fatal("text mode sampled or notified before the terminal result")
	}
	x.observation = Observation{Status: "completed", Summary: "合成最终回复"}
	tick(t, e)
	tick(t, e)
	if api.reads != 1 || l.notifications != 1 || !strings.Contains(body, "合成最终回复") || !strings.Contains(body, "命令已完成") {
		t.Fatalf("unexpected terminal text: %q", body)
	}
}

func TestLongRunActivityPatchesOriginalCard(t *testing.T) {
	e, x, l, _ := testEngine(t)
	now := time.Date(2026, 11, 18, 12, 27, 10, 0, time.UTC)
	e.Now = func() time.Time { return now }
	api := &activityExecutor{fakeExecutor: x, action: &runAction{Label: "命令进行中", At: now.Add(-3 * time.Second)}}
	e.T3 = api
	accept(t, e, message("long", "/nas 检查合成任务"))
	tick(t, e)
	original := onlyTask(t, e).CardID
	tick(t, e)
	assertCard := func(want, absent string) {
		t.Helper()
		body := l.sentCards[original]
		if onlyTask(t, e).CardID != original || len(l.cards) != 1 || !strings.Contains(body, want) || (absent != "" && strings.Contains(body, absent)) {
			t.Fatalf("unexpected original card: %s", body)
		}
	}
	assertCard("11-18 20:27:07 · 命令进行中", "正在生成可见回复")
	notifications := l.notifications
	now = now.Add(time.Second)
	tick(t, e)
	if api.reads != 1 || l.notifications != notifications {
		t.Fatal("unchanged activity caused another read or patch")
	}
	now = now.Add(15 * time.Second)
	api.err = errors.New("private backend error")
	tick(t, e)
	assertCard("T3 活动暂时无法查询", "private backend error")
	assertCard("11-18 20:27:07 · 命令进行中", "")
	api.err = nil
	api.action = nil
	now = now.Add(time.Minute)
	tick(t, e)
	assertCard("T3 最近查询成功 11-18 20:28", "T3 活动暂时无法查询")
	assertCard("11-18 20:27:07 · 命令进行中", "")
	api.action = &runAction{Label: "命令已完成", At: now.Add(time.Second)}
	now = now.Add(15 * time.Second)
	tick(t, e)
	assertCard("命令已完成", "命令进行中")
	next := onlyTask(t, e)
	beginReply(next, message("next", "/nas 继续"), true, RunSettings{})
	if next.RunActivity != nil {
		t.Fatal("old run activity leaked into a new round")
	}
	setProgress := func(show bool) {
		t.Helper()
		if err := e.Store.update(func(s *State) error {
			s.Settings.Progress = &show
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	reads := api.reads
	setProgress(false)
	now = now.Add(15 * time.Second)
	tick(t, e)
	assertCard("主run执行中", "最近动作")
	if api.reads != reads {
		t.Fatal("activity read with progress disabled")
	}
	setProgress(true)
	tick(t, e)
	assertCard("命令已完成", "")
	x.observation = Observation{Status: "completed", Summary: "合成最终回复"}
	tick(t, e)
	assertCard("合成最终回复", "最近动作")
	if x.sends != 1 || x.stops != 0 {
		t.Fatal("activity tracking changed execution")
	}
}
