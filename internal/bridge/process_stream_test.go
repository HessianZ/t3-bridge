package bridge

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestProcessStreamUsesCompleteLocalProviderMessages(t *testing.T) {
	target := testConfig(t).Routes["nas"]
	task := &Task{ThreadID: "thread", RunID: "run", Process: &processMessage{Mode: processStream}}
	page := mcpAgentInputPage(t)
	valid := true
	api := &T3{Calls: scriptedCaller(func(name string, args map[string]any) (any, error) {
		switch name {
		case "t3_thread_wait":
			return map[string]any{"threadId": "thread", "timedOut": true}, nil
		case "t3_thread_read":
			if args["view"] != "messages" {
				t.Fatal("preview read outside the public messages view")
			}
			items := []any{}
			add := func(field string, value any) {
				item := map[string]any{"position": len(items) + 1, "type": "assistant_message", "status": "completed", "runId": "run", "createdBy": "agent",
					"creationSource": "provider", "visibility": "local", "sourceThreadId": "thread", "text": "阶段完成 token=synthetic-secret\n" + strings.Repeat("字", 1600)}
				if field != "" {
					item[field] = value
					item["text"] = "ineligible preview"
				}
				items = append(items, item)
			}
			add("", nil)
			add("runId", "another-run")
			add("runId", "")
			add("sourceThreadId", "other-thread")
			add("visibility", "inherited")
			add("creationSource", "mcp")
			add("createdBy", "user")
			add("status", "running")
			add("textTruncated", true)
			add("type", "reasoning")
			add("type", "proposed_plan")
			add("channel", "analysis")
			add("channel", "private")
			page["items"] = items
			if !valid {
				page["recentRuns"] = []any{}
			}
			return page, nil
		default:
			t.Fatalf("unexpected preview API call %s", name)
			return nil, nil
		}
	})}
	for _, mode := range []toolActivityMode{processStream, processStreamBrief, processStreamDetailed, processCard} {
		t.Run(string(mode), func(t *testing.T) {
			task.setProcessMode(Message{ID: "input"}, mode)
			valid = true
			page = mcpAgentInputPage(t)
			got, err := api.Observe(context.Background(), target, task)
			if err != nil || !strings.HasPrefix(got.StreamPreview, "阶段完成 [已脱敏]") || len([]rune(got.StreamPreview)) != 1600 || !strings.HasSuffix(got.StreamPreview, "…") {
				t.Fatalf("unexpected preview: %q, %v", got.StreamPreview, err)
			}
			valid = false
			got, err = api.Observe(context.Background(), target, task)
			if err != nil || got.StreamPreview != "" {
				t.Fatal("preview released without a verified run", err)
			}
		})
	}
}

func TestProcessStreamWithToolsInTerminalReplyModes(t *testing.T) {
	for _, tc := range []struct {
		mode, reply, msgType string
		detailed             bool
	}{
		{"stream-brief", "text", "text", false},
		{"stream-detailed", "markdown", "post", true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			e, _, ch, x, f := setupAccess(t)
			requests := captureProcessRequests(t, f, nil)
			accessCommand(t, e, ch, "reply", "admin", "chat", "/config reply "+tc.reply)
			accessCommand(t, e, ch, "tools", "admin", "chat", "/config tools off")
			accessCommand(t, e, ch, "process", "admin", "chat", "/config process "+tc.mode)
			now := time.Date(2026, 11, 19, 3, 0, 0, 0, time.UTC)
			e.Now = func() time.Time { return now }
			x.observation = Observation{RunID: "run", Status: "running", StreamPreview: "公开片段 token=synthetic-secret"}
			activity := &activityExecutor{fakeExecutor: x, tools: []runAction{
				{Label: "命令进行中", At: now},
				{Label: "文件搜索已完成", At: now.Add(-time.Second), Finished: true},
			}}
			e.T3 = activity

			accept(t, e, Normalize(groupMessage("task", "user", "chat", "/nas 合成任务", true)))
			tick(t, e)
			first := onlyTask(t, e)
			if first.Process == nil || string(first.Process.Mode) != tc.mode || first.CardID != "" ||
				first.Process.MessageID == "" || len(requests["task"]) != 1 {
				t.Fatal("expected one independent process card before terminal reply")
			}
			now = now.Add(processStreamInterval)
			tick(t, e)
			updates := requests[first.Process.MessageID]
			if len(updates) != 1 || activity.reads != 1 ||
				!strings.Contains(updates[0].Content, "公开片段 [已脱敏]") ||
				!strings.Contains(updates[0].Content, "命令进行中") ||
				strings.Contains(updates[0].Content, "文件搜索已完成") != tc.detailed ||
				strings.Contains(updates[0].Content, "synthetic-secret") ||
				!strings.Contains(updates[0].Content, "plain_text") {
				t.Fatalf("expected the selected tool detail and redacted preview on one card: %+v", updates)
			}
			x.observation = Observation{RunID: "run", Status: "completed", Summary: "最终结论"}
			tick(t, e)
			updates, replies := requests[first.Process.MessageID], requests["task"]
			finished := onlyTask(t, e).Process
			if len(updates) != 2 || !finished.Closed || finished.StreamPreview != "" || strings.Contains(updates[1].Content, "公开正文预览") ||
				len(replies) != 2 || replies[1].MsgType != tc.msgType || !strings.Contains(replies[1].Content, "最终结论") ||
				strings.Contains(replies[1].Content, "命令进行中") {
				t.Fatal("process and final reply did not close independently")
			}
		})
	}
}

func TestProcessStreamUpdatesOriginalPreviewAndKeepsFinalReply(t *testing.T) {
	e, _, ch, x, f := setupAccess(t)
	requests := captureProcessRequests(t, f, nil)
	accessCommand(t, e, ch, "text", "user", "chat", "/config reply text")
	accessCommand(t, e, ch, "stream", "admin", "chat", "/config process stream")
	now := time.Date(2026, 11, 19, 3, 0, 0, 0, time.UTC)
	e.Now = func() time.Time { return now }
	x.observation = Observation{RunID: "run", Status: "running", StreamPreview: "第一段"}
	accept(t, e, Normalize(groupMessage("stream-task", "user", "chat", "/nas 开始合成任务", true)))
	tick(t, e)
	first := onlyTask(t, e)
	if first.Process == nil || first.Process.Mode != processStream || first.Process.MessageID == "" || len(requests["stream-task"]) != 1 {
		t.Fatal("one process card must be created for the round")
	}
	card := first.Process.MessageID
	now = now.Add(processStreamInterval)
	tick(t, e)
	x.observation.StreamPreview = "第二段"
	tick(t, e)
	if len(requests[card]) != 1 || !strings.Contains(requests[card][0].Content, "第一段") || !strings.Contains(requests[card][0].Content, "plain_text") {
		t.Fatal("first preview should be literal text on the original card with updates throttled")
	}
	now = now.Add(processStreamInterval)
	tick(t, e)
	if len(requests[card]) != 2 || !strings.Contains(requests[card][1].Content, "第二段") {
		t.Fatal("next stage failed to update the original card")
	}
	x.observation = Observation{RunID: "run", Status: "completed", Summary: "最终结论"}
	tick(t, e)
	finished := onlyTask(t, e)
	if !finished.Process.Closed || len(requests[card]) != 3 || strings.Contains(requests[card][2].Content, "正文预览") ||
		len(requests["stream-task"]) != 2 || requests["stream-task"][1].MsgType != "text" || !strings.Contains(requests["stream-task"][1].Content, "最终结论") {
		t.Fatal("terminal update and final reply failed to arrive independently")
	}
}

func TestProcessCardKeepsOneResultCardThroughRestart(t *testing.T) {
	e, _, ch, x, f := setupAccess(t)
	requests := captureProcessRequests(t, f, nil)
	accessCommand(t, e, ch, "inline", "admin", "chat", "/config process card")
	accept(t, e, Normalize(groupMessage("inline-task", "user", "chat", "/nas 开始合成任务", true)))
	tick(t, e)
	first := onlyTask(t, e)
	if !first.CardPreviewEnabled || first.Process != nil || first.CardID == "" || len(requests["inline-task"]) != 1 {
		t.Fatal("expected precisely one result card")
	}

	x.observation = Observation{RunID: "run", Status: "running", StreamPreview: "第一段 token=synthetic-secret", Progress: "未核验片段"}
	tick(t, e)
	tick(t, e)
	updates := requests[first.CardID]
	if len(updates) != 1 || !strings.Contains(updates[0].Content, "第一段 [已脱敏]") ||
		strings.Contains(updates[0].Content, "synthetic-secret") || strings.Contains(updates[0].Content, "未核验片段") {
		t.Fatalf("unexpected initial preview: %+v", updates)
	}

	ch = restartAccess(t, e, f.baseURL)
	x.observation.StreamPreview = "第二段"
	tick(t, e)
	updates = requests[first.CardID]
	if len(updates) != 2 || !strings.Contains(updates[1].Content, "第二段") {
		t.Fatalf("restart did not update the original card: %+v", updates)
	}
	accessCommand(t, e, ch, "quiet", "admin", "chat", "/config progress off")
	updates = requests[first.CardID]
	if len(updates) != 3 || strings.Contains(updates[2].Content, "正文预览") {
		t.Fatal("disabling progress did not remove the preview")
	}
	accessCommand(t, e, ch, "resume-progress", "admin", "chat", "/config progress on")
	if updates = requests[first.CardID]; len(updates) != 4 || !strings.Contains(updates[3].Content, "第二段") {
		t.Fatal("enabling progress did not restore the current preview")
	}
	x.observation = Observation{RunID: "run", Status: "completed", Summary: "最终结论"}
	tick(t, e)
	updates = requests[first.CardID]
	if len(requests["inline-task"]) != 1 || len(updates) != 5 || !strings.Contains(updates[4].Content, "最终结论") ||
		strings.Contains(updates[4].Content, "正文预览") || onlyTask(t, e).CardPreview != "" {
		t.Fatal("result was not delivered on the original card")
	}
}

func TestProcessCardRecoversCreationAndRetriesTerminalUpdateBeforeContinuing(t *testing.T) {
	e, _, ch, x, f := setupAccess(t)
	failCreate, failFinal := true, true
	requests := captureProcessRequests(t, f, func(id string, body replyModeRequest) bool {
		if id == "first-input" && failCreate {
			failCreate = false
			return true
		}
		if failFinal && strings.HasPrefix(id, "reply-") && strings.Contains(body.Content, "第一轮结论") {
			failFinal = false
			return true
		}
		return false
	})
	accessCommand(t, e, ch, "inline", "admin", "chat", "/config process card")
	accept(t, e, Normalize(groupMessage("first-input", "user", "chat", "/nas 第一轮", true)))
	tick(t, e)
	if first := onlyTask(t, e); first.CardID != "" || first.NotificationIssue == "" {
		t.Fatal("lost creation response should stay unconfirmed")
	}

	x.observation = Observation{RunID: "run", Status: "running", StreamPreview: "原卡预览"}
	tick(t, e)
	first := onlyTask(t, e)
	creates := requests["first-input"]
	if first.CardID == "" || first.NotifyHash != "" || len(creates) != 2 || creates[0].UUID != creates[1].UUID ||
		strings.Contains(creates[1].Content, "原卡预览") {
		t.Fatal("original card receipt was not recovered separately from the preview")
	}
	tick(t, e)
	if updates := requests[first.CardID]; len(updates) != 1 || !strings.Contains(updates[0].Content, "原卡预览") {
		t.Fatal("recovered card did not get the current preview")
	}

	x.observation = Observation{RunID: "run", Status: "completed", Summary: "第一轮结论"}
	tick(t, e)
	if onlyTask(t, e).NotificationIssue == "" {
		t.Fatal("lost terminal update was marked delivered")
	}
	accept(t, e, Normalize(groupMessage("second-input", "user", "chat", "/nas 继续", true)))
	tick(t, e)
	last := onlyTask(t, e)
	if x.sends != 1 || last.CardID != first.CardID || last.NotificationIssue != "" || len(requests[first.CardID]) != 3 ||
		!strings.Contains(requests[first.CardID][2].Content, "第一轮结论") {
		t.Fatal("terminal update must finish on the original card before the next run")
	}
	tick(t, e)
	if x.sends != 2 || len(requests["second-input"]) != 1 {
		t.Fatal("queued continuation did not resume after terminal confirmation")
	}
}
