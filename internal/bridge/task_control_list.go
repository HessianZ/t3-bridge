package bridge

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	chatTasksCommand = "/status tasks"
	docTasksCommand  = "/doc tasks"
)

var chatTaskTopicIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

type taskListCard struct {
	Title string        `json:"title"`
	Rows  []taskListRow `json:"rows,omitempty"`
	Hint  string        `json:"hint,omitempty"`
}

type taskListRow struct {
	Text string `json:"text"`
	Stop int    `json:"stop,omitempty"` // One-based button index, zero when the row cannot be stopped.
}

func taskListCommand(command string) bool {
	return command == chatTasksCommand || command == docTasksCommand
}

func (e *Engine) chatTasksControl(st *State, m Message, cmd *SessionCommand, args []string) {
	e.managedTasksControl(st, m, cmd, chatTasksCommand, args)
}

func (e *Engine) managedTasksControl(st *State, m Message, cmd *SessionCommand, command string, args []string) {
	const pageSize = 8
	kind, tokenFor, eligible := "聊天", chatTaskToken, e.chatTaskEligible
	usage, controls := "用法：/status tasks [oc_群或私聊ID[:话题ID]] [页码]", "\n停止：/stop task:<令牌>"
	hint := ""
	if command == docTasksCommand {
		kind, tokenFor, eligible = "评论", docTaskToken, e.docTaskEligible
		usage = "用法：/doc tasks [页码]"
		hint = "空闲超时：/timeout comment:<token> [1..120|off|default]"
		controls = "\n停止：/stop comment:<token>\n" + hint
	}
	if !e.privateAdminAllowed(m) {
		cmd.Reply = "仅当前应用的所有者或管理员可在已授权私聊管理" + kind + "任务。"
		return
	}
	cmd.ChatAdminReply, cmd.DocAdminReply = command == chatTasksCommand, command == docTasksCommand
	if len(args) > 2 || (command == docTasksCommand && len(args) > 1) {
		cmd.Reply = usage
		return
	}
	var filter, chatID, topicID string
	if command == chatTasksCommand && len(args) > 0 && (len(args) == 2 || strings.HasPrefix(args[0], "oc_")) {
		filter = args[0]
		var hasTopic bool
		chatID, topicID, hasTopic = strings.Cut(filter, ":")
		if !groupChatIDPattern.MatchString(chatID) || (hasTopic && !chatTaskTopicIDPattern.MatchString(topicID)) {
			cmd.Reply = "请提供有效的聊天或话题 ID。" + usage
			return
		}
		args = args[1:]
	}
	page := 1
	if len(args) == 1 {
		var err error
		page, err = strconv.Atoi(args[0])
		if err != nil || page < 1 {
			cmd.Reply = "页码必须为正整数。" + usage
			return
		}
	}

	type entry struct {
		task    *Task
		updated time.Time
	}
	var tasks []entry
	for _, task := range st.Tasks {
		if !eligible(st, task) || (chatID != "" && task.Source.ChatID != chatID) || (topicID != "" && task.Source.TopicID != topicID) {
			continue
		}
		updated, _ := time.Parse(time.RFC3339Nano, task.Updated)
		tasks = append(tasks, entry{task: task, updated: updated})
	}
	pages := max(1, (len(tasks)+pageSize-1)/pageSize)
	if page > pages && m.CommandAction == "" {
		cmd.Reply = fmt.Sprintf("页码超出范围，共%d页。%s", pages, usage)
		return
	}
	page = min(page, pages)
	slices.SortFunc(tasks, func(a, b entry) int {
		if order := b.updated.Compare(a.updated); order != 0 {
			return order
		}
		return strings.Compare(b.task.ID, a.task.ID)
	})

	card := &CommandCard{AppID: e.Config.AppID, TaskList: &taskListCard{
		Title: fmt.Sprintf("%s任务 %d/%d", kind, page, pages),
		Hint:  hint,
	}}
	if len(tasks) == 0 {
		card.TaskList.Title = "当前没有可管理的" + kind + "任务。"
	}
	pageArgs := func(n int) []string {
		if filter == "" {
			return []string{strconv.Itoa(n)}
		}
		return []string{filter, strconv.Itoa(n)}
	}
	var reply strings.Builder
	reply.WriteString(card.TaskList.Title)
	for _, entry := range tasks[(page-1)*pageSize : min(page*pageSize, len(tasks))] {
		t := entry.task
		token := tokenFor(t)
		row := taskListRow{Text: managedTaskLine(command, token, t, entry.updated)}
		if !terminal(t.Phase) && !t.StopRequested {
			card.Buttons = append(card.Buttons, CommandButton{Label: "停止 " + token, Command: command, TaskID: t.ID, RequestID: t.RequestID, TaskListArgs: pageArgs(page)})
			row.Stop = len(card.Buttons)
		}
		card.TaskList.Rows = append(card.TaskList.Rows, row)
		reply.WriteString("\n" + row.Text)
	}
	if page > 1 {
		card.Buttons = append(card.Buttons, CommandButton{Label: "上一页", Command: command, TaskListArgs: pageArgs(page - 1)})
	}
	if page < pages {
		card.Buttons = append(card.Buttons, CommandButton{Label: "下一页", Command: command, TaskListArgs: pageArgs(page + 1)})
	}
	card.Buttons = append(card.Buttons, CommandButton{Label: "刷新任务", Command: command, TaskListArgs: pageArgs(page)},
		CommandButton{Label: "返回状态", Command: "/status"})
	if pages > 1 {
		reply.WriteString("\n翻页时在原命令末尾指定页码。")
	}
	reply.WriteString(controls)
	cmd.Reply = reply.String()
	// The task list is scoped to the receiving App, independent of the admin's own project.
	cmd.RouteID, cmd.Target, cmd.BaseTargetID = "", Target{}, ""
	cmd.Card = card
}

func managedTaskLine(command, token string, task *Task, updated time.Time) string {
	if command == docTasksCommand {
		return fmt.Sprintf("comment:%s · %s", token, taskControlPhase(task))
	}
	kind := "私聊"
	switch {
	case task.Source.TopicID != "":
		kind = "话题"
	case task.Source.ChatType == "group":
		kind = "群聊"
	}
	line := fmt.Sprintf("task:%s · %s · %s", token, kind, taskControlPhase(task))
	if !updated.IsZero() {
		line += " · " + updated.UTC().Format(time.RFC3339Nano)
	}
	return line
}
