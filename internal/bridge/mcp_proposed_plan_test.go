package bridge

import (
	"context"
	"maps"
	"strings"
	"testing"
)

// The T3 v0.1.10 messages view projects a proposed plan as a turn item without a backing message.
func proposedPlanFixture(run, id, body string, position int) map[string]any {
	return map[string]any{
		"type": "proposed_plan", "status": "completed", "visibility": "local", "sourceThreadId": "thread",
		"runId": run, "itemId": id, "position": position, "updatedAt": "2026-11-19T00:00:00.000Z",
		"messageId": nil, "createdBy": nil, "creationSource": nil,
		"text": body, "textTruncated": false, "nextTextOffset": nil,
	}
}

func observeProposedPlanFixture(t *testing.T, target Target, task *Task, resultRun string, page map[string]any, extraRead func(map[string]any) map[string]any) (Observation, error) {
	t.Helper()
	waited, read := false, false
	api := &T3{Calls: scriptedCaller(func(name string, args map[string]any) (any, error) {
		if args["environmentId"] != target.EnvironmentID {
			t.Fatalf("wrong environment for %s: %v", name, args)
		}
		switch name {
		case "t3_thread_wait":
			if waited || args["threadId"] != task.ThreadID || args["runId"] != task.RunID || args["untilDelegatedWorkSettled"] != true {
				t.Fatalf("wrong settlement request: %v", args)
			}
			waited = true
			if resultRun == "" {
				return map[string]any{"threadId": task.ThreadID, "timedOut": true}, nil
			}
			return map[string]any{"threadId": task.ThreadID, "runId": task.RunID, "status": "completed", "timedOut": false,
				"resultRun": map[string]any{"runId": resultRun, "status": "completed"}}, nil
		case "t3_thread_read":
			if !waited {
				t.Fatal("read before settlement check")
			}
			if args["view"] == "activity" && resultRun != "" && resultRun != task.RunID {
				return resultActivityFixture(target, task.RunID, resultRun), nil
			}
			if !read && args["threadId"] == task.ThreadID && args["view"] == "messages" && args["afterPosition"] == float64(-1) && args["itemId"] == nil {
				read = true
				return page, nil
			}
			if read && extraRead != nil {
				return extraRead(args), nil
			}
		}
		t.Fatalf("unexpected %s: %v", name, args)
		return nil, nil
	})}
	got, err := api.Observe(context.Background(), target, task)
	if !read {
		t.Fatalf("missing messages read: %v", err)
	}
	return got, err
}

func TestT3ProposedPlanPreviewRequiresVerifiedOriginalRunAndEnabledMode(t *testing.T) {
	target := testConfig(t).Routes["nas"]
	const draft = "## 核对需求\n- 补齐验收条件"
	for _, tc := range []struct {
		name      string
		mode      toolActivityMode
		alter     func(map[string]any, map[string]any)
		resultRun string
		want      bool
		wantDrift bool
	}{
		{name: "process stream", mode: processStream, want: true},
		{name: "process card", mode: processCard, want: true, alter: func(_ map[string]any, item map[string]any) {
			delete(item, "messageId")
			delete(item, "createdBy")
			delete(item, "creationSource")
			item["channel"] = ""
		}},
		{name: "tools only", mode: toolActivityBrief},
		{name: "truncated", mode: processStream, alter: func(_ map[string]any, item map[string]any) {
			item["text"], item["textTruncated"], item["nextTextOffset"] = "## 核对", true, 5
		}},
		{name: "other run readback", mode: processCard, alter: func(page, _ map[string]any) {
			page["recentRuns"].([]any)[0].(map[string]any)["runId"] = "other-run"
		}},
		{name: "provider readback drift", mode: processCard, wantDrift: true, alter: func(page, _ map[string]any) {
			page["recentRuns"].([]any)[0].(map[string]any)["providerInstanceId"] = "other-provider"
		}},
		{name: "executed model readback drift", mode: processStream, wantDrift: true, alter: func(page, _ map[string]any) {
			page["recentRuns"].([]any)[0].(map[string]any)["executedModel"] = "other-model"
		}},
		{name: "settlement moved to later run", mode: processCard, resultRun: "settled-run", alter: func(page, _ map[string]any) {
			runs := page["recentRuns"].([]any)
			settled := maps.Clone(runs[0].(map[string]any))
			settled["runId"] = "settled-run"
			page["recentRuns"] = append(runs, settled)
			page["items"] = append(page["items"].([]any), proposedPlanFixture("settled-run", "settled-draft", "后续轮草案", 74))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := mcpAgentInputPage(t)
			page["recentRuns"].([]any)[0].(map[string]any)["status"] = "running"
			item := proposedPlanFixture("run", "draft", draft, 73)
			page["items"] = append(page["items"].([]any), item)
			if tc.alter != nil {
				tc.alter(page, item)
			}
			task := &Task{ThreadID: "thread", RunID: "run", RequestMessage: "用户原文\nrequestId: request"}
			task.setProcessMode(Message{ID: "input"}, tc.mode)
			got, err := observeProposedPlanFixture(t, target, task, tc.resultRun, page, nil)
			if tc.wantDrift {
				if err == nil || !strings.Contains(err.Error(), "provider/model drift") {
					t.Fatalf("expected exact-run provider/model check: %+v, %v", got, err)
				}
				return
			}
			if err != nil || got.Summary != "" || got.Progress != "" {
				t.Fatalf("unexpected observation: %+v, %v", got, err)
			}
			if tc.want {
				if !strings.HasPrefix(got.StreamPreview, "T3 计划草案") || !strings.Contains(got.StreamPreview, draft) {
					t.Fatalf("missing labeled preview: %q", got.StreamPreview)
				}
			} else if got.StreamPreview != "" {
				t.Fatalf("released an ineligible preview: %q", got.StreamPreview)
			}
		})
	}
}

func TestT3ProposedPlanFinalUsesLatestCanonicalPlanOfSettledRun(t *testing.T) {
	target := testConfig(t).Routes["nas"]
	const resultRun, answer, draft = "settled-run", "正式答复", "## 最新草案\n- 核对配置"
	for _, tc := range []struct {
		name, confirmedRun, answer string
		pending                    string
		missingReadback, wantPlan  bool
	}{
		{name: "appended to answer", confirmedRun: resultRun, answer: answer, wantPlan: true},
		{name: "plan-only answer", confirmedRun: resultRun, wantPlan: true},
		{name: "unconfirmed result run", answer: answer},
		{name: "missing result run readback", confirmedRun: resultRun, answer: answer, missingReadback: true},
		{name: "pending approval", confirmedRun: resultRun, answer: answer, pending: "approval"},
		{name: "pending background task", confirmedRun: resultRun, answer: answer, pending: "background"},
		{name: "tracked child still running", confirmedRun: resultRun, answer: answer, pending: "child"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := mcpAgentInputPage(t)
			runs := page["recentRuns"].([]any)
			if !tc.missingReadback {
				settled := maps.Clone(runs[0].(map[string]any))
				settled["runId"] = resultRun
				page["recentRuns"] = append(runs, settled)
			}
			items := append(page["items"].([]any),
				proposedPlanFixture(resultRun, "old-draft", "旧草案", 73),
				proposedPlanFixture(resultRun, "latest-draft", draft, 74),
				proposedPlanFixture("run", "parent-draft", "原始轮草案", 75),
			)
			if tc.answer != "" {
				run := resultRun
				if tc.confirmedRun == "" {
					run = "run"
				}
				items = append(items, map[string]any{"position": 76, "type": "assistant_message", "status": "completed", "createdBy": "agent", "runId": run,
					"creationSource": "provider", "sourceThreadId": "thread", "visibility": "local", "text": tc.answer})
			}
			if tc.wantPlan {
				for _, invalid := range []struct {
					field string
					value any
				}{
					{"type", "todo_list"}, {"status", "running"}, {"visibility", "inherited"},
					{"sourceThreadId", "other-thread"}, {"runId", nil}, {"itemId", ""}, {"updatedAt", nil},
					{"messageId", "message"}, {"createdBy", "agent"}, {"creationSource", "provider"}, {"channel", "analysis"},
				} {
					item := proposedPlanFixture(resultRun, "invalid-"+invalid.field, "非法草案字段 "+invalid.field, 80+len(items))
					item[invalid.field] = invalid.value
					items = append(items, item)
				}
			}
			page["items"] = items
			task := &Task{ThreadID: "thread", RunID: "run", RequestMessage: "用户原文\nrequestId: request"}
			thread := page["thread"].(map[string]any)
			var extraRead func(map[string]any) map[string]any
			switch tc.pending {
			case "approval":
				thread["pendingRequestCount"] = 1
			case "background":
				thread["pendingBackgroundTasks"] = []any{map[string]any{"taskId": "pending-task"}}
			case "child":
				task.ChildThreads = []string{"child"}
				extraRead = func(args map[string]any) map[string]any {
					if args["threadId"] != "child" || args["itemId"] != nil {
						t.Fatalf("unexpected pending read: %v", args)
					}
					child := pageFor(target)
					child["thread"].(map[string]any)["threadId"] = "child"
					child["recentRuns"].([]any)[0].(map[string]any)["status"] = "running"
					return child
				}
			}
			got, err := observeProposedPlanFixture(t, target, task, tc.confirmedRun, page, extraRead)
			want := tc.answer
			if tc.missingReadback {
				want = ""
			}
			if tc.wantPlan {
				if want != "" {
					want += "\n\n"
				}
				want += "T3 计划草案\n\n" + draft
			}
			if err != nil || got.Summary != want || got.StreamPreview != "" {
				t.Fatalf("summary=%q, want %q, preview=%q, err=%v", got.Summary, want, got.StreamPreview, err)
			}
		})
	}
}

func TestT3ProposedPlanPaginationPreservesAnswerAndRejectsDrift(t *testing.T) {
	target := testConfig(t).Routes["nas"]
	const answer = "正式答复"
	fragments := []string{"## 🧭\n", "- 验证🧪\n", "- 完成"}
	for _, tc := range []struct {
		field string
		value any
	}{
		{"", nil},
		{"itemId", "other-item"}, {"runId", "other-run"}, {"sourceThreadId", "other-thread"},
		{"position", 75}, {"updatedAt", "2026-11-19T00:00:01.000Z"}, {"nextTextOffset", 6}, {"nextTextOffset", 21},
	} {
		name := "UTF-16 continuation"
		if tc.field != "" {
			name = tc.field + " drift"
		}
		if tc.field == "nextTextOffset" && tc.value == 21 {
			name = "UTF-8 byte cursor"
		}
		t.Run(name, func(t *testing.T) {
			page := mcpAgentInputPage(t)
			plan := proposedPlanFixture("run", "draft", fragments[0], 74)
			plan["textTruncated"], plan["nextTextOffset"] = true, 6
			page["items"] = append(page["items"].([]any),
				map[string]any{"position": 73, "type": "assistant_message", "status": "completed", "createdBy": "agent", "runId": "run", "text": answer},
				plan,
			)
			task := &Task{ThreadID: "thread", RunID: "run", CardPreviewEnabled: true}
			offsets := []float64{6, 13}
			reads := 0
			got, err := observeProposedPlanFixture(t, target, task, "run", page, func(args map[string]any) map[string]any {
				if reads >= len(offsets) || args["threadId"] != "thread" || args["view"] != "messages" || args["itemId"] != "draft" || args["textOffset"] != offsets[reads] {
					t.Fatalf("wrong continuation %d: %v", reads+1, args)
				}
				part := proposedPlanFixture("run", "draft", fragments[reads+1], 74)
				if reads == 0 {
					part["textTruncated"], part["nextTextOffset"] = true, 13
					if tc.field != "" {
						part[tc.field] = tc.value
					}
				}
				reads++
				tail := maps.Clone(page)
				tail["items"] = []any{part}
				return tail
			})
			if err != nil || got.StreamPreview != "" || !strings.HasPrefix(got.Summary, answer+"\n\nT3 计划草案\n\n") {
				t.Fatalf("unexpected readback: %+v, %v", got, err)
			}
			if tc.field == "" {
				if reads != len(offsets) || !strings.HasSuffix(got.Summary, strings.Join(fragments, "")) {
					t.Fatalf("incomplete UTF-16 readback: %q (%d reads)", got.Summary, reads)
				}
				return
			}
			if reads != 1 || !strings.Contains(got.Summary, "未完整读取") {
				t.Fatalf("missing incomplete-plan notice: %q (%d reads)", got.Summary, reads)
			}
			for _, fragment := range fragments {
				if strings.Contains(got.Summary, fragment) {
					t.Fatalf("leaked a partial plan: %q", got.Summary)
				}
			}
		})
	}
}
