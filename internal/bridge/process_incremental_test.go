package bridge

import (
	"context"
	"maps"
	"testing"
)

func TestProcessStreamIncrementalLocalProviderMessage(t *testing.T) {
	target := testConfig(t).Routes["nas"]
	task := &Task{ThreadID: "thread", RunID: "run", Process: &processMessage{Mode: processStream}}
	newFrame := func(t *testing.T, body string) (map[string]any, map[string]any) {
		t.Helper()
		page := mcpAgentInputPage(t)
		page["thread"].(map[string]any)["activeRunId"] = task.RunID
		page["recentRuns"].([]any)[0].(map[string]any)["status"] = "running"
		item := map[string]any{
			"position": 73, "itemId": "assistant-item", "messageId": "assistant-message",
			"type": "assistant_message", "status": "running", "runId": task.RunID,
			"createdBy": "agent", "creationSource": "provider", "visibility": "local",
			"sourceThreadId": task.ThreadID, "channel": "", "text": body, "textTruncated": false,
		}
		page["items"] = append(page["items"].([]any), item)
		return page, item
	}
	observe := func(t *testing.T, page, wait map[string]any) (Observation, error) {
		t.Helper()
		waited := false
		api := &T3{Calls: scriptedCaller(func(name string, args map[string]any) (any, error) {
			if args["environmentId"] != target.EnvironmentID || args["threadId"] != task.ThreadID {
				t.Fatalf("unexpected target for %s: %v", name, args)
			}
			switch name {
			case "t3_thread_wait":
				if waited || args["runId"] != task.RunID || args["untilDelegatedWorkSettled"] != true {
					t.Fatalf("unexpected wait request: %v", args)
				}
				waited = true
				return wait, nil
			case "t3_thread_read":
				if !waited || args["view"] != "messages" || args["afterPosition"] != float64(-1) || args["itemId"] != nil {
					t.Fatalf("unexpected messages read: %v", args)
				}
				return page, nil
			default:
				t.Fatalf("unexpected preview API call %s", name)
				return nil, nil
			}
		})}
		return api.Observe(context.Background(), target, task)
	}
	timeout := map[string]any{"threadId": task.ThreadID, "runId": task.RunID, "status": "running", "timedOut": true}

	t.Run("preview grows across reads", func(t *testing.T) {
		const first = "公开片段"
		page, item := newFrame(t, first)
		for _, body := range []string{first, first + "继续增长", "已订正", ""} {
			item["text"] = body
			got, err := observe(t, page, timeout)
			if err != nil || got.RunID != task.RunID || got.Status != "running" || !got.Unsettled || got.StreamPreview != body {
				t.Fatalf("preview = %q, want %q, observation = %+v, err = %v", got.StreamPreview, body, got, err)
			}
		}
	})

	for _, tc := range []struct {
		name                    string
		item, thread, run, wait map[string]any
		wantErr                 bool
	}{
		{name: "reasoning", item: map[string]any{"type": "reasoning"}},
		{name: "tool", item: map[string]any{"type": "tool_result"}},
		{name: "analysis channel", item: map[string]any{"channel": "analysis"}},
		{name: "unknown channel", item: map[string]any{"channel": "unrecognized"}},
		{name: "wrong item thread", item: map[string]any{"sourceThreadId": "other-thread"}},
		{name: "wrong item run", item: map[string]any{"runId": "other-run"}},
		{name: "inherited item", item: map[string]any{"visibility": "inherited"}},
		{name: "non-agent item", item: map[string]any{"createdBy": "user"}},
		{name: "non-provider item", item: map[string]any{"creationSource": "mcp"}},
		{name: "truncated item", item: map[string]any{"textTruncated": true}},
		{name: "offset item", item: map[string]any{"nextTextOffset": 1}},
		{name: "missing message ID", item: map[string]any{"messageId": nil}},
		{name: "wrong readback thread", thread: map[string]any{"threadId": "other-thread"}, wantErr: true},
		{name: "wrong readback provider", thread: map[string]any{"providerInstanceId": "other-provider"}, wantErr: true},
		{name: "wrong active run", thread: map[string]any{"activeRunId": "other-run"}},
		{name: "missing active run", thread: map[string]any{"activeRunId": nil}},
		{name: "wrong recent run", run: map[string]any{"runId": "other-run"}},
		{name: "wrong recent run provider", run: map[string]any{"providerInstanceId": "other-provider"}, wantErr: true},
		{name: "terminal recent run", run: map[string]any{"status": "completed"}},
		{name: "wait without timeout", wait: map[string]any{"timedOut": nil}},
		{name: "wait on another run", wait: map[string]any{"runId": "other-run"}},
		{name: "wait on another thread", wait: map[string]any{"threadId": "other-thread"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, item := newFrame(t, "不得显示的片段")
			maps.Copy(item, tc.item)
			maps.Copy(page["thread"].(map[string]any), tc.thread)
			maps.Copy(page["recentRuns"].([]any)[0].(map[string]any), tc.run)
			wait := maps.Clone(timeout)
			maps.Copy(wait, tc.wait)
			got, err := observe(t, page, wait)
			if (err != nil) != tc.wantErr || got.StreamPreview != "" {
				t.Fatalf("ineligible preview = %q, err = %v (want error: %t)", got.StreamPreview, err, tc.wantErr)
			}
		})
	}

	t.Run("settled run keeps its formal answer", func(t *testing.T) {
		page, item := newFrame(t, "未完成的片段")
		page["thread"].(map[string]any)["activeRunId"] = nil
		page["recentRuns"].([]any)[0].(map[string]any)["status"] = "completed"
		wait := map[string]any{
			"threadId": task.ThreadID, "runId": task.RunID, "status": "completed", "timedOut": false,
			"resultRun": map[string]any{"runId": task.RunID, "status": "completed"},
		}
		got, err := observe(t, page, wait)
		if err != nil || got.StreamPreview != "" || got.Summary != "" || got.Progress != "" {
			t.Fatalf("unfinished text escaped its preview after settlement = %+v, err = %v", got, err)
		}

		const answer = "已完成的正式答复"
		item["status"], item["text"] = "completed", answer
		got, err = observe(t, page, wait)
		if err != nil || got.RunID != task.RunID || got.Status != "completed" || got.Unsettled || !got.OriginalRunSettled ||
			got.Summary != answer || got.ResultIssue != "" {
			t.Fatalf("settled answer = %q, want %q, observation = %+v, err = %v", got.Summary, answer, got, err)
		}
	})
}
