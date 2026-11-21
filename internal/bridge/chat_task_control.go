package bridge

import "strings"

const taskControlTokenLength = 12

func taskControlToken(kind string, t *Task) string {
	return stable(kind, t.ReceiverAppID, t.ID, t.RequestID)[:taskControlTokenLength]
}

func chatTaskToken(t *Task) string {
	return taskControlToken("chat-task", t)
}

func findTaskControlRound(st *State, token string, tokenFor func(*Task) string, eligible func(*State, *Task) bool) *Task {
	var found *Task
	for _, task := range st.Tasks {
		if task == nil || tokenFor(task) != token || !eligible(st, task) {
			continue
		}
		if found != nil {
			return nil
		}
		found = task
	}
	return found
}

func (e *Engine) chatTaskEligible(st *State, t *Task) bool {
	if t == nil || t.ID == "" || t.RequestID == "" || t.ReceiverAppID == "" || t.ReceiverAppID != e.Config.AppID ||
		(st.AccountID != "" && st.AccountID != e.Config.AppID) || (terminal(t.Phase) && !t.StopRequested && !t.NeedsReconcile) {
		return false
	}
	m := t.Source
	return (m.ChatType == "p2p" || m.ChatType == "group") && m.ChatID != "" && m.UserID != "" &&
		m.Comment == nil && m.Meeting == nil && m.InputKind != "automatic" && m.InputKind != doctorEchoKind &&
		taskTargetAllowed(e.Config, t)
}

func (e *Engine) stopChatTask(m Message, id string, cmd *SessionCommand, args []string) error {
	if !e.privateAdminAllowed(m) {
		cmd.Reply = "仅当前应用的所有者或管理员可在已授权私聊停止指定聊天轮次。"
		return nil
	}
	cmd.ChatAdminReply = true
	token := strings.TrimPrefix(args[0], "task:")
	if len(args) != 1 || len(token) != taskControlTokenLength {
		cmd.Reply = "用法：/stop task:<令牌>。用 /status tasks 查找轮次。"
		return nil
	}
	return e.Store.update(func(st *State) error {
		task := findTaskControlRound(st, token, chatTaskToken, e.chatTaskEligible)
		cmd.Reply, _ = requestManagedTaskStop(st, task, chatTasksCommand)
		saveCommandState(st, id, cmd)
		return nil
	})
}

func requestManagedTaskStop(st *State, task *Task, command string) (string, bool) {
	kind := "聊天"
	if command == docTasksCommand {
		kind = "评论"
	}
	switch {
	case task == nil:
		return kind + "任务令牌不明或轮次已变化，请用 " + command + " 查看。", false
	case command == chatTasksCommand && task.NeedsReconcile && terminal(task.Phase):
		return "该聊天轮次的结果仍待核对。", false
	case task.StopRequested || terminal(task.Phase):
		return "该" + kind + "轮次已结束或已请求停止。", false
	}
	task.StopRequested = true
	if command == docTasksCommand {
		// A subsequent comment starts fresh, as documents have no /new button.
		scope := sessionKey(task.Source, task.RouteID, task.Target)
		if current, known := st.Current[scope]; !known || current == task.ID {
			if st.Current == nil {
				st.Current = map[string]string{}
			}
			st.Current[scope] = ""
		}
	}
	return "已请求停止该" + kind + "轮次。请用 " + command + " 核对状态。", true
}

func (e *Engine) stopManagedTaskFromCard(st *State, m Message, cmd *SessionCommand) {
	if !e.privateAdminAllowed(m) {
		cmd.Reply = "仅当前应用的所有者或管理员可在已授权私聊停止指定轮次。"
		return
	}
	origin, button := commandCardAction(e.Config, st, m)
	if origin == nil || origin.Card.Used || origin.Card.TaskList == nil || userScope(origin.Source) != userScope(m) || !taskListCommand(button.Command) {
		cmd.Reply = "任务卡已失效。请重新打开列表。"
		return
	}
	eligible := e.chatTaskEligible
	if button.Command == docTasksCommand {
		eligible = e.docTaskEligible
	}
	task := st.Tasks[button.TaskID]
	if !eligible(st, task) || task.RequestID != button.RequestID {
		task = nil
	}
	var stopped bool
	cmd.Reply, stopped = requestManagedTaskStop(st, task, button.Command)
	if stopped {
		origin.Card.Used = true
	}
	cmd.ChatAdminReply, cmd.DocAdminReply = button.Command == chatTasksCommand, button.Command == docTasksCommand
	cmd.Card = &CommandCard{AppID: e.Config.AppID, Buttons: []CommandButton{
		{Label: "返回任务列表", Command: button.Command, TaskListArgs: button.TaskListArgs},
		{Label: "返回状态", Command: "/status"},
	}}
}
