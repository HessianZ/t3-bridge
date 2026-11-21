package bridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func aliasConfig(t *testing.T) Config {
	c := testConfig(t)
	nas := c.Routes["nas"]
	nas.HostName = "storage-a"
	nas.Aliases = []string{"vault", "群晖"}
	c.Routes["nas"] = nas
	other := nas
	other.EnvironmentID = "secondary-env"
	other.HostName = "storage-b"
	other.Aliases = []string{"backup"}
	c.Routes["secondary"] = other
	mac := c.Routes["mac"]
	mac.HostName = "workstation"
	mac.Aliases = []string{"laptop"}
	c.Routes["mac"] = mac
	return c
}
func TestAliasRoutingAcrossRulesAndFallback(t *testing.T) {
	for _, def := range []string{"", "nas"} {
		for _, tc := range []struct{ text, route, action string }{
			{"/VaUlT 只回复已接通，不要执行命令", "nas", "new_task"},
			{"/vault\t检查磁盘", "nas", "new_task"},
			{"/vault 请在backup上检查磁盘", "", "clarify"},
			{"/vault 比较backup和vault的不同，不执行", "nas", "new_task"},
			{"请在BACKUP上检查磁盘", "secondary", "new_task"},
			{"群晖检查磁盘", "nas", "new_task"},
			{"storage-a和backup都检查磁盘", "", "coordinate"},
			{"请重启laptop上的服务", "mac", "new_task"},
			{"NAS检查磁盘", "", "clarify"},
			{"请在unknown-host上检查磁盘", "", "clarify"},
		} {
			t.Run(def+tc.text, func(t *testing.T) {
				c := aliasConfig(t)
				c.DefaultRoute = def
				f := &fakeClassifier{d: Decision{RouteID: "nas", Action: "new_task"}}
				got, err := Route(context.Background(), c, message("m", tc.text), nil, f)
				if err != nil || got != (Decision{tc.route, tc.action}) || f.calls != 0 {
					t.Fatal(got, err, f.calls)
				}
			})
		}
	}
}
func TestAliasBoundAndTrustedEventConflicts(t *testing.T) {
	for _, event := range []bool{false, true} {
		for _, text := range []string{"请在backup上检查磁盘", "/backup 检查磁盘", "storage-a和backup都检查磁盘", "请在laptop上检查磁盘"} {
			t.Run(text+map[bool]string{true: "event", false: "bound"}[event], func(t *testing.T) {
				c := aliasConfig(t)
				m := message("m", text)
				bound := &Task{RouteID: "nas"}
				if event {
					m.EventRoute = "nas"
					bound = nil
				}
				got, err := Route(context.Background(), c, m, bound, &fakeClassifier{d: Decision{"nas", "new_task"}})
				if err != nil || got.Action != "clarify" {
					t.Fatal("binding/event bypass", got, err)
				}
			})
		}
	}
}
func TestAliasConfigurationAndPersistentIdentity(t *testing.T) {
	c := testConfig(t)
	before := c.Routes["nas"]
	identity := targetIdentity(before)
	after := before
	after.HostName = "storage-a"
	after.Aliases = []string{"vault"}
	if targetIdentity(after) != identity || executionPolicy(after) != executionPolicy(before) {
		t.Fatal("recognition names changed executor identity")
	}
	c.Routes["nas"] = after
	if !taskTargetAllowed(c, &Task{RouteID: "nas", Target: before}) || !taskTargetAllowed(c, &Task{RouteID: "nas", Target: before, BaseTargetID: identity}) {
		t.Fatal("old Task invalidated")
	}
	for _, bad := range []string{"cross-route", "case-route", "duplicate", "command", "whitespace", "slash", "empty", "unicode-case", "route-case"} {
		t.Run(bad, func(t *testing.T) {
			v := aliasConfig(t)
			b := v.Routes["secondary"]
			switch bad {
			case "cross-route":
				b.Aliases = []string{"VAULT"}
			case "case-route":
				b.HostName = "NAS"
			case "duplicate":
				b.Aliases = []string{"backup", "BACKUP"}
			case "command":
				b.Aliases = []string{"stop"}
			case "whitespace":
				b.HostName = "bad name"
			case "slash":
				b.Aliases = []string{"/bad"}
			case "empty":
				b.Aliases = []string{""}
			case "unicode-case":
				main := v.Routes["nas"]
				main.Aliases = []string{"Σ"}
				v.Routes["nas"] = main
				b.Aliases = []string{"ς"}
			case "route-case":
				v.Routes["NAS"] = v.Routes["nas"]
			}
			v.Routes["secondary"] = b
			if v.Validate() == nil {
				t.Fatal("accepted conflicting/unsafe names")
			}
		})
	}
	raw, _ := json.Marshal(before)
	var old Target
	json.Unmarshal(raw, &old)
	if !reflect.DeepEqual(before, old) || targetIdentity(old) != identity {
		t.Fatal("legacy target changed")
	}
}

func TestAliasEngineCurrentFallbackAndRestart(t *testing.T) {
	for _, def := range []string{"", "nas"} {
		t.Run("default="+def, func(t *testing.T) {
			e, x, _, _ := testEngine(t)
			e.Config = aliasConfig(t)
			e.Config.StateDir = e.Store.dir
			e.Config.DefaultRoute = def
			accept(t, e, message("first", "/vault 先回复已接通"))
			tick(t, e)
			x.observation.Status = "completed"
			x.observation.Summary = "旧结论"
			tick(t, e)
			before := onlyTask(t, e)
			// Renaming recognition metadata must not invalidate old Current/project identity.
			target := e.Config.Routes["nas"]
			target.HostName = "renamed-storage"
			target.Aliases = []string{"renamed-vault"}
			e.Config.Routes["nas"] = target
			reopenLifecycleStore(t, e)
			reloaded := e.Store.snapshot()
			if q := currentTask(e.Config, &reloaded, message("next", "继续"), "nas"); q == nil || q.ID != before.ID {
				t.Fatal("old current identity invalidated")
			}
			accept(t, e, message("switch", "请在backup上检查磁盘"))
			tick(t, e)
			after := e.Store.snapshot()
			if x.sends != 2 || len(after.Tasks) != 2 || !reflect.DeepEqual(before, after.Tasks[before.ID]) {
				t.Fatal("alias was swallowed by implicit current/fallback")
			}
			for id, q := range after.Tasks {
				if id != before.ID && q.RouteID != "secondary" {
					t.Fatal("cross-machine input sent to old target")
				}
			}
		})
	}
}

func TestAliasStopQueueAndQuoteBoundaries(t *testing.T) {
	e, x, _, _ := testEngine(t)
	e.Config = aliasConfig(t)
	e.Config.StateDir = e.Store.dir
	e.Config.DefaultRoute = "nas"
	accept(t, e, message("first", "/vault 开始任务"))
	tick(t, e)
	q := onlyTask(t, e)
	next := message("other-machine", "请在backup上检查磁盘")
	accept(t, e, next)
	accept(t, e, message("stop", "/vault 停止"))
	state := e.Store.snapshot()
	for _, m := range state.Inbox {
		if m.ID == next.ID && (m.Cancelled || m.RouteAtReceive != "secondary") {
			t.Fatal("stop crossed named target boundary", m)
		}
	}
	if !state.Tasks[q.ID].StopRequested {
		t.Fatal("alias stop ignored")
	}
	tick(t, e)
	if x.stops != 1 {
		t.Fatal("wrong stop target")
	}
	for _, text := range []string{"引用“backup重启服务”", "不要在backup上执行命令，只解释方案", "比较storage-a和laptop的区别"} {
		got, err := Route(context.Background(), e.Config, message("quote", text), &Task{RouteID: "nas"}, nil)
		if err != nil || got != (Decision{"nas", "continue"}) {
			t.Fatal("context became target authorization", got, err)
		}
	}
}

func TestAliasHy4ProductionRouteContextAndACL(t *testing.T) {
	c := aliasConfig(t)
	c.DefaultRoute = "nas"
	secondary := c.Routes["secondary"]
	secondary.Users = []string{"other"}
	c.Routes["secondary"] = secondary
	review := c.Routes["nas"]
	review.HostName = "review-only"
	review.Aliases = []string{"verify-host"}
	c.Routes["review"] = review
	c.ReviewRoute = "review"
	key := filepath.Join(t.TempDir(), "hy4.secret")
	os.WriteFile(key, []byte("synthetic-key"), 0600)
	calls := 0
	h := Hy4{Config: Hy4Config{BaseURL: "https://copilot.tencent.com/v2", Model: "hy4flash", KeyFile: key, Verified: true}, Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		var body struct {
			Messages []struct{ Role, Content string }
		}
		json.NewDecoder(r.Body).Decode(&body)
		var input struct {
			Text, DefaultRoute string
			Routes             []string
			RouteNames         map[string][]string
		}
		json.Unmarshal([]byte(body.Messages[1].Content), &input)
		if input.DefaultRoute != "nas" || !reflect.DeepEqual(input.RouteNames["nas"], []string{"nas", "storage-a", "vault", "群晖"}) || input.RouteNames["secondary"] != nil || input.RouteNames["review"] != nil || strings.Contains(body.Messages[1].Content, "backup") || !strings.Contains(body.Messages[0].Content, "routeNames") {
			t.Fatal("classifier lost names/ACL boundary", input)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(sseDelta(`{"routeId":"nas","action":"new_task"}`, "stop") + "data: [DONE]\n\n")), Header: http.Header{}}, nil
	})}}
	got, err := Route(context.Background(), c, message("ordinary", "解释vault与laptop的区别，不要执行命令"), nil, h)
	if err != nil || got != (Decision{"nas", "new_task"}) || calls != 1 {
		t.Fatal(got, err, calls)
	}
	for _, text := range []string{"/backup 检查磁盘", "backup检查磁盘", "verify-host检查磁盘", "/verify-host 只回复好"} {
		e, x, _, _ := testEngine(t)
		e.Config = c
		e.Config.StateDir = e.Store.dir
		e.Classifier = h
		accept(t, e, message("denied", text))
		tick(t, e)
		if q := onlyTask(t, e); q.Phase != "clarify" || x.opens != 0 || x.sends != 0 || calls != 1 {
			t.Fatal("aliases expanded ACL/review entry", q.Phase, x.opens, x.sends, calls)
		}
	}
}

func TestAliasLegacyStoreUpgradePreservesCurrentAndFence(t *testing.T) {
	e, x, _, _ := testEngine(t)
	accept(t, e, message("legacy", "/nas 原会话"))
	tick(t, e)
	x.observation.Status = "completed"
	x.observation.Summary = "保留原结果"
	tick(t, e)
	before := e.Store.snapshot()
	old := onlyTask(t, e)
	target := e.Config.Routes["nas"]
	target.HostName = "new-host"
	target.Aliases = []string{"new-name"}
	e.Config.Routes["nas"] = target
	reopenLifecycleStore(t, e)
	if !reflect.DeepEqual(before, e.Store.snapshot()) {
		t.Fatal("recognition config migrated private state")
	}
	accept(t, e, message("continue", "/NEW-NAME 再解释一下"))
	tick(t, e)
	q := onlyTask(t, e)
	if x.opens != 1 || x.sends != 2 || q.ThreadID != old.ThreadID || q.Source.ID != old.Source.ID || q.BaseTargetID != old.BaseTargetID || len(q.Replies) != 1 || q.Replies[0].RequestID != old.RequestID || q.Replies[0].Result != old.Result {
		t.Fatal("legacy Task/request/result changed identity")
	}
}
func TestAliasEventCannotOverrideDifferentBoundTarget(t *testing.T) {
	c := aliasConfig(t)
	m := message("event", "只解释这条告警")
	m.EventRoute = "nas"
	d, err := Route(context.Background(), c, m, &Task{RouteID: "secondary"}, nil)
	if err != nil || d.Action != "clarify" {
		t.Fatal("event moved bound conversation", d, err)
	}
}
