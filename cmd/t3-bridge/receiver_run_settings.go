package main

import (
	"context"
	"errors"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

var (
	errReceiverRunSettingsBusy        = errors.New("a run settings request is already pending for this account")
	errReceiverRunSettingsUnsupported = errors.New("receiver does not support live run settings; restart it with an updated t3-bridge")
	errReceiverRunSettingsUnknown     = errors.New("run settings result is unknown")
	errReceiverRunSettingsFailed      = errors.New("receiver could not complete the run settings request")
)

var receiverRunSettingsIPC = receiverIPC[bridge.RunSettingsPatch, bridge.RunSettingsView]{
	prefix:      "run-settings",
	name:        "run settings",
	supported:   func(p receiverProcess) bool { return p.RunSettings },
	replyLimit:  receiverIPCCommandLimit,
	strict:      true,
	busy:        errReceiverRunSettingsBusy,
	unsupported: errReceiverRunSettingsUnsupported,
	unknown:     errReceiverRunSettingsUnknown,
	fail:        errReceiverRunSettingsFailed,
}

func (r receiverProcessRegistry) runSettings(ctx context.Context, id, tenant, appID string, patch bridge.RunSettingsPatch) (bridge.RunSettingsView, error) {
	return receiverRunSettingsIPC.exchange(ctx, r, id, newReceiverGroupAccount(tenant, appID), patch)
}
