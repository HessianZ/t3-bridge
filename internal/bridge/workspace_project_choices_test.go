package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

type projectChoiceCatalog struct {
	projects     []Project
	lists, reads []string
	listErr      error
}

func projectChoicesFixture(t *testing.T) (*Engine, *fakeMessenger, *Lark, *projectChoiceCatalog) {
	t.Helper()
	e, x, replies, _ := testEngine(t)
	e.Config.DefaultRoute = "nas"
	e.Config.Admins = []string{"user"}
	e.Config.access = &accessRuntime{}
	e.Lark = commandCardRecorder{replies}
	lark := &Lark{Config: e.Config, Store: e.Store}
	if err := e.Store.update(func(st *State) error {
		st.AccountID = e.Config.AppID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	catalog := &projectChoiceCatalog{}
	for i := 1; i <= 7; i++ {
		id := fmt.Sprintf("project-%02d", i)
		catalog.projects = append(catalog.projects, Project{ID: id, Title: fmt.Sprintf("Catalog-Name-%02d", i), Root: "/srv/" + id})
	}
	when := "2026-01-01T00:00:00Z"
	catalog.projects = append(catalog.projects,
		Project{ID: "archived-id", Title: "Archived Hidden", Root: "/srv/archived", ArchivedAt: &when},
		Project{ID: "deleted-id", Title: "Deleted Hidden", Root: "/srv/deleted", DeletedAt: &when},
		Project{ID: "nameless-id", Root: "/srv/nameless"},
		Project{Title: "Missing ID Hidden", Root: "/srv/idless"},
		Project{ID: "relative-id", Title: "Relative Path Hidden", Root: "relative/path"},
	)
	e.T3 = &workspaceExecutor{fakeExecutor: x, api: &T3{Calls: scriptedCaller(func(tool string, args map[string]any) (any, error) {
		env, _ := args["environmentId"].(string)
		switch tool {
		case "t3_project_list":
			catalog.lists = append(catalog.lists, env)
			return map[string]any{"projects": catalog.projects}, catalog.listErr
		case "t3_project_read":
			id, _ := args["projectId"].(string)
			catalog.reads = append(catalog.reads, env+"/"+id)
			for _, p := range catalog.projects {
				if p.ID == id && id != "" {
					return p, nil
				}
			}
			return nil, errors.New("synthetic project unavailable")
		default:
			t.Fatalf("unexpected project API operation %s", tool)
			return nil, nil
		}
	})}}
	t.Cleanup(func() {
		if x.preflights != 0 || x.opens != 0 || x.sends != 0 || x.stops != 0 {
			t.Errorf("project commands invoked executor: preflights=%d opens=%d sends=%d stops=%d", x.preflights, x.opens, x.sends, x.stops)
		}
	})
	return e, replies, lark, catalog
}

func TestWorkspaceProjectChoicesDirect(t *testing.T) {
	e, _, _, catalog := projectChoicesFixture(t)
	source := message("original", "old request")
	source.ChatType = "p2p"
	base := e.Config.Routes["nas"]
	oldKey := sessionKey(source, "nas", base)
	old := Task{ID: "old-task", Source: source, RouteID: "nas", Target: base, ReceiverAppID: e.Config.AppID,
		Phase: "completed", ThreadID: "old-thread", RequestID: "old-request", RunID: "old-run"}
	if err := e.Store.update(func(st *State) error {
		st.Tasks[old.ID] = &old
		setCurrent(e.Config, st, source, &old)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := e.Store.snapshot()
	p := catalog.projects[0]
	for i, id := range []string{"project-0", "PROJECT-01", p.Title, "archived-id", "deleted-id", "nameless-id", "relative-id"} {
		resumeFlowCommand(t, e, fmt.Sprint("invalid-project-", i), "/cd use "+id)
		st := e.Store.snapshot()
		if !reflect.DeepEqual(before.Workspaces, st.Workspaces) || st.Current[oldKey] != old.ID {
			t.Fatalf("invalid project %q changed the binding", id)
		}
	}
	readCount := len(catalog.reads)
	cmd := resumeFlowCommand(t, e, "choose-id", "/cd use "+p.ID)
	st := e.Store.snapshot()
	selected := effectiveTarget(e.Config, &st, source, "nas")
	newKey := sessionKey(source, "nas", selected)
	if cmd.Kind != workspaceProjectsCommand || cmd.Reply == "" || cmd.Target.ProjectID != p.ID ||
		selected.ProjectID != p.ID || selected.ProjectName != p.Title || selected.ProjectPath != p.Root ||
		executionPolicy(selected) != executionPolicy(base) || e.Config.Routes["nas"].ProjectID != base.ProjectID {
		t.Fatalf("direct selection failed: command=%+v target=%+v", cmd, selected)
	}
	if current, ok := st.Current[newKey]; !ok || current != "" {
		t.Fatalf("new project did not start a fresh session: %q (present=%t)", current, ok)
	}
	if history := st.Tasks[old.ID]; history == nil || history.ThreadID != old.ThreadID || history.RequestID != old.RequestID ||
		history.RunID != old.RunID || history.Target.ProjectID != base.ProjectID || st.Current[oldKey] != old.ID {
		t.Fatalf("direct selection lost original session history: %+v", history)
	}
	if len(catalog.lists) != 0 || !slices.Equal(catalog.reads[readCount:], []string{base.EnvironmentID + "/" + p.ID}) {
		t.Fatalf("direct selection must do one exact read: lists=%v reads=%v", catalog.lists, catalog.reads)
	}
	current := Task{ID: "selected-task", Source: source, RouteID: "nas", Target: selected, BaseTargetID: targetIdentity(base),
		ReceiverAppID: e.Config.AppID, Phase: "completed", ThreadID: "selected-thread", RequestID: "selected-request"}
	if err := e.Store.update(func(st *State) error {
		st.Tasks[current.ID] = &current
		setCurrent(e.Config, st, source, &current)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	resumeFlowCommand(t, e, "choose-current", "/cd use "+p.ID)
	if got := e.Store.snapshot().Current[newKey]; got != current.ID {
		t.Fatalf("selecting the current project reset its session: %q", got)
	}
	p = catalog.projects[1]
	resumeFlowCommand(t, e, "choose-path", "/cd "+p.Root)
	st = e.Store.snapshot()
	if got := effectiveTarget(e.Config, &st, source, "nas"); got.ProjectID != p.ID || len(catalog.lists) != 1 || catalog.reads[len(catalog.reads)-1] != base.EnvironmentID+"/"+p.ID {
		t.Fatalf("existing path selection changed behavior: target=%+v lists=%v reads=%v", got, catalog.lists, catalog.reads)
	}
}

func TestWorkspaceProjectChoicesCardPagesAndSelection(t *testing.T) {
	e, replies, lark, catalog := projectChoicesFixture(t)
	source := message("", "")
	if err := e.Store.update(func(st *State) error {
		st.Selected = map[string]string{userScope(source): "mac"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	first := resumeFlowCommand(t, e, "project-page-one", "/cd list")
	assertPage := func(cmd *SessionCommand, size, next int) (choices []CommandButton, navigation string) {
		t.Helper()
		if cmd == nil || cmd.Kind != workspaceProjectsCommand || cmd.RouteID != "mac" || cmd.Card == nil ||
			cmd.Card.AppID != e.Config.AppID || cmd.Card.ReplyID == "" {
			t.Fatalf("missing scoped project card: %+v", cmd)
		}
		for _, b := range cmd.Card.Buttons {
			if b.Command != workspaceProjectsCommand {
				continue
			}
			if b.Workspace != nil {
				if !strings.Contains(cmd.Reply, b.Workspace.ProjectID) || !strings.Contains(cmd.Reply, b.Workspace.Name) {
					t.Fatalf("text fallback omits the exact project name or ID: %s", cmd.Reply)
				}
				choices = append(choices, b)
			}
			if b.ProjectPage == next {
				navigation = b.Label
			}
		}
		if len(choices) != size || navigation == "" {
			t.Fatalf("page choices=%+v navigation=%q, want %d choices and link to page %d", choices, navigation, size, next)
		}
		return choices, navigation
	}
	firstChoices, next := assertPage(first, 6, 2)
	firstBody := replies.replies[len(replies.replies)-1]
	clickCommandCard(t, lark, commandCardClick(t, first, firstBody, next, "project-next"))
	second := consumeResumeFlowMessage(t, e, "card:project-next")
	secondChoices, _ := assertPage(second, 1, 1)
	secondBody := replies.replies[len(replies.replies)-1]
	var ids []string
	for _, choice := range append(firstChoices, secondChoices...) {
		ids = append(ids, choice.Workspace.ProjectID)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"project-01", "project-02", "project-03", "project-04", "project-05", "project-06", "project-07"}) {
		t.Fatalf("invalid or missing projects in list: %v", ids)
	}
	for _, hidden := range []string{"archived-id", "deleted-id", "nameless-id", "Missing ID Hidden", "relative-id"} {
		if strings.Contains(first.Reply, hidden) || strings.Contains(second.Reply, hidden) {
			t.Fatalf("invalid project %q appeared in the list text", hidden)
		}
	}
	if manual := resumeFlowCommand(t, e, "project-page-two-text", "/cd list 2"); manual.Reply != second.Reply {
		t.Fatalf("callback and direct page differ: %q and %q", second.Reply, manual.Reply)
	}
	base := e.Config.Routes["mac"]
	if !slices.Equal(catalog.lists, []string{base.EnvironmentID, base.EnvironmentID, base.EnvironmentID}) || len(catalog.reads) != 0 {
		t.Fatalf("paging used the wrong T3 project API: lists=%v reads=%v", catalog.lists, catalog.reads)
	}
	choice := secondChoices[0]
	confirmed := false
	for _, element := range card2Elements(t, secondBody) {
		b := element.(map[string]any)
		if b["tag"] == "button" && b["text"].(map[string]any)["content"] == choice.Label {
			confirmed = b["confirm"] != nil
		}
	}
	if !confirmed {
		t.Fatal("project choice lacks native confirmation")
	}
	action := commandCardClick(t, second, secondBody, choice.Label, "project-choose-card")
	wrongUser, wrongChat, wrongPage := action, action, action
	wrongUser.Operator.OpenID = "other"
	wrongChat.ChatID = "other-chat"
	wrongPage.MessageID = first.Card.ReplyID
	clickCommandCard(t, lark, wrongUser)
	clickCommandCard(t, lark, wrongChat)
	clickCommandCard(t, lark, wrongPage)
	otherApp := &Lark{Config: e.Config, Store: e.Store}
	otherApp.Config.AppID = "different-app"
	clickCommandCard(t, otherApp, action)
	if len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("project card accepted a callback outside its App, user, chat or page")
	}
	clickCommandCard(t, lark, action)
	before := e.Store.snapshot()
	clickID := "card:" + action.EventID
	queued, ok := queuedPresent(&before, message(clickID, ""))
	if !ok || queued.ChatType != "p2p" || userScope(queued) != userScope(second.Source) {
		t.Fatalf("callback lost private scope: %+v", queued)
	}
	originKey := key(second.Source, second.Source.ID)
	assertCommit := func(st State) {
		t.Helper()
		selected := effectiveTarget(e.Config, &st, second.Source, "mac")
		current, mapped := st.Current[sessionKey(second.Source, "mac", selected)]
		receipt := st.Commands[key(second.Source, clickID)]
		if st.Workspaces[workspaceKey(e.Config, second.Source, "mac")] != *choice.Workspace || !mapped || current != "" ||
			!st.Commands[originKey].Card.Used || receipt == nil || receipt.Reply == "" || receipt.Target.ProjectID != choice.Workspace.ProjectID {
			t.Fatalf("workspace, current session, consumed card and receipt were not committed together: target=%+v current=%q receipt=%+v", selected, current, receipt)
		}
	}
	saveErr := errors.New("synthetic project choice write failure")
	e.Store.writeState = func(_ string, data []byte, _ bool) (bool, error) {
		var candidate State
		if err := json.Unmarshal(data, &candidate); err != nil {
			t.Error(err)
		} else {
			assertCommit(candidate)
		}
		return false, saveErr
	}
	err := e.consume(t.Context(), queued)
	e.Store.writeState = nil
	if !errors.Is(err, saveErr) || !reflect.DeepEqual(e.Store.snapshot(), before) {
		t.Fatalf("failed card selection did not roll back together: %v", err)
	}
	consumeResumeFlowMessage(t, e, clickID)
	assertCommit(e.Store.snapshot())
	action.EventID = "project-choose-replay"
	clickCommandCard(t, lark, action)
	wantRead := base.EnvironmentID + "/" + choice.Workspace.ProjectID
	if len(e.Store.snapshot().Inbox) != 0 || !slices.Equal(catalog.reads, []string{wantRead, wantRead}) || len(catalog.lists) != 3 {
		t.Fatalf("unexpected selection or replay calls: lists=%v reads=%v", catalog.lists, catalog.reads)
	}
}

func TestWorkspaceProjectChoicesRejectStaleCard(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*State, *projectChoiceCatalog, Message)
		reads  []string
	}{
		{"changed route", func(st *State, _ *projectChoiceCatalog, m Message) {
			st.Selected = map[string]string{userScope(m): "mac"}
		}, nil},
		{"changed path", func(_ *State, c *projectChoiceCatalog, _ Message) {
			c.projects[0].Root = "/srv/moved"
		}, []string{"nas-env/project-01"}},
		{"revoked route", func(st *State, _ *projectChoiceCatalog, m Message) {
			st.AccessPolicy.Routes = map[string]map[string]bool{"nas": {m.UserID: false}}
		}, nil},
		{"revoked admin", func(st *State, _ *projectChoiceCatalog, m Message) {
			st.AccessPolicy.Admins = map[string]bool{m.UserID: false}
		}, nil},
		{"unavailable project", func(_ *State, c *projectChoiceCatalog, _ Message) {
			c.projects = c.projects[1:]
		}, []string{"nas-env/project-01"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, replies, lark, catalog := projectChoicesFixture(t)
			card := resumeFlowCommand(t, e, "stale-project-page", "/cd list")
			button := projectChoiceButton(t, card, "project-01")
			action := commandCardClick(t, card, replies.replies[len(replies.replies)-1], button.Label, "stale-project-click")
			clickCommandCard(t, lark, action)
			if err := e.Store.update(func(st *State) error {
				tc.change(st, catalog, card.Source)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			consumeResumeFlowMessage(t, e, "card:stale-project-click")
			st := e.Store.snapshot()
			if len(st.Workspaces) != 0 || len(st.Current) != 0 || st.Commands[key(card.Source, card.Source.ID)].Card.Used ||
				!slices.Equal(catalog.reads, tc.reads) || len(catalog.lists) != 1 {
				t.Fatalf("stale callback changed state or used the wrong API: workspaces=%v current=%v lists=%v reads=%v",
					st.Workspaces, st.Current, catalog.lists, catalog.reads)
			}
		})
	}
}

func TestWorkspaceProjectChoicesBlockBusyScope(t *testing.T) {
	for _, mode := range []string{"running", "ambiguous", "queued"} {
		t.Run(mode, func(t *testing.T) {
			e, replies, lark, catalog := projectChoicesFixture(t)
			card := resumeFlowCommand(t, e, "busy-project-page", "/cd list")
			button := projectChoiceButton(t, card, "project-01")
			action := commandCardClick(t, card, replies.replies[len(replies.replies)-1], button.Label, "busy-project-click")
			input := card.Source
			input.ID, input.Text = "work-in-scope", "ordinary input"
			if mode == "queued" {
				accept(t, e, input)
			} else {
				phase := "running"
				if mode == "ambiguous" {
					phase = "failed"
				}
				if err := e.Store.update(func(st *State) error {
					st.Tasks["busy-task"] = &Task{ID: "busy-task", Source: input, RouteID: "nas", Target: e.Config.Routes["nas"],
						ReceiverAppID: e.Config.AppID, Phase: phase, NeedsReconcile: mode == "ambiguous"}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			before := e.Store.snapshot()
			direct := resumeFlowCommand(t, e, "busy-project-use", "/cd use project-01")
			clickCommandCard(t, lark, action)
			chosen := consumeResumeFlowMessage(t, e, "card:busy-project-click")
			after := e.Store.snapshot()
			queueUnchanged := slices.EqualFunc(before.Inbox, after.Inbox, func(a, b Message) bool { return reflect.DeepEqual(a, b) })
			if chosen == nil || chosen.Reply != direct.Reply || !strings.Contains(direct.Reply, "排队") ||
				len(after.Workspaces) != 0 || len(after.Current) != 0 || after.Commands[key(card.Source, card.Source.ID)].Card.Used ||
				!reflect.DeepEqual(before.Tasks, after.Tasks) || !queueUnchanged ||
				len(catalog.lists) != 1 || len(catalog.reads) != 0 {
				t.Fatalf("busy selection changed scope or called project API: text=%+v click=%+v lists=%v reads=%v", direct, chosen, catalog.lists, catalog.reads)
			}
		})
	}
}

func TestWorkspaceProjectChoicesListAccessAndUnavailable(t *testing.T) {
	e, _, _, catalog := projectChoicesFixture(t)
	for _, m := range []Message{
		{ID: "ordinary-projects", Text: "/cd list", ChatType: "p2p", UserID: "other", ChatID: "chat", TopicID: "topic"},
		{ID: "group-projects", Text: "/cd list", ChatType: "group", UserID: "user", ChatID: "chat", TopicID: "topic"},
	} {
		if cmd := runSessionStatusCommand(t, e, m); cmd != nil && (cmd.Card != nil || strings.Contains(cmd.Reply, "project-01")) {
			t.Fatalf("project catalog was exposed to %s: %+v", m.ID, cmd)
		}
	}
	if len(catalog.lists) != 0 || len(catalog.reads) != 0 {
		t.Fatalf("unauthorized requests reached T3: lists=%v reads=%v", catalog.lists, catalog.reads)
	}
	for i, text := range []string{"/cd list 0", "/cd list wrong"} {
		if cmd := resumeFlowCommand(t, e, fmt.Sprint("invalid-project-page-", i), text); cmd.Card != nil || !strings.Contains(cmd.Reply, "页") {
			t.Fatalf("invalid page produced a catalog: %+v", cmd)
		}
	}
	if len(catalog.lists) != 0 {
		t.Fatalf("invalid page fetched project catalog: %v", catalog.lists)
	}
	catalog.listErr = errors.New("synthetic list unavailable")
	cmd := resumeFlowCommand(t, e, "project-list-error", "/cd list")
	if cmd.Card != nil || !strings.Contains(cmd.Reply, "不可用") || !slices.Equal(catalog.lists, []string{"nas-env"}) || len(catalog.reads) != 0 {
		t.Fatalf("failed listing exposed options or retried: command=%+v lists=%v reads=%v", cmd, catalog.lists, catalog.reads)
	}
}

func projectChoiceButton(t *testing.T, cmd *SessionCommand, projectID string) CommandButton {
	t.Helper()
	if cmd.Card != nil {
		for _, b := range cmd.Card.Buttons {
			if b.Command == workspaceProjectsCommand && b.Workspace != nil && b.Workspace.ProjectID == projectID {
				return b
			}
		}
	}
	t.Fatalf("no button for %q on project card: %+v", projectID, cmd)
	return CommandButton{}
}
