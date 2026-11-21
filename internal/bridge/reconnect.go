package bridge

import (
	"context"
	"errors"
	"time"
)

const reconnectUncertain = "本次重连已尝试，结果待 /doctor 核对；未重复重连或重放任务。"

const (
	ReconnectStarted           = "started"
	ReconnectPending           = "pending"
	ReconnectUnconfirmed       = "unconfirmed"
	ReconnectScheduled         = "scheduled"
	ReconnectCancelled         = "cancelled"
	ReconnectIdle              = "idle"
	ReconnectRecoveryRequested = "recovery-requested"
	ReconnectWorkerOffline     = "worker-offline"

	reconnectWaitingText   = "等待在途 T3 工作收口，已暂停本账号新派发。"
	reconnectCancelledText = "已取消等待重连，恢复新派发。"
	localReconnectIntent   = "local" // Unlike a Feishu command key, this contains no scope separators.
)

type ReconnectAction string

const (
	ReconnectNow        ReconnectAction = "now"
	ReconnectAfterRuns  ReconnectAction = "wait"
	ReconnectCancelWait ReconnectAction = "cancel"
)

func (a ReconnectAction) Valid() bool {
	return a == ReconnectNow || a == ReconnectAfterRuns || a == ReconnectCancelWait
}

type LocalReconnectView struct {
	State string `json:"state"`
}

// LocalReconnect runs on the bound App's worker. New connections use its lifetime.
func (e *Engine) LocalReconnect(ctx context.Context) (LocalReconnectView, error) {
	return e.ChangeLocalReconnect(ctx, ReconnectNow)
}

func (e *Engine) ChangeLocalReconnect(ctx context.Context, action ReconnectAction) (LocalReconnectView, error) {
	s := e.Store
	if !action.Valid() {
		return LocalReconnectView{}, errors.New("无效的本机重连操作")
	}
	if s == nil || e.Config.AppID == "" || (e.Reconnect == nil && action != ReconnectCancelWait) {
		return LocalReconnectView{}, errors.New("接收端尚未提供本机重连")
	}
	if err := ctx.Err(); err != nil {
		return LocalReconnectView{}, err
	}
	s.mu.Lock()
	appID, pending := s.state.AccountID, s.state.PendingReconnect
	s.mu.Unlock()
	if appID != e.Config.AppID {
		return LocalReconnectView{}, errors.New("接收端状态未绑定到当前应用")
	}
	if action == ReconnectCancelWait {
		if pending == "" {
			return LocalReconnectView{State: ReconnectIdle}, nil
		}
		err := s.update(func(st *State) error {
			clearReconnectWait(st, reconnectCancelledText)
			return nil
		})
		return LocalReconnectView{State: ReconnectCancelled}, err
	}
	if pending != "" {
		return LocalReconnectView{State: ReconnectPending}, nil
	}
	if action == ReconnectAfterRuns {
		err := s.update(func(st *State) error {
			setReconnectWait(st, localReconnectIntent)
			return nil
		})
		return LocalReconnectView{State: ReconnectScheduled}, err
	}
	return e.performLocalReconnect(ctx)
}

func (e *Engine) performLocalReconnect(ctx context.Context) (LocalReconnectView, error) {
	if err := e.Store.update(func(st *State) error {
		clearReconnectWait(st, reconnectUncertain)
		return nil
	}); err != nil {
		return LocalReconnectView{}, err
	}
	status, ok := e.replaceReceiverClients(ctx)
	view := LocalReconnectView{State: ReconnectUnconfirmed}
	if ok {
		view.State = ReconnectStarted
	}
	err := e.Store.update(func(st *State) error {
		st.ReconnectStatus = status
		return nil
	})
	return view, err
}

func (e *Engine) reconnectCommand(ctx context.Context, m Message, id string, cmd *SessionCommand, args []string) error {
	if !e.Config.isAdmin(m.UserID) {
		cmd.Reply = "仅owner/admin可重连本Bridge。"
		return nil
	}
	switch {
	case len(args) == 0:
		status, err := e.performReconnect(ctx, id, cmd)
		cmd.Reply = status
		return err
	case len(args) != 1 || (args[0] != "--wait" && args[0] != "--cancel"):
		cmd.Reply = "用法：/reconnect [--wait|--cancel]"
		return nil
	case args[0] == "--wait" && e.Reconnect == nil:
		cmd.Reply = "本Bridge未提供重连入口。"
		return nil
	}
	return e.Store.update(func(st *State) error {
		if args[0] == "--cancel" {
			if st.PendingReconnect == "" {
				cmd.Reply = "当前无等待中的重连。"
			} else {
				clearReconnectWait(st, reconnectCancelledText)
				cmd.Reply = st.ReconnectStatus
			}
		} else if st.PendingReconnect != "" {
			cmd.Reply = "已有等待中的重连。可用 /doctor 查看，或用 /reconnect --cancel 取消。"
		} else {
			setReconnectWait(st, id)
			cmd.Reply = st.ReconnectStatus + " 可用 /reconnect --cancel 取消等待。"
		}
		saveCommandState(st, id, cmd)
		return nil
	})
}

func setReconnectWait(st *State, id string) {
	st.PendingReconnect, st.ReconnectStatus = id, reconnectWaitingText
	st.ReconnectRuns = nil
	for _, task := range st.Tasks {
		if reconnectTaskInFlight(task) && task.RequestID != "" {
			if st.ReconnectRuns == nil {
				st.ReconnectRuns = make(map[string]string)
			}
			st.ReconnectRuns[task.ID] = task.RequestID
		}
	}
}

func clearReconnectWait(st *State, status string) {
	st.PendingReconnect, st.ReconnectStatus = "", status
	st.ReconnectRuns = nil
}

func reconnectTaskInFlight(task *Task) bool {
	if task.NeedsReconcile {
		return true
	}
	switch task.Phase {
	case "ready", "waiting_capacity", "waiting_resource", "opening":
		return false
	default:
		return !terminal(task.Phase)
	}
}

func (e *Engine) finishDeferredReconnect(ctx context.Context) error {
	st := e.Store.snapshot()
	id := st.PendingReconnect
	if id == "" {
		return nil
	}
	cmd := st.Commands[id]
	if id != localReconnectIntent {
		var cancelReason string
		switch {
		case cmd == nil || !e.Config.Allowed(cmd.Source) || !e.Config.isAdmin(cmd.Source.UserID):
			cancelReason = "管理员权限或命令已失效，恢复新派发。"
		case cmd.ReconnectAttempted:
			cancelReason = reconnectUncertain
		default:
			if _, pending := queuedPresent(&st, cmd.Source); pending {
				return nil
			}
			if cmd.ReplyAt == "" || cmd.Issue != "" {
				cancelReason = "等待回执未确认，恢复新派发。"
			}
		}
		if cancelReason != "" {
			return e.Store.update(func(st *State) error {
				clearReconnectWait(st, cancelReason)
				return nil
			})
		}
	}
	for _, t := range st.Tasks {
		if reconnectTaskInFlight(t) {
			return nil
		}
	}
	if !e.reconnectStoppedRunsSettled(ctx, &st) {
		return nil
	}
	if id == localReconnectIntent {
		_, err := e.performLocalReconnect(ctx)
		return err
	}
	_, err := e.performReconnect(ctx, id, cmd)
	return err
}

func (e *Engine) reconnectStoppedRunsSettled(ctx context.Context, st *State) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	api, available := e.T3.(threadMetadataAPI)
	for id, request := range st.ReconnectRuns {
		t := st.Tasks[id]
		if t == nil || t.RequestID != request || t.Phase != "stopped" || t.RunID == "" || t.ThreadID == "" ||
			t.stoppedRunSettled() || (t.ThreadMeta != nil && t.ThreadMeta.SettledRequest == request) {
			continue
		}
		// A stop acknowledgement alone does not show whether T3 or its children
		// are quiet. Read back only the stopped runs captured by this wait.
		if !available || !e.lifecycleOwned(st, t) {
			return false
		}
		native, err := api.InspectThread(ctx, t.Target, t)
		if err != nil || !native.Quiet {
			return false
		}
	}
	return true
}

func (e *Engine) performReconnect(ctx context.Context, id string, cmd *SessionCommand) (string, error) {
	if cmd.ReconnectAttempted {
		return reconnectUncertain, nil
	}
	// Persist the attempt before replacing clients. A restart can inspect the result
	// but cannot repeat an unconfirmed reconnect on the same command.
	cmd.ReconnectAttempted = true
	if err := e.Store.update(func(st *State) error {
		clearReconnectWait(st, reconnectUncertain)
		saveCommandState(st, id, cmd)
		return nil
	}); err != nil {
		return "", err
	}
	status, _ := e.replaceReceiverClients(ctx)
	return status, e.Store.update(func(st *State) error {
		st.ReconnectStatus = status
		return nil
	})
}

func (e *Engine) replaceReceiverClients(ctx context.Context) (string, bool) {
	if e.Reconnect == nil {
		return "本Bridge未提供重连入口；未重启服务或重放请求。", false
	}
	clear(e.idleRuns)
	status, err := e.Reconnect(ctx)
	if err != nil {
		e.commandFailed(ctx, CommandFailureReconnect)
		return "本Bridge重连未确认，请用 /doctor 核对；原任务/fence保留，不重放、不切provider。", false
	}
	return status, true
}
