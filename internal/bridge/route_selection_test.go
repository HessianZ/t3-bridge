package bridge

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	channel "github.com/larksuite/channel-sdk-go"
)

func newRouteSelectionFixture(t *testing.T) toolIdentitySelectionFixture {
	t.Helper()
	f := newToolIdentitySelectionFixture(t)
	f.engine.Config.Admins = []string{"ou_other"}
	f.lark.Config = f.engine.Config
	return f
}

func routeSelectionClick(t *testing.T, f toolIdentitySelectionFixture, cmd *SessionCommand, route string, page int, event string) channel.CardActionEvent {
	t.Helper()
	if cmd == nil || cmd.Card == nil || cmd.Card.ReplyID == "" {
		t.Fatalf("route listing did not deliver a card: %+v", cmd)
	}
	for _, button := range cmd.Card.Buttons {
		if button.Command == routeSelectionCommand && button.RouteSelectionPage == page &&
			(page != 0 || button.Name == route && button.RouteSelectionID != "") {
			return commandCardClick(t, cmd, f.replies.replies[len(f.replies.replies)-1], button.Label, event)
		}
	}
	t.Fatalf("missing route %q or page %d in %+v", route, page, cmd.Card.Buttons)
	return channel.CardActionEvent{}
}

func TestRouteSelectionListsExactAuthorizedRoutesByPage(t *testing.T) {
	f := newRouteSelectionFixture(t)
	e := f.engine
	base := e.Config.Routes["mac"]
	for _, id := range []string{"zulu", "Alpha", "bravo", "zebra"} {
		e.Config.Routes[id] = base
	}
	e.Config.ReviewRoute = "Review-only"
	e.Config.Routes[e.Config.ReviewRoute] = base
	denied, unavailable := base, base
	denied.Users = []string{"ou_other"}
	unavailable.UnavailableReason = "synthetic outage"
	e.Config.Routes["No-access"], e.Config.Routes["Offline"] = denied, unavailable
	f.lark.Config = e.Config

	assertPage := func(cmd *SessionCommand, routes []string, navigation int) {
		t.Helper()
		if cmd == nil || cmd.Card == nil || cmd.Card.ReplyID == "" || cmd.Card.Used {
			t.Fatalf("expected a delivered route page: %+v", cmd)
		}
		var names []string
		var pages []int
		for _, button := range cmd.Card.Buttons {
			if button.Command != routeSelectionCommand {
				continue
			}
			if button.RouteSelectionPage != 0 {
				pages = append(pages, button.RouteSelectionPage)
				continue
			}
			if button.RouteSelectionID == "" {
				t.Errorf("route %q has no selection binding", button.Name)
			}
			names = append(names, button.Name)
		}
		if !slices.Equal(names, routes) || !slices.Equal(pages, []int{navigation}) {
			t.Fatalf("routes = %v, pages = %v, want %v and [%d]", names, pages, routes, navigation)
		}
		body := f.replies.replies[len(f.replies.replies)-1]
		for _, hidden := range []string{"Review-only", "No-access", "Offline", "synthetic outage"} {
			if strings.Contains(cmd.Reply, hidden) || strings.Contains(body, hidden) {
				t.Errorf("page exposed %q", hidden)
			}
		}
	}
	firstPage := []string{"Alpha", personalIdentityRoute, "bravo", "mac", "nas", "other-runtime"}
	secondPage := []string{"zebra", "zulu"}
	first := f.command(t, "route-list", "/config route")
	assertPage(first, firstPage, 2)
	next := routeSelectionClick(t, f, first, "", 2, "route-next")
	clickCommandCard(t, f.lark, next)
	second := consumeResumeFlowMessage(t, e, "card:"+next.EventID)
	assertPage(second, secondPage, 1)
	prev := routeSelectionClick(t, f, second, "", 1, "route-prev")
	clickCommandCard(t, f.lark, prev)
	assertPage(consumeResumeFlowMessage(t, e, "card:"+prev.EventID), firstPage, 2)
	assertPage(f.command(t, "route-list-direct-page", "/config route 2"), secondPage, 1)
	if e.Store.snapshot().Commands[key(first.Source, first.Source.ID)].Card.Used {
		t.Fatal("pagination consumed the source card")
	}
	for _, hidden := range []string{"Review-only", "No-access", "Offline"} {
		f.command(t, "route-direct-"+hidden, "/config route use "+hidden)
		f.selected(t, "nas")
	}
}

func TestRouteSelectionDirectSwitchPreservesSessions(t *testing.T) {
	f := newRouteSelectionFixture(t)
	e := f.engine
	before := e.Store.snapshot()
	for _, tc := range []struct{ name, text, parent, chatType string }{
		{"wrong-case", "/config route use work.personal", "", "p2p"},
		{"quoted-id", `/config route use "Work.Personal"`, "", "p2p"},
		{"quoted-message", "/config route use " + personalIdentityRoute, f.source.ID, "p2p"},
		{"group", "/config route use mac", "", "group"},
	} {
		m := f.source
		m.ID, m.Text, m.ParentID, m.ChatType = tc.name, tc.text, tc.parent, tc.chatType
		accept(t, e, m)
		consumeResumeFlowMessage(t, e, m.ID)
		f.selected(t, "nas")
	}
	f.command(t, "direct-route", "/config route use "+personalIdentityRoute)
	disk, err := readStateSnapshot(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	wantSelected := before.Selected
	wantSelected[userScope(f.source)] = personalIdentityRoute
	if !reflect.DeepEqual(disk.Selected, wantSelected) || !reflect.DeepEqual(disk.Tasks, before.Tasks) ||
		!reflect.DeepEqual(disk.Current, before.Current) || !reflect.DeepEqual(disk.Workspaces, before.Workspaces) {
		t.Fatal("direct selection altered tasks, current sessions, workspaces or another scope")
	}

	f.command(t, "switch-to-history", "/config route use nas")
	f.selected(t, "nas")
	m := f.source
	m.ID, m.Text = "route-followup", "继续核对资料"
	accept(t, e, m)
	consumeResumeFlowMessage(t, e, m.ID)
	after := e.Store.snapshot()
	task := after.Tasks[f.source.ID]
	if len(after.Tasks) != len(before.Tasks) || task == nil || task.ThreadID != "old-thread" || task.Pending == nil || task.Pending.ID != m.ID {
		t.Fatalf("ordinary input did not continue the selected route's prior thread: %+v", task)
	}
}

func TestRouteSelectionCardCommitsChangeAndReceiptTogether(t *testing.T) {
	f := newRouteSelectionFixture(t)
	e := f.engine
	card := f.command(t, "route-atomic-list", "/config route")
	action := routeSelectionClick(t, f, card, personalIdentityRoute, 0, "route-atomic-select")
	clickCommandCard(t, f.lark, action)
	st := e.Store.snapshot()
	queued, ok := queuedPresent(&st, message("card:"+action.EventID, ""))
	if !ok || queued.TopicID != f.source.TopicID || queued.UserID != f.source.UserID || queued.ChatID != f.source.ChatID {
		t.Fatalf("callback did not preserve the source scope: %+v", queued)
	}
	originID, changeID := key(card.Source, card.Source.ID), key(queued, queued.ID)
	assertChange := func(st State) {
		t.Helper()
		origin := st.Commands[originID]
		if st.Selected[userScope(f.source)] != personalIdentityRoute || origin == nil || origin.Card == nil || !origin.Card.Used || st.Commands[changeID] == nil {
			t.Fatal("selection, used card and action receipt were not recorded together")
		}
	}
	writeErr := errors.New("synthetic state write failure")
	var attempted State
	e.Store.writeState = func(_ string, data []byte, _ bool) (bool, error) {
		if err := json.Unmarshal(data, &attempted); err != nil {
			return false, err
		}
		return false, writeErr
	}
	_, err := e.sessionControl(t.Context(), queued)
	e.Store.writeState = nil
	if !errors.Is(err, writeErr) {
		t.Fatalf("selection write: %v", err)
	}
	assertChange(attempted)
	if rolledBack := e.Store.snapshot(); !reflect.DeepEqual(rolledBack, st) {
		t.Fatal("failed publication changed durable in-memory state")
	}
	consumeResumeFlowMessage(t, e, queued.ID)
	disk, err := readStateSnapshot(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	assertChange(disk)
	action.EventID = "route-card-replay"
	clickCommandCard(t, f.lark, action)
	if len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("used route card queued another selection")
	}
}

func TestRouteSelectionCardRejectsForeignScope(t *testing.T) {
	f := newRouteSelectionFixture(t)
	e := f.engine
	card := f.command(t, "route-scope-list", "/config route")
	action := routeSelectionClick(t, f, card, personalIdentityRoute, 0, "route-foreign")
	wrongUser, wrongChat := action, action
	wrongUser.Operator.OpenID = "ou_other"
	wrongChat.ChatID = "another-chat"
	wrongApp := *f.lark
	wrongApp.Config.AppID = "cli_other"
	clickCommandCard(t, &wrongApp, action)
	clickCommandCard(t, f.lark, wrongUser)
	clickCommandCard(t, f.lark, wrongChat)
	if len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("foreign app, user or chat queued a selection")
	}
	m := card.Source
	m.ID, m.Text, m.TopicID = "route-wrong-topic", "", "other-topic"
	m.ParentID, m.CommandAction = card.Card.ReplyID, action.Action.Value["id"].(string)
	f.update(t, func(st *State) { st.Selected[userScope(m)] = "nas" })
	accept(t, e, m)
	st := e.Store.snapshot()
	queued, ok := queuedPresent(&st, m)
	if !ok {
		t.Fatal("wrong-topic fixture failed to queue")
	}
	if err := e.consume(t.Context(), queued); err != nil {
		t.Fatal(err)
	}
	after := e.Store.snapshot()
	if !reflect.DeepEqual(st.Selected, after.Selected) || after.Commands[key(card.Source, card.Source.ID)].Card.Used {
		t.Fatal("a different topic changed the route or consumed the card")
	}
}

func TestRouteSelectionRejectsStaleOrBusyCard(t *testing.T) {
	for _, scenario := range []string{"selection changed", "route ACL revoked before publish", "user ACL revoked", "configured target changed", "effective workspace changed", "running", "unresolved", "ordinary input queued"} {
		t.Run(scenario, func(t *testing.T) {
			f := newRouteSelectionFixture(t)
			e := f.engine
			card := f.command(t, "route-stale-list", "/config route")
			action := routeSelectionClick(t, f, card, personalIdentityRoute, 0, "route-stale-select")
			clickCommandCard(t, f.lark, action)
			switch scenario {
			case "selection changed":
				f.command(t, "route-changed", "/config route use mac")
				f.selected(t, "mac")
			case "route ACL revoked before publish":
				f.update(t, func(st *State) {
					st.AccessPolicy.Routes = map[string]map[string]bool{personalIdentityRoute: {f.source.UserID: false}}
				})
			case "user ACL revoked":
				f.update(t, func(st *State) { st.AccessPolicy.Users = map[string]bool{f.source.UserID: false} })
				e.publishPolicy()
			case "configured target changed":
				target := e.Config.Routes[personalIdentityRoute]
				target.Model = "replacement-model"
				e.Config.Routes[personalIdentityRoute] = target
			case "effective workspace changed":
				f.update(t, func(st *State) {
					st.Workspaces[workspaceKey(e.Config, f.source, personalIdentityRoute)] = Workspace{ProjectID: "different-project", Path: "/synthetic/moved"}
				})
			case "running", "unresolved":
				f.update(t, func(st *State) {
					task := st.Tasks["other-run"]
					task.Source.TopicID = f.source.TopicID
					if scenario == "unresolved" {
						task.Phase, task.NeedsReconcile = "failed", true
					}
				})
			case "ordinary input queued":
				m := f.source
				m.ID, m.Text = "route-queued-input", "/mac pending ordinary input"
				accept(t, e, m)
			}
			before := e.Store.snapshot()
			id := "card:" + action.EventID
			consumeResumeFlowMessage(t, e, id)
			after := e.Store.snapshot()
			if !reflect.DeepEqual(before.Selected, after.Selected) || !reflect.DeepEqual(before.Tasks, after.Tasks) ||
				after.Commands[key(card.Source, card.Source.ID)].Card.Used {
				t.Fatal("rejected callback changed selection, task or card state")
			}
			pending := slices.DeleteFunc(before.Inbox, func(q Message) bool { return q.ID == id })
			if len(pending) != len(after.Inbox) || (len(pending) > 0 && !reflect.DeepEqual(pending, after.Inbox)) {
				t.Fatal("rejected callback altered other queued input")
			}
		})
	}
}

func TestRouteSelectionTextRetryWithholdsRevokedRoute(t *testing.T) {
	f := newRouteSelectionFixture(t)
	e := f.engine
	e.Lark = f.replies
	f.replies.replyErr = errors.New("synthetic reply acknowledgement lost")
	cmd := f.command(t, "route-list-text", "/config route")
	if cmd == nil || cmd.Issue == "" || len(f.replies.replies) != 1 || !strings.Contains(f.replies.replies[0], personalIdentityRoute) {
		t.Fatal("text list was not attempted and retained for retry")
	}
	f.update(t, func(st *State) {
		st.AccessPolicy.Routes = map[string]map[string]bool{personalIdentityRoute: {f.source.UserID: false}}
	})
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	e.Store = store
	e.publishPolicy()
	f.replies.replyErr = nil
	consumeResumeFlowMessage(t, e, cmd.Source.ID)
	if len(f.replies.replies) != 1 || len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("pending text list was retried after a listed route was revoked")
	}
}
