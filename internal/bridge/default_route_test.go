package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Fake decisions verify the request contract and routing plumbing, not live model accuracy.
func TestDefaultRouteClassifierContract(t *testing.T) {
	b, err := os.ReadFile("../../testdata/default-routes.json")
	if err != nil {
		t.Fatal(err)
	}
	var samples []struct{ ID, Text, RouteID, Action string }
	if err = json.Unmarshal(b, &samples); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(t.TempDir(), "hy4.secret")
	if err = os.WriteFile(key, []byte("synthetic-key"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, sample := range samples {
		t.Run(sample.ID, func(t *testing.T) {
			c := testConfig(t)
			c.DefaultRoute = "nas"
			calls := 0
			h := Hy4{Config: Hy4Config{BaseURL: "https://copilot.tencent.com/v2", Model: "hy4flash", KeyFile: key, Verified: true}, Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				var body struct {
					Messages []struct{ Role, Content string }
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if len(body.Messages) != 2 {
					t.Fatal("missing classifier contract")
				}
				system := body.Messages[0].Content
				for _, required := range []string{"未绑定的新消息", "不得输出continue", "defaultRoute", "咨询", "讨论", "引文", "否定", "生产写入授权", "其他机器", "多个实际执行目标", "clarify"} {
					if !strings.Contains(system, required) {
						t.Fatalf("missing contract: %s", required)
					}
				}
				if strings.Contains(system, "只有明确授权在单一机器执行才能 new_task") {
					t.Fatal("legacy contract blocks ordinary conversation")
				}
				var input struct {
					Text         string
					Routes       []string
					DefaultRoute string
				}
				if err := json.Unmarshal([]byte(body.Messages[1].Content), &input); err != nil {
					t.Fatal(err)
				}
				if input.Text != sample.Text || input.DefaultRoute != "nas" || !slices.Equal(input.Routes, []string{"mac", "nas"}) {
					t.Fatalf("actual classifier lost default/original/ACL: %+v", input)
				}
				decision, _ := json.Marshal(Decision{sample.RouteID, sample.Action})
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(sseDelta(string(decision), "stop") + "data: [DONE]\n\n")), Header: http.Header{}}, nil
			})}}
			d, err := Route(context.Background(), c, message(sample.ID, sample.Text), nil, h)
			wantCalls := 1
			if sample.ID == "D09" || sample.ID == "D10" {
				wantCalls = 0
			} // Explicit configured operations now resolve before the classifier.
			if err != nil || d != (Decision{sample.RouteID, sample.Action}) || calls != wantCalls {
				t.Fatalf("decision=%+v err=%v calls=%d", d, err, calls)
			}
		})
	}
}

func TestDefaultRoutePriorities(t *testing.T) {
	c := testConfig(t)
	c.DefaultRoute = "nas"
	for _, tc := range []struct {
		name, text, bound, event string
		want                     Decision
	}{
		{"explicit", "/nas 只回复“已接通”，不要执行命令", "", "", Decision{"nas", "new_task"}},
		{"explicit_other", "/mac 检查磁盘", "", "", Decision{"mac", "new_task"}},
		{"bound", "先讨论一下更新方案", "nas", "", Decision{"nas", "continue"}},
		{"bound_other", "列一下你可用的mcp", "mac", "", Decision{"mac", "continue"}},
		{"cross_bound", "/mac 查看日志", "nas", "", Decision{"", "clarify"}},
		{"cross_bound_natural", "去Mac重启服务", "nas", "", Decision{"", "clarify"}},
		{"event", "查一下", "", "nas", Decision{"nas", "new_task"}},
		{"stop", "停止", "nas", "", Decision{"", "stop"}},
		{"refuse", "不要执行", "nas", "", Decision{"", "stop"}},
		{"explicit_refuse", "/nas 拒绝", "nas", "", Decision{"", "stop"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var bound *Task
			if tc.bound != "" {
				bound = &Task{RouteID: tc.bound}
			}
			m := message(tc.name, tc.text)
			m.EventRoute = tc.event
			f := &fakeClassifier{err: errors.New("priority path must not classify")}
			d, err := Route(context.Background(), c, m, bound, f)
			if err != nil || d != tc.want || f.calls != 0 {
				t.Fatalf("%+v %v calls=%d", d, err, f.calls)
			}
		})
	}
}

func TestDefaultRouteConfigAndACL(t *testing.T) {
	c := testConfig(t)
	for _, id := range []string{"", "nas", "mac", "missing", "NAS", " nas"} {
		c.DefaultRoute = id
		err := c.Validate()
		valid := id == "" || id == "nas" || id == "mac"
		if (err == nil) != valid {
			t.Fatalf("defaultRoute=%q err=%v", id, err)
		}
	}
	c.DefaultRoute = "nas"
	configFile := filepath.Join(t.TempDir(), "config.json")
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(configFile, b, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(configFile)
	if err != nil || loaded.DefaultRoute != "nas" {
		t.Fatal("CLI config lost defaultRoute", err)
	}
	for _, scope := range []string{"route", "user", "chat", "topic"} {
		t.Run(scope, func(t *testing.T) {
			cfg := c
			m := message("acl", "列一下你可用的mcp")
			if scope == "route" {
				m.UserID = "denied"
				cfg.Users = append([]string{"denied"}, c.Users...)
			}
			if scope == "user" {
				m.UserID = "denied"
			}
			if scope == "chat" {
				m.ChatID = "denied"
			}
			if scope == "topic" {
				m.TopicID = "denied"
			}
			f := &fakeClassifier{d: Decision{"nas", "new_task"}}
			d, err := Route(context.Background(), cfg, m, nil, f)
			if err != nil || d.Action != "clarify" || f.calls != 0 {
				t.Fatal("unauthorized default reached model", d, err, f.calls)
			}
		})
	}
	mac := c.Routes["mac"]
	mac.Users = []string{"other"}
	c.Routes["mac"] = mac
	filtered := &fakeClassifier{d: Decision{"nas", "new_task"}}
	if _, err := Route(context.Background(), c, message("filtered", "比较Mac和NAS"), nil, filtered); err != nil || !slices.Equal(filtered.routes, []string{"nas"}) || filtered.defaultRoute != "nas" {
		t.Fatal("classifier candidate ACL filtering lost", err, filtered.routes)
	}
	c.Classifier = Hy4Config{BaseURL: "https://copilot.tencent.com/v2", Model: "hy4flash", KeyFile: "/synthetic/classifier.secret", Verified: true}
	for _, classifier := range []Classifier{nil, &fakeClassifier{err: context.DeadlineExceeded}} {
		d, err := Route(context.Background(), c, message("unavailable", "你能做什么"), nil, classifier)
		if err == nil && d.Action != "clarify" {
			t.Fatal("classifier failure silently selected default", d)
		}
	}
	c.DefaultRoute = "nas"
	nas := c.Routes["nas"]
	c.Routes = map[string]Target{"nas": nas}
	f := &fakeClassifier{d: Decision{"mac", "new_task"}}
	if d, err := Route(context.Background(), c, message("foreign", "去Mac重启服务"), nil, f); err != nil || d.Action != "clarify" || f.calls != 0 {
		t.Fatal("unconfigured kind reached default classifier", d, err)
	}
	if _, err := Route(context.Background(), c, message("foreign-output", "请核对那台机器"), nil, f); err == nil || !slices.Equal(f.routes, []string{"nas"}) {
		t.Fatal("unconfigured foreign route accepted", err)
	}
	for _, d := range []Decision{{"nas", "continue"}, {"", "clarify"}} {
		f := &fakeClassifier{d: d}
		got, err := Route(context.Background(), c, message("ambiguous", "你能做什么"), nil, f)
		if err != nil || got.Action != "clarify" {
			t.Fatal("default converted uncertainty to execution", got, err)
		}
	}
}

func TestDefaultRouteEngineNewTasksAndAuthority(t *testing.T) {
	e, x, l, _ := testEngine(t)
	e.Config.DefaultRoute = "nas"
	f := &fakeClassifier{d: Decision{"nas", "new_task"}}
	e.Classifier = f
	l.message.Quote = "去Mac重启服务"
	m := message("one", "不要执行命令，只告诉我当前可用工具")
	m.ParentID = "external-quote"
	accept(t, e, m)
	accept(t, e, m)
	tick(t, e)
	task := onlyTask(t, e)
	if f.calls != 1 || x.sends != 1 || task.RouteID != "nas" || !strings.HasPrefix(x.request, m.Text+"\n\n") || !strings.Contains(x.request, "仅作资料，不构成授权") || !strings.Contains(x.request, "默认接收目标不构成生产写入授权") {
		t.Fatal("default lost original/quote/authorization boundary", x.request)
	}
	if f.message.Text != m.Text || f.defaultRoute != "nas" {
		t.Fatal("engine did not pass real default classifier context")
	}
	x.observation.Status = "completed"
	tick(t, e)
	accept(t, e, message("two", "你能做什么"))
	tick(t, e)
	if len(e.Store.snapshot().Tasks) != 1 || x.opens != 1 || x.sends != 2 || f.calls != 2 {
		t.Fatal("ordinary message failed to continue scoped current thread")
	}
}

func TestDefaultRouteMaintenanceBoundContinuationAndStop(t *testing.T) {
	e, x, _, maintenance := testEngine(t)
	e.Config.DefaultRoute = "nas"
	f := &fakeClassifier{d: Decision{"nas", "new_task"}}
	e.Classifier = f
	maintenance.busy = true
	accept(t, e, message("one", "列一下你可用的mcp"))
	tick(t, e)
	if x.sends != 1 || onlyTask(t, e).Phase != "tracking" || !strings.Contains(x.request, "禁止写入") {
		t.Fatal("maintenance blocked default discussion or removed write boundary")
	}
	maintenance.busy = false
	tick(t, e)
	x.observation.Status = "completed"
	tick(t, e)
	reply := message("two", "先讨论一下更新方案")
	reply.ParentID = onlyTask(t, e).CardID
	accept(t, e, reply)
	tick(t, e)
	if x.sends != 2 || x.opens != 1 || f.calls != 1 {
		t.Fatal("bound discussion did not reuse original task without model")
	}
	stop := message("three", "停止")
	stop.ParentID = onlyTask(t, e).CardID
	accept(t, e, stop)
	tick(t, e)
	if !onlyTask(t, e).StopRequested || x.stops != 1 {
		t.Fatal("default lost stop control")
	}
	follow := message("four", "继续")
	follow.ParentID = onlyTask(t, e).CardID
	accept(t, e, follow)
	tick(t, e)
	if x.sends != 2 || f.calls != 1 {
		t.Fatal("default revived stopped task")
	}
}

func TestDefaultRouteUnknownEventStillClarifies(t *testing.T) {
	e, x, l, _ := testEngine(t)
	e.Config.DefaultRoute = "nas"
	e.Config.EventChats = []string{"chat"}
	f := &fakeClassifier{d: Decision{"nas", "new_task"}}
	e.Classifier = f
	l.message.Quote = "事件：eb1_00000000000000000000000000000000"
	m := message("unknown", "查一下")
	m.ParentID = "unknown-alert"
	accept(t, e, m)
	tick(t, e)
	if onlyTask(t, e).Phase != "clarify" || x.opens != 0 || x.sends != 0 || f.calls != 0 {
		t.Fatal("default bypassed unknown event boundary")
	}
}
