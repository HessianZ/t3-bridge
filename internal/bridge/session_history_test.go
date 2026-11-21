package bridge

import (
	"fmt"
	"slices"
	"testing"
)

func TestSessionHistoryCandidatesAndOrder(t *testing.T) {
	const route = "work"
	m := Message{ChatType: "group", UserID: "owner", ChatID: "group-a", TopicID: "topic-a"}
	base := Target{EnvironmentID: "env", ProjectID: "default", Users: []string{"owner", "other"}, RuntimeMode: "approval-required"}
	c := Config{
		AppID: "app", Users: base.Users, Chats: []string{"group-a", "group-b"},
		Routes: map[string]Target{route: base, "other": base},
	}
	workspace := Workspace{ProjectID: "project", Path: "/workspace/project"}
	st := State{AccountID: c.AppID, Workspaces: map[string]Workspace{workspaceKey(c, m, route): workspace}}
	target := base
	target.ProjectID, target.ProjectPath = workspace.ProjectID, workspace.Path
	owned := Task{
		Source: m, RouteID: route, Target: target, BaseTargetID: targetIdentity(base),
		ThreadID: "thread", ReceiverAppID: c.AppID, Phase: "completed",
	}

	t.Run("all candidates sorted by update then ID", func(t *testing.T) {
		st := st
		st.Tasks = map[string]*Task{}
		for i := range 10 {
			task := owned
			task.ID = fmt.Sprintf("task-%02d", i)
			task.Updated = "2026-11-18T00:00:00Z"
			st.Tasks[task.ID] = &task
		}
		st.Tasks["task-00"].Updated = "2026-11-19T00:00:00Z"
		st.Tasks["task-09"].Updated = "2026-11-17T00:00:00Z"
		foreign := owned
		foreign.ID, foreign.ReceiverAppID = "foreign", "other-app"
		st.Tasks[foreign.ID] = &foreign
		want := []string{"task-00", "task-08", "task-07", "task-06", "task-05", "task-04", "task-03", "task-02", "task-01", "task-09"}
		var got []string
		for _, task := range sessionHistory(c, &st, m, route) {
			got = append(got, task.ID)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("history IDs = %v, want %v", got, want)
		}
	})

	for _, tc := range []struct {
		name   string
		change func(*Task, *State, *Target)
		want   bool
	}{
		{"same scope", nil, true},
		{"active", func(task *Task, _ *State, _ *Target) { task.Phase = "tracking" }, true},
		{"other user", func(task *Task, _ *State, _ *Target) { task.Source.UserID = "other" }, false},
		{"other group", func(task *Task, _ *State, _ *Target) { task.Source.ChatID = "group-b" }, false},
		{"other topic", func(task *Task, _ *State, _ *Target) { task.Source.TopicID = "topic-b" }, false},
		{"other route", func(task *Task, _ *State, _ *Target) { task.RouteID = "other" }, false},
		{"other project ID", func(task *Task, _ *State, _ *Target) { task.Target.ProjectID = "other-project" }, false},
		{"same project ID at other path", func(task *Task, _ *State, _ *Target) { task.Target.ProjectPath = "/workspace/other" }, false},
		{"legacy task project path", func(task *Task, _ *State, _ *Target) { task.Target.ProjectPath = "" }, true},
		{"unknown current project path", func(_ *Task, _ *State, target *Target) { target.ProjectPath = "" }, true},
		{"other receiver", func(task *Task, _ *State, _ *Target) { task.ReceiverAppID = "other-app" }, false},
		{"other receiver in unbound state", func(task *Task, st *State, _ *Target) {
			task.ReceiverAppID, st.AccountID = "other-app", ""
		}, false},
		{"legacy receiver in foreign state", func(task *Task, st *State, _ *Target) {
			task.ReceiverAppID, st.AccountID = "", "other-app"
		}, false},
		{"legacy receiver in bound state", func(task *Task, _ *State, _ *Target) { task.ReceiverAppID = "" }, true},
		{"unknown receiver and state account", func(task *Task, st *State, _ *Target) {
			task.ReceiverAppID, st.AccountID = "", ""
		}, true},
		{"changed execution permission", func(task *Task, _ *State, _ *Target) { task.Target.RuntimeMode = "full-access" }, false},
		{"meeting", func(task *Task, _ *State, _ *Target) { task.Source.Meeting = &meetingSource{} }, false},
		{"diagnostic", func(task *Task, _ *State, _ *Target) { task.Source.InputKind = doctorEchoKind }, false},
		{"no T3 thread", func(task *Task, _ *State, _ *Target) { task.ThreadID = "" }, false},
		{"stop requested", func(task *Task, _ *State, _ *Target) { task.StopRequested = true }, false},
		{"stopped", func(task *Task, _ *State, _ *Target) { task.Phase = "stopped" }, false},
		{"failed", func(task *Task, _ *State, _ *Target) { task.Phase = "failed" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task, st, target := owned, st, target
			if tc.change != nil {
				tc.change(&task, &st, &target)
			}
			if got := sessionHistoryCandidate(c, &st, m, route, target, &task); got != tc.want {
				t.Fatalf("candidate = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("revoked ACL", func(t *testing.T) {
		c := c
		c.Users = []string{"other"}
		if sessionHistoryCandidate(c, &st, m, route, target, &owned) {
			t.Fatal("candidate remained eligible after user access was revoked")
		}
	})
}
