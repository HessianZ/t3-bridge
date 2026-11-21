package bridge

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

type FollowupFlushConfig struct {
	Enabled    bool   `json:"enabled"`
	Command    string `json:"command"`
	ConfigFile string `json:"configFile"`
}
type followupResult struct {
	OK        *bool `json:"ok"`
	Attempted *int  `json:"attempted"`
	Sent      *int  `json:"sent"`
	Failed    *int  `json:"failed"`
	Pending   *int  `json:"pending"`
}

func (e *Engine) flushFollowups(ctx context.Context) error {
	c := e.Config.FollowupFlush
	if !c.Enabled || (!e.nextFollowupFlush.IsZero() && e.now().Before(e.nextFollowupFlush)) {
		return nil
	}
	e.nextFollowupFlush = e.now().Add(30 * time.Second)
	call, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(call, c.Command, "agent-followups-flush", c.ConfigFile)
	out, stderr := &limitedBuffer{Limit: 1024}, &limitedBuffer{Limit: 4096}
	cmd.Stdout = out
	cmd.Stderr = stderr
	err := cmd.Run()
	code := cmd.ProcessState
	exit := -1
	if code != nil {
		exit = code.ExitCode()
	}
	issue := "报告通知基础状态未确认；保留原队列，等待下次有界flush，可能重复；未重放T3任务"
	status := ""
	var result followupResult
	if (exit == 0 || exit == 2) && stderr.Len() == 0 && decodeStrict(out.Bytes(), &result) == nil && result.OK != nil && result.Attempted != nil && result.Sent != nil && result.Failed != nil && result.Pending != nil {
		attempted, sent, failed, pending := *result.Attempted, *result.Sent, *result.Failed, *result.Pending
		if attempted >= 0 && attempted <= 2 && sent >= 0 && failed >= 0 && sent+failed == attempted && pending >= 0 && ((exit == 0 && err == nil && *result.OK && failed == 0) || (exit == 2 && !*result.OK && failed > 0 && pending >= failed)) {
			status = fmt.Sprintf("最近报告通知批次 %s：尝试%d · 确认发送%d · 失败%d · 剩余%d（至少一次，非特定报告送达证明）", e.now().UTC().Format(time.RFC3339Nano), attempted, sent, failed, pending)
			issue = ""
			if failed > 0 {
				issue = "本批报告通知发送失败，原记录仍在队列；等待下次有界flush，不补造成功"
			}
		}
	}
	return e.Store.update(func(st *State) error { st.FollowupIssue = issue; st.FollowupStatus = status; return nil })
}
