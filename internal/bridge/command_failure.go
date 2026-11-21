package bridge

import "context"

// CommandFailureStep is an operational stage, independent of command input and errors.
type CommandFailureStep string

const (
	CommandFailureHandler       CommandFailureStep = "handler"
	CommandFailureReply         CommandFailureStep = "reply"
	CommandFailureReplyFallback CommandFailureStep = "reply-fallback"
	CommandFailureReplyExpired  CommandFailureStep = "reply-expired"
	CommandFailureReconnect     CommandFailureStep = "reconnect"
	CommandFailureDoctorRun     CommandFailureStep = "doctor-run"
	CommandFailureWorkspace     CommandFailureStep = "workspace"
	CommandFailureGroupRead     CommandFailureStep = "group-read"
	CommandFailureChatCreate    CommandFailureStep = "chat-create"
	CommandFailureChatWelcome   CommandFailureStep = "chat-welcome"
	CommandFailureMeeting       CommandFailureStep = "meeting"
	CommandFailureReceiverStop  CommandFailureStep = "receiver-stop"
)

func (s CommandFailureStep) Valid() bool {
	switch s {
	case CommandFailureHandler, CommandFailureReply, CommandFailureReplyFallback,
		CommandFailureReplyExpired, CommandFailureReconnect, CommandFailureDoctorRun,
		CommandFailureWorkspace, CommandFailureGroupRead, CommandFailureChatCreate,
		CommandFailureChatWelcome, CommandFailureMeeting, CommandFailureReceiverStop:
		return true
	default:
		return false
	}
}

func (e *Engine) commandFailed(ctx context.Context, step CommandFailureStep) {
	if e.OnCommandFailed != nil && ctx.Err() != context.Canceled {
		e.OnCommandFailed(step)
	}
}
