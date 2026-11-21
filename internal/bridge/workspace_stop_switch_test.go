package bridge

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestWorkspaceStopSwitchPinsCurrentScope(t *testing.T) {
	e, x, l, _, creates := workspaceFixture(t)
	e.Config.DefaultRoute = "nas"
	e.Config.Chats = append(e.Config.Chats, "other-chat")
	e.Config.Topics = append(e.Config.Topics, "other-topic")
	workspaceCommand(t, e, l, "alpha", "/cd /srv/alpha")
	workspaceCommand(t, e, l, "save", "/ws save 工作 一")
	workspaceCommand(t, e, l, "home", "/cd /main")
	accept(t, e, message("first", "/nas 第一轮"))
	tick(t, e)
	x.observation.Status, x.observation.Summary = "completed", "原轮全文"
	tick(t, e)
	x.observation.Status = "running"
	accept(t, e, message("current", "/nas 当前轮"))
	tick(t, e)
	original := onlyTask(t, e)
	if len(original.Replies) != 1 || original.Replies[0].CardID == "" || original.CardID == "" {
		t.Fatalf("fixture did not retain both rounds: %+v", original)
	}

	cmd := message("switch", "/ws use --stop 工作 一")
	alpha := original.Target
	alpha.ProjectID, alpha.ProjectName, alpha.ProjectPath = "alpha", "同名项目", "/srv/alpha"
	user, chat, topic := cmd, cmd, cmd
	user.UserID, chat.ChatID, topic.TopicID = "other", "other-chat", "other-topic"
	others := []struct {
		name   string
		source Message
		route  string
		target Target
	}{
		{"user", user, "nas", original.Target},
		{"chat", chat, "nas", original.Target},
		{"topic", topic, "nas", original.Target},
		{"route", cmd, "mac", e.Config.Routes["mac"]},
		{"project", cmd, "nas", alpha},
		{"detached", cmd, "nas", original.Target},
	}
	if err := e.Store.update(func(st *State) error {
		for _, other := range others {
			task := *original
			task.ID, task.RouteID, task.Target = other.name, other.route, other.target
			task.BaseTargetID = targetIdentity(e.Config.Routes[other.route])
			task.Source = other.source
			task.ThreadID, task.RunID, task.RequestID = "thread-"+other.name, "run-"+other.name, "request-"+other.name
			task.CardID = "card-" + other.name
			st.Tasks[task.ID] = &task
			st.Bindings[key(task.Source, task.CardID)] = task.ID
			if other.name != "detached" {
				st.Current[sessionKey(task.Source, task.RouteID, task.Target)] = task.ID
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	oldQueue := message("old-queue", "原项目排队")
	accept(t, e, oldQueue)
	alphaQueue := message("alpha-queue", "另一项目先前入队")
	alphaQueue.ProjectChoices = map[string]Workspace{
		workspaceKey(e.Config, alphaQueue, "nas"): {ProjectID: alpha.ProjectID, Name: alpha.ProjectName, Path: alpha.ProjectPath},
	}
	accept(t, e, alphaQueue)
	for _, other := range others {
		q := other.source
		q.ID, q.Text, q.ParentID = "queued-"+other.name, "/"+other.route+" 排队", "card-"+other.name
		accept(t, e, q)
	}
	accept(t, e, cmd)
	// Consume through the normal command path before the worker advances its tasks.
	if err := e.consume(t.Context(), cmd); err != nil {
		t.Fatal(err)
	}
	st := e.Store.snapshot()
	newKey := sessionKey(cmd, "nas", alpha)
	if got := effectiveTarget(e.Config, &st, cmd, "nas"); got.ProjectID != "alpha" || got.ProjectPath != alpha.ProjectPath {
		t.Fatalf("selected workspace = %+v", got)
	}
	if got, ok := st.Current[newKey]; !ok || got != "" || st.Selected[userScope(cmd)] != "nas" {
		t.Fatalf("destination mapping was not reset: current=%q present=%t selected=%q", got, ok, st.Selected[userScope(cmd)])
	}
	got := st.Tasks[original.ID]
	if !got.StopRequested || got.StopSent || got.Phase != original.Phase || x.stops != 0 {
		t.Fatalf("switch did not leave a pending stop: phase=%s requested=%t sent=%t calls=%d", got.Phase, got.StopRequested, got.StopSent, x.stops)
	}
	if got.ThreadID != original.ThreadID || got.Link != original.Link || got.RunID != original.RunID || got.RequestID != original.RequestID ||
		got.OpenRequestID != original.OpenRequestID || got.CardID != original.CardID || got.CardRequestID != original.CardRequestID ||
		!reflect.DeepEqual(got.Target, original.Target) || !reflect.DeepEqual(got.Replies, original.Replies) {
		t.Fatal("switch changed original task identity or receipts")
	}
	for _, card := range []string{original.CardID, original.Replies[0].CardID} {
		if st.Bindings[key(cmd, card)] != original.ID {
			t.Errorf("original card %s no longer resolves to its task", card)
		}
	}
	if q, ok := queuedPresent(&st, oldQueue); !ok || !q.Cancelled {
		t.Fatalf("original queued input was not cancelled: present=%t cancelled=%t", ok, q.Cancelled)
	}
	if q, ok := queuedPresent(&st, alphaQueue); !ok || q.Cancelled {
		t.Fatalf("input pinned to another project was affected: present=%t cancelled=%t", ok, q.Cancelled)
	}
	for _, other := range others {
		task := st.Tasks[other.name]
		q := other.source
		q.ID = "queued-" + other.name
		input, ok := queuedPresent(&st, q)
		if task.StopRequested || task.StopSent || !ok || input.Cancelled {
			t.Errorf("%s was affected: requested=%t sent=%t queued=%t cancelled=%t", other.name, task.StopRequested, task.StopSent, ok, input.Cancelled)
		}
	}
	if *creates != 0 || x.sends != 2 || !strings.Contains(l.replies[len(l.replies)-1], "已请求停止原轮次") {
		t.Fatalf("unexpected create, dispatch or reply: creates=%d sends=%d replies=%v", *creates, x.sends, l.replies)
	}
}

func TestWorkspaceStopSwitchRecoversIntentAndDoesNotReplay(t *testing.T) {
	e, x, l, _, _ := workspaceFixture(t)
	e.Config.DefaultRoute = "nas"
	accept(t, e, message("old", "/nas 原轮次"))
	tick(t, e)
	old := onlyTask(t, e)
	cmd := message("switch", "/cd --stop /srv/alpha")
	l.replyErr = errors.New("synthetic lost switch reply")
	accept(t, e, cmd)
	if err := e.consume(t.Context(), cmd); err != nil {
		t.Fatal(err)
	}
	pending := e.Store.task(old.ID)
	if !pending.StopRequested || pending.StopSent || x.stops != 0 {
		t.Fatal("fixture did not persist stop intent before dispatch")
	}
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	e = &Engine{Config: e.Config, Store: s, T3: e.T3, Lark: l, Maintenance: e.Maintenance, Now: e.Now}
	st := s.snapshot()
	receipt := st.Commands[key(cmd, cmd.ID)]
	if _, ok := queuedPresent(&st, cmd); !ok || receipt == nil || receipt.Issue == "" || effectiveTarget(e.Config, &st, cmd, "nas").ProjectID != "alpha" {
		t.Fatal("restart did not recover the selection and pending reply")
	}

	// Give replay a different active round to hit while the original reply is retried.
	next := message("next", "/nas 新项目首轮")
	accept(t, e, next)
	if err := e.consume(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	st = s.snapshot()
	current := currentTask(e.Config, &st, next, "nas")
	if current == nil || current.ID == old.ID || current.Target.ProjectID != "alpha" {
		t.Fatal("new input did not bind to the switched workspace")
	}
	l.replyErr = nil
	x.stopErr = errors.New("synthetic lost Stop response")
	accept(t, e, cmd)
	tick(t, e)
	accept(t, e, cmd)
	tick(t, e)

	st = s.snapshot()
	recovered := st.Tasks[old.ID]
	if x.stops != 1 || x.sends != 2 || !recovered.StopSent || !recovered.NeedsReconcile ||
		recovered.ThreadID != old.ThreadID || recovered.RunID != old.RunID || recovered.RequestID != old.RequestID {
		t.Fatalf("original Stop was not recovered once with its identity: stops=%d sends=%d task=%+v", x.stops, x.sends, recovered)
	}
	if got := currentTask(e.Config, &st, next, "nas"); got == nil || got.ID != current.ID || got.StopRequested || terminal(got.Phase) {
		t.Fatalf("command replay changed the new current round: %+v", got)
	}
	if got := st.Commands[key(cmd, cmd.ID)]; got == nil || got.Reply != receipt.Reply || got.Issue != "" {
		t.Fatalf("command receipt was not delivered on retry: %+v", got)
	}
}

func TestWorkspaceStopSwitchRejectsUnverifiedProjectsAndBusyCards(t *testing.T) {
	e, x, l, _, _ := workspaceFixture(t)
	e.Config.DefaultRoute = "nas"
	e.Lark = commandCardRecorder{l}
	cards := &Lark{Config: e.Config, Store: e.Store}
	workspaceCommand(t, e, l, "alpha", "/cd /srv/alpha")
	workspaceCommand(t, e, l, "save", "/ws save Alpha")
	workspaceCommand(t, e, l, "home", "/cd /main")
	body := workspaceCommand(t, e, l, "list", "/ws list")
	list := message("list", "")
	card := e.Store.snapshot().Commands[key(list, list.ID)]
	accept(t, e, message("first", "/nas 旧轮"))
	tick(t, e)
	x.observation.Status = "completed"
	tick(t, e)
	oldCard := onlyTask(t, e).CardID
	x.observation.Status = "running"
	accept(t, e, message("current", "/nas 新轮"))
	tick(t, e)
	current := onlyTask(t, e)
	if oldCard == "" || oldCard == current.CardID {
		t.Fatal("fixture lacks a distinct historical result card")
	}

	clickCommandCard(t, cards, commandCardClick(t, card, body, "切换：Alpha", "busy"))
	tick(t, e)
	if got := l.replies[len(l.replies)-1]; !strings.Contains(got, "仍有运行") {
		t.Fatalf("card did not report busy: %s", got)
	}
	for _, tc := range []struct{ id, command, want string }{
		{"plain", "/cd /srv/alpha", "仍有运行"},
		{"relative", "/cd --stop srv/alpha", "绝对路径"},
		{"unknown-alias", "/ws use --stop Missing", "没有这个工作区别名"},
	} {
		if got := workspaceCommand(t, e, l, tc.id, tc.command); !strings.Contains(got, tc.want) {
			t.Fatalf("%s reply = %q, want %q", tc.command, got, tc.want)
		}
	}
	quoted := message("old-card", "/cd --stop /srv/alpha")
	quoted.ParentID = oldCard
	accept(t, e, quoted)
	tick(t, e)
	if got := l.replies[len(l.replies)-1]; !strings.Contains(got, "直接发送") {
		t.Fatalf("historical card reference did not require a direct command: %s", got)
	}

	api := e.T3.(*workspaceExecutor).api
	call := api.Calls.(scriptedCaller)
	for _, invalid := range []Project{
		{ID: "beta", Root: "/srv/alpha"},
		{ID: "alpha", Root: "/srv/moved"},
	} {
		api.Calls = scriptedCaller(func(name string, args map[string]any) (any, error) {
			if name == "t3_project_read" && args["projectId"] == "alpha" {
				return invalid, nil
			}
			return call(name, args)
		})
		for _, tc := range []struct{ mode, command string }{
			{"path", "/cd --stop /srv/alpha"},
			{"alias", "/ws use --stop Alpha"},
		} {
			if got := workspaceCommand(t, e, l, tc.mode+"-"+invalid.ID, tc.command); !strings.Contains(got, "原绑定保留") {
				t.Fatalf("%s accepted mismatching readback %+v: %q", tc.command, invalid, got)
			}
		}
	}
	st := e.Store.snapshot()
	if got := effectiveTarget(e.Config, &st, message("check", ""), "nas"); got.ProjectID != "homeops" ||
		x.stops != 0 || st.Tasks[current.ID].StopRequested || st.Commands[key(list, list.ID)].Card.Used {
		t.Fatal("rejected switch changed workspace, consumed the card or requested Stop")
	}
}

func TestWorkspaceStopSwitchFailedCommitRollsBackTogether(t *testing.T) {
	e, x, _, _, _ := workspaceFixture(t)
	e.Config.DefaultRoute = "nas"
	accept(t, e, message("old", "/nas 原轮次"))
	tick(t, e)
	old := onlyTask(t, e)
	q := message("queue", "原项目排队")
	cmd := message("switch", "/cd --stop /srv/alpha")
	accept(t, e, q)
	accept(t, e, cmd)
	before := e.Store.snapshot()
	saveErr := errors.New("synthetic unpublished workspace switch")
	writes := 0
	e.Store.writeState = func(_ string, data []byte, _ bool) (bool, error) {
		writes++
		// This fixture runs under the Store lock. Inspect the candidate bytes.
		var candidate State
		if err := json.Unmarshal(data, &candidate); err != nil {
			t.Error(err)
			return false, saveErr
		}
		selected := effectiveTarget(e.Config, &candidate, cmd, "nas")
		mapping, mapped := candidate.Current[sessionKey(cmd, "nas", selected)]
		input, queued := queuedPresent(&candidate, q)
		task := candidate.Tasks[old.ID]
		if selected.ProjectID != "alpha" || !mapped || mapping != "" || candidate.Selected[userScope(cmd)] != "nas" ||
			task == nil || !task.StopRequested || task.StopSent || !queued || !input.Cancelled ||
			candidate.Commands[key(cmd, cmd.ID)] == nil {
			t.Errorf("partial switch commit: project=%s mapping=%q present=%t task=%+v queue=%+v", selected.ProjectID, mapping, mapped, task, input)
		}
		return false, saveErr
	}
	err := e.Tick(t.Context())
	e.Store.writeState = nil
	if !errors.Is(err, saveErr) || writes != 1 || x.stops != 0 || x.sends != 1 || !reflect.DeepEqual(e.Store.snapshot(), before) {
		t.Fatalf("failed switch did not roll back: err=%v writes=%d stops=%d sends=%d", err, writes, x.stops, x.sends)
	}
}
