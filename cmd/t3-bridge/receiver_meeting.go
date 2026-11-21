package main

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

const liveMeetingTimeout = 15 * time.Second

var errReceiverMeetingUnknown = errors.New("meeting result is unknown; check receiver status and Feishu before retrying")

var receiverMeetingIPC = receiverIPC[bridge.LocalMeetingRequest, bridge.LocalMeetingView]{
	prefix:      "meetings",
	name:        "meeting",
	supported:   func(p receiverProcess) bool { return p.Meetings },
	timeout:     liveMeetingTimeout,
	replyLimit:  2 << 20,
	strict:      true,
	busy:        errors.New("a meeting request is already pending for this account"),
	unsupported: errors.New("receiver does not support local meetings; restart it with an updated t3-bridge"),
	unknown:     errReceiverMeetingUnknown,
	fail:        errReceiverMeetingUnknown,
}

type liveMeetingOperation func(context.Context, string, string, string, bridge.LocalMeetingRequest) (bridge.LocalMeetingView, error)

func (r receiverProcessRegistry) meeting(ctx context.Context, process, tenant, appID string, request bridge.LocalMeetingRequest) (bridge.LocalMeetingView, error) {
	if err := request.Validate(); err != nil {
		return bridge.LocalMeetingView{}, err
	}
	view, err := receiverMeetingIPC.exchange(ctx, r, process, newReceiverGroupAccount(tenant, appID), request)
	if err != nil {
		return bridge.LocalMeetingView{}, err
	}
	validOutcome := view.Outcome == "" && request.Action == "status"
	if request.Action != "status" {
		switch view.Outcome {
		case "joined", "ended":
			validOutcome = request.Action == "join"
		case "left":
			validOutcome = request.Action == "leave"
		case "confirmed":
			validOutcome = request.Action == "confirm-left"
		case "pending", "rejected", "conflict", "unavailable":
			validOutcome = true
		}
	}
	if !validOutcome || view.Meetings == nil {
		return bridge.LocalMeetingView{}, errReceiverMeetingUnknown
	}
	for _, entry := range view.Meetings {
		if err := (bridge.LocalMeetingRequest{Action: "leave", Number: entry.Number, Revision: entry.Revision}).Validate(); err != nil {
			return bridge.LocalMeetingView{}, errReceiverMeetingUnknown
		}
		switch entry.Phase {
		case "join_pending", "join_accepted", "join_rejected", "leave_pending":
		default:
			return bridge.LocalMeetingView{}, errReceiverMeetingUnknown
		}
		switch entry.Origin {
		case "local", "invite", "chat":
		default:
			return bridge.LocalMeetingView{}, errReceiverMeetingUnknown
		}
	}
	return view, nil
}

func findLiveMeetingProcess(profiles profileRegistry, account receiverProcessAccount) (receiverProcess, error) {
	processes, err := readRuntimeReceivers(profiles)
	if err != nil {
		return receiverProcess{}, errors.New("当前用户的接收进程清单不可用")
	}
	var match receiverProcess
	for _, process := range processes {
		if !slices.Contains(process.Accounts, account) {
			continue
		}
		if match.ID != "" {
			return receiverProcess{}, errors.New("当前账号匹配多个接收进程，请用完整进程 ID 查询")
		}
		match = process
	}
	if match.ID == "" {
		return receiverProcess{}, errors.New("当前账号没有匹配的运行中接收进程")
	}
	if !match.Meetings {
		return receiverProcess{}, errors.New("当前账号的接收进程版本不支持本机会议控制")
	}
	return match, nil
}
