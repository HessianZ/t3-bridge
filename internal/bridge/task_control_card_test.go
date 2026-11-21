package bridge

import (
	"fmt"
	"maps"
	"strings"
	"testing"
)

func TestManagedTaskCardsKeepFilterOnPagination(t *testing.T) {
	e, _, messenger := stage67StopFixture(t)
	e.Config.CommentRoute = "mac"
	e.Lark = commandCardRecorder{messenger.fakeMessenger}
	lark := &Lark{Config: e.Config, Store: e.Store}
	var tasks []*Task
	for i := range 9 {
		task := stage67GroupTask(e, fmt.Sprintf("chat-row-%d", i))
		task.Updated = fmt.Sprintf("2026-11-19T00:00:%02dZ", 9-i)
		tasks = append(tasks, task)
	}
	last := tasks[len(tasks)-1]
	otherTopic := stage67GroupTask(e, "other-topic")
	otherTopic.Source.TopicID = "omt_other_topic"
	comment := stage66Task(e, "comment", true)
	stage67SaveTasks(t, e, append(tasks, otherTopic, comment)...)

	status := runSessionStatusCommand(t, e, stage67AdminMessage("manage-status", "/status"))
	doc := clickManagedTaskCard(t, e, lark, status, "评论任务", "open-comments")
	if doc.Card.TaskList == nil || len(doc.Card.TaskList.Rows) != 1 || !strings.Contains(doc.Reply, "comment:"+docTaskToken(comment)) ||
		!strings.Contains(commandCardJSON(doc), "/timeout comment:") {
		t.Fatalf("comment navigation: %+v", doc)
	}
	back := clickManagedTaskCard(t, e, lark, doc, "返回状态", "manage-status-return")
	chat := clickManagedTaskCard(t, e, lark, back, "聊天任务", "open-chat")
	if chat.Card.TaskList == nil || !strings.HasPrefix(chat.Reply, "聊天任务 1/2") || back.RouteID != "mac" {
		t.Fatalf("chat navigation or status route: %+v", chat)
	}
	filter := "/status tasks " + last.Source.ChatID + ":" + last.Source.TopicID
	first := runSessionStatusCommand(t, e, stage67AdminMessage("filtered-page", filter))
	second := clickManagedTaskCard(t, e, lark, first, "下一页", "filtered-next")
	if second.Card.TaskList == nil || len(second.Card.TaskList.Rows) != 1 || !strings.HasPrefix(second.Reply, "聊天任务 2/2") ||
		!strings.Contains(second.Reply, chatTaskToken(last)) || strings.Contains(second.Reply, chatTaskToken(otherTopic)) {
		t.Fatalf("page navigation lost filter: %s", second.Reply)
	}
	if prev := clickManagedTaskCard(t, e, lark, second, "上一页", "filtered-prev"); prev.Reply != first.Reply {
		t.Fatalf("previous page changed the query: %s", prev.Reply)
	}
	body := commandCardJSON(second)
	for _, hidden := range []string{last.Source.UserID, last.Source.ChatID, last.Source.TopicID, last.RequestID, last.Source.Text, last.RunID} {
		if strings.Contains(body, hidden) {
			t.Errorf("card disclosed %q", hidden)
		}
	}
}

func TestManagedTaskCardsStopOnlyConfirmedOriginalRound(t *testing.T) {
	for _, kind := range []string{chatTasksCommand, docTasksCommand} {
		t.Run(kind, func(t *testing.T) {
			e, x, messenger := stage67StopFixture(t)
			e.Config.CommentRoute = "mac"
			e.Lark = commandCardRecorder{messenger.fakeMessenger}
			lark := &Lark{Config: e.Config, Store: e.Store}
			target := stage67GroupTask(e, "selected")
			tokenFor := chatTaskToken
			if kind == docTasksCommand {
				target = stage66Task(e, "selected", true)
				tokenFor = docTaskToken
			}
			sibling := stage67GroupTask(e, "other")
			sibling.Source.TopicID = "omt_other_topic"
			stage67SaveTasks(t, e, target, sibling)

			card := runSessionStatusCommand(t, e, stage67AdminMessage("old-card", kind))
			action := commandCardClick(t, card, commandCardJSON(card), "停止 "+tokenFor(target), "stale-stop")
			foreign := action
			foreign.Operator.OpenID = "ou_stage67_owner"
			clickCommandCard(t, lark, foreign)
			if len(e.Store.snapshot().Inbox) != 0 {
				t.Fatal("another administrator could use the owner's card")
			}
			clickCommandCard(t, lark, action)
			stage66Save(t, e, func(st *State) { st.Tasks[target.ID].RequestID = "new-request" })
			stale := consumeManagedTaskAction(t, e, action.EventID)
			if stale == nil || !strings.Contains(stale.Reply, "轮次已变化") || e.Store.task(target.ID).StopRequested {
				t.Fatalf("stale action stopped the new round: %+v", stale)
			}

			fresh := clickManagedTaskCard(t, e, lark, stale, "返回任务列表", "fresh-list")
			task := e.Store.task(target.ID)
			body := commandCardJSON(fresh)
			if !strings.Contains(body, `"confirm":`) || !strings.Contains(body, "停止指定轮次") {
				t.Fatal("stop button lacks an explicit confirmation")
			}
			action = commandCardClick(t, fresh, body, "停止 "+tokenFor(task), "revoked-stop")
			clickCommandCard(t, lark, action)
			stage66Save(t, e, func(st *State) { st.AccessPolicy.Admins = map[string]bool{"user": false} })
			e.publishPolicy()
			if receipt := consumeManagedTaskAction(t, e, action.EventID); receipt != nil || e.Store.task(task.ID).StopRequested {
				t.Fatalf("dequeued click bypassed revoked access: %+v", receipt)
			}
			stage66Save(t, e, func(st *State) { st.AccessPolicy.Admins = nil })
			e.publishPolicy()

			before := e.Store.snapshot()
			action.EventID = "confirmed-stop"
			clickCommandCard(t, lark, action)
			result := consumeManagedTaskAction(t, e, action.EventID)
			after := e.Store.snapshot()
			if result == nil || !strings.Contains(result.Reply, "已请求停止") || after.Tasks[sibling.ID].StopRequested ||
				!after.Tasks[task.ID].StopRequested || after.Tasks[task.ID].RequestID != task.RequestID ||
				!after.Commands[key(fresh.Source, fresh.Source.ID)].Card.Used || x.stops != 0 {
				t.Fatalf("unexpected stop transaction: %+v", result)
			}
			scope := sessionKey(task.Source, task.RouteID, task.Target)
			if kind == docTasksCommand {
				before.Current[scope] = ""
			}
			if !maps.Equal(before.Current, after.Current) {
				t.Fatal("stop changed unrelated session mappings")
			}
			action.EventID = "duplicate-confirmation"
			clickCommandCard(t, lark, action)
			if len(e.Store.snapshot().Inbox) != 0 {
				t.Fatal("consumed card accepted a second stop")
			}

			stage66Save(t, e, func(st *State) { st.Settings.ReplyMode = "text" })
			plain := runSessionStatusCommand(t, e, stage67AdminMessage("plain-list", kind))
			if plain.Card != nil || !strings.Contains(plain.Reply, "/stop ") || messenger.replies[len(messenger.replies)-1] != plain.Reply {
				t.Fatalf("text mode lost token instructions: %+v", plain)
			}
		})
	}
}

func clickManagedTaskCard(t *testing.T, e *Engine, lark *Lark, command *SessionCommand, label, event string) *SessionCommand {
	t.Helper()
	clickCommandCard(t, lark, commandCardClick(t, command, commandCardJSON(command), label, event))
	result := consumeManagedTaskAction(t, e, event)
	if result == nil {
		t.Fatalf("missing receipt for %q", label)
	}
	return result
}

func consumeManagedTaskAction(t *testing.T, e *Engine, event string) *SessionCommand {
	t.Helper()
	message := stage67AdminMessage("card:"+event, "")
	st := e.Store.snapshot()
	queued, ok := queuedPresent(&st, message)
	if !ok {
		t.Fatalf("callback %s was not queued", event)
	}
	if err := e.consume(t.Context(), queued); err != nil {
		t.Fatal(err)
	}
	return e.Store.snapshot().Commands[key(message, message.ID)]
}
