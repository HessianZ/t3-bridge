package bridge

import (
	"fmt"
	"strings"
)

func docTaskToken(t *Task) string {
	return taskControlToken("comment-task", t)
}

func (e *Engine) docTaskEligible(st *State, t *Task) bool {
	if t == nil || t.ID == "" || t.RequestID == "" || t.ReceiverAppID == "" || t.ReceiverAppID != e.Config.AppID ||
		(st.AccountID != "" && st.AccountID != e.Config.AppID) || t.Source.Comment == nil ||
		t.RouteID == "" || t.RouteID != e.Config.CommentRoute || !taskTargetAllowed(e.Config, t) {
		return false
	}
	return !terminal(t.Phase) || t.StopRequested || st.Current[sessionKey(t.Source, t.RouteID, t.Target)] == t.ID
}

func (e *Engine) docTaskControl(m Message, id string, cmd *SessionCommand, args []string, timeout bool) error {
	if !e.privateAdminAllowed(m) {
		cmd.Reply = "仅当前应用的所有者或管理员可在已授权私聊管理评论任务。"
		return nil
	}
	cmd.DocAdminReply = true
	token := strings.TrimPrefix(args[0], "comment:")
	if len(token) != taskControlTokenLength || (!timeout && len(args) != 1) || len(args) > 2 {
		cmd.Reply = "用法：/stop comment:<令牌> 或 /timeout comment:<令牌> [1..120分钟|off|default]。用 /doc tasks 查看令牌。"
		return nil
	}
	var resetIdle string
	published, err := e.Store.updatePublished(func(st *State) error {
		t := findTaskControlRound(st, token, docTaskToken, e.docTaskEligible)
		switch {
		case t == nil:
			cmd.Reply = "评论任务令牌不明或轮次已变化，请用 /doc tasks 查看。"
		case timeout:
			if e.docTaskTimeout(st, cmd, t, args[1:]) {
				resetIdle = t.ID
			}
		default:
			cmd.Reply, _ = requestManagedTaskStop(st, t, docTasksCommand)
		}
		saveCommandState(st, id, cmd)
		return nil
	})
	if published && resetIdle != "" {
		delete(e.idleRuns, resetIdle)
	}
	return err
}

// A document's T3 session can span comments. The watchdog setting follows the
// selected round's comment thread, including when replying in a newer thread.
func (e *Engine) docTaskTimeout(st *State, cmd *SessionCommand, t *Task, args []string) bool {
	source := replySource(t)
	current := currentTask(e.Config, st, source, t.RouteID)
	if source.Comment == nil || source.Comment.CommentID == "" || current == nil || current.ID != t.ID {
		cmd.Reply = "评论会话已切换或授权已变化，请用 /doc tasks 核对。"
		return false
	}
	scope := idleTimeoutScope(source, t.RouteID, t.Target)
	if len(args) == 0 {
		mode := "跟随应用默认值"
		if _, ok := st.IdleTimeouts[scope]; ok {
			mode = "会话覆盖"
		}
		cmd.Reply = fmt.Sprintf("该评论线程后续轮次空闲超时：%d分钟（0关闭，%s）。", st.idleTimeout(scope), mode)
		if !terminal(t.Phase) && !t.StopRequested {
			cmd.Reply += fmt.Sprintf("当前轮次：%d分钟。", t.IdleTimeoutMinutes)
		}
		return false
	}
	value := strings.ToLower(args[0])
	if value == "0" {
		value = "off"
	}
	minutes, ok := parseIdleTimeout(value)
	inherit := value == "default"
	if !ok && !inherit {
		cmd.Reply = "评论线程空闲超时为1..120分钟，或用 off 关闭、default 跟随默认值。"
		return false
	}
	minutes = st.setIdleTimeout(scope, t, minutes, inherit)
	cmd.Reply = fmt.Sprintf("已设置该评论线程的空闲超时为%d分钟（0关闭）。", minutes)
	if inherit {
		cmd.Reply += "后续轮次跟随应用默认值。"
	}
	return true
}
