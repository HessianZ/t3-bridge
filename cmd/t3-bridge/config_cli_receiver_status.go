package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"slices"
	"strings"
)

type receiverStatusView struct {
	Process string                    `json:"process"`
	PID     int                       `json:"pid"`
	AppID   string                    `json:"appId"`
	Tenant  string                    `json:"tenant"`
	Sample  *receiverDiagnosticSample `json:"sample"`
	Worker  *receiverWorkerSample     `json:"worker,omitempty"`
}

func runConfigReceiverStatus(args []string, output io.Writer, lookup func(string, receiverProcessAccount) (receiverStatusView, error)) error {
	flags := flag.NewFlagSet("t3-bridge config receiver-status", flag.ContinueOnError)
	flags.SetOutput(output)
	process := flags.String("process", "", "required full receiver process ID from t3-bridge ps")
	appID := flags.String("app-id", "", "required application ID in the receiver process")
	tenant := flags.String("tenant", "feishu", "application tenant: feishu or lark")
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
	view, err := lookup(*process, receiverProcessAccount{Tenant: *tenant, AppID: *appID})
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(view)
}

func localReceiverStatus(process string, account receiverProcessAccount) (receiverStatusView, error) {
	profiles, err := localProfiles()
	if err != nil {
		return receiverStatusView{}, err
	}
	return receiverStatusForAccount(profiles, process, account)
}

func receiverStatusForAccount(profiles profileRegistry, processID string, account receiverProcessAccount) (receiverStatusView, error) {
	processes, err := readRuntimeReceivers(profiles)
	if err != nil {
		return receiverStatusView{}, errors.New("当前用户的接收进程清单不可用")
	}
	var target receiverProcess
	for _, process := range processes {
		if (processID != "" && process.ID != processID) || !slices.Contains(process.Accounts, account) {
			continue
		}
		if target.ID != "" {
			return receiverStatusView{}, errors.New("当前账号匹配多个接收进程，无法确定状态来源")
		}
		target = process
	}
	if target.ID == "" {
		return receiverStatusView{}, errors.New("当前账号没有匹配的运行中接收进程")
	}
	view := receiverStatusView{Process: target.ID, PID: target.PID, AppID: account.AppID, Tenant: account.Tenant}
	view.Sample, view.Worker = localReceiverProcesses(profiles).receiverSnapshot(target, account)
	return view, nil
}
