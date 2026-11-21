package bridge

import (
	"context"
	"testing"
	"time"

	channel "github.com/larksuite/channel-sdk-go"
)

func TestTaskCopyDoesNotAliasStoreOrSiblingHistory(t *testing.T) {
	e, _, _, _ := testEngine(t)
	accept(t, e, message("one", "/nas input"))
	tick(t, e)
	task := onlyTask(t, e)
	copy := e.Store.task(task.ID)
	copy.Source.ProjectChoices["extra"] = Workspace{ProjectID: "outside"}
	copy.Target.Options["effort"] = "changed"
	copy.Summary = "changed"
	latest := e.Store.task(task.ID)
	if latest.Summary == "changed" || latest.Target.Options["effort"] == "changed" || latest.Source.ProjectChoices["extra"].ProjectID != "" {
		t.Fatal("per-task copy aliased persistent state")
	}
	if e.Store.task("unknown") != nil {
		t.Fatal("unknown task acquired identity")
	}
}
func TestTaskLoopRetainsConcurrentStopBeforeNotify(t *testing.T) {
	e, x, l, _ := testEngine(t)
	accept(t, e, message("one", "/nas input"))
	tick(t, e)
	task := onlyTask(t, e)
	notifiedStop := false
	l.onNotify = func(t *Task) { notifiedStop = t.StopRequested }
	x.observation.Summary = "visible update"
	x.onObserve = func() {
		x.onObserve = nil
		done := make(chan error, 1)
		go func() { m := message("stop", "/stop"); m.ParentID = task.CardID; done <- e.Store.Accept(e.Config, m) }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("task read held lock through IO")
		}
	}
	tick(t, e)
	if !e.Store.task(task.ID).StopRequested || !notifiedStop {
		t.Fatal("loop notified stale task or overwrote concurrent stop")
	}
	tick(t, e)
	if x.stops != 1 || x.sends != 1 {
		t.Fatal("concurrent stop not applied once")
	}
}
func TestTaskLoopRetainsConcurrentFormSubmissionBeforeNotify(t *testing.T) {
	e, _, ch, x, _ := setupAccess(t)
	receiveAccess(t, ch, groupMessage("one", "user", "chat", "/nas input", true))
	tick(t, e)
	roleCommand(t, e, ch, "input", "user", "/input", "")
	task := onlyTask(t, e)
	x.onObserve = func() {
		x.onObserve = nil
		done := make(chan error, 1)
		go func() {
			done <- ch.action(context.Background(), &channel.CardActionEvent{EventID: "during-observe", MessageID: task.CardID, ChatID: "chat", Operator: channel.CardActionOperator{OpenID: "user"}, Action: channel.CardActionPayload{Tag: "button", Name: stable(task.ID, task.RequestID, "form-submit"), FormValue: map[string]any{"reply_text": "用户可见补充"}}})
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("form acceptance blocked by read lock")
		}
	}
	tick(t, e)
	latest := e.Store.task(task.ID)
	if latest.InputOpen || latest.FormEcho != "用户可见补充" || len(e.Store.snapshot().Inbox) != 1 || x.sends != 1 {
		t.Fatal("task commit lost concurrent form/round state")
	}
}
