package main

import (
	"os"
	"time"
)

const (
	receiverWorkerRetrying  = "retrying"
	receiverWorkerExhausted = "exhausted"
)

type receiverWorkerStatus struct {
	State    string    `json:"state"`
	Attempts int       `json:"attempts"`
	Failure  string    `json:"failure,omitempty"`
	RetryAt  time.Time `json:"retryAt,omitzero"`
}

type receiverWorkerSample struct {
	At time.Time `json:"at"`
	receiverWorkerStatus
}

func (p *receiverProcessRegistration) writeWorkerStatus(account receiverProcessAccount, sample *receiverWorkerSample) {
	if sample == nil {
		_ = os.Remove(receiverDiagnosticsPath(p.dir, account))
		return
	}
	p.writeDiagnosticFile(receiverDiagnosticFile{
		Instance: p.process.ID, Account: account, At: sample.At, Worker: &sample.receiverWorkerStatus,
	})
}

func (s receiverWorkerStatus) valid(at, now time.Time) bool {
	if s.Failure != "" && receiverFailureText(s.Failure) == "" {
		return false
	}
	switch s.State {
	case receiverWorkerRetrying:
		return s.Attempts > 0 && s.Attempts <= sharedReceiverMaxRetries &&
			s.RetryAt.After(now) && s.RetryAt.Sub(at) <= sharedReceiverRetryDelay
	case receiverWorkerExhausted:
		return s.Attempts == sharedReceiverMaxRetries+1 && s.RetryAt.IsZero()
	default:
		return false
	}
}

func receiverFailureText(kind string) string {
	switch kind {
	case "store-open-failed":
		return "状态目录打开失败"
	case "account-bind-failed":
		return "状态归属核验失败"
	case "initial-connect-failed":
		return "初次建连失败"
	case "inbound-failed":
		return "入站消息处理失败"
	case "channel-stopped":
		return "飞书连接已退出"
	case "worker-tick-failed":
		return "接收任务处理失败"
	default:
		return ""
	}
}
