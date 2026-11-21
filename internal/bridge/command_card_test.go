package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	channel "github.com/larksuite/channel-sdk-go"
)

type commandCardRecorder struct{ *fakeMessenger }

func (r commandCardRecorder) ReplyCommandCard(_ context.Context, m Message, body string, _ bool) (string, error) {
	r.replies = append(r.replies, body)
	return "reply-" + m.ID, nil
}

func commandCardClick(t *testing.T, cmd *SessionCommand, body, label, event string) channel.CardActionEvent {
	t.Helper()
	for _, el := range card2Elements(t, body) {
		b := el.(map[string]any)
		if b["tag"] == "button" && b["text"].(map[string]any)["content"] == label {
			v := b["behaviors"].([]any)[0].(map[string]any)["value"].(map[string]any)
			return channel.CardActionEvent{EventID: event, ChatID: cmd.Source.ChatID, MessageID: cmd.Card.ReplyID,
				Operator: channel.CardActionOperator{OpenID: cmd.Source.UserID}, Action: channel.CardActionPayload{Tag: "button", Value: v}}
		}
	}
	t.Fatalf("missing button %q in %s", label, body)
	return channel.CardActionEvent{}
}

func clickCommandCard(t *testing.T, l *Lark, a channel.CardActionEvent) {
	t.Helper()
	if err := l.handleAction(context.Background(), &a); err != nil {
		t.Fatal(err)
	}
}

func TestCommandCardStatusThreadLinkUsesScopedTaskOnRefresh(t *testing.T) {
	e, x, replies, _ := testEngine(t)
	e.Config.DefaultRoute = "nas"
	e.Lark = commandCardRecorder{replies}
	l := &Lark{Config: e.Config, Store: e.Store}
	const url = "https://t3.example/threads/nas-env/thread"
	const mismatchedURL = "https://t3.example/threads/mac-env/thread"
	accept(t, e, message("thread-task", "/nas 核对"))
	tick(t, e)
	taskID := onlyTask(t, e).ID
	setTaskLink := func(link string) {
		t.Helper()
		if err := e.Store.update(func(st *State) error {
			st.Tasks[taskID].Link = link
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	setTaskLink(url)
	status := message("link-status", "/status")
	cmd := runSessionStatusCommand(t, e, status)
	body := replies.replies[len(replies.replies)-1]
	assertThreadButton(t, body, url)
	if !strings.Contains(cmd.Reply, "会话："+url) || cmd.Card.Buttons[0].Command != "/stop" {
		t.Fatalf("status lost the session reference or stop index: %#v", cmd)
	}
	stop := commandCardClick(t, cmd, body, "停止本轮任务", "stop-link-status")
	if stop.Action.Value["id"] != commandButtonID(cmd, 0) {
		t.Fatalf("stop callback changed index: %#v", stop.Action.Value)
	}

	foreign := message("foreign-link-status", "/status")
	foreign.UserID, foreign.ParentID = "other", "thread-task"
	foreignReply := runSessionStatusCommand(t, e, foreign)
	if foreignReply.Card != nil || strings.Contains(foreignReply.Reply, url) {
		t.Fatalf("status exposed another user's thread: %#v", foreignReply)
	}

	setTaskLink(mismatchedURL)
	clickCommandCard(t, l, commandCardClick(t, cmd, body, "刷新状态", "refresh-link-status"))
	tick(t, e)
	refreshed := replies.replies[len(replies.replies)-1]
	assertThreadButton(t, refreshed, "")
	if !strings.Contains(refreshed, "会话：链接不可用") || strings.Contains(refreshed, url) || strings.Contains(refreshed, mismatchedURL) || x.sends != 1 {
		t.Fatalf("refresh exposed an unvalidated thread or dispatched to T3: %s, sends=%d", refreshed, x.sends)
	}
}

func TestCommandCardNavigationAndTextFallback(t *testing.T) {
	e, x, _, _ := testEngine(t)
	e.Config.DefaultRoute = "nas"
	var failures []CommandFailureStep
	e.OnCommandFailed = func(step CommandFailureStep) { failures = append(failures, step) }
	type reply struct {
		Content string
		Type    string `json:"msg_type"`
		UUID    string `json:"uuid"`
		Thread  bool   `json:"reply_in_thread"`
		Parent  string `json:"-"`
	}
	var replies []reply
	lost := true
	lostBeforeFallback, fallbackFails := true, true
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/tenant_access_token/") {
			io.WriteString(w, `{"code":0,"tenant_access_token":"synthetic-token","expire":7200}`)
			return w.Result(), nil
		}
		if !strings.HasSuffix(r.URL.Path, "/reply") {
			return nil, fmt.Errorf("unexpected request: %s", r.URL.Path)
		}
		var b reply
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Error(err)
		}
		b.Parent = strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/open-apis/im/v1/messages/"), "/reply")
		replies = append(replies, b)
		if b.Parent == "fallback-failed" && b.Type == "interactive" && lostBeforeFallback {
			lostBeforeFallback = false
			io.WriteString(w, `{"code":0}`)
		} else if (b.Parent == "rejected" || b.Parent == "resume-rejected" || b.Parent == "fallback-failed") && b.Type == "interactive" {
			io.WriteString(w, `{"code":999,"msg":"synthetic card rejection"}`)
		} else if b.Parent == "fallback-failed" && fallbackFails {
			io.WriteString(w, `{"code":999,"msg":"synthetic fallback rejection"}`)
		} else if b.Parent == "unconfirmed" && lost {
			lost = false
			io.WriteString(w, `{"code":0}`)
		} else {
			fmt.Fprintf(w, `{"code":0,"data":{"message_id":%q,"parent_id":%q,"chat_id":"chat","thread_id":"topic"}}`, "reply-"+b.Parent, b.Parent)
		}
		return w.Result(), nil
	})
	sdk, err := channel.New(e.Config.AppID, "synthetic-secret", channel.WithHTTPClient(&http.Client{Transport: transport}), channel.WithEnableTokenCache(false))
	if err != nil {
		t.Fatal(err)
	}
	ch := &accessChannel{Channel: sdk}
	l := newLark(ch, e.Config, e.Store)
	e.Config, e.Lark = l.Config, l
	accessCommand(t, e, ch, "help", "user", "chat", "/help")
	st := e.Store.snapshot()
	help := st.Commands[key(message("help", ""), "help")]
	if help.Card == nil || help.Card.ReplyID != "reply-help" || len(replies) != 1 || replies[0].Type != "interactive" {
		t.Fatal("help card was not recorded", help, replies)
	}
	clickCommandCard(t, l, commandCardClick(t, help, replies[0].Content, "查看状态", "status"))
	tick(t, e)
	st = e.Store.snapshot()
	status := st.Commands[key(message("card:status", ""), "card:status")]
	if status.Card == nil || status.Source.TopicID != "topic" || status.RouteID != "nas" || replies[1].Parent != help.Card.ReplyID || !replies[1].Thread {
		t.Fatal("navigation lost reply anchor, topic or route", status, replies[1])
	}
	clickCommandCard(t, l, commandCardClick(t, status, replies[1].Content, "工作区", "workspaces"))
	tick(t, e)
	if len(replies) != 3 || replies[2].Type != "interactive" || !strings.Contains(replies[2].Content, "本范围命名工作区") || x.sends != 0 {
		t.Fatal("workspaces navigation failed or dispatched to T3", replies)
	}
	accessCommand(t, e, ch, "rejected", "user", "chat", "/help")
	if len(replies) != 5 || replies[3].Type != "interactive" || replies[4].Type != "text" || replies[3].UUID == "" || replies[3].UUID != replies[4].UUID || !strings.Contains(replies[4].Content, "/stop") {
		t.Fatal("text fallback did not keep the command receipt", replies)
	}
	accessCommand(t, e, ch, "unconfirmed", "user", "chat", "/help")
	st = e.Store.snapshot()
	if pending := st.Commands[key(message("unconfirmed", ""), "unconfirmed")]; pending.Card.ReplyID != "" || len(st.Inbox) != 1 || len(replies) != 6 {
		t.Fatal("unconfirmed card was treated as delivered", pending)
	}
	tick(t, e)
	if len(replies) != 7 || replies[5] != replies[6] || replies[6].Type != "interactive" {
		t.Fatal("unconfirmed card retry changed its UUID or content", replies)
	}
	if err := e.Store.update(func(st *State) error { st.Settings.ReplyMode = "text"; return nil }); err != nil {
		t.Fatal(err)
	}
	accessCommand(t, e, ch, "plain", "user", "chat", "/status")
	if len(replies) != 8 || replies[7].Type != "text" || !strings.Contains(replies[7].Content, "状态") {
		t.Fatal("text setting did not apply to session cards", replies)
	}
	if err := e.Store.update(func(st *State) error { st.Settings.ReplyMode = ""; return nil }); err != nil {
		t.Fatal(err)
	}
	clickCommandCard(t, l, commandCardClick(t, help, replies[0].Content, "会话历史", "group-history"))
	tick(t, e)
	if len(replies) != 9 || replies[8].Type != "text" || !strings.Contains(replies[8].Content, "可恢复的会话") {
		t.Fatal("group history navigation did not return text", replies)
	}
	private := message("resume-rejected", "/resume")
	private.ChatType = "p2p"
	if err := e.Store.update(func(st *State) error {
		st.Tasks["history-task"] = &Task{ID: "history-task", RequestID: "history-request", ThreadID: "thread", Phase: "completed", NotifyHash: "done",
			ReceiverAppID: e.Config.AppID, RouteID: "nas", Target: e.Config.Routes["nas"], Source: private}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.sessionControl(context.Background(), private); err != nil {
		t.Fatal(err)
	}
	if len(replies) != 11 || replies[9].Type != "interactive" || replies[10].Type != "text" || replies[9].UUID != replies[10].UUID {
		t.Fatal("resume card rejection lost the short-token fallback", replies)
	}
	var fallback struct{ Text string }
	if err := json.Unmarshal([]byte(replies[10].Content), &fallback); err != nil ||
		!strings.Contains(fallback.Text, "history-") || !strings.Contains(fallback.Text, "/resume <短令牌>") {
		t.Fatal("resume text did not preserve the token and instructions", fallback, err)
	}
	accessCommand(t, e, ch, "fallback-failed", "user", "chat", "/help")
	tick(t, e) // The retried card is rejected and its first text fallback fails.
	tick(t, e) // The pending text reply fails again without adding a sample.
	fallbackFails = false
	tick(t, e)
	if len(e.Store.snapshot().Inbox) != 0 || !reflect.DeepEqual(failures, []CommandFailureStep{
		CommandFailureReply, CommandFailureReply, CommandFailureReply, CommandFailureReply, CommandFailureReplyFallback,
	}) {
		t.Fatalf("unexpected command failure samples: %v", failures)
	}
}

func TestCommandCardStopPinsRoundAndRechecksACL(t *testing.T) {
	e, x, replies, _ := testEngine(t)
	e.Config.DefaultRoute = "nas"
	e.Config.access = &accessRuntime{}
	e.Lark = commandCardRecorder{replies}
	l := &Lark{Config: e.Config, Store: e.Store}
	accept(t, e, message("one", "/nas 第一轮"))
	tick(t, e)
	oldBody := workspaceCommand(t, e, replies, "old-status", "/status")
	old := e.Store.snapshot().Commands[key(message("old-status", ""), "old-status")]
	x.observation.Status = "completed"
	tick(t, e)
	x.observation.Status = "running"
	accept(t, e, message("two", "/nas 第二轮"))
	tick(t, e)
	if current := onlyTask(t, e); current.ID != old.Card.Buttons[0].TaskID || current.RequestID == old.Card.Buttons[0].RequestID {
		t.Fatal("fixture did not reuse a task for a new request")
	}
	clickCommandCard(t, l, commandCardClick(t, old, oldBody, "停止本轮任务", "late"))
	tick(t, e)
	if x.stops != 0 || onlyTask(t, e).StopRequested {
		t.Fatal("old status card stopped a newer round")
	}
	body := workspaceCommand(t, e, replies, "status", "/status")
	cmd := e.Store.snapshot().Commands[key(message("status", ""), "status")]
	a := commandCardClick(t, cmd, body, "停止本轮任务", "stop")
	for _, bad := range []struct {
		name string
		edit func(*channel.CardActionEvent)
	}{
		{"user", func(a *channel.CardActionEvent) { a.Operator.OpenID = "other" }},
		{"chat", func(a *channel.CardActionEvent) { a.ChatID = "elsewhere" }},
		{"wrong card", func(a *channel.CardActionEvent) { a.MessageID = old.Card.ReplyID }},
		{"text", func(a *channel.CardActionEvent) { a.Action.FormValue = map[string]any{"text": "/new"} }},
	} {
		b := a
		bad.edit(&b)
		clickCommandCard(t, l, b)
		if len(e.Store.snapshot().Inbox) != 0 {
			t.Fatal("accepted invalid callback", bad.name)
		}
	}
	otherApp := *l
	otherApp.Config.AppID = "different-app"
	clickCommandCard(t, &otherApp, a)
	if len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("another application accepted the card")
	}
	clickCommandCard(t, l, a)
	if err := e.Store.update(func(st *State) error {
		st.AccessPolicy.Routes = map[string]map[string]bool{"nas": {"user": false}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.publishPolicy()
	tick(t, e)
	if x.stops != 0 || onlyTask(t, e).StopRequested {
		t.Fatal("queued callback bypassed revoked route")
	}
	if err := e.Store.update(func(st *State) error { st.AccessPolicy.Routes = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	e.publishPolicy()
	a.EventID = "authorized"
	clickCommandCard(t, l, a)
	clickCommandCard(t, l, a)
	a.EventID = "another-press"
	clickCommandCard(t, l, a)
	tick(t, e)
	if x.stops != 1 || onlyTask(t, e).Phase != "stopped" || len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("authorized stop was lost or repeated", x.stops, onlyTask(t, e).Phase)
	}
}

func TestCommandCardWorkspaceSelectionAfterRestart(t *testing.T) {
	e, _, replies, _, creates := workspaceFixture(t)
	e.Config.DefaultRoute = "nas"
	e.Lark = commandCardRecorder{replies}
	l := &Lark{Config: e.Config, Store: e.Store}
	workspaceCommand(t, e, replies, "alpha", "/cd /srv/alpha")
	workspaceCommand(t, e, replies, "save", "/ws save 工作 一")
	workspaceCommand(t, e, replies, "beta", "/cd /srv/beta")
	body := workspaceCommand(t, e, replies, "list", "/ws list")
	scope := message("list", "")
	cmd := e.Store.snapshot().Commands[key(scope, scope.ID)]
	a := commandCardClick(t, cmd, body, "切换：工作 一", "select")
	if err := e.Store.update(func(st *State) error {
		st.Commands[key(scope, scope.ID)].ReplyAt = time.Now().Add(-time.Hour).Format(time.RFC3339Nano)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	clickCommandCard(t, l, a)
	if len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("expired workspace card callback was accepted")
	}
	scope.ID = "fresh-list"
	body = workspaceCommand(t, e, replies, scope.ID, "/ws list")
	cmd = e.Store.snapshot().Commands[key(scope, scope.ID)]
	a = commandCardClick(t, cmd, body, "切换：工作 一", "select")
	clickCommandCard(t, l, a)
	// Remove the selected alias before the worker consumes the click.
	aliasKey := workspaceKey(e.Config, scope, "nas")
	if err := e.Store.update(func(st *State) error {
		delete(st.WorkspaceNames[aliasKey], "工作 一")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	tick(t, e)
	st := e.Store.snapshot()
	if effectiveTarget(e.Config, &st, scope, "nas").ProjectID != "beta" {
		t.Fatal("callback used an alias removed after the card was rendered")
	}
	if err := e.Store.update(func(st *State) error {
		st.WorkspaceNames[aliasKey]["工作 一"] = Workspace{"alpha", "同名项目", "/srv/alpha"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a.EventID = "accepted-after-restart"
	clickCommandCard(t, l, a)
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e.Store, l.Store = s, s
	tick(t, e)
	st = e.Store.snapshot()
	if effectiveTarget(e.Config, &st, scope, "nas").ProjectID != "alpha" || !st.Commands[key(scope, scope.ID)].Card.Used || *creates != 0 {
		t.Fatal("durable callback did not select the saved project exactly once")
	}
	workspaceCommand(t, e, replies, "back", "/cd /srv/beta")
	a.EventID = "repeat"
	clickCommandCard(t, l, a)
	if len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("consumed workspace card was usable after returning to its original workspace")
	}
}

func TestCommandCardWorkspaceStopPinsCurrentRound(t *testing.T) {
	e, x, replies, _, _ := workspaceFixture(t)
	e.Config.DefaultRoute = "nas"
	e.Lark = commandCardRecorder{replies}
	l := &Lark{Config: e.Config, Store: e.Store}
	workspaceCommand(t, e, replies, "alpha", "/cd /srv/alpha")
	workspaceCommand(t, e, replies, "save", "/ws save 工作一")
	workspaceCommand(t, e, replies, "beta", "/cd /srv/beta")
	action := func(card, label, event string) channel.CardActionEvent {
		body := workspaceCommand(t, e, replies, card, "/ws list")
		cmd := e.Store.snapshot().Commands[key(message(card, ""), card)]
		return commandCardClick(t, cmd, body, label, event)
	}
	plain := action("idle-list", "切换：工作一", "idle-click")
	accept(t, e, message("first", "/nas 第一轮"))
	tick(t, e)
	clickCommandCard(t, l, plain)
	tick(t, e)
	if x.stops != 0 || onlyTask(t, e).Target.ProjectID != "beta" {
		t.Fatal("ordinary workspace button silently stopped a run")
	}

	stale := action("first-list", "停止本轮并切换：工作一", "stale-click")
	first := onlyTask(t, e)
	x.observation.Status = "completed"
	tick(t, e)
	x.observation.Status = "running"
	accept(t, e, message("next", "/nas 下一轮"))
	tick(t, e)
	next := onlyTask(t, e)
	if first.ID != next.ID || first.RequestID == next.RequestID {
		t.Fatal("fixture did not start a later round on the same thread")
	}
	clickCommandCard(t, l, stale)
	tick(t, e)
	if x.stops != 0 || onlyTask(t, e).StopRequested {
		t.Fatal("old workspace button stopped a later round")
	}

	active := action("next-list", "停止本轮并切换：工作一", "active-click")
	clickCommandCard(t, l, active)
	tick(t, e)
	st := e.Store.snapshot()
	if x.stops != 1 || st.Tasks[next.ID].Phase != "stopped" || st.Tasks[next.ID].RequestID != next.RequestID ||
		effectiveTarget(e.Config, &st, message("current", ""), "nas").ProjectID != "alpha" {
		t.Fatal("current workspace card did not stop and switch the expected round")
	}
}

func TestCommandCardResumeSelectsOnlyMappingAfterRestart(t *testing.T) {
	e, x, replies, _ := testEngine(t)
	e.Config.DefaultRoute = "nas"
	e.Lark = commandCardRecorder{replies}
	l := &Lark{Config: e.Config, Store: e.Store}
	send := func(id, text string) Message {
		m := message(id, text)
		m.ChatType = "p2p"
		accept(t, e, m)
		tick(t, e)
		return m
	}
	x.observation.Status = "completed"
	send("first", "第一条会话")
	tick(t, e)
	first := onlyTask(t, e)
	send("new", "/new")
	send("second", "第二条会话")
	tick(t, e)
	helpSource := send("private-help", "/help")
	help := e.Store.snapshot().Commands[key(helpSource, helpSource.ID)]
	clickCommandCard(t, l, commandCardClick(t, help, replies.replies[len(replies.replies)-1], "会话历史", "history"))
	tick(t, e)
	before := e.Store.snapshot()
	historyKey := key(helpSource, "card:history")
	history := before.Commands[historyKey]
	if history == nil || history.Card == nil || history.Source.ParentID != help.Card.ReplyID || history.Source.TopicID != helpSource.TopicID {
		t.Fatal("help history lost its private reply scope", history)
	}
	var choose CommandButton
	count := 0
	for _, button := range history.Card.Buttons {
		if button.Command == "/resume" && button.TaskID != "" {
			count++
			if button.TaskID == first.ID {
				choose = button
			}
		}
	}
	if count != 2 || choose.TaskID != first.ID || choose.RequestID != first.RequestID {
		t.Fatal("history did not bind two exact rounds", history.Card.Buttons)
	}
	a := commandCardClick(t, history, replies.replies[len(replies.replies)-1], choose.Label, "select-history")
	for _, edit := range []func(*channel.CardActionEvent){
		func(a *channel.CardActionEvent) { a.Operator.OpenID = "other" },
		func(a *channel.CardActionEvent) { a.ChatID = "other-chat" },
		func(a *channel.CardActionEvent) { a.MessageID = help.Card.ReplyID },
		func(a *channel.CardActionEvent) {
			a.Action.Value = map[string]any{"action": commandCardActionType, "id": strings.Repeat("0", 32)}
		},
		func(a *channel.CardActionEvent) { a.Action.FormValue = map[string]any{"taskId": choose.TaskID} },
	} {
		bad := a
		edit(&bad)
		clickCommandCard(t, l, bad)
	}
	otherApp := *l
	otherApp.Config.AppID = "other-app"
	clickCommandCard(t, &otherApp, a)
	if len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("tampered or cross-account callback was queued")
	}
	clickCommandCard(t, l, a)
	clickCommandCard(t, l, a)
	a.EventID = "select-history-again"
	clickCommandCard(t, l, a)
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e.Store, l.Store = s, s
	tick(t, e)
	after := e.Store.snapshot()
	scope := sessionKey(helpSource, first.RouteID, first.Target)
	if before.Current[scope] == first.ID || after.Current[scope] != first.ID || !after.Commands[historyKey].Card.Used || len(after.Inbox) != 0 ||
		after.Commands[key(helpSource, "card:select-history-again")] != nil || !strings.Contains(replies.replies[len(replies.replies)-1], "已切换") {
		t.Fatal("queued history choice did not switch exactly once after restart", before.Current, after.Current)
	}
	if !reflect.DeepEqual(before.Tasks, after.Tasks) || !reflect.DeepEqual(before.Workspaces, after.Workspaces) || !reflect.DeepEqual(before.Selected, after.Selected) || x.opens != 2 || x.sends != 2 || x.stops != 0 {
		t.Fatal("selecting changed more than the current mapping or invoked execution", x.opens, x.sends, x.stops)
	}
	a.EventID = "repeat-consumed-card"
	clickCommandCard(t, l, a)
	if len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("used history card accepted another action")
	}
	x.onSend = func(task *Task) {
		if task.ID != first.ID || task.ThreadID != first.ThreadID {
			t.Error("next input did not use the chosen session", task.ID, task.ThreadID)
		}
	}
	send("next", "继续")
	if x.opens != 2 || x.sends != 3 {
		t.Fatal("next input did not continue the chosen session", x.opens, x.sends)
	}
}

func TestCommandCardResumeRechecksQueuedChoice(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*State, Message, *Task)
	}{
		{"round", func(_ *State, _ Message, task *Task) { task.RequestID = "new-request" }},
		{"task identity", func(_ *State, _ Message, task *Task) { task.ID = "other-task" }},
		{"task receiver", func(_ *State, _ Message, task *Task) { task.ReceiverAppID = "other-app" }},
		{"task topic", func(_ *State, _ Message, task *Task) { task.Source.TopicID = "other-topic" }},
		{"callback topic", func(st *State, _ Message, _ *Task) { st.Inbox[0].TopicID = "other-topic" }},
		{"stopped", func(_ *State, _ Message, task *Task) { task.StopRequested = true }},
		{"failed", func(_ *State, _ Message, task *Task) { task.Phase = "failed" }},
		{"missing thread", func(_ *State, _ Message, task *Task) { task.ThreadID = "" }},
		{"route", func(st *State, m Message, _ *Task) { st.Selected[userScope(m)] = "mac" }},
		{"project", func(st *State, _ Message, _ *Task) {
			for key, project := range st.Workspaces {
				project.ProjectID = "new-project"
				st.Workspaces[key] = project
			}
		}},
		{"project path", func(st *State, _ Message, _ *Task) {
			for key, project := range st.Workspaces {
				project.Path = "/new/path"
				st.Workspaces[key] = project
			}
		}},
		{"revoked route", func(st *State, _ Message, _ *Task) {
			st.AccessPolicy.Routes = map[string]map[string]bool{"nas": {"user": false}}
		}},
		{"revoked user", func(st *State, _ Message, _ *Task) { st.AccessPolicy.Users = map[string]bool{"user": false} }},
		{"expired", func(st *State, m Message, _ *Task) {
			st.Commands[key(m, m.ID)].ReplyAt = time.Now().Add(-time.Hour).Format(time.RFC3339Nano)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, x, replies, _ := testEngine(t)
			e.Config.DefaultRoute = "nas"
			e.Config.Topics = nil
			e.Config.access = &accessRuntime{}
			e.Lark = commandCardRecorder{replies}
			l := &Lark{Config: e.Config, Store: e.Store}
			x.observation.Status = "completed"
			m := message("session", "待恢复会话")
			m.ChatType = "p2p"
			accept(t, e, m)
			tick(t, e)
			tick(t, e)
			task := onlyTask(t, e)
			m.ID, m.Text = "new", "/new"
			accept(t, e, m)
			tick(t, e)
			m.ID, m.Text = "history", "/resume"
			accept(t, e, m)
			tick(t, e)
			id := key(m, m.ID)
			origin := e.Store.snapshot().Commands[id]
			clickCommandCard(t, l, commandCardClick(t, origin, replies.replies[len(replies.replies)-1], "切换到 1 · "+shortTaskID(task.ID), "choose"))
			if len(e.Store.snapshot().Inbox) != 1 {
				t.Fatal("fixture did not queue the choice")
			}
			if err := e.Store.update(func(st *State) error { tc.change(st, m, st.Tasks[task.ID]); return nil }); err != nil {
				t.Fatal(err)
			}
			e.publishPolicy()
			before := e.Store.snapshot()
			if err := e.consume(context.Background(), before.Inbox[0]); err != nil {
				t.Fatal(err)
			}
			after := e.Store.snapshot()
			if !reflect.DeepEqual(before.Current, after.Current) || after.Commands[id].Card.Used || len(after.Inbox) != 0 || x.sends != 1 || x.stops != 0 {
				t.Fatal("invalid history click changed state or execution", after.Current)
			}
		})
	}
}
