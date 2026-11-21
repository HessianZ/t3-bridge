package bridge

import (
	"fmt"
	"testing"
)

func TestObserveSettlesOriginalRunWhenThreadResultIsLater(t *testing.T) {
	target := Target{EnvironmentID: "environment", ProjectID: "project", ProviderID: "provider", Model: "original-model", RuntimeMode: "auto", InteractionMode: "default"}
	for _, tc := range []struct {
		name              string
		delegationRun     string
		addDelegation     bool
		rememberedChild   bool
		rememberedPending bool
		timedOut          bool
		settled           bool
	}{
		{name: "later independent reply", settled: true},
		{name: "only later run has delegates", addDelegation: true, delegationRun: "later", settled: true},
		{name: "original run has an unattributed delegate", addDelegation: true, delegationRun: "run"},
		{name: "delegate has unknown origin", addDelegation: true},
		{name: "bridge has remembered a child thread", rememberedChild: true},
		{name: "bridge has remembered pending work", rememberedPending: true},
		{name: "wait did not settle", timedOut: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := &Task{ThreadID: "thread", RunID: "run"}
			if tc.rememberedChild {
				task.ChildThreads = []string{"child-thread"}
			}
			if tc.rememberedPending {
				task.TaskIDs = []string{"background-work"}
			}
			caller := scriptedCaller(func(name string, args map[string]any) (any, error) {
				if name == "t3_thread_wait" {
					if args["runId"] != task.RunID || args["untilDelegatedWorkSettled"] != true {
						t.Fatal("wait did not target the original run and its delegated work")
					}
					return map[string]any{"threadId": task.ThreadID, "runId": task.RunID, "status": "completed", "timedOut": tc.timedOut,
						"resultRun": map[string]string{"runId": "later", "status": "completed"}}, nil
				}
				if name != "t3_thread_read" {
					t.Fatalf("unexpected tool %s", name)
				}
				page := pageFor(target)
				run := page["recentRuns"].([]any)[0].(map[string]any)
				run["ordinal"] = 1
				if args["threadId"] == "child-thread" {
					page["thread"].(map[string]any)["threadId"] = "child-thread"
					return page, nil
				}
				page["recentRuns"] = []any{run, map[string]any{"runId": "later", "ordinal": 2, "status": "completed", "model": "later-model", "providerInstanceId": target.ProviderID}}
				if args["view"] == "activity" {
					if tc.addDelegation {
						page["items"] = []any{map[string]any{"type": "subagent", "runId": tc.delegationRun, "position": 1, "sourceThreadId": task.ThreadID, "visibility": "local"}}
					}
					return page, nil
				}
				page["items"] = []any{
					map[string]any{"type": "assistant_message", "status": "completed", "createdBy": "agent", "runId": task.RunID, "position": 2, "text": "original answer"},
					map[string]any{"type": "assistant_message", "status": "completed", "createdBy": "agent", "runId": "later", "position": 3, "text": "unrelated answer"},
				}
				return page, nil
			})
			got, err := (&T3{Calls: caller}).Observe(t.Context(), target, task)
			if err != nil || got.Unsettled == tc.settled || got.OriginalRunSettled != tc.settled || got.ResultRunID != "" || got.RunID != task.RunID || got.Summary != "original answer" {
				t.Fatalf("observation = %+v, err = %v; want original answer, settled = %t", got, err, tc.settled)
			}
		})
	}
}

func TestAttributedResultRun(t *testing.T) {
	const title = "stage114-child"
	const notice = `Delegated task "stage114-child" finished`
	const repeatedTitleNotice = "2 delegated tasks finished: stage114-child, stage114-child"
	const twoTitlesNotice = "2 delegated tasks finished: stage114-child, worker"
	target := Target{EnvironmentID: "environment", ProjectID: "project", ProviderID: "provider", Model: "model", RuntimeMode: "auto", InteractionMode: "default"}
	task := &Task{ThreadID: "thread", RunID: "run"}
	item := func(position int, run, kind, text string) map[string]any {
		return map[string]any{
			"position": position, "visibility": "local", "sourceThreadId": task.ThreadID, "itemId": fmt.Sprint("item-", position),
			"runId": run, "type": kind, "status": "completed", "text": text, "textTruncated": false,
			"title": nil, "messageId": nil, "createdBy": nil, "creationSource": nil, "nextTextOffset": nil, "updatedAt": "2026-11-20T00:00:00Z",
		}
	}
	subagent := func(position int, run, id, title string) map[string]any {
		row := item(position, run, "subagent", "child result")
		row["subagentId"], row["title"] = id, title
		return row
	}
	userInput := func(position int, run string) map[string]any {
		row := item(position, run, "user_message", "ordinary input")
		row["messageId"], row["createdBy"], row["creationSource"] = "user-input", "agent", "mcp"
		return row
	}
	run := func(id string, ordinal int) map[string]any {
		return map[string]any{
			"runId": id, "ordinal": ordinal, "status": "completed", "model": target.Model,
			"providerInstanceId": target.ProviderID, "executedModel": target.Model,
		}
	}
	for _, tt := range []struct {
		name, want string
		change     func(items, runs *[]map[string]any)
	}{
		{name: "single wake", want: "wake"},
		{name: "same title and origin", want: "wake", change: func(items, runs *[]map[string]any) {
			*items = append([]map[string]any{subagent(2, "run", "child", title)}, *items...)
		}},
		{name: "same title different child on original run", want: "wake", change: func(items, runs *[]map[string]any) {
			*items = append([]map[string]any{subagent(2, "run", "other-child", title)}, *items...)
		}},
		{name: "same title owned by a later run", want: "wake", change: func(items, runs *[]map[string]any) {
			*items = append(*items, subagent(6, "manual", "other-child", title))
			*runs = append([]map[string]any{run("manual", 4)}, *runs...)
		}},
		{name: "same title also matches an earlier unrelated run", change: func(items, runs *[]map[string]any) {
			*items = append([]map[string]any{subagent(1, "earlier", "earlier-child", title), subagent(2, "run", "other-child", title)}, *items...)
			*runs = append(*runs, run("earlier", 1))
		}},
		{name: "reuse a title after a proven earlier delivery", want: "wake", change: func(items, runs *[]map[string]any) {
			(*runs)[0]["ordinal"], (*runs)[1]["ordinal"] = 4, 3
			*runs = append(*runs, run("earlier", 1), run("earlier-wake", 2))
			*items = append([]map[string]any{subagent(0, "earlier", "earlier-child", title), item(1, "earlier-wake", "notification", notice)}, *items...)
		}},
		{name: "cancelled earlier wake leaves the same title plausible", change: func(items, runs *[]map[string]any) {
			(*runs)[0]["ordinal"], (*runs)[1]["ordinal"] = 4, 3
			cancelled := run("earlier-wake", 2)
			cancelled["status"] = "cancelled"
			*runs = append(*runs, run("earlier", 1), cancelled)
			*items = append([]map[string]any{subagent(0, "earlier", "earlier-child", title), item(1, "earlier-wake", "notification", notice)}, *items...)
		}},
		{name: "group with repeated title and distinct task IDs", want: "wake", change: func(items, runs *[]map[string]any) {
			(*items)[1]["text"] = repeatedTitleNotice
			*items = append([]map[string]any{subagent(2, "run", "other-child", title)}, *items...)
		}},
		{name: "group cannot count the same task ID twice", change: func(items, runs *[]map[string]any) {
			(*items)[1]["text"] = repeatedTitleNotice
			*items = append([]map[string]any{subagent(2, "run", "child", title)}, *items...)
		}},
		{name: "group source must precede notification", change: func(items, runs *[]map[string]any) {
			(*items)[1]["text"] = repeatedTitleNotice
			*items = append(*items, subagent(6, "run", "other-child", title))
		}},
		{name: "partial group on original run", want: "wake", change: func(items, runs *[]map[string]any) {
			(*items)[1]["text"] = "2 of 3 delegated tasks finished: stage114-child, worker"
			*items = append([]map[string]any{subagent(1, "run", "spare-child", "spare"), subagent(2, "run", "worker-child", "worker")}, *items...)
		}},
		{name: "complete group also matches an earlier unrelated run", change: func(items, runs *[]map[string]any) {
			(*items)[1]["text"] = twoTitlesNotice
			*items = append([]map[string]any{
				subagent(0, "earlier", "earlier-child", title), subagent(1, "earlier", "earlier-worker", "worker"),
				subagent(2, "run", "worker-child", "worker"),
			}, *items...)
			*runs = append(*runs, run("earlier", 1))
		}},
		{name: "group count exceeds its listed titles", change: func(items, runs *[]map[string]any) {
			(*items)[1]["text"] = "3 delegated tasks finished: stage114-child, worker"
			*items = append([]map[string]any{subagent(1, "run", "spare-child", "spare"), subagent(2, "run", "worker-child", "worker")}, *items...)
		}},
		{name: "failed single notification", want: "wake", change: func(items, runs *[]map[string]any) {
			(*items)[0]["status"] = "failed"
			(*items)[1]["text"] = `Delegated task "stage114-child" failed`
		}},
		{name: "stopped group notification", want: "wake", change: func(items, runs *[]map[string]any) {
			(*items)[1]["text"] = "2 delegated tasks stopped: stage114-child, worker"
			worker := subagent(2, "run", "worker-child", "worker")
			worker["status"] = "cancelled"
			*items = append([]map[string]any{worker}, *items...)
		}},
		{name: "title comparison is case sensitive", want: "wake", change: func(items, runs *[]map[string]any) {
			*items = append([]map[string]any{subagent(2, "run", "other-child", "Stage114-child")}, *items...)
		}},
		{name: "missing source task ID", change: func(items, runs *[]map[string]any) {
			delete((*items)[0], "subagentId")
		}},
		{name: "foreign source", change: func(items, runs *[]map[string]any) {
			(*items)[0]["sourceThreadId"] = "other-thread"
			(*items)[0]["visibility"] = "inherited"
		}},
		{name: "mismatched notification text", change: func(items, runs *[]map[string]any) {
			(*items)[1]["text"] = `Delegated task "Stage114-child" finished`
		}},
		{name: "notification detail", change: func(items, runs *[]map[string]any) {
			(*items)[1]["text"] = notice + "\nAdditional detail"
		}},
		{name: "truncated notification", change: func(items, runs *[]map[string]any) {
			(*items)[1]["textTruncated"], (*items)[1]["nextTextOffset"] = true, len(notice)
		}},
		{name: "another notification on wake", change: func(items, runs *[]map[string]any) {
			*items = append(*items, item(6, "wake", "notification", "Monitor finished"))
		}},
		{name: "ordinary input on wake", change: func(items, runs *[]map[string]any) {
			*items = append(*items, userInput(6, "wake"))
		}},
		{name: "optional executed model omitted by ACP", want: "wake", change: func(items, runs *[]map[string]any) {
			(*runs)[0]["executedModel"] = nil
		}},
		{name: "executed model drift", change: func(items, runs *[]map[string]any) {
			(*runs)[0]["executedModel"] = "other-model"
		}},
		{name: "nonterminal original", change: func(items, runs *[]map[string]any) {
			(*runs)[1]["status"] = "running"
		}},
		{name: "duplicate ordinal", change: func(items, runs *[]map[string]any) {
			*runs = append(*runs, run("duplicate", 3))
		}},
		{name: "missing ordinal", change: func(items, runs *[]map[string]any) {
			(*runs)[0]["ordinal"] = 4
		}},
		{name: "nested wake", want: "second-wake", change: func(items, runs *[]map[string]any) {
			*items = append(*items, subagent(6, "wake", "nested-child", "nested-child"),
				item(7, "second-wake", "notification", `Delegated task "nested-child" finished`))
			*runs = append([]map[string]any{run("second-wake", 4)}, *runs...)
		}},
		{name: "group cannot combine sources from two owned runs", change: func(items, runs *[]map[string]any) {
			*items = append(*items, subagent(6, "wake", "worker-child", "worker"),
				item(7, "second-wake", "notification", twoTitlesNotice))
			*runs = append([]map[string]any{run("second-wake", 4)}, *runs...)
		}},
		{name: "later manual input with unaccounted children stays pending", change: func(items, runs *[]map[string]any) {
			*items = append(*items, subagent(6, "wake", "nested-child", "nested-child"), userInput(7, "manual"),
				item(8, "second-wake", "notification", `Delegated task "nested-child" finished`))
			*runs = append([]map[string]any{run("second-wake", 5), run("manual", 4)}, *runs...)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			items := []map[string]any{subagent(3, "run", "child", title), item(4, "wake", "notification", notice), item(5, "wake", "assistant_message", "result")}
			runs := []map[string]any{run("wake", 3), run("run", 2)}
			if tt.change != nil {
				tt.change(&items, &runs)
			}
			firstPosition := items[0]["position"].(int)
			reads := 0
			caller := scriptedCaller(func(name string, args map[string]any) (any, error) {
				if name != "t3_thread_read" || args["view"] != "activity" || args["environmentId"] != target.EnvironmentID ||
					args["threadId"] != task.ThreadID || args["limit"] != float64(100) || args["runLimit"] != float64(50) ||
					args["maxCharsPerItem"] != float64(512) {
					t.Fatalf("unexpected read %s %#v", name, args)
				}
				page := pageFor(target)
				page["recentRuns"] = runs
				switch reads {
				case 0:
					if args["afterPosition"] != float64(-1) {
						t.Fatalf("first cursor = %v", args["afterPosition"])
					}
					page["items"], page["hasMore"], page["nextPosition"] = items[:1], true, firstPosition
				case 1:
					if args["afterPosition"] != float64(firstPosition) {
						t.Fatalf("second cursor = %v", args["afterPosition"])
					}
					page["items"], page["nextPosition"] = items[1:], items[len(items)-1]["position"]
				default:
					t.Fatal("unexpected extra page")
				}
				reads++
				return page, nil
			})
			got, err := (&T3{Calls: caller}).attributedResultRun(t.Context(), target, task)
			if got != tt.want || err != nil || reads != 2 {
				t.Fatalf("attributed run = %q, err = %v, reads = %d; want %q after two reads", got, err, reads, tt.want)
			}
		})
	}
}
