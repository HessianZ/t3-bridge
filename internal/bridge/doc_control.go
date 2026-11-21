package bridge

import (
	"fmt"
	"strings"
)

func (e *Engine) docControl(st *State, m Message, cmd *SessionCommand) {
	words := strings.Fields(routeText(m))
	if len(words) > 1 && words[1] == "tasks" {
		e.managedTasksControl(st, m, cmd, docTasksCommand, words[2:])
		return
	}
	route := e.Config.CommentRoute
	if route == "" {
		cmd.Reply = "当前账号未启用云文档评论。请在接收端主机的配置工作台为该账号设置「云文档评论路由 ID」（commentRoute），保存后重启接收端。"
		return
	}
	// Check the actor as a document commenter, using the same ACL as comment intake.
	commenter := Message{UserID: m.UserID, Comment: &CommentSource{}}
	if !e.Config.TargetAllowed(commenter, e.Config.Routes[route], route) {
		cmd.Reply = "当前账号已启用云文档评论。使用前请联系应用管理员确认你的账号权限及评论路由授权。"
		return
	}
	cmd.Reply = "当前账号已启用云文档评论。获授权的用户可在支持的文档评论中 @ 机器人提问，答复会发回该文档。请确认机器人有该文档的评论权限。"
}

func taskControlPhase(t *Task) string {
	if t.NeedsReconcile {
		return "待核对"
	}
	if t.StopRequested && !terminal(t.Phase) {
		return "正在停止"
	}
	switch t.Phase {
	case "ready":
		return "排队中"
	case "waiting_capacity":
		return "等待并发额度"
	case "waiting_resource":
		return "等待资源"
	case "opening", "dispatching":
		return "正在派发"
	case "needs_reconcile", "settling":
		return "待核对"
	case "tracking":
		if t.PendingApprovals > 0 {
			return fmt.Sprintf("等待 T3 审批或问题（%d 项）", t.PendingApprovals)
		}
		return "已派发"
	case "awaiting_children":
		return "等待子任务"
	case "completed":
		return "已完成"
	case "failed":
		return "失败"
	case "stopped":
		return "已停止"
	case "blocked":
		return "受阻"
	case "clarify":
		return "待澄清"
	default:
		return "状态未知"
	}
}
