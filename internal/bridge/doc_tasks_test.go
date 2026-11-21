package bridge

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestDocTasksListsOnlyEligibleRounds(t *testing.T) {
	e, x, lark, _ := testEngine(t)
	e.Config.Admins = []string{"user"}
	e.Config.CommentRoute = "PRIVATE_ROUTE"
	target := e.Config.Routes["mac"]
	target.ProjectID, target.ProjectName, target.ProjectPath = "PRIVATE_PROJECT_ID", "PRIVATE_PROJECT_NAME", "/private/project"
	e.Config.Routes[e.Config.CommentRoute] = target
	lark.onResolve = func() { t.Error("listing resolved message content") }

	newTask := func(i int, phase, updated string) *Task {
		return &Task{
			ID: fmt.Sprintf("PRIVATE_TASK_%02d", i), RequestID: fmt.Sprintf("PRIVATE_REQUEST_%02d", i),
			ReceiverAppID: e.Config.AppID, RouteID: e.Config.CommentRoute, Target: target,
			Source: Message{
				UserID: "other", ChatID: "PRIVATE_CHAT", Text: "PRIVATE_QUESTION",
				Comment: &CommentSource{FileType: "docx", FileToken: "PRIVATE_FILE", CommentID: fmt.Sprintf("PRIVATE_COMMENT_%02d", i)},
			},
			Phase: phase, Updated: updated, Result: "PRIVATE_RESULT", Summary: "PRIVATE_SUMMARY",
			ThreadID: "PRIVATE_THREAD", Link: "https://example.test/private-thread", RunID: "PRIVATE_RUN",
		}
	}
	tasks := []*Task{
		newTask(0, "PRIVATE_UNKNOWN_PHASE", ""),
		newTask(1, "ready", "2026-11-19T00:00:02Z"),
		newTask(2, "waiting_capacity", "2026-11-19T00:00:02Z"),
		newTask(3, "opening", "2026-11-19T00:00:03Z"),
		newTask(4, "dispatching", "2026-11-19T00:00:04Z"),
		newTask(5, "tracking", "2026-11-19T00:00:05Z"),
		newTask(6, "stopped", "2026-11-19T00:00:06Z"),
		newTask(7, "tracking", "2026-11-19T00:00:07Z"),
		newTask(8, "awaiting_children", "2026-11-19T00:00:07.1Z"),
		newTask(9, "completed", "2026-11-19T00:00:09Z"),
	}
	tasks[5].StopRequested = true
	tasks[6].StopRequested, tasks[6].NeedsReconcile = true, true
	current := tasks[9]
	old := newTask(10, "completed", "2026-11-19T00:00:10Z")
	otherApp := newTask(11, "tracking", "2026-11-19T00:00:11Z")
	otherApp.ReceiverAppID = "PRIVATE_OTHER_APP"
	ordinary := newTask(12, "tracking", "2026-11-19T00:00:12Z")
	ordinary.Source.Comment = nil
	oldTarget := newTask(13, "tracking", "2026-11-19T00:00:13Z")
	oldTarget.Target.ProjectID = "PRIVATE_OLD_PROJECT"
	if err := e.Store.update(func(st *State) error {
		st.AccountID = e.Config.AppID
		st.Current = map[string]string{sessionKey(current.Source, current.RouteID, current.Target): current.ID}
		for _, task := range append(tasks, old, otherApp, ordinary, oldTarget) {
			st.Tasks[task.ID] = task
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := e.Store.snapshot()
	labels := []string{"状态未知", "排队中", "等待并发额度", "正在派发", "正在派发", "正在停止", "待核对", "已派发", "等待子任务", "已完成"}
	for _, tc := range []struct {
		command, header string
		indices         []int
	}{
		{"/doc tasks", "评论任务 1/2", []int{9, 8, 7, 6, 5, 4, 3, 2}},
		{"/doc tasks 2", "评论任务 2/2", []int{1, 0}},
	} {
		t.Run(tc.command, func(t *testing.T) {
			m := message(tc.header, tc.command)
			m.ChatType = "p2p"
			accept(t, e, m)
			// Run the command without advancing the seeded task lifecycles.
			if handled, err := e.sessionControl(context.Background(), m); !handled || err != nil {
				t.Fatalf("list handled=%t, error=%v", handled, err)
			}
			st := e.Store.snapshot()
			cmd := st.Commands[key(m, m.ID)]
			if cmd == nil || !cmd.DocAdminReply || cmd.AccountAdminReply || cmd.Issue != "" || len(lark.replies) == 0 || lark.replies[len(lark.replies)-1] != cmd.Reply {
				t.Fatalf("unexpected listing receipt: %+v, replies=%q", cmd, lark.replies)
			}
			var rows []string
			for _, line := range strings.Split(cmd.Reply, "\n") {
				if strings.HasPrefix(line, "comment:") {
					rows = append(rows, line)
				}
			}
			var want []string
			for _, i := range tc.indices {
				want = append(want, "comment:"+docTaskToken(tasks[i])+" · "+labels[i])
			}
			if !slices.Equal(rows, want) || !strings.HasPrefix(cmd.Reply, tc.header+"\n") ||
				!strings.Contains(cmd.Reply, "/stop comment:<token>") || !strings.Contains(cmd.Reply, "/timeout comment:<token> [1..120|off|default]") {
				t.Fatalf("unexpected listing: %q, want rows %q", cmd.Reply, want)
			}
			for _, hidden := range []string{"PRIVATE_", "/private/", "other", e.Config.AppID, target.EnvironmentID, "https://example.test"} {
				if strings.Contains(cmd.Reply, hidden) {
					t.Errorf("listing disclosed %q: %q", hidden, cmd.Reply)
				}
			}
			if !reflect.DeepEqual(st.Tasks, before.Tasks) || !reflect.DeepEqual(st.Current, before.Current) || len(st.Inbox) != 0 ||
				x.preflights != 0 || x.opens != 0 || x.sends != 0 || x.stops != 0 || lark.notifications != 0 {
				t.Fatal("listing changed tasks or dispatched work")
			}
		})
	}
}

func TestDocTasksPermissionsAndUsage(t *testing.T) {
	for _, tc := range []struct {
		name, user, chat, command, want string
		privileged                      bool
	}{
		{"owner", "owner", "p2p", "/doc tasks 1", "当前没有可管理的评论任务", true},
		{"admin", "user", "p2p", "/doc tasks", "当前没有可管理的评论任务", true},
		{"member", "other", "p2p", "/doc tasks", "已授权私聊", false},
		{"group admin", "user", "group", "/doc tasks", "已授权私聊", false},
		{"zero", "user", "p2p", "/doc tasks 0", "页码必须为正整数", true},
		{"negative", "user", "p2p", "/doc tasks -1", "页码必须为正整数", true},
		{"noninteger", "user", "p2p", "/doc tasks PRIVATE_ARG", "页码必须为正整数", true},
		{"outside range", "user", "p2p", "/doc tasks 2", "页码超出范围", true},
		{"extra argument", "user", "p2p", "/doc tasks 1 PRIVATE_ARG", "用法：/doc tasks [页码]", true},
		{"regular doc", "other", "group", "/doc", "在支持的文档评论中 @ 机器人", false},
		{"unsupported args", "other", "p2p", "/doc bind PRIVATE_ARG", "在支持的文档评论中 @ 机器人", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _, lark, _ := testEngine(t)
			e.Config.CommentRoute = "mac"
			e.Config.Admins = []string{"user"}
			e.Config.access = &accessRuntime{owner: "owner"}
			m := message("doc", tc.command)
			m.UserID, m.ChatType = tc.user, tc.chat
			accept(t, e, m)
			tick(t, e)
			cmd := e.Store.snapshot().Commands[key(m, m.ID)]
			if cmd == nil || cmd.Kind != "/doc" || cmd.DocAdminReply != tc.privileged || len(lark.replies) != 1 ||
				cmd.Reply != lark.replies[0] || !strings.Contains(cmd.Reply, tc.want) || strings.Contains(cmd.Reply, "PRIVATE_ARG") {
				t.Fatalf("unexpected /doc receipt: %+v, replies=%q", cmd, lark.replies)
			}
		})
	}
}
