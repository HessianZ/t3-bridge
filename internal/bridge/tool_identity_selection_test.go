package bridge

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	channel "github.com/larksuite/channel-sdk-go"
)

const personalIdentityRoute = "Work.Personal"

type toolIdentitySelectionFixture struct {
	engine  *Engine
	lark    *Lark
	replies *fakeMessenger
	source  Message
}

func newToolIdentitySelectionFixture(t *testing.T) toolIdentitySelectionFixture {
	t.Helper()
	e, _, replies, _ := testEngine(t)
	e.Config = personalToolConfig(t, e.Config)
	personal := e.Config.Routes["nas"]
	personal.ProjectID, personal.ProviderID = "personal-base", "personal-provider"
	e.Config.Routes[personalIdentityRoute] = personal
	installGuardedToolTestRoute(t, e)
	bot := e.Config.Routes["nas"]
	bot.ProviderID, bot.Users = "bot-provider", e.Config.Users
	e.Config.Routes["nas"] = bot
	other := e.Config.Routes["mac"]
	other.Users = e.Config.Users
	e.Config.Routes["mac"] = other
	differentRuntime := personal
	differentRuntime.RuntimeMode = "auto"
	e.Config.Routes["other-runtime"] = differentRuntime
	e.Config.Admins = []string{"ou_personal", "ou_other"}
	e.Config.DefaultRoute, e.Config.Topics = "nas", nil
	e.Config.access = &accessRuntime{}
	if err := e.Config.Validate(); err != nil {
		t.Fatal(err)
	}
	e.Lark = commandCardRecorder{replies}
	// Any Executor call in these commands is unexpected.
	e.T3 = struct{ Executor }{}
	e.verifyToolBot = func(context.Context, string, ToolBotBinding) error {
		t.Fatal("identity selection invoked bot verification")
		return nil
	}
	e.verifyToolUser = func(context.Context, string, ToolUserBinding) error {
		t.Fatal("identity selection invoked user verification")
		return nil
	}
	m := Message{ID: "history", ChatID: "chat", TopicID: "topic", ChatType: "p2p", UserID: "ou_personal"}
	otherScope := m
	otherScope.ID, otherScope.TopicID = "other-run", "other-topic"
	f := toolIdentitySelectionFixture{e, &Lark{Config: e.Config, Store: e.Store}, replies, m}
	f.update(t, func(st *State) {
		st.AccountID = e.Config.AppID
		w := Workspace{ProjectID: "effective-project", Path: "/synthetic/workspace"}
		st.Workspaces = map[string]Workspace{}
		for _, route := range []string{"nas", personalIdentityRoute, "other-runtime"} {
			st.Workspaces[workspaceKey(e.Config, m, route)] = w
		}
		for _, task := range []*Task{
			{ID: m.ID, Source: m, RouteID: "nas", Phase: "completed",
				ThreadID: "old-thread", RequestID: "old-request", RunID: "old-run"},
			{ID: otherScope.ID, Source: otherScope, RouteID: "mac", Phase: "tracking",
				ThreadID: "active-thread", RequestID: "active-request", RunID: "active-run"},
		} {
			task.Target = effectiveTarget(e.Config, st, task.Source, task.RouteID)
			task.BaseTargetID, task.ReceiverAppID = targetIdentity(e.Config.Routes[task.RouteID]), e.Config.AppID
			st.Tasks[task.ID] = task
			setCurrent(e.Config, st, task.Source, task)
		}
	})
	return f
}

func (f toolIdentitySelectionFixture) update(t *testing.T, edit func(*State)) {
	t.Helper()
	if err := f.engine.Store.update(func(st *State) error {
		edit(st)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func (f toolIdentitySelectionFixture) command(t *testing.T, id, text string) *SessionCommand {
	t.Helper()
	m := f.source
	m.ID, m.Text = id, text
	accept(t, f.engine, m)
	return consumeResumeFlowMessage(t, f.engine, id)
}

func (f toolIdentitySelectionFixture) choice(t *testing.T, id, current, candidate string) (*SessionCommand, channel.CardActionEvent) {
	t.Helper()
	cmd := f.command(t, id, "/config identity")
	if cmd == nil || cmd.Card == nil || cmd.Card.ReplyID == "" || !strings.Contains(cmd.Reply, current) || !strings.Contains(cmd.Reply, candidate) {
		t.Fatalf("identity listing did not report %q and %q in a delivered card: %+v", current, candidate, cmd)
	}
	var choices []CommandButton
	for _, button := range cmd.Card.Buttons {
		if button.Command == toolIdentityCommand {
			choices = append(choices, button)
		}
	}
	if len(choices) != 1 || choices[0].Name != candidate || choices[0].ToolIdentityTargetID == "" {
		t.Fatalf("identity buttons = %+v, want one bound choice for %q", choices, candidate)
	}
	return cmd, commandCardClick(t, cmd, f.replies.replies[len(f.replies.replies)-1], choices[0].Label, id+"-choose")
}

func (f toolIdentitySelectionFixture) selected(t *testing.T, want string) {
	t.Helper()
	if got := f.engine.Store.snapshot().Selected[userScope(f.source)]; got != want {
		t.Fatalf("selected route = %q, want %q", got, want)
	}
}

func TestToolIdentitySelectionPersistsWithoutChangingTasks(t *testing.T) {
	f := newToolIdentitySelectionFixture(t)
	e := f.engine
	before := e.Store.snapshot()
	quoted := f.source
	quoted.ID, quoted.Text, quoted.ParentID = "quoted-choice", "/config identity use "+personalIdentityRoute, f.source.ID
	accept(t, e, quoted)
	if cmd := consumeResumeFlowMessage(t, e, quoted.ID); cmd == nil || !strings.Contains(cmd.Reply, "请直接发送") {
		t.Fatal("quoted command was not redirected to the direct entry")
	}
	f.selected(t, "nas")
	f.command(t, "wrong-case", "/config identity use work.personal")
	f.selected(t, "nas")
	f.command(t, "by-text", "/config identity use "+personalIdentityRoute)
	disk, err := readStateSnapshot(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if disk.Selected[userScope(f.source)] != personalIdentityRoute {
		t.Fatalf("text selection was not persisted: %v", disk.Selected)
	}

	card, action := f.choice(t, "switch-back", personalIdentityRoute, "nas")
	clickCommandCard(t, f.lark, action)
	receipt := consumeResumeFlowMessage(t, e, "card:"+action.EventID)
	disk, err = readStateSnapshot(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	origin := disk.Commands[key(card.Source, card.Source.ID)]
	if receipt == nil || disk.Commands[key(receipt.Source, receipt.Source.ID)] == nil || origin == nil || origin.Card == nil || !origin.Card.Used {
		t.Fatal("card selection did not persist the consumed card and action receipt")
	}
	if !reflect.DeepEqual(before.Selected, disk.Selected) || !reflect.DeepEqual(before.Tasks, disk.Tasks) ||
		!reflect.DeepEqual(before.Current, disk.Current) || !reflect.DeepEqual(before.Workspaces, disk.Workspaces) {
		t.Fatal("switching back changed an existing run, workspace, or session mapping")
	}

	f.command(t, "select-personal-again", "/config identity use "+personalIdentityRoute)
	action.EventID = "replay-with-new-event"
	clickCommandCard(t, f.lark, action)
	if len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("a used card accepted a second event")
	}
	f.selected(t, personalIdentityRoute)
}

func TestToolIdentitySelectionRoutesOrdinaryFollowup(t *testing.T) {
	f := newToolIdentitySelectionFixture(t)
	e := f.engine
	f.command(t, "select-personal", "/config identity use "+personalIdentityRoute)
	m := f.source
	m.ID, m.Text = "personal-input", "继续核对资料"
	accept(t, e, m)
	consumeResumeFlowMessage(t, e, m.ID)
	personal := onlyOwnedTask(e, m.ID)
	if personal == nil || personal.RouteID != personalIdentityRoute || personal.Target.ProviderID != "personal-provider" || personal.ThreadID != "" || personal.Phase != "ready" {
		t.Fatalf("plain input did not start a separate personal context: %+v", personal)
	}
	f.update(t, func(st *State) { st.Tasks[personal.ID].Phase = "completed" })
	f.command(t, "select-bot", "/config identity use nas")
	m.ID, m.Text = "bot-input", "继续核对资料"
	accept(t, e, m)
	consumeResumeFlowMessage(t, e, m.ID)
	bot := e.Store.snapshot().Tasks[f.source.ID]
	if bot.Pending == nil || bot.Pending.ID != m.ID || bot.ThreadID != "old-thread" || bot.Target.ProviderID != "bot-provider" {
		t.Fatalf("plain input did not resume the original bot context: %+v", bot)
	}
}

func TestToolIdentitySelectionRejectsStaleOrUnauthorizedCard(t *testing.T) {
	for _, scenario := range []string{"scope", "app", "current ACL", "candidate ACL", "workspace drift", "binding drift"} {
		t.Run(scenario, func(t *testing.T) {
			f := newToolIdentitySelectionFixture(t)
			e := f.engine
			card, action := f.choice(t, "open", "nas", personalIdentityRoute)
			want := e.Store.snapshot().Selected
			switch scenario {
			case "scope":
				m := card.Source
				m.ID, m.Text, m.TopicID = "wrong-topic", "", "unbound-topic"
				m.ParentID, m.CommandAction = card.Card.ReplyID, action.Action.Value["id"].(string)
				f.update(t, func(st *State) {
					want[userScope(m)] = "nas"
					st.Selected[userScope(m)] = "nas"
					for _, route := range []string{"nas", personalIdentityRoute} {
						st.Workspaces[workspaceKey(e.Config, m, route)] = st.Workspaces[workspaceKey(e.Config, f.source, route)]
					}
				})
				accept(t, e, m)
				if err := e.consume(t.Context(), m); err != nil {
					t.Fatal(err)
				}
			case "app":
				wrongApp := &Lark{Config: e.Config, Store: e.Store}
				wrongApp.Config.AppID = "cli_wrong_app"
				clickCommandCard(t, wrongApp, action)
				if len(e.Store.snapshot().Inbox) != 0 {
					t.Fatal("another receiving app queued the action")
				}
			default:
				clickCommandCard(t, f.lark, action)
				switch scenario {
				case "current ACL", "candidate ACL":
					route := "nas"
					if scenario == "candidate ACL" {
						route = personalIdentityRoute
					}
					f.update(t, func(st *State) {
						st.AccessPolicy.Routes = map[string]map[string]bool{route: {f.source.UserID: false}}
					})
					e.publishPolicy()
				case "workspace drift":
					f.update(t, func(st *State) {
						for _, route := range []string{"nas", personalIdentityRoute} {
							k := workspaceKey(e.Config, f.source, route)
							w := st.Workspaces[k]
							w.Path = "/synthetic/moved"
							st.Workspaces[k] = w
						}
					})
				case "binding drift":
					for _, route := range []string{personalIdentityRoute, "other-runtime"} {
						target := e.Config.Routes[route]
						tool, binding := *target.ToolContext, *target.ToolContext.UserBinding
						binding.Profile = "Changed.Profile"
						tool.UserBinding, target.ToolContext = &binding, &tool
						e.Config.Routes[route] = target
					}
				}
				consumeResumeFlowMessage(t, e, "card:"+action.EventID)
			}
			if !reflect.DeepEqual(e.Store.snapshot().Selected, want) {
				t.Fatal("an unauthorized or stale card changed the selection")
			}
		})
	}
}

func TestToolIdentitySelectionRejectsWrongActorAndBusyScope(t *testing.T) {
	for _, scenario := range []string{"different personal actor", "non-admin", "group", "running on another route", "queued input on another route"} {
		t.Run(scenario, func(t *testing.T) {
			f := newToolIdentitySelectionFixture(t)
			e := f.engine
			m := f.source
			m.ID, m.Text = "try-switch", "/config identity use "+personalIdentityRoute
			switch scenario {
			case "different personal actor":
				m.UserID = "ou_other"
				f.update(t, func(st *State) {
					st.AccessPolicy.Routes = map[string]map[string]bool{personalIdentityRoute: {m.UserID: true}}
					for _, route := range []string{"nas", personalIdentityRoute} {
						st.Workspaces[workspaceKey(e.Config, m, route)] = st.Workspaces[workspaceKey(e.Config, f.source, route)]
					}
				})
				e.publishPolicy()
			case "non-admin":
				e.Config.Admins = nil
			case "group":
				m.ChatType = "group"
			case "running on another route":
				f.update(t, func(st *State) { st.Tasks["other-run"].Source.TopicID = m.TopicID })
			case "queued input on another route":
				_, action := f.choice(t, "before-queue", "nas", personalIdentityRoute)
				q := m
				q.ID, q.Text = "queued", "/mac pending ordinary input"
				accept(t, e, q)
				clickCommandCard(t, f.lark, action)
				m.ID = "card:" + action.EventID
			}
			if m.ID == "try-switch" {
				accept(t, e, m)
			}
			before := e.Store.snapshot()
			consumeResumeFlowMessage(t, e, m.ID)
			after := e.Store.snapshot()
			if !reflect.DeepEqual(after.Selected, before.Selected) || !reflect.DeepEqual(after.Tasks, before.Tasks) {
				t.Fatal("denied command changed a route selection or run")
			}
			pending := slices.DeleteFunc(before.Inbox, func(q Message) bool { return q.ID == m.ID })
			if len(pending) != len(after.Inbox) || (len(pending) > 0 && !reflect.DeepEqual(pending, after.Inbox)) {
				t.Fatal("denied command modified pending input")
			}
		})
	}
}
