package bridge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	channel "github.com/larksuite/channel-sdk-go"
)

func TestP2aPolicySaveFailureCannotGrantAndUnknownOwnerCannotBeDeleted(t *testing.T) {
	e, _, ch, _, f := setupAccess(t)
	directory := e.Store.dir
	e.Store.dir = filepath.Join(directory, "missing")
	n := groupMessage("save-failure", "user", "chat", "/invite route mac @person", true)
	n.Mentions = []channel.Mention{{Key: "@person", OpenID: "stranger"}}
	if err := ch.message(context.Background(), n); err == nil {
		t.Fatal("inbound save failure acknowledged")
	}
	// Inbound persistence itself must fail closed. Restore the path before testing worker commit.
	e.Store.dir = directory
	if len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("failed inbound acknowledgement retained partial state")
	}
	receiveAccess(t, ch, n)
	e.Store.dir = filepath.Join(directory, "missing")
	if err := e.Tick(context.Background()); err == nil {
		t.Fatal("failed policy commit acknowledged")
	}
	m := Normalize(groupMessage("probe", "stranger", "chat", "", true))
	if e.Config.TargetAllowed(m, e.Config.Routes["mac"], "mac") || len(e.Store.snapshot().AccessPolicy.Routes) != 0 || len(e.Store.snapshot().Inbox) != 1 {
		t.Fatal("failed policy commit published a grant or lost its input")
	}
	e.Store.dir = directory
	tick(t, e)
	if !e.Config.TargetAllowed(m, e.Config.Routes["mac"], "mac") {
		t.Fatal("retained authorization command could not complete")
	}
	receiveAccess(t, ch, groupMessage("unknown-owner", "admin", "chat", "/remove admin ou_synthetic", true))
	e.Config.access.mu.Lock()
	e.Config.access.owner = ""
	e.Config.access.mu.Unlock()
	tick(t, e)
	if !strings.Contains(f.replies[len(f.replies)-1], "Owner暂未解析") {
		t.Fatal("owner unavailable did not protect identity")
	}
}

func TestP2aReconnectCrashFenceSurvivesRestart(t *testing.T) {
	e, _, ch, x, fixture := setupAccess(t)
	m := Normalize(groupMessage("reconnect-unknown", "user", "chat", "/reconnect", true))
	receiveAccess(t, ch, groupMessage(m.ID, "user", "chat", m.Text, true))
	if err := e.saveCommand(key(m, m.ID), &SessionCommand{Source: m, Kind: "/reconnect", ReconnectAttempted: true}); err != nil {
		t.Fatal(err)
	}
	e.Store.Close()
	s, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e.Store = s
	calls := 0
	e.Reconnect = func(context.Context) (string, error) { calls++; return "unexpected", nil }
	tick(t, e)
	if calls != 0 || x.sends != 0 || len(s.snapshot().Inbox) != 0 || !strings.Contains(fixture.replies[len(fixture.replies)-1], "待 /doctor 核对") {
		t.Fatal("uncertain reconnect repeated or lost its visible outcome")
	}
}
func TestP2aSettingsCannotChangeExecutionPolicyAndHaveNoDefaultDeadline(t *testing.T) {
	e, _, ch, x, _ := setupAccess(t)
	target := e.Config.Routes["nas"]
	roleCommand(t, e, ch, "ordinary", "other", "/config concurrency 1", "")
	roleCommand(t, e, ch, "no-provider", "user", "/config provider foreign", "")
	roleCommand(t, e, ch, "bad-budget", "user", "/config concurrency 99", "")
	roleCommand(t, e, ch, "bad-progress", "user", "/config progress maybe", "")
	roleCommand(t, e, ch, "bad-time", "user", "/timeout 1s", "")
	if !reflect.DeepEqual(target, e.Config.Routes["nas"]) || e.Store.runSettings().MaxConcurrent != 0 {
		t.Fatal("chat config mutated execution policy")
	}
	receiveAccess(t, ch, groupMessage("task", "user", "chat", "/nas 长任务", true))
	tick(t, e)
	if onlyTask(t, e).TimeoutSeconds != 0 || onlyTask(t, e).DeadlineAt != "" || x.sends != 1 {
		t.Fatal("existing tasks received default short deadline")
	}
}
func TestP2aLegacyAccountAdoptionIsExplicitAndCannotRetag(t *testing.T) {
	e, _, _, _ := testEngine(t)
	accept(t, e, message("one", "/nas original"))
	tick(t, e)
	if err := e.Store.update(func(st *State) error {
		for _, task := range st.Tasks {
			task.ReceiverAppID = ""
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := onlyTask(t, e)
	if err := e.Store.BindAccount(e.Config.AppID); err == nil {
		t.Fatal("unbound legacy state silently adopted")
	}
	if err := e.Store.BindAccount(e.Config.AppID, true); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.BindAccount("foreign-app", true); err == nil {
		t.Fatal("explicit adoption retagged another app")
	}
	if !reflect.DeepEqual(before, onlyTask(t, e)) {
		t.Fatal("account binding changed original task")
	}
}
func TestP2aConfigToolsAndReviewRequireExplicitSafeCombination(t *testing.T) {
	for _, mode := range []string{"valid", "foreign-tools", "user-tools", "review-model", "review-option"} {
		t.Run(mode, func(t *testing.T) {
			c := testConfig(t)
			target := c.Routes["nas"]
			target.ToolContext = &ToolContext{CLIPath: "/synthetic/bin/lark-cli", EnvironmentID: target.EnvironmentID, Version: "1.0.96", Identity: "bot", Verified: true}
			c.Routes["nas"] = target
			review := target
			review.Model = "codebuddy-direct/hy4free"
			review.Options = map[string]any{"variant": "high"}
			c.Routes["nas-review"] = review
			c.ReviewRoute = "nas-review"
			switch mode {
			case "foreign-tools":
				target.ToolContext.EnvironmentID = "foreign"
			case "user-tools":
				target.ToolContext.Identity = "user"
			case "review-model":
				review.Model = "configured-review-model"
			case "review-option":
				review.Options = map[string]any{}
			}
			c.Routes["nas"] = target
			c.Routes["nas-review"] = review
			if (c.Validate() == nil) != (mode == "valid" || mode == "review-model" || mode == "review-option") {
				t.Fatal("unsafe tool/review config accepted", mode)
			}
		})
	}
}
func TestP2aUnavailableMacDoesNotFallbackOrOpen(t *testing.T) {
	e, x, _, _ := testEngine(t)
	mac := e.Config.Routes["mac"]
	mac.UnavailableReason = "未正式反向关联及验权"
	e.Config.Routes["mac"] = mac
	accept(t, e, message("mac", "/mac 检查"))
	tick(t, e)
	if x.opens != 0 || x.sends != 0 || !strings.Contains(onlyTask(t, e).Summary, "未正式反向关联") {
		t.Fatal("unavailable route silently fallback")
	}
}

func TestP2aReviewRouteIsExcludedFromOrdinaryRouting(t *testing.T) {
	c := testConfig(t)
	review := c.Routes["nas"]
	review.Model = "codebuddy-direct/hy4free"
	review.Options = map[string]any{"variant": "high"}
	c.Routes["nas-review"] = review
	c.ReviewRoute = "nas-review"
	c.DefaultRoute = "nas"
	classifier := &fakeClassifier{d: Decision{RouteID: "nas", Action: "new_task"}}
	d, err := Route(context.Background(), c, message("discussion", "先讨论一下更新方案"), nil, classifier)
	if err != nil || d.RouteID != "nas" || classifier.defaultRoute != "nas" || slices.Contains(classifier.routes, "nas-review") {
		t.Fatal("ordinary discussion was offered the review model", d, err)
	}
	for _, action := range []string{"new_task", "continue", "coordinate"} {
		classifier.d = Decision{RouteID: "nas-review", Action: action}
		if _, err := Route(context.Background(), c, message("bad", "普通咨询"), nil, classifier); err == nil {
			t.Fatal("classifier selected reserved review route", action)
		}
	}
	c.DefaultRoute = ""
	delete(c.Routes, "mac")
	if selectedRoute(c, &State{}, message("scope", "")) != "nas" {
		t.Fatal("reserved review route made the ordinary NAS selection ambiguous")
	}
	d, err = Route(context.Background(), c, message("legacy", "请在NAS检查磁盘"), nil, classifier)
	if err != nil || d != (Decision{RouteID: "nas", Action: "new_task"}) {
		t.Fatal("review route changed the legacy NAS rule", d, err)
	}
	c.DefaultRoute = "nas-review"
	if c.Validate() == nil {
		t.Fatal("review route accepted as silent default")
	}
}

func TestP2aDirectReviewPrefixCannotBypassExplicitReadOnlyEntry(t *testing.T) {
	e, x, _, _ := testEngine(t)
	review := e.Config.Routes["nas"]
	review.Model = "codebuddy-direct/hy4free"
	review.Options = map[string]any{"variant": "high"}
	e.Config.Routes["nas-review"] = review
	e.Config.ReviewRoute = "nas-review"
	e.Config.DefaultRoute = "nas"
	accept(t, e, message("ds", "/nas 讨论"))
	tick(t, e)
	x.observation.Status = "completed"
	tick(t, e)
	ds := onlyTask(t, e)
	accept(t, e, message("prefix", "/nas-review 执行变更"))
	tick(t, e)
	st := e.Store.snapshot()
	if x.sends != 1 || st.Selected[userScope(ds.Source)] != "nas" || st.Tasks[ds.ID].RequestID != ds.RequestID {
		t.Fatal("generic prefix launched Hy4 or changed the DS context")
	}
	if prefix := st.Tasks[stable("chat", "topic", "prefix")]; prefix == nil || prefix.Phase != "clarify" || !strings.Contains(prefix.Summary, "/review") {
		t.Fatal("missing useful explicit review instruction")
	}
	accept(t, e, message("review", "/review 仅复核方案"))
	tick(t, e)
	st = e.Store.snapshot()
	r := st.Tasks[stable("chat", "topic", "review")]
	if x.sends != 2 || x.opens != 2 || r == nil || r.DispatchSource.InputKind != "review" || st.Selected[userScope(ds.Source)] != "nas" {
		t.Fatal("explicit review failed or redirected DS")
	}
	tick(t, e)
	m := message("review-next", "再解释一下，不执行")
	m.ParentID = r.CardID
	accept(t, e, m)
	tick(t, e)
	latest := e.Store.task(r.ID)
	if x.sends != 3 || latest.ThreadID != r.ThreadID || latest.DispatchSource.InputKind != "review" || !strings.Contains(x.request, "仅获只读讨论授权") || st.Selected[userScope(ds.Source)] != "nas" {
		t.Fatal("review-card continuation lost its read-only model boundary")
	}
}
func TestP2aPureConfigCheckDoesNotReadToolOrSecretFiles(t *testing.T) {
	c := testConfig(t)
	target := c.Routes["nas"]
	target.ToolContext = &ToolContext{CLIPath: "/does/not/exist/lark-cli", EnvironmentID: target.EnvironmentID, Version: "1.0.96", Identity: "bot", Verified: true}
	c.Routes["nas"] = target
	b, _ := json.Marshal(c)
	p := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(p, b, 0600)
	if _, err := LoadConfig(p); err != nil {
		t.Fatal("static check tried to resolve remote CLI or secrets", err)
	}
}
