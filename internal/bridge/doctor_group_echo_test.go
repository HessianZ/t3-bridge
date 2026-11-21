package bridge

import (
	"context"
	"strings"
	"testing"
)

const (
	doctorGroupEchoAdmin  = "ou_synthetic_echo_admin"
	doctorGroupEchoOwner  = "ou_synthetic_echo_owner"
	doctorGroupEchoMember = "ou_synthetic_echo_member"
	doctorGroupEchoChat   = "oc_synthetic_echo_group"
)

func doctorGroupEchoFixture(t *testing.T) (*Engine, *fakeExecutor, *fakeMessenger, Message) {
	t.Helper()
	e, x, l := doctorFixture(t)
	e.Config.AppID = "cli_synthetic_echo_receiver"
	e.Config.Admins = []string{doctorGroupEchoAdmin}
	e.Config.Chats = []string{doctorGroupEchoChat}
	e.Config.access = &accessRuntime{owner: doctorGroupEchoOwner}
	target := e.Config.Routes["diagnostic"]
	target.Users = []string{doctorGroupEchoAdmin, doctorGroupEchoOwner, doctorGroupEchoMember}
	e.Config.Routes["diagnostic"] = target
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	m := Message{ID: "om_synthetic_group_echo", ChatType: "group", ChatID: doctorGroupEchoChat,
		TopicID: "omt_synthetic_echo_topic", UserID: doctorGroupEchoAdmin, Text: "/doctor --echo diagnostic"}
	return e, x, l, m
}

func TestDoctorGroupEchoAcknowledgesOnceAndResumesPrivateVerdict(t *testing.T) {
	for _, name := range []string{"admin in a topic with explicit route", "owner in group with default route"} {
		t.Run(name, func(t *testing.T) {
			e, x, l, m := doctorGroupEchoFixture(t)
			if name == "owner in group with default route" {
				m.TopicID, m.UserID, m.Text = "", doctorGroupEchoOwner, "/doctor --echo"
				e.Config.DefaultRoute = "diagnostic"
			}
			m.Quote, m.History = "synthetic quoted content", "synthetic topic history"
			m.Images = []Image{{Key: "synthetic image"}}
			var notices []Task
			l.onNotify = func(task *Task) { notices = append(notices, *task) }

			accept(t, e, m)
			accept(t, e, m)
			tick(t, e)
			probe := onlyTask(t, e)
			if probe.Source.InputKind != doctorEchoKind || probe.RouteID != "diagnostic" || !probe.TextReply ||
				probe.Process != nil || probe.CardID != "" || probe.RequestID == "" || probe.RunID == "" ||
				x.opens != 1 || x.sends != 1 || x.uploads != 0 || len(notices) != 0 {
				t.Fatalf("expected one isolated echo in progress: %+v", probe)
			}
			if x.request != doctorEchoPrompt+"\nBridge request: "+probe.RequestID {
				t.Fatal("echo request must contain only the fixed prompt and receipt ID")
			}
			if len(l.replies) != 1 || !strings.Contains(l.replies[0], "已登记模型回声") || !strings.Contains(l.replies[0], "私信") {
				t.Fatalf("expected one group acknowledgement before settlement: %q", l.replies)
			}

			reopenLifecycleStore(t, e)
			// Feishu can redeliver the same group message with different topic metadata.
			redelivery := m
			redelivery.TopicID = "omt_synthetic_redelivery_topic"
			accept(t, e, redelivery)
			x.observation = Observation{Status: "completed", RunID: probe.RunID, FoundRequest: true, Summary: "OK", Unsettled: true}
			tick(t, e)
			if len(notices) != 0 {
				t.Fatal("a verdict was sent while native work was unsettled")
			}
			x.observation.Unsettled = false
			tick(t, e)
			tick(t, e)

			finished := onlyTask(t, e)
			if finished.ID != probe.ID || finished.RequestID != probe.RequestID || finished.Phase != "completed" ||
				finished.CardID == "" || x.opens != 1 || x.sends != 1 || len(notices) != 1 {
				t.Fatalf("expected one recovered run and one terminal notification: %+v (opens=%d, sends=%d, notices=%d)", finished, x.opens, x.sends, len(notices))
			}
			for _, source := range []Message{finished.Source, notices[0].Source} {
				if source.ChatType != "group" || source.InputKind != doctorEchoKind || source.ID != m.ID ||
					source.ChatID != m.ChatID || source.TopicID != m.TopicID || source.UserID != m.UserID {
					t.Fatalf("group provenance changed before private delivery: %+v", source)
				}
			}
			if !notices[0].TextReply || !strings.Contains(notices[0].TextReplyBody, "OK") {
				t.Fatal("private notification did not contain the echo verdict")
			}
			if len(l.replies) != 1 {
				t.Fatalf("redelivery or restart produced another group reply: %q", l.replies)
			}
			for _, restricted := range []string{"OK", "通过", "失败", probe.ID, probe.RequestID, probe.ThreadID, probe.RunID,
				probe.RouteID, probe.Target.ProjectID, probe.ReceiverAppID, m.ID, m.ChatID, m.TopicID, m.UserID, finished.CardID} {
				if restricted != "" && strings.Contains(l.replies[0], restricted) {
					t.Errorf("group acknowledgement disclosed %q", restricted)
				}
			}
			state := e.Store.snapshot()
			if len(state.Current) != 0 || len(state.Selected) != 0 || len(state.Bindings) != 0 ||
				currentTask(e.Config, &state, m, finished.RouteID) != nil || len(sessionHistory(e.Config, &state, m, finished.RouteID)) != 0 {
				t.Fatal("group echo entered the normal conversation mappings or history")
			}
		})
	}
}

func TestDoctorGroupEchoRequiresExplicitAuthorizedCommand(t *testing.T) {
	for _, name := range []string{"bare doctor", "member", "ungranted group", "malformed sender", "missing route", "route ACL", "interaction mode", "runtime mode", "private tool identity"} {
		t.Run(name, func(t *testing.T) {
			e, x, l, m := doctorGroupEchoFixture(t)
			target := e.Config.Routes["diagnostic"]
			switch name {
			case "bare doctor":
				m.Text = "/doctor"
				e.Config.DefaultRoute = "diagnostic"
			case "member":
				m.UserID = doctorGroupEchoMember
			case "ungranted group":
				m.UserID, m.ChatID = doctorGroupEchoOwner, "oc_synthetic_ungranted"
			case "malformed sender":
				m.UserID = " " + doctorGroupEchoAdmin
				e.Config.Admins = []string{m.UserID}
				target.Users = []string{m.UserID}
			case "missing route":
				m.Text = "/doctor --echo absent"
			case "route ACL":
				target.Users = []string{doctorGroupEchoOwner}
			case "interaction mode":
				target.InteractionMode = "default"
			case "runtime mode":
				target.RuntimeMode = "full-access"
			case "private tool identity":
				target.ToolContext = &ToolContext{UserBinding: &ToolUserBinding{UserOpenID: m.UserID}}
			}
			e.Config.Routes["diagnostic"] = target
			accept(t, e, m)
			tick(t, e)
			if state := e.Store.snapshot(); len(state.Tasks) != 0 || x.preflights != 0 || x.opens != 0 || x.sends != 0 || l.notifications != 0 {
				t.Fatal("group command crossed the execution or private delivery boundary")
			}
			for _, reply := range l.replies {
				if strings.Contains(reply, "已登记模型回声") {
					t.Fatal("acknowledged a group echo that was never registered")
				}
			}
			if name == "bare doctor" && len(l.replies) != 1 {
				t.Fatalf("expected a passive group reply: %q", l.replies)
			}
		})
	}
}

func TestDoctorGroupEchoRevalidatesBeforeDispatchAndDelivery(t *testing.T) {
	ctx := context.Background()
	for _, stage := range []string{"dispatch", "delivery"} {
		for _, change := range []string{"admin", "group", "route", "receiver app"} {
			t.Run(stage+"/"+change, func(t *testing.T) {
				e, x, l, m := doctorGroupEchoFixture(t)
				accept(t, e, m)
				wantSends := 0
				if stage == "dispatch" {
					if err := e.consume(ctx, m); err != nil {
						t.Fatal(err)
					}
					if probe := onlyTask(t, e); probe.Phase != "ready" || x.preflights != 0 {
						t.Fatalf("expected registration before dispatch: %+v", probe)
					}
				} else {
					tick(t, e)
					probe := onlyTask(t, e)
					wantSends = 1
					x.observation = Observation{Status: "completed", RunID: probe.RunID, FoundRequest: true, Summary: "OK"}
					// Commit the terminal observation without running the notification outbox.
					if err := e.advance(ctx, probe); err != nil {
						t.Fatal(err)
					}
					if probe = onlyTask(t, e); probe.Phase != "completed" || l.notifications != 0 {
						t.Fatalf("expected a terminal result pending delivery: %+v", probe)
					}
				}

				appID := e.Config.AppID
				setAccess := func(mode string) {
					t.Helper()
					switch change {
					case "admin", "route":
						request := LocalAccessPolicyRequest{Kind: change, UserID: m.UserID, Mode: mode}
						if change == "route" {
							request.RouteID = "diagnostic"
						}
						if _, err := e.LocalAccessPolicy(ctx, request); err != nil {
							t.Fatal(err)
						}
					case "group":
						if _, err := e.LocalGroupAccess(ctx, GroupAccessRequest{ChatID: m.ChatID, Access: mode}); err != nil {
							t.Fatal(err)
						}
					case "receiver app":
						e.Config.AppID = appID
						if mode == "deny" {
							e.Config.AppID = "cli_synthetic_replacement"
						}
					}
				}
				setAccess("deny")
				tick(t, e)
				if x.preflights != wantSends || x.opens != wantSends || x.sends != wantSends || l.notifications != 0 {
					t.Fatalf("revocation leaked execution or private delivery: preflights=%d, opens=%d, sends=%d, notices=%d", x.preflights, x.opens, x.sends, l.notifications)
				}
				if err := e.Store.DrainCheck(appID); err != nil {
					t.Fatalf("revocation left the unsent diagnostic pending: %v", err)
				}
				if stage == "delivery" {
					setAccess("inherit")
					reopenLifecycleStore(t, e)
					tick(t, e)
					if l.notifications != 0 || x.sends != wantSends {
						t.Fatal("restored access reopened a suppressed terminal delivery")
					}
				}
			})
		}
	}
}
