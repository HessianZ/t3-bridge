package main

import (
	"context"
	"errors"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

var errReceiverReconnectUnknown = errors.New("reconnect result is unknown; inspect receiver status before retrying")

type receiverReconnectRequest struct {
	Confirm bool                   `json:"confirm"`
	Action  bridge.ReconnectAction `json:"action,omitempty"`
}

var receiverReconnectIPC = receiverIPC[receiverReconnectRequest, bridge.LocalReconnectView]{
	prefix:      "reconnect",
	name:        "reconnect",
	supported:   func(p receiverProcess) bool { return p.Reconnect },
	replyLimit:  receiverIPCCommandLimit,
	strict:      true,
	busy:        errors.New("a reconnect request is already pending for this account"),
	unsupported: errors.New("receiver does not support local reconnect; restart it with an updated t3-bridge"),
	unknown:     errReceiverReconnectUnknown,
	fail:        errReceiverReconnectUnknown,
}

func (r receiverProcessRegistry) reconnect(ctx context.Context, id, tenant, appID string, action bridge.ReconnectAction) (bridge.LocalReconnectView, error) {
	if !action.Valid() {
		return bridge.LocalReconnectView{}, errors.New("invalid reconnect action")
	}
	ipc := receiverReconnectIPC
	request := receiverReconnectRequest{Confirm: true}
	if action != bridge.ReconnectNow {
		request.Action = action
		ipc.supported = func(p receiverProcess) bool { return p.Reconnect && p.ReconnectWait }
		ipc.unsupported = errors.New("receiver does not support waiting or cancelling local reconnect; restart it with an updated t3-bridge")
	}
	view, err := ipc.exchange(ctx, r, id, newReceiverGroupAccount(tenant, appID), request)
	if err != nil {
		return bridge.LocalReconnectView{}, err
	}
	switch action {
	case bridge.ReconnectNow:
		if view.State == bridge.ReconnectStarted || view.State == bridge.ReconnectPending ||
			view.State == bridge.ReconnectUnconfirmed || view.State == bridge.ReconnectRecoveryRequested {
			return view, nil
		}
	case bridge.ReconnectAfterRuns:
		if view.State == bridge.ReconnectScheduled || view.State == bridge.ReconnectPending || view.State == bridge.ReconnectWorkerOffline {
			return view, nil
		}
	case bridge.ReconnectCancelWait:
		if view.State == bridge.ReconnectCancelled || view.State == bridge.ReconnectIdle || view.State == bridge.ReconnectWorkerOffline {
			return view, nil
		}
	}
	return bridge.LocalReconnectView{}, errReceiverReconnectUnknown
}

func (p *receiverProcessRegistration) recoverReceiverAccount(ctx context.Context, config bridge.Config) bool {
	account := newReceiverGroupAccount(config.Tenant, config.AppID)
	pending := receiverReconnectIPC.pendingPath(p.dir, account)
	requested := false
	p.serveReconnect(ctx, account, pending, func(_ context.Context, action bridge.ReconnectAction) (bridge.LocalReconnectView, error) {
		if action != bridge.ReconnectNow {
			return bridge.LocalReconnectView{State: bridge.ReconnectWorkerOffline}, nil
		}
		requested = true
		return bridge.LocalReconnectView{State: bridge.ReconnectRecoveryRequested}, nil
	})
	return requested
}

func (p *receiverProcessRegistration) serveReconnect(ctx context.Context, account receiverProcessAccount, pending string, apply func(context.Context, bridge.ReconnectAction) (bridge.LocalReconnectView, error)) {
	receiverReconnectIPC.serve(ctx, p, account, pending, func(requestCtx context.Context, request receiverReconnectRequest) (bridge.LocalReconnectView, error) {
		if !request.Confirm {
			return bridge.LocalReconnectView{}, errors.New("reconnect requires confirmation")
		}
		if request.Action == "" || request.Action == bridge.ReconnectNow {
			// Replacement clients need the worker lifetime after the IPC reply deadline.
			return apply(ctx, bridge.ReconnectNow)
		}
		if !p.process.ReconnectWait || !request.Action.Valid() {
			return bridge.LocalReconnectView{}, errors.New("unsupported reconnect action")
		}
		return apply(requestCtx, request.Action)
	})
}
