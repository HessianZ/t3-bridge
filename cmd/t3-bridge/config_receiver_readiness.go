package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

type receiverReadinessReport struct {
	Revision string                     `json:"revision"`
	Ready    bool                       `json:"ready"`
	Accounts []receiverReadinessAccount `json:"accounts"`
}

type receiverReadinessAccount struct {
	receiverProcessAccount
	State           string                    `json:"state"`
	Process         string                    `json:"process,omitempty"`
	PID             int                       `json:"pid,omitempty"`
	Version         string                    `json:"version,omitempty"`
	ProcessRevision string                    `json:"processRevision,omitempty"`
	Sample          *receiverDiagnosticSample `json:"sample,omitempty"`
	Worker          *receiverWorkerSample     `json:"worker,omitempty"`
}

func runConfigReceiverReadiness(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("t3-bridge config receiver-readiness", flag.ContinueOnError)
	flags.SetOutput(output)
	path := flags.String("config", "", "private "+defaultConfigFlagHelp)
	profile := flags.String("profile", "", "use the named configuration profile")
	revision := flags.String("expected-revision", "", "optional SHA-256 of the saved configuration")
	var wait time.Duration
	flags.Func("wait", "wait for readiness for up to this duration (for example, 30s)", func(value string) error {
		duration, err := time.ParseDuration(value)
		if err != nil || duration <= 0 {
			return errors.New("wait must be a positive duration")
		}
		wait = duration
		return nil
	})
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	resolved, err := resolveConfigPath(*path, *profile, defaultConfigPath)
	if err != nil {
		return err
	}
	profiles, err := localProfiles()
	if err != nil {
		return err
	}
	report, err := savedReceiverReadiness(profiles, resolved, *revision)
	if err != nil {
		return err
	}
	if wait > 0 && !report.Ready {
		report, err = waitForReceiverReadiness(profiles, resolved, report.Revision, wait)
		if err != nil {
			return err
		}
	}
	if err := json.NewEncoder(output).Encode(report); err != nil {
		return err
	}
	if !report.Ready {
		if wait > 0 {
			return errors.New("timed out waiting for receiver readiness for the saved configuration")
		}
		return errors.New("receiver readiness could not be confirmed for the saved configuration")
	}
	return nil
}

func waitForReceiverReadiness(profiles profileRegistry, path, revision string, duration time.Duration) (receiverReadinessReport, error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	timer := time.NewTimer(duration)
	defer timer.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return receiverReadinessReport{}, ctx.Err()
		case <-timer.C:
			return savedReceiverReadiness(profiles, path, revision)
		case <-ticker.C:
			report, err := savedReceiverReadiness(profiles, path, revision)
			if err != nil || report.Ready {
				return report, err
			}
		}
	}
}

func savedReceiverReadiness(profiles profileRegistry, path, revision string) (receiverReadinessReport, error) {
	raw, err := bridge.ReadPrivateConfigJSON(path)
	if revision != "" && (err != nil || configRevision(raw) != revision) {
		return receiverReadinessReport{}, errWorkspaceServiceConfigChanged
	}
	if err != nil {
		return receiverReadinessReport{}, err
	}
	config, err := bridge.ParseConfig(raw)
	if err != nil {
		return receiverReadinessReport{}, err
	}
	processes, err := readRuntimeReceivers(profiles)
	if err != nil {
		return receiverReadinessReport{}, errors.New("当前用户的接收进程清单不可用")
	}
	registry := localReceiverProcesses(profiles)
	report := receiverReadinessReport{Revision: configRevision(raw)}
	enabled, ready := 0, 0
	for _, account := range config.Accounts() {
		row := receiverReadinessAccount{receiverProcessAccount: newReceiverGroupAccount(account.Tenant, account.AppID), State: "disabled"}
		if account.ReceiverAuthorized {
			enabled++
			row = matchReceiverReadiness(row, processes, registry, report.Revision)
			if row.State == "ready" {
				ready++
			}
		}
		report.Accounts = append(report.Accounts, row)
	}
	report.Ready = enabled > 0 && enabled == ready
	latest, err := bridge.ReadPrivateConfigJSON(path)
	if err != nil || configRevision(latest) != report.Revision {
		return receiverReadinessReport{}, errWorkspaceServiceConfigChanged
	}
	return report, nil
}

func matchReceiverReadiness(row receiverReadinessAccount, processes []receiverProcess, registry receiverProcessRegistry, revision string) receiverReadinessAccount {
	var current receiverProcess
	for _, process := range processes {
		if !slices.Contains(process.Accounts, row.receiverProcessAccount) {
			continue
		}
		if current.ID != "" {
			row.State = "ambiguous"
			return row
		}
		current = process
	}
	if current.ID == "" {
		row.State = "offline"
		return row
	}
	row.Process, row.PID, row.Version, row.ProcessRevision = current.ID, current.PID, current.Version, current.ConfigRevision
	switch {
	case current.ConfigRevision == "":
		row.State = "revision-unknown"
	case current.ConfigRevision != revision:
		row.State = "different-revision"
	default:
		row.Sample, row.Worker = registry.receiverSnapshot(current, row.receiverProcessAccount)
		switch {
		case row.Worker != nil:
			row.State = row.Worker.State
		case row.Sample == nil:
			row.State = "waiting-sample"
		case row.Sample.Data.ReconnectWaiting:
			row.State = "reconnect-wait"
		case row.Sample.Data.MCPVerified && row.Sample.Data.FeishuReady:
			row.State = "ready"
		default:
			row.State = "connecting"
		}
	}
	return row
}

func receiverReadinessStatusText(state string) string {
	switch state {
	case "disabled":
		return "未启用接收"
	case "offline":
		return "未找到接收进程"
	case "ambiguous":
		return "匹配多个进程，无法核对"
	case "revision-unknown":
		return "进程未登记配置版本"
	case "different-revision":
		return "进程加载了不同的配置版本"
	case "waiting-sample":
		return "等待有效诊断样本"
	case "retrying":
		return "等待自动重试"
	case "exhausted":
		return "重试已耗尽"
	case "reconnect-wait":
		return "暂停新派发，等待重连"
	case "connecting":
		return "连接尚未就绪"
	case "ready":
		return "已就绪"
	default:
		return "未知"
	}
}
