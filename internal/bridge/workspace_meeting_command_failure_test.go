package bridge

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestWorkspaceCommandFailureCountsAPIAndIdentityFailures(t *testing.T) {
	e, _, lark, _, creates := workspaceFixture(t)
	e.Config.DefaultRoute = "nas"
	failures := 0
	e.OnCommandFailed = func(step CommandFailureStep) {
		if step == CommandFailureWorkspace {
			failures++
		}
	}
	workspaceCommand(t, e, lark, "switch", "/cd /srv/alpha")
	workspaceCommand(t, e, lark, "save", "/ws save Alpha")
	e.Config.Admins = nil
	workspaceCommand(t, e, lark, "denied", "/cd /srv/unregistered")
	e.Config.Admins = []string{"user"}
	workspaceCommand(t, e, lark, "registration", "/cd /srv/missing")
	if reply := workspaceCommand(t, e, lark, "pending", "/cd /srv/missing"); !strings.Contains(reply, "此前登记结果仍未知") || failures != 1 || *creates != 1 {
		t.Fatalf("registration: reply=%q, workspace failures=%d, creates=%d", reply, failures, *creates)
	}

	api := e.T3.(*workspaceExecutor).api
	call := api.Calls.(scriptedCaller)
	api.Calls = scriptedCaller(func(name string, args map[string]any) (any, error) {
		switch name {
		case "t3_project_list":
			return nil, errors.New("synthetic list transport failure")
		case "t3_project_read":
			return Project{ID: "other", Root: "/srv/alpha"}, nil
		}
		return call(name, args)
	})
	for _, tc := range []struct {
		id, command, reply string
		wantFailures       int
	}{
		{"syntax", "/cd relative", "绝对路径", 1},
		{"missing-alias", "/ws use Missing", "没有这个工作区别名", 1},
		{"lookup", "/cd /srv/other", "无法完整读取", 2},
		{"mismatch", "/ws use Alpha", "原绑定保留", 3},
	} {
		if reply := workspaceCommand(t, e, lark, tc.id, tc.command); !strings.Contains(reply, tc.reply) || failures != tc.wantFailures {
			t.Fatalf("%s: reply=%q, workspace failures=%d, want %d", tc.id, reply, failures, tc.wantFailures)
		}
	}

	m := message("recover", "/cd /srv/other")
	if err := e.saveCommand(key(m, m.ID), &SessionCommand{
		Source: m, Kind: "/cd", RouteID: "nas", Target: e.Config.Routes["nas"],
		WorkspacePath: "/srv/other", ProjectAttempted: true,
	}); err != nil {
		t.Fatal(err)
	}
	if reply := workspaceCommand(t, e, lark, m.ID, m.Text); !strings.Contains(reply, "无法完整读取") || failures != 3 || *creates != 1 {
		t.Fatalf("recovery: reply=%q, workspace failures=%d, creates=%d", reply, failures, *creates)
	}
}

type failureMeetingMessenger struct {
	*fakeMeetingMessenger
	excerptErr error
}

func (f failureMeetingMessenger) meetingExcerpt(context.Context, string) (string, bool, error) {
	return "合成字幕", true, f.excerptErr
}

func TestMeetingCommandFailureCountsCallsWithoutRecountingPendingReceipt(t *testing.T) {
	e, _, f := meetingFixture(t)
	e.Config.MeetingEnabled = true
	e.Config.MeetingRoute = "nas"
	target := e.Config.Routes["nas"]
	target.InteractionMode = "plan"
	e.Config.Routes["nas"] = target
	failures := 0
	e.OnCommandFailed = func(step CommandFailureStep) {
		if step == CommandFailureMeeting {
			failures++
		}
	}
	f.joinErr = &meetingRejection{Reason: "synthetic permission denial"}
	meetingReply(t, e, f, meetingMessage("denied", "/meeting join 123456789"), "permission denial")
	f.joinErr = errMeetingUnconfirmed
	meetingReply(t, e, f, meetingMessage("failed", "/meeting join 123456789"), "入会结果待核对")
	meetingReply(t, e, f, meetingMessage("pending", "/meeting join 123456789"), "已有回执")
	if failures != 1 || len(f.joins) != 2 {
		t.Fatalf("meeting failures=%d, join calls=%d, want 1 and 2", failures, len(f.joins))
	}

	f.joinErr = nil
	meetingReply(t, e, f, meetingMessage("joined", "/meeting join 987654321"), "已受理")
	lark := &failureMeetingMessenger{fakeMeetingMessenger: f}
	e.Lark = lark
	meetingReply(t, e, f, meetingMessage("partial", "/meeting transcript 987654321"), "本次读取部分可用")
	if failures != 1 {
		t.Fatalf("meeting failures=%d after partial read, want 1", failures)
	}
	lark.excerptErr = errMeetingUnconfirmed
	meetingReply(t, e, f, meetingMessage("read-failed", "/meeting transcript 987654321"), "本次读取部分可用")
	if failures != 2 {
		t.Fatalf("meeting failures=%d, want 2", failures)
	}
}
