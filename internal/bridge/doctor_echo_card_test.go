package bridge

import (
	"context"
	"maps"
	"strings"
	"testing"
	"time"
)

func setupDoctorEchoCard(t *testing.T) (*Engine, *Lark, *accessChannel, *fakeExecutor, map[string][]replyModeRequest) {
	t.Helper()
	e, lark, ch, executor, fixture := setupStage68RunSettings(t)
	e.Config.DefaultRoute, lark.Config.DefaultRoute = "nas", "nas"
	base := e.Config.Routes["nas"]
	base.Users = append(base.Users, stage68Admin)
	e.Config.Routes["nas"] = base
	base.ProjectID, base.InteractionMode = "synthetic-echo-project", "plan"
	e.Config.Routes["echo-a"] = base
	return e, lark, ch, executor, captureReplyModes(t, fixture)
}

func openDoctorEchoCard(t *testing.T, e *Engine, ch *accessChannel, replies map[string][]replyModeRequest) (*SessionCommand, string) {
	t.Helper()
	const id = "doctor"
	accessDMCommand(t, e, ch, id, stage68Admin, "/doctor")
	origin := e.Store.snapshot().Commands[key(Message{ChatID: "dm"}, id)]
	sent := replies[id]
	if origin == nil || origin.Card == nil || origin.Card.ReplyID == "" || len(sent) != 1 || sent[0].MsgType != "interactive" {
		t.Fatalf("missing stored /doctor card and IM reply: command=%+v, replies=%+v", origin, sent)
	}
	return origin, sent[0].Content
}

func TestDoctorEchoCardShowsOnlyEligibleRoutesInAdminDM(t *testing.T) {
	e, _, ch, executor, replies := setupDoctorEchoCard(t)
	e.Version = "synthetic-version"
	const secondRoute = "echo-synthetic-route-identifier-longer-than-48-characters"
	base := e.Config.Routes["echo-a"]
	second := base
	second.ProjectID = "synthetic-second-project"
	e.Config.Routes[secondRoute] = second
	restricted := base
	restricted.Users = []string{"other"}
	e.Config.Routes["restricted"] = restricted
	unsafe := base
	unsafe.RuntimeMode = "full-access"
	e.Config.Routes["unsafe"] = unsafe

	origin, body := openDoctorEchoCard(t, e, ch, replies)
	for _, route := range []string{"echo-a", secondRoute} {
		action := commandCardClick(t, origin, body, "模型回声 · "+route, "inspect-"+route)
		if action.Action.Value["action"] != commandCardActionType || len(action.Action.Value) != 2 ||
			!strings.Contains(origin.Reply, "/doctor --echo "+route) || !strings.Contains(body, "/doctor --echo "+route) {
			t.Errorf("route %q missing text entry or opaque command-card action", route)
		}
	}
	if !strings.Contains(body, "synthetic-version") {
		t.Error("diagnostic card omits the receiver version")
	}
	for _, route := range []string{"nas", "restricted", "unsafe"} {
		if strings.Contains(body, "模型回声 · "+route) || strings.Contains(body, "/doctor --echo "+route) {
			t.Errorf("diagnostic card offers ineligible route %q", route)
		}
	}

	accessDMCommand(t, e, ch, "member-doctor", "other", "/doctor")
	accessCommand(t, e, ch, "group-doctor", stage68Admin, "chat", "/doctor")
	for _, id := range []string{"member-doctor", "group-doctor"} {
		got := replies[id]
		if len(got) != 1 || !strings.Contains(got[0].Content, "配置已解析") ||
			strings.Contains(got[0].Content, "模型回声 · ") || strings.Contains(got[0].Content, "/doctor --echo echo-a") {
			t.Errorf("passive diagnostic reply exposed a private admin control for %s: %+v", id, got)
		}
	}
	if state := e.Store.snapshot(); len(state.Tasks) != 0 || executor.preflights != 0 || executor.opens != 0 || executor.sends != 0 {
		t.Fatal("passive diagnostics attempted a model probe")
	}
}

func TestDoctorEchoCardLaunchesOnceAndRepliesToOriginalCard(t *testing.T) {
	e, lark, ch, executor, replies := setupDoctorEchoCard(t)
	now := e.now()
	e.Now = func() time.Time { return now }
	accessDMCommand(t, e, ch, "ordinary", stage68Admin, "/nas synthetic ordinary conversation")
	ordinary := onlyTask(t, e)
	executor.observation = Observation{Status: "completed", Summary: "synthetic ordinary result"}
	tick(t, e)
	before := e.Store.snapshot()
	if executor.sends != 1 || before.Tasks[ordinary.ID].Phase != "completed" {
		t.Fatal("ordinary conversation fixture did not settle")
	}
	executor.observation = Observation{Status: "running", RunID: "run", FoundRequest: true}

	origin, body := openDoctorEchoCard(t, e, ch, replies)
	const event = "echo-click"
	action := commandCardClick(t, origin, body, "模型回声 · echo-a", event)
	forged := commandCardClick(t, origin, body, "模型回声 · echo-a", "forged")
	forged.Action.Value["routeId"] = "nas"
	clickCommandCard(t, lark, forged)
	if len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("callback accepted a client-supplied route")
	}
	clickCommandCard(t, lark, action)
	clickCommandCard(t, lark, action)
	action.EventID = "different-click"
	clickCommandCard(t, lark, action)
	tick(t, e)

	state := e.Store.snapshot()
	id := stable(origin.Source.ChatID, origin.Source.TopicID, "card:"+event)
	probe := state.Tasks[id]
	if probe == nil || probe.RouteID != "echo-a" || probe.Target.ProjectID != "synthetic-echo-project" ||
		probe.Source.ID != "card:"+event || probe.ReplySource == nil || probe.ReplySource.ID != origin.Card.ReplyID ||
		!probe.TextReply || len(state.Tasks) != 2 || executor.opens != 2 || executor.sends != 2 {
		t.Fatalf("expected one isolated probe with the stored route and reply anchor: %+v (opens=%d, sends=%d)", probe, executor.opens, executor.sends)
	}
	const prompt = "Bridge model echo check. Do not use tools or inspect files or history. Reply exactly: OK"
	if executor.request != prompt+"\nBridge request: "+probe.RequestID || executor.uploads != 0 {
		t.Fatal("echo dispatch did not use the fixed prompt and receipt ID only")
	}

	executor.observation = Observation{Status: "completed", RunID: "run", FoundRequest: true, Summary: " OK\n"}
	tick(t, e)
	// A distinct event remains unable to launch after the user's cooldown has expired.
	now = now.Add(doctorEchoCooldown + time.Second)
	action.EventID = "after-cooldown"
	clickCommandCard(t, lark, action)
	tick(t, e)

	state = e.Store.snapshot()
	probe = state.Tasks[id]
	var verdicts int
	for _, sent := range replies[origin.Card.ReplyID] {
		if sent.MsgType == "text" && sent.UUID == cardUUID(probe) && strings.Contains(sent.Content, "模型回声通过（OK）") {
			verdicts++
		}
	}
	if verdicts != 1 || len(replies["card:"+event]) != 0 || probe.CardID == "" || probe.Phase != "completed" ||
		executor.sends != 2 || len(state.Tasks) != 2 {
		t.Fatalf("expected a single delivered verdict as an IM reply to the original card: %+v", replies[origin.Card.ReplyID])
	}
	current := currentTask(e.Config, &state, origin.Source, "")
	if current == nil || current.ID != ordinary.ID || !maps.Equal(state.Current, before.Current) ||
		!maps.Equal(state.Selected, before.Selected) || !maps.Equal(state.Bindings, before.Bindings) {
		t.Fatal("diagnostic changed the ordinary conversation mappings")
	}
}

func TestDoctorEchoCardRechecksQueuedSelectionBeforeDispatch(t *testing.T) {
	for _, change := range []string{"model", "project", "route ACL", "admin ACL"} {
		t.Run(change, func(t *testing.T) {
			e, lark, ch, executor, replies := setupDoctorEchoCard(t)
			origin, body := openDoctorEchoCard(t, e, ch, replies)
			clickCommandCard(t, lark, commandCardClick(t, origin, body, "模型回声 · echo-a", "stale"))
			if inbox := e.Store.snapshot().Inbox; len(inbox) != 1 || inbox[0].ID != "card:stale" {
				t.Fatalf("callback did not reach the durable inbox before the change: %+v", inbox)
			}

			target := e.Config.Routes["echo-a"]
			switch change {
			case "model":
				target.Model = "synthetic-replacement-model"
			case "project":
				target.ProjectID = "synthetic-replacement-project"
			case "route ACL", "admin ACL":
				request := LocalAccessPolicyRequest{Kind: "admin", UserID: stage68Admin, Mode: "deny"}
				if change == "route ACL" {
					request.Kind, request.RouteID = "route", "echo-a"
				}
				if _, err := e.LocalAccessPolicy(context.Background(), request); err != nil {
					t.Fatal(err)
				}
			}
			e.Config.Routes["echo-a"] = target
			tick(t, e)
			if len(e.Store.snapshot().Inbox) != 0 || executor.preflights != 0 || executor.opens != 0 || executor.sends != 0 {
				t.Fatalf("queued card callback reached T3 after %s changed", change)
			}
		})
	}
}
