package bridge

import (
	"strings"
	"testing"
)

func TestTimeoutSettingsCardAppliesBothUnitsAndClearsIdleSampling(t *testing.T) {
	e, x, messenger, _ := testEngine(t)
	e.Config.DefaultRoute = "nas"
	e.Lark = commandCardRecorder{messenger}
	lark := &Lark{Config: e.Config, Store: e.Store}
	stage66Save(t, e, func(st *State) { st.Settings.IdleTimeoutMinutes = 9 })
	accept(t, e, message("timeout-run", "/nas 合成任务"))
	tick(t, e)
	run := func(id, text string) *SessionCommand {
		t.Helper()
		return runSessionStatusCommand(t, e, message(id, text))
	}
	run("absolute", "/timeout deadline 600")
	run("idle", "/timeout idle 4")
	task := onlyTask(t, e)
	e.idleRuns = map[string]idleRunStreak{task.ID: {}}
	status := run("timeout-status", "/status")
	clickCommandCard(t, lark, commandCardClick(t, status, commandCardJSON(status), "超时设置", "timeout-menu"))
	origin := consumeResumeFlowMessage(t, e, "card:timeout-menu")
	if origin == nil || origin.Card == nil || origin.Card.TimeoutSettings == nil || origin.Card.ReplyID == "" {
		t.Fatalf("status did not open settings: %+v", origin)
	}
	base := *origin.Card.TimeoutSettings
	if base.DeadlineSeconds != 600 || base.IdleMinutes != 4 || base.IdleInherited || base.DefaultIdle != 9 {
		t.Fatalf("incorrect timeout defaults: %+v", base)
	}
	fields := map[string]any{"deadline_seconds": "0", "idle_minutes": "default"}
	action := stage68FormAction(origin, "save-timeout", fields)
	action.Action.Name = checkStage68FormWire(t, origin, commandCardJSON(origin), fields)
	forged := action
	forged.Operator.OpenID = "other"
	clickCommandCard(t, lark, forged)
	if len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("another user could submit the settings form")
	}
	clickCommandCard(t, lark, action)
	result := consumeResumeFlowMessage(t, e, "card:"+action.EventID)
	st := e.Store.snapshot()
	current := st.Tasks[task.ID]
	scope := sessionKey(task.Source, task.RouteID, task.Target)
	if result == nil || !strings.Contains(result.Reply, "已保存超时设置") || current.TimeoutSeconds != 0 ||
		current.DeadlineAt != "" || current.IdleTimeoutMinutes != 9 || st.Timeouts[scope] != 0 ||
		!st.Commands[key(origin.Source, origin.Source.ID)].Card.Used || x.sends != 1 || x.stops != 0 {
		t.Fatalf("timeout form did not commit both settings and receipt: task=%+v reply=%+v", current, result)
	}
	if _, ok := st.IdleTimeouts[scope]; ok || len(e.idleRuns) != 0 {
		t.Fatal("restoring inheritance retained an override or the prior sampling window")
	}
	action.EventID = "retry-consumed-timeout"
	clickCommandCard(t, lark, action)
	if len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("consumed form admitted a replay")
	}

	run("legacy-numeric", "/timeout 600")
	if original := onlyTask(t, e); original.TimeoutSeconds != 600 || original.IdleTimeoutMinutes != 9 {
		t.Fatal("legacy numeric deadline semantics changed")
	}
	stage66Save(t, e, func(st *State) { st.Settings.ReplyMode = "text" })
	if plain := run("text-timeout", "/timeout"); plain.Card != nil || !strings.Contains(plain.Reply, "/timeout deadline") ||
		!strings.Contains(plain.Reply, "/timeout idle") || messenger.replies[len(messenger.replies)-1] != plain.Reply {
		t.Fatalf("missing explicit unit instructions in text mode: %+v", plain)
	}
}

func TestTimeoutSettingsCardRejectsStaleRoundAndValues(t *testing.T) {
	for _, name := range []string{"new round", "intervening edit", "other project", "invalid field"} {
		t.Run(name, func(t *testing.T) {
			e, _, messenger, _ := testEngine(t)
			e.Config.DefaultRoute = "nas"
			e.Lark = commandCardRecorder{messenger}
			lark := &Lark{Config: e.Config, Store: e.Store}
			accept(t, e, message("active", "/nas 合成任务"))
			tick(t, e)
			task := onlyTask(t, e)
			scope := sessionKey(task.Source, task.RouteID, task.Target)
			origin := runSessionStatusCommand(t, e, message("timeout-form", "/timeout idle"))
			fields := map[string]any{"deadline_seconds": "900", "idle_minutes": "15"}
			if name == "invalid field" {
				fields["idle_minutes"] = "121"
			}
			action := stage68FormAction(origin, "timeout-submit", fields)
			action.Action.Name = checkStage68FormWire(t, origin, commandCardJSON(origin), fields)
			clickCommandCard(t, lark, action)
			stage66Save(t, e, func(st *State) {
				switch name {
				case "new round":
					st.Tasks[task.ID].RequestID = "new-request"
				case "intervening edit":
					st.IdleTimeouts = map[string]int{scope: 22}
					st.Tasks[task.ID].IdleTimeoutMinutes = 22
				case "other project":
					target := task.Target
					target.ProjectID, target.ProjectPath = "new-project", "/new/project"
					rememberWorkspace(e.Config, st, task.Source, task.RouteID, target)
				}
			})
			result := consumeResumeFlowMessage(t, e, "card:"+action.EventID)
			want := "已变化"
			if name == "invalid field" {
				want = "输入无效"
			}
			st := e.Store.snapshot()
			if result == nil || !strings.Contains(result.Reply, want) || st.Timeouts[scope] != 0 ||
				st.IdleTimeouts[scope] == 15 || st.Commands[key(origin.Source, origin.Source.ID)].Card.Used {
				t.Fatalf("stale or invalid form mutated the scope: %+v", result)
			}
		})
	}
}
