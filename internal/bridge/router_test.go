package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func TestAnnotatedRoutes(t *testing.T) {
	b, err := os.ReadFile("../../testdata/routes.json")
	if err != nil {
		t.Fatal(err)
	}
	var samples []struct{ ID, Category, Text, RouteID, Action, Bound string }
	if err = json.Unmarshal(b, &samples); err != nil {
		t.Fatal(err)
	}
	if len(samples) < 40 {
		t.Fatal("need at least 40 annotated samples")
	}
	correct := 0
	c := testConfig(t)
	names, err := os.ReadFile("../../testdata/route-names.json")
	if err != nil {
		t.Fatal(err)
	}
	var migration struct{ Routes map[string]Target }
	if err = json.Unmarshal(names, &migration); err != nil {
		t.Fatal(err)
	}
	for id, names := range migration.Routes {
		target := c.Routes[id]
		target.HostName, target.Aliases = names.HostName, names.Aliases
		c.Routes[id] = target
	}
	for _, s := range samples {
		t.Run(s.ID+"/"+s.Category, func(t *testing.T) {
			var bound *Task
			if s.Bound != "" {
				bound = &Task{RouteID: s.Bound}
			}
			d, err := Route(context.Background(), c, message(s.ID, s.Text), bound, nil)
			if err != nil || d.Action != s.Action || d.RouteID != s.RouteID {
				t.Errorf("want %s/%s, got %+v %v", s.Action, s.RouteID, d, err)
				return
			}
			correct++
		})
	}
	t.Logf("rules/offline clarification accuracy: %d/%d; live Hy4 not measured", correct, len(samples))
	if correct*100 < len(samples)*95 {
		t.Fatal("below 95 percent")
	}
}
func TestClassifierBoundary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		d       Decision
		err     error
		wantErr bool
		action  string
	}{
		{"approved", Decision{"nas", "new_task"}, nil, false, "new_task"},
		{"unknown_route", Decision{"vps", "new_task"}, nil, true, ""},
		{"unknown_action", Decision{"nas", "execute"}, nil, true, ""},
		{"unbound_continue", Decision{"nas", "continue"}, nil, false, "clarify"},
		{"timeout", Decision{}, context.DeadlineExceeded, true, ""},
		{"unavailable", Decision{}, errors.New("unavailable"), true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testConfig(t)
			f := &fakeClassifier{d: tc.d, err: tc.err}
			d, err := Route(context.Background(), c, message("id", "请核对那台机器"), nil, f)
			if (err != nil) != tc.wantErr || (!tc.wantErr && d.Action != tc.action) {
				t.Fatalf("decision=%+v err=%v", d, err)
			}
		})
	}
	c := testConfig(t)
	f := &fakeClassifier{err: errors.New("must not call")}
	for _, m := range []Message{message("one", "NAS检查磁盘"), message("two", "继续")} {
		var bound *Task
		if m.ID == "two" {
			bound = &Task{RouteID: "nas"}
		}
		if _, err := Route(context.Background(), c, m, bound, f); err != nil {
			t.Fatal(err)
		}
	}
	if f.calls != 0 {
		t.Fatal("rule/binding called model")
	}
}
func TestStrictDecisionJSON(t *testing.T) {
	for _, raw := range []string{`{"routeId":"nas","action":"new_task","provider":"evil"}`, `{"routeId":"nas"} {}`, `{"routeId":"nas"} garbage`, `null trailing`} {
		var d Decision
		if decodeStrict([]byte(raw), &d) == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestExplicitRouteQuotedReplyAndLimits(t *testing.T) {
	c := testConfig(t)
	for _, tc := range []struct{ text, route string }{
		{`/nas 只回复“已接通”，不要执行命令`, "nas"},
		{`/nas 不要执行命令，只回复“已接通”`, "nas"},
		{`/nas 写一篇介绍Mac和NAS的文章，引用“不要执行”`, "nas"},
		{`/mac 只回复"已接通"，不要升级NAS`, "mac"},
	} {
		for _, bound := range []bool{false, true} {
			t.Run(tc.text+map[bool]string{false: "/new", true: "/bound"}[bound], func(t *testing.T) {
				m := message("one", tc.text)
				var task *Task
				action := "new_task"
				if bound {
					task = &Task{RouteID: tc.route}
					action = "continue"
				}
				f := &fakeClassifier{err: errors.New("explicit route must not call classifier")}
				d, err := Route(context.Background(), c, m, task, f)
				if err != nil || d != (Decision{RouteID: tc.route, Action: action}) || f.calls != 0 || m.Text != tc.text {
					t.Fatalf("decision=%+v err=%v original=%q", d, err, m.Text)
				}
			})
		}
	}
}

func TestExplicitRouteControlAndBinding(t *testing.T) {
	c := testConfig(t)
	for _, body := range []string{"停止", "拒绝", "不要执行", "取消这个任务", "/stop"} {
		for _, bound := range []bool{false, true} {
			t.Run(body+map[bool]string{false: "/new", true: "/bound"}[bound], func(t *testing.T) {
				var task *Task
				if bound {
					task = &Task{RouteID: "nas"}
				}
				d, err := Route(context.Background(), c, message("one", "/nas "+body), task, nil)
				if err != nil || d.Action != "stop" {
					t.Fatalf("decision=%+v err=%v", d, err)
				}
			})
		}
	}
	for _, text := range []string{`/mac 停止`, `/mac 只回复“已接通”，不要执行命令`} {
		d, err := Route(context.Background(), c, message("one", text), &Task{RouteID: "nas"}, nil)
		if err != nil || d.Action != "clarify" {
			t.Fatal("cross-target binding bypass", d, err)
		}
	}
	for _, text := range []string{`引用“/nas 重启服务”`, `写文章介绍NAS，不要执行命令`, `不要在NAS上升级`, `> /nas 重启服务`} {
		d, err := Route(context.Background(), c, message("one", text), nil, nil)
		if err != nil || d.Action != "clarify" {
			t.Fatal("mentioned machine became execution target", d, err)
		}
	}
}
