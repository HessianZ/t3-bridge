package bridge

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ReceiverProcess describes the host process serving this app. Stop requests a
// graceful receiver shutdown through the host's own process registry.
type ReceiverProcess struct {
	ID        string
	PID       int
	StartedAt time.Time
	Shared    bool
	Stop      func() error
}

func (e *Engine) receiverCommand(m Message, cmd *SessionCommand) {
	receiver := e.Receiver
	if receiver == nil {
		cmd.Reply = "当前接收实例没有宿主登记。"
		return
	}
	words := strings.Fields(routeText(m))
	if cmd.Kind == "/ps" {
		if len(words) != 1 {
			cmd.Reply = "用法：/ps"
			return
		}
		cmd.Reply = fmt.Sprintf("当前应用的接收实例\nID：%s\nPID：%d\n启动：%s",
			receiver.ID, receiver.PID, receiver.StartedAt.Format(time.RFC3339))
		if receiver.Shared {
			cmd.Reply += "\n此接收进程服务多个应用。宿主 CLI 可管理进程。"
		} else if e.canStopReceiver(m) {
			cmd.Reply += "\n关闭当前接收实例：/exit " + receiver.ID
		}
		return
	}
	if !e.canStopReceiver(m) {
		cmd.Reply = "只有当前应用的所有者或管理员可在已授权私聊关闭单应用接收进程。共用接收进程由宿主 CLI 管理。"
		return
	}
	if len(words) != 2 || words[1] != receiver.ID {
		cmd.Reply = "请从 /ps 复制当前接收实例的完整 ID，再发送 /exit <实例ID>。"
		return
	}
	cmd.ReceiverStopID = receiver.ID
	cmd.Reply = "收到关闭请求。发送本回执后将尝试停止当前接收实例。在途 T3 工作需另外核对。"
}

func (e *Engine) canStopReceiver(m Message) bool {
	return e.Receiver != nil && !e.Receiver.Shared && e.Receiver.Stop != nil && m.ChatType == "p2p" &&
		e.Config.Allowed(m) && e.Config.isAdmin(m.UserID)
}

func (e *Engine) stopReceiverAfterReply(ctx context.Context, m Message, id string, cmd *SessionCommand) error {
	if !e.canStopReceiver(m) || e.Receiver.ID != cmd.ReceiverStopID {
		cmd.Issue = "实例或管理员权限已变化，未关闭接收端。"
		return e.saveCommand(id, cmd)
	}
	if err := e.Receiver.Stop(); err != nil {
		e.commandFailed(ctx, CommandFailureReceiverStop)
		cmd.Issue = "关闭接收端请求未确认，请在宿主核对。"
		return e.saveCommand(id, cmd)
	}
	return ctx.Err()
}
