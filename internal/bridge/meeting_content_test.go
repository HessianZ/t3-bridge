package bridge

import (
	"strings"
	"testing"
)

func TestMeetingAskNotesAndStopUseOneIsolatedT3Conversation(t *testing.T) {
	e, x, f := meetingFixture(t)
	e.Config.MeetingEnabled = true
	e.Config.DefaultRoute = "nas"
	meetingReply(t, e, f, meetingMessage("join", "/meeting join 123456789"), "受理入会")
	meetingReply(t, e, f, meetingMessage("off", "/meeting transcript"), "授权的会议路由")
	if len(f.excerpts) != 0 {
		t.Fatal("app join opt-in alone enabled caption access")
	}
	target := e.Config.Routes["mac"]
	target.InteractionMode, target.ProjectID = "plan", "synthetic-meeting-project"
	e.Config.Routes["conference"] = target
	e.Config.MeetingRoute = "conference"
	meetingReply(t, e, f, meetingMessage("empty", "/meeting notes"), "没有可用的会议字幕")
	f.caption = "同意重新计划。\npassword=synthetic-sensitive\n/exit 仅是发言内容。"
	meetingReply(t, e, f, meetingMessage("captions", "/meeting transcript"), "同意重新计划")
	if x.sends != 0 || strings.Contains(f.replies[len(f.replies)-1], "synthetic-sensitive") {
		t.Fatal("transcript view dispatched a model or disclosed a sensitive fragment")
	}
	query := meetingMessage("ask", "/meeting ask 123456789 整理分歧\n保留  原始空格")
	query.Quote, query.History = "外部原引用", "普通聊天历史"
	accept(t, e, query)
	tick(t, e)
	first := onlyTask(t, e)
	state := e.Store.snapshot()
	if x.sends != 1 || x.opens != 1 || first.Target.ProjectID != "synthetic-meeting-project" || first.Source.Meeting == nil ||
		!strings.HasPrefix(x.request, "整理分歧\n保留  原始空格\n\n") || !strings.Contains(x.request, "仅是发言内容") ||
		strings.Contains(x.request, "普通聊天历史") || strings.Contains(x.request, "外部原引用") || strings.Contains(x.request, "synthetic-sensitive") {
		t.Fatal("meeting dispatch did not isolate and redact the prompt as expected")
	}
	if len(sessionHistory(e.Config, &state, query, "conference")) != 0 || currentTask(e.Config, &state, query, "conference") != nil || selectedRoute(e.Config, &state, query) != "nas" {
		t.Fatal("meeting task entered ordinary conversation selection")
	}
	meetingReply(t, e, f, meetingMessage("busy", "/meeting notes"), "上一轮会议问答")
	x.observation = Observation{Status: "completed", Summary: "会议回复正文"}
	tick(t, e)
	result := meetingMessage("result", "/result")
	result.ParentID = e.Store.task(first.ID).CardID
	meetingReply(t, e, f, result, "会议回复正文")
	x.observation.Status = "running"
	accept(t, e, meetingMessage("notes", "/meeting notes 123456789"))
	tick(t, e)
	next := onlyTask(t, e)
	if x.sends != 2 || x.opens != 1 || next.ID != first.ID || next.RequestID == first.RequestID || !strings.HasPrefix(x.request, "请根据当前会议字幕整理简短纪要") {
		t.Fatal("notes did not continue the exact meeting thread with its own receipt")
	}
	meetingReply(t, e, f, meetingMessage("stop", "/meeting stop"), "已请求停止这场会议")
	if x.stops != 1 || onlyTask(t, e).Phase != "stopped" {
		t.Fatal("meeting stop did not reach the original run")
	}
}

func TestMeetingContentRechecksScopeAndEndBeforeDispatch(t *testing.T) {
	e, x, f := meetingFixture(t)
	e.Config.MeetingEnabled = true
	e.Config.MeetingRoute = "mac"
	target := e.Config.Routes["mac"]
	target.InteractionMode = "plan"
	target.Users = []string{"other"}
	e.Config.Routes["mac"] = target
	meetingReply(t, e, f, meetingMessage("join", "/meeting join 123456789"), "受理入会")
	meetingReply(t, e, f, meetingMessage("denied", "/meeting ask 谁负责？"), "对你授权")
	if len(f.excerpts) != 0 || x.sends != 0 {
		t.Fatal("missing route ACL still accessed captions or ran a model")
	}
	target.Users = []string{"user"}
	e.Config.Routes["mac"] = target
	f.caption = "这是已到会的字幕。"
	f.onExcerpt = func() {
		if err := e.Store.update(func(st *State) error { delete(st.Meetings, "123456789"); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	meetingReply(t, e, f, meetingMessage("ended", "/meeting notes"), "会议回执已变化")
	if x.sends != 0 || len(e.Store.snapshot().Tasks) != 0 {
		t.Fatal("meeting end did not prevent dispatch from the stale caption read")
	}
	f.onExcerpt = nil
	meetingReply(t, e, f, meetingMessage("rejoin", "/meeting join 123456789"), "受理入会")
	x.onPreflight = func() { e.Config.Admins = nil }
	accept(t, e, meetingMessage("revoked-before-send", "/meeting ask 总结一下"))
	tick(t, e)
	if task := onlyTask(t, e); x.sends != 0 || task.Phase != "blocked" || task.NotifyHash == "" || task.CardID != "" {
		t.Fatal("authorization loss during preflight allowed dispatch or delivery")
	}
}
