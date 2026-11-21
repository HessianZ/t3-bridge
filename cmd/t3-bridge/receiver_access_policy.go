package main

import (
	"context"
	"errors"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

// The view combines configured users and runtime overrides across all routes.
const receiverAccessPolicyLimit = 2 << 20

var (
	errReceiverAccessPolicyBusy        = errors.New("an access policy request is already pending for this account")
	errReceiverAccessPolicyUnsupported = errors.New("receiver does not support live access policy; restart it with an updated t3-bridge")
	errReceiverAccessPolicyUnknown     = errors.New("access policy result is unknown")
	errReceiverAccessPolicyFailed      = errors.New("receiver could not complete the access policy request")
)

var receiverAccessPolicyIPC = receiverIPC[bridge.LocalAccessPolicyRequest, bridge.LocalAccessPolicyView]{
	prefix:      "access-policy",
	name:        "access policy",
	supported:   func(p receiverProcess) bool { return p.AccessPolicy },
	replyLimit:  receiverAccessPolicyLimit,
	strict:      true,
	busy:        errReceiverAccessPolicyBusy,
	unsupported: errReceiverAccessPolicyUnsupported,
	unknown:     errReceiverAccessPolicyUnknown,
	fail:        errReceiverAccessPolicyFailed,
}

func (r receiverProcessRegistry) accessPolicy(ctx context.Context, id, tenant, appID string, request bridge.LocalAccessPolicyRequest) (bridge.LocalAccessPolicyView, error) {
	return receiverAccessPolicyIPC.exchange(ctx, r, id, newReceiverGroupAccount(tenant, appID), request)
}
