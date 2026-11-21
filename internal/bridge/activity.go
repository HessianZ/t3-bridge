package bridge

import (
	"context"
	"slices"
	"strings"
	"time"
)

type runAction struct {
	Label    string    `json:"label"`
	At       time.Time `json:"at"`
	Finished bool      `json:"finished,omitempty"`
}

type toolActivityMode string

const (
	toolActivityOff      toolActivityMode = "off"
	toolActivityBrief    toolActivityMode = "brief"
	toolActivityDetailed toolActivityMode = "detailed"
	maxToolActions                        = 5
)

func (m toolActivityMode) enabled() bool {
	return m == toolActivityBrief || m == toolActivityDetailed
}

type runActivity struct {
	Action      *runAction       `json:"action,omitempty"`
	Tools       []runAction      `json:"tools,omitempty"`
	ToolMode    toolActivityMode `json:"toolMode,omitempty"`
	CheckedAt   time.Time        `json:"checkedAt"`
	ConfirmedAt time.Time        `json:"confirmedAt"`
}

type runActivitySample struct {
	Action     *runAction
	Tools      []runAction
	IdleMarker string
}

// Decode only metadata. T3 can put raw tool content or reasoning in title and text.
type runActivityPage struct {
	Thread struct {
		threadIdentity
		ActiveRunID string     `json:"activeRunId"`
		Status      string     `json:"status"`
		Pending     *int       `json:"pendingRequestCount"`
		Background  []struct{} `json:"pendingBackgroundTasks"`
		Waiting     bool       `json:"blockedOnThreadWait"`
	} `json:"thread"`
	RecentRuns []threadRun `json:"recentRuns"`
	Items      []struct {
		Position   *int   `json:"position"`
		ID         string `json:"itemId"`
		RunID      string `json:"runId"`
		Source     string `json:"sourceThreadId"`
		Visibility string `json:"visibility"`
		Type       string `json:"type"`
		Status     string `json:"status"`
		Author     string `json:"createdBy"`
		UpdatedAt  string `json:"updatedAt"`
	} `json:"items"`
	HasMore         *bool `json:"hasMore"`
	EarlierPosition *int  `json:"earlierPosition"`
}

func (p runActivityPage) confirmsRunning(task *Task) bool {
	t := p.Thread
	if t.ActiveRunID != task.RunID || t.Status != "running" || t.Pending == nil || *t.Pending != 0 || len(t.Background) != 0 || t.Waiting || len(p.RecentRuns) != 1 {
		return false
	}
	run := p.RecentRuns[0]
	return run.RunID == task.RunID && run.Status == "running" && run.Model == task.Target.Model && run.ProviderID == task.Target.ProviderID && (run.ExecutedModel == nil || *run.ExecutedModel == task.Target.Model)
}

func (t *T3) readRunActivity(ctx context.Context, task *Task, watchIdle bool, requestPosition *int) (runActivitySample, error) {
	var sample runActivitySample
	var markers []string
	// An explicit upper cursor selects the newest window on the NAS MCP server.
	before := 1<<31 - 1
	for n := 0; n < 5; n++ {
		var page runActivityPage
		err := t.Calls.Call(ctx, "t3_thread_read", map[string]any{
			"environmentId": task.Target.EnvironmentID, "threadId": task.ThreadID,
			"view": "activity", "beforePosition": before, "limit": 50, "maxCharsPerItem": 1, "runLimit": 1,
		}, &page)
		if err != nil {
			return sample, err
		}
		if err := verifyThreadIdentity(task.Target, task.ThreadID, page.Thread.threadIdentity); err != nil {
			return sample, err
		}
		watchIdle = watchIdle && page.confirmsRunning(task)
		runStart := false
		for _, item := range page.Items {
			if item.RunID != task.RunID || item.Visibility == "inherited" || item.Visibility == "synthetic" || (item.Source != "" && item.Source != task.ThreadID) {
				continue
			}
			if item.Visibility != "local" || item.Source == "" {
				watchIdle = false
				continue
			}
			if item.Type == "reasoning" || item.Type == "reasoning_summary" {
				continue
			}
			label := activityLabel(item.Type, item.Status, item.Author)
			at, err := time.Parse(time.RFC3339Nano, item.UpdatedAt)
			if n == 0 && label != "" && err == nil && (sample.Action == nil || !at.Before(sample.Action.At)) {
				sample.Action = &runAction{Label: label, At: at}
			}
			if n == 0 && err == nil && !at.IsZero() {
				if tool := toolActivityLabel(item.Type, item.Status); tool != "" {
					finished := slices.Contains([]string{"completed", "failed", "interrupted", "cancelled"}, item.Status)
					sample.Tools = append(sample.Tools, runAction{Label: tool, At: at, Finished: finished})
				}
			}
			if !watchIdle {
				continue
			}
			if (item.Type == "user_message" || item.Type == "message") &&
				requestPosition != nil && item.Position != nil && *requestPosition == *item.Position {
				runStart = true
				continue
			}
			if item.Type == "user_message" {
				continue
			}
			switch item.Status {
			case "idle":
				if label != "" && item.Type != "assistant_message" {
					watchIdle = false
				}
			case "running", "pending", "waiting":
				// A public operation can remain active while newer replies arrive.
				if label == "" || item.Type != "assistant_message" {
					watchIdle = false
				}
			case "completed", "failed", "interrupted", "cancelled":
			default:
				if label != "" {
					watchIdle = false
				}
			}
			if label != "" {
				if item.ID == "" || err != nil || at.IsZero() {
					watchIdle = false
				} else {
					markers = append(markers, stable(item.ID, item.Type, item.Status, item.UpdatedAt))
				}
			}
		}
		if !watchIdle || page.HasMore == nil || page.Items == nil {
			break
		}
		// An earlier page may contain a still-running tool. Only the verified
		// input boundary or exhausted history establishes complete coverage.
		if runStart || !*page.HasMore {
			slices.Sort(markers)
			sample.IdleMarker = stable(markers...)
			break
		}
		if page.EarlierPosition == nil || *page.EarlierPosition < 0 || *page.EarlierPosition >= before {
			break
		}
		before = *page.EarlierPosition
	}
	slices.Reverse(sample.Tools)
	slices.SortStableFunc(sample.Tools, func(a, b runAction) int { return b.At.Compare(a.At) })
	sample.Tools = sample.Tools[:min(len(sample.Tools), maxToolActions)]
	return sample, nil
}

func toolActivityLabel(kind, status string) string {
	switch kind {
	case "command_execution", "tool_call", "mcp_tool_call", "file_change":
		return activityLabel(kind, status, "")
	case "dynamic_tool":
		return activityStatusLabel("工具调用", status)
	case "file_search":
		return activityStatusLabel("文件搜索", status)
	case "web_search":
		return activityStatusLabel("网页搜索", status)
	}
	return ""
}

// Map only known metadata to fixed text. Activity payloads may contain secrets.
func activityLabel(kind, status, author string) string {
	var action string
	switch kind {
	case "command_execution":
		action = "命令"
	case "tool_call", "mcp_tool_call":
		action = "工具调用"
	case "file_change":
		action = "文件修改"
	case "assistant_message":
		if author == "agent" {
			action = "回复"
		}
	}
	if action == "" {
		return ""
	}
	return activityStatusLabel(action, status)
}

func activityStatusLabel(action, status string) string {
	switch status {
	case "pending", "waiting":
		return action + "等待中"
	case "running":
		return action + "进行中"
	case "completed":
		return action + "已完成"
	case "failed":
		return action + "失败"
	case "interrupted", "cancelled":
		return action + "已中止"
	}
	return action
}

const runActivityInterval = 15 * time.Second

type idleRunStreak struct {
	RequestID, RunID, Marker string
	Since, CheckedAt         time.Time
}

func (e *Engine) refreshRunActivity(ctx context.Context, t *Task, show, final, watchIdle bool, requestPosition *int) string {
	api, ok := e.T3.(interface {
		readRunActivity(context.Context, *Task, bool, *int) (runActivitySample, error)
	})
	if !ok || t.RunID == "" {
		delete(e.idleRuns, t.ID)
		return ""
	}
	now := e.now()
	if show && t.RunActivity == nil {
		t.RunActivity = &runActivity{}
	}
	showDue := show && (final || !now.Before(t.RunActivity.CheckedAt.Add(runActivityInterval)))
	streak := e.idleRuns[t.ID]
	if streak.RequestID != t.RequestID || streak.RunID != t.RunID || now.Before(streak.CheckedAt) {
		streak = idleRunStreak{}
	}
	if !showDue && (!watchIdle || now.Before(streak.CheckedAt.Add(runActivityInterval))) {
		return ""
	}
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	sample, err := api.readRunActivity(probe, t, watchIdle, requestPosition)
	now = e.now()
	if show {
		a := t.RunActivity
		a.CheckedAt = now.UTC()
		if err == nil {
			a.ConfirmedAt = a.CheckedAt
			a.Tools = sample.Tools
			if sample.Action != nil && (a.Action == nil || !sample.Action.At.Before(a.Action.At)) {
				a.Action = sample.Action
			}
		}
	}
	if !watchIdle {
		delete(e.idleRuns, t.ID)
		return ""
	}
	if e.idleRuns == nil {
		e.idleRuns = map[string]idleRunStreak{}
	}
	if err != nil || sample.IdleMarker == "" {
		e.idleRuns[t.ID] = idleRunStreak{RequestID: t.RequestID, RunID: t.RunID, CheckedAt: now}
		return ""
	}
	// The clock measures consecutive local reads. MCP timestamps are change markers only.
	if streak.Marker != sample.IdleMarker || now.Before(streak.CheckedAt) || now.Sub(streak.CheckedAt) > 2*runActivityInterval {
		streak = idleRunStreak{RequestID: t.RequestID, RunID: t.RunID, Marker: sample.IdleMarker, Since: now}
	}
	streak.CheckedAt = now
	e.idleRuns[t.ID] = streak
	if now.Sub(streak.Since) >= time.Duration(t.IdleTimeoutMinutes)*time.Minute {
		return streak.Marker
	}
	return ""
}

func runActivityText(t *Task) string {
	a := t.RunActivity
	if a == nil {
		return ""
	}
	finished := terminal(t.Phase)
	zone := time.FixedZone("UTC+8", 8*3600)
	if a.ToolMode.enabled() {
		return toolActivityText(a, a.ToolMode, finished)
	}
	if finished || a.ToolMode != "" {
		return ""
	}
	lines := []string{"最近动作（主任务） 暂未读取到"}
	if a.Action != nil {
		lines[0] = "最近动作（主任务） " + a.Action.At.In(zone).Format("01-02 15:04:05") + " · " + a.Action.Label
	}
	if !a.ConfirmedAt.IsZero() {
		lines = append(lines, "T3 最近查询成功 "+a.ConfirmedAt.In(zone).Format("01-02 15:04")+"（UTC+8）")
	}
	if !a.CheckedAt.Equal(a.ConfirmedAt) {
		lines = append(lines, "T3 活动暂时无法查询")
	}
	return strings.Join(lines, "\n")
}

func toolActivityText(a *runActivity, mode toolActivityMode, finished bool) string {
	if a == nil {
		return ""
	}
	var lines []string
	if len(a.Tools) > 0 {
		if finished {
			lines = append(lines, "工具活动（主任务末次记录）")
		} else {
			lines = append(lines, "最近工具活动（主任务）")
		}
		zone := time.FixedZone("UTC+8", 8*3600)
		for _, tool := range a.Tools {
			label := tool.Label
			if finished && !tool.Finished {
				label += "（终态未确认）"
			}
			if mode == toolActivityBrief {
				lines = append(lines, label)
				break
			}
			lines = append(lines, tool.At.In(zone).Format("01-02 15:04:05")+" · "+label)
		}
	}
	if !a.CheckedAt.Equal(a.ConfirmedAt) {
		lines = append(lines, "T3 工具活动暂时无法查询")
	}
	return strings.Join(lines, "\n")
}
