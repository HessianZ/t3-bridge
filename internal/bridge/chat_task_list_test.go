package bridge

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestChatTasksControlPermissions(t *testing.T) {
	c := testConfig(t)
	c.Admins = []string{"user"}
	c.access = &accessRuntime{owner: "owner"}
	for _, tc := range []struct {
		name, user, chatType, topicID string
		comment                       *CommentSource
		meeting                       *meetingSource
		allowed                       bool
	}{
		{name: "owner", user: "owner", chatType: "p2p", topicID: "topic", allowed: true},
		{name: "admin", user: "user", chatType: "p2p", topicID: "topic", allowed: true},
		{name: "member", user: "other", chatType: "p2p", topicID: "topic"},
		{name: "group", user: "owner", chatType: "group", topicID: "topic"},
		{name: "outside ACL", user: "owner", chatType: "p2p"},
		{name: "comment", user: "owner", chatType: "p2p", topicID: "topic", comment: &CommentSource{}},
		{name: "meeting", user: "owner", chatType: "p2p", topicID: "topic", meeting: &meetingSource{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := Engine{Config: c}
			m := Message{UserID: tc.user, ChatType: tc.chatType, ChatID: "chat", TopicID: tc.topicID, Comment: tc.comment, Meeting: tc.meeting}
			cmd := SessionCommand{}
			var st *State
			want := "已授权私聊"
			if tc.allowed {
				st = &State{}
				want = "当前没有可管理的聊天任务"
			}
			e.chatTasksControl(st, m, &cmd, []string{"oc_PRIVATE_FILTER"})
			if cmd.ChatAdminReply != tc.allowed || !strings.Contains(cmd.Reply, want) || strings.Contains(cmd.Reply, "PRIVATE_FILTER") {
				t.Fatalf("unexpected reply: %q, privileged=%t", cmd.Reply, cmd.ChatAdminReply)
			}
		})
	}
}

func TestChatTasksControlListsFilteredMetadataInPageOrder(t *testing.T) {
	c := testConfig(t)
	c.Admins = []string{"user"}
	target := c.Routes["mac"]
	target.ProjectID, target.ProjectName, target.ProjectPath = "PRIVATE_PROJECT", "PRIVATE_NAME", "/private/workspace"
	c.Routes["mac"] = target
	e := Engine{Config: c}
	m := Message{UserID: "user", ChatType: "p2p", ChatID: "chat", TopicID: "topic"}
	st := State{AccountID: c.AppID, Tasks: map[string]*Task{}}
	tasks := make([]*Task, 10)
	for i := range tasks {
		task := &Task{
			ID: fmt.Sprintf("PRIVATE_TASK_%02d", i), RequestID: fmt.Sprintf("PRIVATE_REQUEST_%02d", i),
			ReceiverAppID: c.AppID, RouteID: "mac", Target: target,
			Source: Message{ChatID: "oc_PRIVATE_GROUP", ChatType: "group", ID: "PRIVATE_MESSAGE", UserID: "PRIVATE_USER", Text: "PRIVATE_TEXT"},
			Phase:  "ready", Updated: fmt.Sprintf("2026-11-19T00:00:%02dZ", i),
			Summary: "PRIVATE_SUMMARY", Result: "PRIVATE_RESULT", ThreadID: "PRIVATE_THREAD", RunID: "PRIVATE_RUN", Link: "https://example.test/thread",
		}
		tasks[i] = task
		st.Tasks[task.ID] = task
	}
	tasks[0].Updated = "PRIVATE_INVALID_TIMESTAMP"
	tasks[1].Source.ChatID, tasks[1].Source.ChatType = "oc_PRIVATE_DIRECT", "p2p"
	tasks[2].Updated = tasks[1].Updated
	tasks[3].Source.ChatID = "oc_PRIVATE_GROUP_SUFFIX"
	tasks[3].Source.TopicID, tasks[4].Source.TopicID = "omt_PRIVATE_TOPIC", "omt_PRIVATE_TOPIC"
	tasks[5].Source.TopicID, tasks[6].Source.TopicID = "omt_PRIVATE_TOPIC_SUFFIX", "om_PRIVATE_TOPIC"
	tasks[7].Source.TopicID, tasks[8].Source.TopicID = "PRIVATE.topic-v2", "private.topic-v2"
	tasks[8].Updated = "2026-11-19T08:00:07.1+08:00"
	tasks[9].Phase, tasks[9].StopRequested = "stopped", true
	original, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		args   []string
		header string
		order  []int
	}{
		{"page one", nil, "聊天任务 1/2", []int{9, 8, 7, 6, 5, 4, 3, 2}},
		{"page two", []string{"2"}, "聊天任务 2/2", []int{1, 0}},
		{"group", []string{"oc_PRIVATE_GROUP"}, "聊天任务 1/1", []int{9, 8, 7, 6, 5, 4, 2, 0}},
		{"private", []string{"oc_PRIVATE_DIRECT"}, "聊天任务 1/1", []int{1}},
		{"topic with page", []string{"oc_PRIVATE_GROUP:omt_PRIVATE_TOPIC", "1"}, "聊天任务 1/1", []int{4}},
		{"message-shaped topic", []string{"oc_PRIVATE_GROUP:om_PRIVATE_TOPIC"}, "聊天任务 1/1", []int{6}},
		{"opaque topic", []string{"oc_PRIVATE_GROUP:PRIVATE.topic-v2"}, "聊天任务 1/1", []int{7}},
		{"missing topic", []string{"oc_PRIVATE_DIRECT:omt_PRIVATE_TOPIC"}, "当前没有可管理的聊天任务", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := SessionCommand{}
			e.chatTasksControl(&st, m, &cmd, tc.args)
			var rows []string
			for _, line := range strings.Split(cmd.Reply, "\n") {
				if strings.HasPrefix(line, "task:") {
					rows = append(rows, line)
				}
			}
			labels := []string{"群聊", "私聊", "群聊", "话题", "话题", "话题", "话题", "话题", "话题", "群聊"}
			var want []string
			for _, i := range tc.order {
				phase, utc := "排队中", tasks[i].Updated
				switch i {
				case 0:
					utc = ""
				case 8:
					utc = "2026-11-19T00:00:07.1Z"
				case 9:
					phase = "已停止"
				}
				row := "task:" + chatTaskToken(tasks[i]) + " · " + labels[i] + " · " + phase
				if utc != "" {
					row += " · " + utc
				}
				want = append(want, row)
			}
			if !cmd.ChatAdminReply || !strings.HasPrefix(cmd.Reply, tc.header) || !slices.Equal(rows, want) {
				t.Fatalf("unexpected reply %q, want rows %q", cmd.Reply, want)
			}
			for _, hidden := range []string{"PRIVATE", "private.topic", "/private/", c.AppID, target.EnvironmentID, "https://example.test"} {
				if strings.Contains(cmd.Reply, hidden) {
					t.Errorf("reply exposed %q: %q", hidden, cmd.Reply)
				}
			}
		})
	}
	after, err := json.Marshal(st)
	if err != nil || string(after) != string(original) {
		t.Fatalf("listing modified state or made it unreadable: %v", err)
	}
}

func TestChatTasksControlArgumentBounds(t *testing.T) {
	c := testConfig(t)
	c.Admins = []string{"user"}
	e := Engine{Config: c}
	m := Message{UserID: "user", ChatType: "p2p", ChatID: "chat", TopicID: "topic"}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"0"}, "页码必须为正整数"},
		{[]string{"-1"}, "页码必须为正整数"},
		{[]string{"1.5"}, "页码必须为正整数"},
		{[]string{"999999999999999999999999999999"}, "页码必须为正整数"},
		{[]string{"oc_PRIVATE_GROUP", "-1"}, "页码必须为正整数"},
		{[]string{"2"}, "页码超出范围"},
		{[]string{"oc_PRIVATE_GROUP:om_PRIVATE_TOPIC", "2"}, "页码超出范围"},
		{[]string{"PRIVATE_BAD_CHAT", "1"}, "有效的聊天或话题 ID"},
		{[]string{"oc_PRIVATE_GROUP:"}, "有效的聊天或话题 ID"},
		{[]string{"oc_PRIVATE_GROUP:om_PRIVATE_TOPIC:extra"}, "有效的聊天或话题 ID"},
		{[]string{"oc_PRIVATE_GROUP:" + strings.Repeat("a", 129)}, "有效的聊天或话题 ID"},
		{[]string{"oc_PRIVATE_GROUP:话题"}, "有效的聊天或话题 ID"},
		{[]string{"oc_PRIVATE_GROUP", "1", "PRIVATE_EXTRA"}, "用法"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			cmd := SessionCommand{}
			e.chatTasksControl(&State{}, m, &cmd, tc.args)
			if !cmd.ChatAdminReply || !strings.Contains(cmd.Reply, tc.want) || !strings.Contains(cmd.Reply, "/status tasks") || strings.Contains(cmd.Reply, "PRIVATE") {
				t.Fatalf("unexpected reply: %q, want %q", cmd.Reply, tc.want)
			}
		})
	}
}
