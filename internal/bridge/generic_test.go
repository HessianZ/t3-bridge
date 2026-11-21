package bridge

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func genericEngine(t *testing.T) (*Engine, *fakeExecutor, *fakeMaintenance) {
	t.Helper()
	c, err := LoadConfig("../../config/generic.example.json")
	if err != nil {
		t.Fatal(err)
	}
	e, x, _, maintenance := testEngine(t)
	c.StateDir = e.Store.dir
	e.Config = c
	e.Classifier = nil
	return e, x, maintenance
}
func genericMessage(id, text string) Message {
	return Message{ID: id, Text: text, UserID: "REPLACE_OPEN_ID", ChatID: "REPLACE_CHAT_ID"}
}
func TestGenericNoClassifierTwoRoundRuntimeContract(t *testing.T) {
	e, x, maintenance := genericEngine(t)
	accept(t, e, genericMessage("one", "只回复已接通，不要执行命令。"))
	tick(t, e)
	first := onlyTask(t, e)
	if x.sends != 1 || first.RouteID != "work" || first.Target.Kind != "" || maintenance.calls != 0 || first.MaintenanceNotice != "" {
		t.Fatal("generic runtime required a classifier/HomeOps", first.Phase)
	}
	for _, forbidden := range []string{"homeops-coordination", "Nginx", "HomeOps", "固定协调入口", "[本轮维护边界]"} {
		if strings.Contains(x.request, forbidden) {
			t.Fatal("generic prompt contains host-specific policy", forbidden)
		}
	}
	if !strings.HasPrefix(x.request, first.Source.Text+"\n") {
		t.Fatal("user text is not first")
	}
	x.observation.Status = "completed"
	x.observation.Summary = "已接通"
	tick(t, e)
	reopenLifecycleStore(t, e)
	accept(t, e, genericMessage("two", "再解释一句，不要调用工具。"))
	tick(t, e)
	q := onlyTask(t, e)
	if x.opens != 1 || x.sends != 2 || q.ThreadID != first.ThreadID || q.RequestID == first.RequestID || q.Source.ID != first.Source.ID || len(q.Replies) != 1 || q.Replies[0].Result != "已接通" || maintenance.calls != 0 {
		t.Fatal("generic continuation lost original thread/round")
	}
}
func TestGenericNoClassifierStillRejectsTargetAndPermissionConflicts(t *testing.T) {
	for _, text := range []string{"请去Windows服务器重启服务", "请重启未知主机上的服务", "workstation和未知机器都检查磁盘", "/unknown 检查磁盘", "去Mac重启服务"} {
		t.Run(text, func(t *testing.T) {
			e, x, _ := genericEngine(t)
			accept(t, e, genericMessage("m", text))
			tick(t, e)
			if strings.HasPrefix(text, "/") {
				if x.opens != 0 || x.sends != 0 || len(e.Store.snapshot().Tasks) != 0 || len(e.Lark.(*fakeMessenger).replies) == 0 {
					t.Fatal("unknown command did not remain in control layer")
				}
				return
			}
			if x.opens != 0 || x.sends != 0 || onlyTask(t, e).Phase != "clarify" {
				t.Fatal("default swallowed explicit unknown/multiple target")
			}
		})
	}
	for _, failure := range []string{"acl", "capability", "identity", "configured-classifier-missing"} {
		t.Run(failure, func(t *testing.T) {
			e, x, _ := genericEngine(t)
			switch failure {
			case "acl":
				r := e.Config.Routes["work"]
				r.Users = []string{"another-user"}
				e.Config.Routes["work"] = r
			case "capability":
				x.preflightErr = errors.New("provider/model exceeds capabilities")
			case "identity":
				x.verifyErr = errors.New("native identity differs")
			case "configured-classifier-missing":
				e.Config.Classifier = Hy4Config{BaseURL: "https://copilot.tencent.com/v2", Model: "hy4flash", KeyFile: "/synthetic/key", Verified: true}
			}
			accept(t, e, genericMessage("m", "只回复好"))
			tick(t, e)
			if x.sends != 0 {
				t.Fatal("permission/identity/classifier failure silently defaulted")
			}
		})
	}
}
func TestGenericReviewAndExplicitMaintenancePolicies(t *testing.T) {
	c, err := LoadConfig("../../config/multi-host.example.json")
	if err != nil {
		t.Fatal(err)
	}
	review := c.Routes[c.ReviewRoute]
	review.Model = "another-explicit-model"
	review.Options = map[string]any{"CustomOption": true}
	c.Routes[c.ReviewRoute] = review
	if c.Validate() != nil {
		t.Fatal("review still tied to a specific model")
	}
	c.DefaultRoute = c.ReviewRoute
	if c.Validate() == nil {
		t.Fatal("review accepted as ordinary default")
	}
	e, x, maintenance := genericEngine(t)
	route := e.Config.Routes["work"]
	route.Checks = []string{"service:example"}
	e.Config.Routes["work"] = route
	if e.Config.Validate() == nil {
		t.Fatal("checks accepted without explicit coordinator")
	}
	e.Config.CoordinationCommand = "/synthetic/coordinator"
	if e.Config.Validate() != nil {
		t.Fatal("explicit maintenance config rejected")
	}
	accept(t, e, genericMessage("coordinated", "只读核查"))
	tick(t, e)
	if x.sends != 1 || maintenance.calls != 1 || !strings.Contains(x.request, "/synthetic/coordinator") || !strings.Contains(x.request, "guard") || !strings.Contains(x.request, "service:example") {
		t.Fatal("explicit coordination lost guard")
	}
}
func TestHomeOpsEnabledCannotDropMaintenanceChecks(t *testing.T) {
	c, err := LoadConfig("../../config/homeops.example.json")
	if err != nil {
		t.Fatal(err)
	}
	r := c.Routes["operations"]
	r.Checks = nil
	c.Routes["operations"] = r
	if c.Validate() == nil {
		t.Fatal("event integration weakened maintenance")
	}
}
func TestGenericNilClassifierMirrorKeepsConfiguredErrors(t *testing.T) {
	c, err := LoadConfig("../../config/generic.example.json")
	if err != nil {
		t.Fatal(err)
	}
	classifier := &fakeClassifier{err: context.DeadlineExceeded}
	if _, err = Route(context.Background(), c, genericMessage("m", "普通问题"), nil, classifier); err == nil {
		t.Fatal("configured classifier error fell back")
	}
	c.DefaultRoute = ""
	d, err := Route(context.Background(), c, genericMessage("m", "普通问题"), nil, nil)
	if err != nil || d.Action != "clarify" {
		t.Fatal("no-default unknown scope guessed")
	}
}
