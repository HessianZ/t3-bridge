package bridge

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestSessionResumeFlowPagination(t *testing.T) {
	e, x, replies, l, history := resumeFlowFixture(t, 9, false)
	first := resumeFlowCommand(t, e, "first-page", "/resume")
	firstBody := replies.replies[len(replies.replies)-1]
	assertPage := func(cmd *SessionCommand, page string, start, end int, nav string) {
		t.Helper()
		if cmd == nil || cmd.Card == nil || cmd.Card.ReplyID == "" || !strings.Contains(cmd.Reply, page) {
			t.Fatalf("expected a delivered history card for %s, got %+v", page, cmd)
		}
		var labels, want []string
		for _, button := range cmd.Card.Buttons {
			if button.Command == "/resume" {
				labels = append(labels, button.Label)
			}
		}
		for i, task := range history {
			row := fmt.Sprintf("%d · %s", i+1, shortTaskID(task.ID))
			visible := i >= start && i < end
			if strings.Contains(cmd.Reply, "\n"+row+" ·") != visible {
				t.Errorf("page %s, row %q visible want %t: %q", page, row, visible, cmd.Reply)
			}
			if visible {
				want = append(want, "切换到 "+row)
			}
		}
		want = append(want, nav)
		if !slices.Equal(labels, want) {
			t.Fatalf("page %s buttons = %v, want %v", page, labels, want)
		}
	}
	assertPage(first, "1/2", 0, 8, "下一页")
	clickCommandCard(t, l, commandCardClick(t, first, firstBody, "下一页", "resume-next"))
	second := consumeResumeFlowMessage(t, e, "card:resume-next")
	secondBody := replies.replies[len(replies.replies)-1]
	assertPage(second, "2/2", 8, 9, "上一页")
	clickCommandCard(t, l, commandCardClick(t, second, secondBody, "上一页", "resume-prev"))
	assertPage(consumeResumeFlowMessage(t, e, "card:resume-prev"), "1/2", 0, 8, "下一页")
	assertPage(resumeFlowCommand(t, e, "command-page", "/resume page 2"), "2/2", 8, 9, "上一页")
	if cmd := resumeFlowCommand(t, e, "past-end", "/resume page 3"); cmd.Card != nil || !strings.Contains(cmd.Reply, "2 页") {
		t.Fatalf("out-of-range page did not report the page limit: %+v", cmd)
	}

	ninth := shortTaskID(history[8].ID)
	choose := commandCardClick(t, second, secondBody, "切换到 9 · "+ninth, "resume-choose")
	wrongUser, wrongPage := choose, choose
	wrongUser.EventID, wrongUser.Operator.OpenID = "resume-wrong-user", "other"
	wrongPage.EventID, wrongPage.MessageID = "resume-wrong-page", first.Card.ReplyID
	clickCommandCard(t, l, wrongUser)
	clickCommandCard(t, l, wrongPage)
	if len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("a different user or page accepted the selection callback")
	}

	clickCommandCard(t, l, choose)
	clickCommandCard(t, l, commandCardClick(t, second, secondBody, "上一页", "resume-revoked-prev"))
	if err := e.Store.update(func(st *State) error {
		st.AccessPolicy.Routes = map[string]map[string]bool{"nas": {"user": false}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.publishPolicy()
	if cmd := consumeResumeFlowMessage(t, e, "card:resume-choose"); cmd != nil {
		t.Fatalf("a queued callback from a previously authorized page produced a receipt: %+v", cmd)
	}
	if cmd := consumeResumeFlowMessage(t, e, "card:resume-revoked-prev"); cmd != nil {
		t.Fatalf("navigation from a previously authorized page produced a receipt: %+v", cmd)
	}
	st := e.Store.snapshot()
	currentKey := sessionKey(first.Source, "nas", history[0].Target)
	if st.Current[currentKey] != history[0].ID || st.Commands[key(second.Source, second.Source.ID)].Card.Used {
		t.Fatal("revoked route changed the selection or consumed the old page")
	}
	if err := e.Store.update(func(st *State) error { st.AccessPolicy.Routes = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	e.publishPolicy()
	choose.EventID = "resume-authorized"
	clickCommandCard(t, l, choose)
	consumeResumeFlowMessage(t, e, "card:resume-authorized")
	st = e.Store.snapshot()
	if st.Current[currentKey] != history[8].ID || !st.Commands[key(second.Source, second.Source.ID)].Card.Used {
		t.Fatal("the second-page choice did not select the ninth session and consume the card")
	}

	for i, prefix := range []string{"/resume ", "/resume use "} {
		resumeFlowCommand(t, e, fmt.Sprintf("select-first-%d", i), "/resume "+shortTaskID(history[0].ID))
		if got := e.Store.snapshot().Current[currentKey]; got != history[0].ID {
			t.Fatalf("switching back to the first session selected %q, want %q", got, history[0].ID)
		}
		resumeFlowCommand(t, e, fmt.Sprintf("select-ninth-%d", i), prefix+ninth)
		if got := e.Store.snapshot().Current[currentKey]; got != history[8].ID {
			t.Fatalf("%s selected %q, want ninth session %q", prefix+ninth, got, history[8].ID)
		}
	}
	if x.opens != 0 || x.sends != 0 || x.stops != 0 {
		t.Fatal("paging or choosing a completed session invoked the executor")
	}
}

func TestSessionResumeFlowStopAndSwitch(t *testing.T) {
	for _, mode := range []string{"command", "card", "stale card"} {
		t.Run(mode, func(t *testing.T) {
			e, x, replies, l, history := resumeFlowFixture(t, 2, true)
			original, chosen := history[0], history[1]
			token := shortTaskID(chosen.ID)
			oldInput, chosenInput := original.Source, chosen.Source
			oldInput.ID, oldInput.SessionTask = "queued-original", original.ID
			chosenInput.ID, chosenInput.SessionTask = "queued-chosen", chosen.ID
			accept(t, e, oldInput)
			accept(t, e, chosenInput)

			var receipt, card *SessionCommand
			if mode == "command" {
				receipt = resumeFlowCommand(t, e, "resume-stop", "/resume --stop "+token)
			} else {
				card = resumeFlowCommand(t, e, "busy-page", "/resume")
				action := commandCardClick(t, card, replies.replies[len(replies.replies)-1], "停止本轮并切换到 2 · "+token, "resume-stop-card")
				clickCommandCard(t, l, action)
				if mode == "stale card" {
					// The same task and thread now represent a different round.
					original.RequestID, original.RunID = "later-request", "later-run"
					if err := e.Store.update(func(st *State) error {
						st.Tasks[original.ID].RequestID, st.Tasks[original.ID].RunID = original.RequestID, original.RunID
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				}
				receipt = consumeResumeFlowMessage(t, e, "card:resume-stop-card")
			}

			st := e.Store.snapshot()
			stopped := mode != "stale card"
			wantCurrent := original.ID
			if stopped {
				wantCurrent = chosen.ID
			}
			if got := st.Current[sessionKey(original.Source, "nas", original.Target)]; got != wantCurrent {
				t.Fatalf("current session = %q, want %q", got, wantCurrent)
			}
			if receipt == nil || strings.Contains(receipt.Reply, "已请求停止原轮次") != stopped {
				t.Fatalf("unexpected stop reply: %+v", receipt)
			}
			if card != nil && st.Commands[key(card.Source, card.Source.ID)].Card.Used != stopped {
				t.Fatal("card consumption did not match whether the switch succeeded")
			}
			original.StopRequested = stopped
			if !reflect.DeepEqual(st.Tasks[original.ID], &original) || !reflect.DeepEqual(st.Tasks[chosen.ID], &chosen) {
				t.Fatal("the command changed task state beyond stop intent for the original round")
			}
			if q, ok := queuedPresent(&st, oldInput); !ok || q.Cancelled != stopped {
				t.Fatalf("original queue present=%t, cancelled=%t, want cancellation=%t", ok, q.Cancelled, stopped)
			}
			if q, ok := queuedPresent(&st, chosenInput); !ok || q.Cancelled {
				t.Fatalf("selected session queue was affected: present=%t, cancelled=%t", ok, q.Cancelled)
			}
			if x.opens != 0 || x.sends != 0 || x.stops != 0 {
				t.Fatal("resume command invoked the executor before the worker advanced tasks")
			}
		})
	}
}

func resumeFlowFixture(t *testing.T, count int, running bool) (*Engine, *fakeExecutor, *fakeMessenger, *Lark, []Task) {
	t.Helper()
	e, x, replies, _ := testEngine(t)
	e.Config.DefaultRoute = "nas"
	e.Config.access = &accessRuntime{}
	e.Lark = commandCardRecorder{replies}
	l := &Lark{Config: e.Config, Store: e.Store}
	target := e.Config.Routes["nas"]
	history := make([]Task, count)
	if err := e.Store.update(func(st *State) error {
		for i := range history {
			id := fmt.Sprintf("%08d-task", i+1)
			source := message("source-"+id, "会话 "+id)
			source.ChatType = "p2p"
			task := Task{ID: id, Source: source, RouteID: "nas", Target: target, ReceiverAppID: e.Config.AppID,
				Phase: "completed", ThreadID: "thread-" + id, RequestID: "request-" + id, RunID: "run-" + id,
				Updated: fmt.Sprintf("2026-01-%02dT00:00:00Z", count-i)}
			if i == 0 && running {
				task.Phase = "running"
			}
			st.Tasks[id] = &task
			history[i] = task
		}
		setCurrent(e.Config, st, history[0].Source, st.Tasks[history[0].ID])
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return e, x, replies, l, history
}

func resumeFlowCommand(t *testing.T, e *Engine, id, text string) *SessionCommand {
	t.Helper()
	m := message(id, text)
	m.ChatType = "p2p"
	accept(t, e, m)
	cmd := consumeResumeFlowMessage(t, e, id)
	if cmd == nil {
		t.Fatalf("no receipt for %q", text)
	}
	return cmd
}

func consumeResumeFlowMessage(t *testing.T, e *Engine, id string) *SessionCommand {
	t.Helper()
	st := e.Store.snapshot()
	m, ok := queuedPresent(&st, message(id, ""))
	if !ok {
		t.Fatalf("message %q was not queued", id)
	}
	// Inspect the command's durable effects before the worker can send Stop or drain the queue.
	if err := e.consume(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	return e.Store.snapshot().Commands[key(m, id)]
}
