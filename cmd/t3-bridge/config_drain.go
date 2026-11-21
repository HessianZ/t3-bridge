package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func checkSavedDrain(path, revision string) (bridge.DrainReport, error) {
	raw, err := bridge.ReadPrivateConfigJSON(path)
	if revision != "" && (err != nil || configRevision(raw) != revision) {
		return bridge.DrainReport{}, errWorkspaceServiceConfigChanged
	}
	if err != nil {
		return bridge.DrainReport{}, err
	}
	revision = configRevision(raw)
	config, err := bridge.ParseConfig(raw)
	if err != nil {
		return bridge.DrainReport{}, err
	}
	report := bridge.InspectReceiverDrain(config)
	latest, err := bridge.ReadPrivateConfigJSON(path)
	if err != nil || configRevision(latest) != revision {
		return bridge.DrainReport{}, errWorkspaceServiceConfigChanged
	}
	return report, nil
}

func runConfigDrain(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("t3-bridge config drain", flag.ContinueOnError)
	flags.SetOutput(output)
	path := flags.String("config", "", "private "+defaultConfigFlagHelp)
	profile := flags.String("profile", "", "use the named configuration profile")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	configPath, err := resolveConfigPath(*path, *profile, defaultConfigPath)
	if err != nil {
		return err
	}
	report, err := checkSavedDrain(configPath, "")
	if err != nil {
		return err
	}
	if err := json.NewEncoder(output).Encode(report); err != nil {
		return err
	}
	if !report.Drained {
		return errors.New("receiver drain could not be confirmed for every app")
	}
	return nil
}

func drainStatusText(status string) string {
	switch status {
	case "drained":
		return "已排空"
	case "blocked":
		return "有未结事项"
	case "busy":
		return "状态被占用，先停止接收端或其他状态操作"
	case "missing":
		return "无已保存状态"
	case "unavailable":
		return "无法获取私有状态锁"
	case "invalid":
		return "无法读取有效状态"
	case "unbound":
		return "状态未绑定 App"
	case "foreign":
		return "状态归属不匹配"
	default:
		return "未知"
	}
}

func drainBlockerText(code string) string {
	switch code {
	case "active":
		return "在途任务"
	case "inbox":
		return "入站队列"
	case "controls":
		return "待处理控制操作"
	case "meetings":
		return "未结会议回执"
	case "command-outcome":
		return "待核对命令"
	case "run-outcome":
		return "待核对运行"
	case "delivery":
		return "待核对回复或过程消息"
	default:
		return "未知待核对项"
	}
}

func drainDeliverySampleText(kind string) string {
	if kind == "process" {
		return "过程卡"
	}
	return "结果回复"
}
