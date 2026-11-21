package bridge

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type workspaceExecutor struct {
	*fakeExecutor
	api *T3
}

func (x *workspaceExecutor) Projects(c context.Context, t Target) ([]Project, error) {
	return x.api.Projects(c, t)
}
func (x *workspaceExecutor) Project(c context.Context, t Target, id string) (Project, error) {
	return x.api.Project(c, t, id)
}
func (x *workspaceExecutor) RegisterProject(c context.Context, t Target, p string) (Project, error) {
	return x.api.RegisterProject(c, t, p)
}
func (x *workspaceExecutor) Open(c context.Context, t Target, id, title string) (Execution, error) {
	ex, err := x.fakeExecutor.Open(c, t, id, title)
	ex.ThreadID = "thread-" + t.ProjectID
	return ex, err
}
func workspaceFixture(t *testing.T) (*Engine, *fakeExecutor, *fakeMessenger, *[]Project, *int) {
	e, x, l, _ := testEngine(t)
	e.Config.Admins = []string{"user"}
	projects := []Project{{ID: "homeops", Title: "管理", Root: "/main"}, {ID: "alpha", Title: "同名项目", Root: "/srv/alpha"}, {ID: "beta", Title: "同名项目", Root: "/srv/beta"}}
	creates := 0
	api := &T3{Calls: scriptedCaller(func(name string, a map[string]any) (any, error) {
		if a["environmentId"] != e.Config.Routes["nas"].EnvironmentID && a["environmentId"] != e.Config.Routes["mac"].EnvironmentID {
			t.Fatal("project API lost target environment")
		}
		switch name {
		case "t3_project_list":
			cursor := int(a["cursor"].(float64))
			limit := 1
			if cursor >= len(projects) {
				return map[string]any{"projects": []Project{}}, nil
			}
			var next any
			if cursor+limit < len(projects) {
				next = cursor + limit
			}
			return map[string]any{"projects": projects[cursor : cursor+limit], "nextCursor": next}, nil
		case "t3_project_read":
			for _, p := range projects {
				if p.ID == a["projectId"] {
					return p, nil
				}
			}
			return nil, errors.New("unavailable")
		case "t3_project_create":
			creates++
			if a["createWorkspaceRootIfMissing"] != false || a["scripts"] != nil || a["defaultModelSelection"] != nil {
				t.Fatal("registration created folder or changed execution policy", a)
			}
			root := a["workspaceRoot"].(string)
			if strings.Contains(root, "missing") {
				return nil, errors.New("folder not found")
			}
			p := Project{ID: "registered", Title: "新项目", Root: root}
			projects = append(projects, p)
			if strings.Contains(root, "lost") {
				return nil, errors.New("lost response")
			}
			return p, nil
		}
		t.Fatal("unexpected project operation", name)
		return nil, nil
	})}
	e.T3 = &workspaceExecutor{fakeExecutor: x, api: api}
	return e, x, l, &projects, &creates
}
func workspaceCommand(t *testing.T, e *Engine, l *fakeMessenger, id, text string) string {
	t.Helper()
	accept(t, e, message(id, text))
	tick(t, e)
	return l.replies[len(l.replies)-1]
}
func TestWorkspaceSelectionAliasesRestartAndOriginalTask(t *testing.T) {
	e, x, l, _, creates := workspaceFixture(t)
	accept(t, e, message("first", "/nas 首轮"))
	tick(t, e)
	first := onlyTask(t, e)
	if got := workspaceCommand(t, e, l, "busy", "/cd /srv/alpha"); !strings.Contains(got, "仍有运行") {
		t.Fatal("busy switch", got)
	}
	if x.stops != 0 {
		t.Fatal("project switch stopped run")
	}
	x.observation.Status = "completed"
	x.observation.Summary = "首轮结论"
	tick(t, e)
	if got := workspaceCommand(t, e, l, "cd", "/cd /srv/alpha"); !strings.Contains(got, "已切换") {
		t.Fatal(got)
	}
	if e.Config.Routes["nas"].ProjectID != "homeops" {
		t.Fatal("global route mutated")
	}
	workspaceCommand(t, e, l, "save", "/ws save 工作一")
	accept(t, e, message("second", "/nas 次轮"))
	tick(t, e)
	st := e.Store.snapshot()
	var second *Task
	for _, v := range st.Tasks {
		if v.Source.ID == "second" {
			second = v
		}
	}
	if second == nil || second.Target.ProjectID != "alpha" || second.ThreadID == first.ThreadID || x.opens != 2 {
		t.Fatal("selected project not dispatched", second)
	}
	if st.Tasks[first.ID].Target.ProjectID != "homeops" || st.Tasks[first.ID].RequestID != first.RequestID {
		t.Fatal("old Task migrated")
	}
	tick(t, e)
	e.Store.Close()
	s, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e.Store = s
	if got := workspaceCommand(t, e, l, "list", "/ws list"); !strings.Contains(got, "工作一") {
		t.Fatal("aliases lost after restart")
	}
	if got := workspaceCommand(t, e, l, "status", "/status"); !strings.Contains(got, "/srv/alpha") {
		t.Fatal("status wrong project", got)
	}
	workspaceCommand(t, e, l, "beta", "/cd /srv/beta")
	workspaceCommand(t, e, l, "use", "/ws use 工作一")
	if got := workspaceCommand(t, e, l, "resume", "/resume"); strings.Contains(got, shortTaskID(first.ID)) || !strings.Contains(got, shortTaskID(second.ID)) {
		t.Fatal("resume leaked other project", got)
	}
	resume := message("resume-old-card", "/resume use "+shortTaskID(first.ID))
	resume.ParentID = first.CardID
	accept(t, e, resume)
	tick(t, e)
	resumed := e.Store.snapshot()
	if !strings.Contains(l.replies[len(l.replies)-1], "已切换到会话") || effectiveTarget(e.Config, &resumed, resume, "nas").ProjectID != "homeops" || x.sends != 2 || x.stops != 0 {
		t.Fatal("explicit history reference did not switch to its owned project without dispatching")
	}
	// Explicit own old card returns to its original context without changing global route.
	quote := message("old-card", "继续原项目")
	quote.ParentID = first.CardID
	accept(t, e, quote)
	tick(t, e)
	if e.Store.snapshot().Tasks[first.ID].Target.ProjectID != "homeops" || x.opens != 2 {
		t.Fatal("old card moved project")
	}
	if *creates != 0 {
		t.Fatal("existing project created twice")
	}
}
func TestWorkspaceRegistrationUnknownAndAccess(t *testing.T) {
	for _, root := range []string{"/srv/new", "/srv/lost", "/srv/missing"} {
		t.Run(root, func(t *testing.T) {
			e, _, l, _, creates := workspaceFixture(t)
			e.Config.DefaultRoute = "nas"
			first := workspaceCommand(t, e, l, "cd", "/cd "+root)
			if root == "/srv/new" && !strings.Contains(first, "已切换") {
				t.Fatal(first)
			}
			if root == "/srv/missing" {
				if got := workspaceCommand(t, e, l, "pending-status", "/status"); !strings.Contains(got, "登记结果待只读核对") {
					t.Fatal("pending registration issue hidden", got)
				}
			}
			workspaceCommand(t, e, l, "again", "/cd "+root)
			if *creates != 1 {
				t.Fatal("uncertain/definite registration replayed", *creates)
			}
			state := e.Store.snapshot()
			selected := effectiveTarget(e.Config, &state, message("x", ""), "nas")
			if root != "/srv/missing" && selected.ProjectPath != root {
				t.Fatal("unknown result not reconciled by list", selected)
			}
		})
	}
	e, _, l, projects, creates := workspaceFixture(t)
	e.Config.DefaultRoute = "nas"
	e.Config.Admins = nil
	if got := workspaceCommand(t, e, l, "ordinary", "/cd /unregistered"); !strings.Contains(got, "仅owner/admin") {
		t.Fatal(got)
	}
	if *creates != 0 {
		t.Fatal("ordinary user registered project")
	}
	for _, p := range []string{"relative", "~/nas", "../x"} {
		if got := workspaceCommand(t, e, l, "bad-"+p, "/cd "+p); !strings.Contains(got, "绝对路径") {
			t.Fatal(got)
		}
	}
	*projects = append(*projects, Project{ID: "duplicate", Root: "/srv/alpha"})
	if got := workspaceCommand(t, e, l, "duplicate", "/cd /srv/alpha"); !strings.Contains(got, "多个T3项目") {
		t.Fatal(got)
	}
	m := message("other", "/ws list")
	m.UserID = "other"
	accept(t, e, m)
	tick(t, e)
	if strings.Contains(l.replies[len(l.replies)-1], "工作一") {
		t.Fatal("alias scope leaked")
	}
}

func TestWorkspaceSwitchRecoversPersistedRegistrationAttempt(t *testing.T) {
	e, x, l, projects, creates := workspaceFixture(t)
	e.Config.DefaultRoute = "nas"
	accept(t, e, message("original", "/nas 原轮次"))
	tick(t, e)
	original := onlyTask(t, e)
	request := message("switch", "/cd --stop /srv/recovered")
	accept(t, e, request)

	// A crash after T3 accepted the registration can leave only the attempt.
	*projects = append(*projects, Project{ID: "recovered", Title: "恢复项目", Root: "/srv/recovered"})
	if err := e.saveCommand(key(request, request.ID), &SessionCommand{
		Source: request, Kind: "/cd", RouteID: "nas", BaseTargetID: targetIdentity(e.Config.Routes["nas"]),
		Target: original.Target, WorkspacePath: "/srv/recovered", ProjectAttempted: true,
	}); err != nil {
		t.Fatal(err)
	}
	tick(t, e)
	state := e.Store.snapshot()
	if selected := effectiveTarget(e.Config, &state, request, "nas"); selected.ProjectID != "recovered" {
		t.Fatalf("workspace did not follow verified project readback: %+v", selected)
	}
	if *creates != 0 || x.stops != 1 || !strings.Contains(l.replies[len(l.replies)-1], "已请求停止原轮次") {
		t.Fatalf("unexpected replay or missing reply: creates=%d stops=%d replies=%v", *creates, x.stops, l.replies)
	}
}

func TestWorkspaceWindowsPathsFromAnotherHost(t *testing.T) {
	e, _, lark, projects, creates := workspaceFixture(t)
	e.Config.DefaultRoute = "nas"
	const drive = `C:\work\Project`
	const share = `\\files\Projects\Shared`
	*projects = append(*projects, Project{ID: "windows-drive", Title: "Drive", Root: drive}, Project{ID: "windows-share", Title: "Share", Root: share})
	for _, step := range []struct{ id, command, want string }{
		{"first", `/cd c:/work/./Project`, drive},
		{"save", `/ws save Drive`, "已保存"},
		{"share", "/cd " + share, share},
		{"use", "/ws use Drive", drive},
		{"status", "/status", drive},
		{"device", `/cd \\.\C:\work\Project`, "绝对路径"},
	} {
		if got := workspaceCommand(t, e, lark, step.id, step.command); !strings.Contains(got, step.want) {
			t.Fatalf("%s returned %q, want %q", step.command, got, step.want)
		}
	}
	if *creates != 0 || sameWorkspacePath(drive, `c:\work\project`) {
		t.Fatal("workspace control created a duplicate project or treated a different directory case as equal")
	}
}

func TestWorkspaceAliasScopeAndSaveFailure(t *testing.T) {
	e, _, l, _, creates := workspaceFixture(t)
	e.Config.DefaultRoute = "nas"
	e.Config.Topics = nil
	workspaceCommand(t, e, l, "select", "/cd /srv/alpha")
	workspaceCommand(t, e, l, "save", "/ws save 私有别名")
	for _, mode := range []string{"user", "topic", "route"} {
		m := message("list-"+mode, "/ws list")
		if mode == "user" {
			m.UserID = "other"
		}
		if mode == "topic" {
			m.TopicID = "elsewhere"
		}
		if mode == "route" {
			e.Store.update(func(st *State) error { st.Selected[userScope(m)] = "mac"; return nil })
		}
		accept(t, e, m)
		tick(t, e)
		if strings.Contains(l.replies[len(l.replies)-1], "私有别名") {
			t.Fatal("alias leaked scope", mode)
		}
	}
	// Restore the route only; selection remains scoped and aliases remove never change the project.
	e.Store.update(func(st *State) error { st.Selected[userScope(message("x", ""))] = "nas"; return nil })
	workspaceCommand(t, e, l, "remove", "/ws remove 私有别名")
	if got := workspaceCommand(t, e, l, "list-removed", "/ws list"); strings.Contains(got, "私有别名") {
		t.Fatal("alias remove failed")
	}
	old := e.Store.snapshot()
	originalDir := e.Store.dir
	accept(t, e, message("failing-register", "/cd /never-created"))
	e.Store.dir = originalDir + "/missing"
	if err := e.Tick(context.Background()); err == nil {
		t.Fatal("persist failure falsely succeeded")
	}
	e.Store.dir = originalDir
	if *creates != 0 || len(e.Store.snapshot().Workspaces) != len(old.Workspaces) {
		t.Fatal("registration occurred before durable intent")
	}
}
func TestWorkspaceProjectPaginationAndReadIdentity(t *testing.T) {
	for _, mode := range []string{"loop", "shifted", "error", "overflow"} {
		t.Run(mode, func(t *testing.T) {
			api := &T3{Calls: scriptedCaller(func(name string, a map[string]any) (any, error) {
				if mode == "error" {
					return nil, errors.New("private remote error")
				}
				cursor := int(a["cursor"].(float64))
				id := "same"
				if mode == "overflow" {
					id = string(rune('a' + cursor))
				}
				next := cursor + 1
				if mode == "loop" {
					next = cursor
				}
				return map[string]any{"projects": []Project{{ID: id}}, "nextCursor": next}, nil
			})}
			if _, err := api.Projects(context.Background(), testConfig(t).Routes["nas"]); err == nil {
				t.Fatal("partial/unstable project listing accepted")
			}
		})
	}
}
