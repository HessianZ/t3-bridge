package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

const liveSettingsTimeout = 8 * time.Second

type liveSettingsOperation func(context.Context, string, string, string, bridge.RunSettingsPatch) (bridge.RunSettingsView, error)

func runConfigLiveSettings(args []string, output io.Writer, operate liveSettingsOperation) error {
	flags := flag.NewFlagSet("t3-bridge config live-settings", flag.ContinueOnError)
	flags.SetOutput(output)
	process := flags.String("process", "", "required full receiver process ID from t3-bridge ps")
	appID := flags.String("app-id", "", "required application ID in the receiver process")
	tenant := flags.String("tenant", "feishu", "application tenant: feishu or lark")
	concurrent := flags.Int("max-concurrent", 0, fmt.Sprintf("dispatch budget from 0 to %d (0 adds no limit)", bridge.MaxRunConcurrency))
	idle := flags.Int("idle-timeout-minutes", 0, "default idle timeout from 0 to 120 minutes (0 disables it)")
	progress := flags.Bool("progress", false, "show card progress (use -progress=false to hide it)")
	reply := flags.String("reply-mode", "", "reply mode: card, text or markdown")
	tools := flags.String("tool-activity", "", "tool activity: off, brief, detailed or default")
	processMode := flags.String("process-mode", "", "process display for future Feishu chat turns: off, brief, detailed, card (inline preview for card replies), "+
		"stream (separate preview card), stream-brief (separate preview card with brief tool activity), or stream-detailed (separate preview card with detailed tool activity)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	switch {
	case flags.NArg() != 0:
		return errors.New("unexpected arguments")
	case !receiverProcessID.MatchString(*process):
		return errors.New("-process requires the complete process ID from t3-bridge ps")
	case *appID == "" || strings.TrimSpace(*appID) != *appID:
		return errors.New("-app-id requires a non-empty application ID without surrounding whitespace")
	case *tenant != "feishu" && *tenant != "lark":
		return errors.New("-tenant must be feishu or lark")
	}
	var patch bridge.RunSettingsPatch
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "max-concurrent":
			patch.MaxConcurrent = concurrent
		case "idle-timeout-minutes":
			patch.IdleTimeoutMinutes = idle
		case "progress":
			patch.Progress = progress
		case "reply-mode":
			patch.ReplyMode = reply
		case "tool-activity":
			patch.ToolActivity = tools
		case "process-mode":
			patch.ProcessMode = processMode
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), liveSettingsTimeout)
	defer cancel()
	view, err := operate(ctx, *process, *tenant, *appID, patch)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(view)
}

func localLiveSettings(ctx context.Context, process, tenant, appID string, patch bridge.RunSettingsPatch) (bridge.RunSettingsView, error) {
	profiles, err := localProfiles()
	if err != nil {
		return bridge.RunSettingsView{}, err
	}
	return localReceiverProcesses(profiles).runSettings(ctx, process, tenant, appID, patch)
}
