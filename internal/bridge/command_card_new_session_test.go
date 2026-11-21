package bridge

import (
	"errors"
	"strings"
	"testing"
)

func TestNewSessionCardCommitsResetAndStopTogether(t *testing.T) {
	e, x, replies, l, history := resumeFlowFixture(t, 1, true)
	task := history[0]
	scope := sessionKey(task.Source, task.RouteID, task.Target)
	other := task.Source
	other.ID, other.TopicID, other.Text = "other-topic", "other", "另一个话题"
	e.Config.Topics = append(e.Config.Topics, other.TopicID)
	input := task.Source
	input.ID, input.Text = "queued-followup", "本轮补充"
	accept(t, e, input)
	accept(t, e, other)

	help := resumeFlowCommand(t, e, "reset-help", "/help")
	commandCardClick(t, help, replies.replies[len(replies.replies)-1], "新会话", "from-help")
	status := resumeFlowCommand(t, e, "reset-status", "/status")
	body := replies.replies[len(replies.replies)-1]
	if !strings.Contains(body, `"confirm":`) || !strings.Contains(body, "历史记录会保留") {
		t.Fatalf("new session lacks confirmation: %s", body)
	}
	click := commandCardClick(t, status, body, "新会话", "confirmed-reset")
	wrongUser := click
	wrongUser.Operator.OpenID = "other-user"
	clickCommandCard(t, l, wrongUser)
	if len(e.Store.snapshot().Inbox) != 2 {
		t.Fatal("foreign user queued a reset")
	}
	clickCommandCard(t, l, click)

	st := e.Store.snapshot()
	queued, _ := queuedPresent(&st, message("card:confirmed-reset", ""))
	writeErr := errors.New("synthetic state write failure")
	e.Store.writeState = func(string, []byte, bool) (bool, error) { return false, writeErr }
	if _, err := e.sessionControl(t.Context(), queued); !errors.Is(err, writeErr) {
		t.Fatalf("reset write: %v", err)
	}
	e.Store.writeState = nil
	st = e.Store.snapshot()
	if st.Current[scope] != task.ID || st.Tasks[task.ID].StopRequested || st.Commands[key(status.Source, status.Source.ID)].Card.Used {
		t.Fatal("failed reset changed the mapping, task or source card")
	}

	result := consumeResumeFlowMessage(t, e, queued.ID)
	st = e.Store.snapshot()
	cancelled, _ := queuedPresent(&st, input)
	unaffected, otherQueued := queuedPresent(&st, other)
	if result == nil || !strings.Contains(result.Reply, "已请求停止") || st.Current[scope] != "" ||
		!st.Tasks[task.ID].StopRequested || st.Tasks[task.ID].RunID != task.RunID ||
		!st.Commands[key(status.Source, status.Source.ID)].Card.Used || !cancelled.Cancelled || !otherQueued || unaffected.Cancelled ||
		x.stops != 0 {
		t.Fatal("reset did not commit the exact mapping, queue, task and receipt")
	}
	click.EventID = "replayed-reset"
	clickCommandCard(t, l, click)
	if len(e.Store.snapshot().Inbox) != 2 {
		t.Fatal("consumed card queued a second reset")
	}
	if status := resumeFlowCommand(t, e, "empty-status", "/status"); status.Card == nil || strings.Contains(replies.replies[len(replies.replies)-1], "新会话") {
		t.Fatal("empty mapping offered a reset")
	}
}

func TestNewSessionCardRejectsChangedSelection(t *testing.T) {
	for _, change := range []string{"round", "workspace"} {
		t.Run(change, func(t *testing.T) {
			e, _, replies, l, history := resumeFlowFixture(t, 1, false)
			task := history[0]
			scope := sessionKey(task.Source, task.RouteID, task.Target)
			card := resumeFlowCommand(t, e, "reset-stale", "/status")
			click := commandCardClick(t, card, replies.replies[len(replies.replies)-1], "新会话", "stale-reset")
			clickCommandCard(t, l, click)
			if err := e.Store.update(func(st *State) error {
				switch change {
				case "round":
					st.Tasks[task.ID].RequestID = "new-request"
				case "workspace":
					target := task.Target
					target.ProjectID, target.ProjectPath = "new-project", "/new/project"
					rememberWorkspace(e.Config, st, task.Source, task.RouteID, target)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			rejected := consumeResumeFlowMessage(t, e, "card:stale-reset")
			st := e.Store.snapshot()
			if rejected == nil || !strings.Contains(rejected.Reply, "已变化") || st.Current[scope] != task.ID ||
				st.Tasks[task.ID].StopRequested || st.Commands[key(card.Source, card.Source.ID)].Card.Used {
				t.Fatal("stale reset changed the mapping or task")
			}
		})
	}
}
