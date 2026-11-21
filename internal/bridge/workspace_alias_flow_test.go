package bridge

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestWorkspaceAliasPages(t *testing.T) {
	e, _, replies, l, _ := resumeFlowFixture(t, 1, false)
	scope := workspaceKey(e.Config, message("", ""), "nas")
	if err := e.Store.update(func(st *State) error {
		st.WorkspaceNames = map[string]map[string]Workspace{scope: {}}
		for i := 1; i <= 9; i++ {
			name := fmt.Sprintf("alias-%02d", i)
			st.WorkspaceNames[scope][name] = Workspace{ProjectID: name, Path: "/srv/" + name}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	first := resumeFlowCommand(t, e, "alias-list", "/ws list")
	if !strings.Contains(first.Reply, "1/2 页") || !strings.Contains(first.Reply, "alias-08") || strings.Contains(first.Reply, "alias-09") {
		t.Fatalf("unexpected first page: %s", first.Reply)
	}
	click := func(cmd *SessionCommand, label, event string) {
		t.Helper()
		body := replies.replies[len(replies.replies)-1]
		clickCommandCard(t, l, commandCardClick(t, cmd, body, label, event))
	}
	click(first, "下一页", "alias-next")
	second := consumeResumeFlowMessage(t, e, "card:alias-next")
	if second == nil || !strings.Contains(second.Reply, "2/2 页") || !strings.Contains(second.Reply, "alias-09") || strings.Contains(second.Reply, "alias-08") {
		t.Fatalf("unexpected second page: %+v", second)
	}
	click(second, "上一页", "alias-prev")
	if prev := consumeResumeFlowMessage(t, e, "card:alias-prev"); prev == nil || prev.Reply != first.Reply {
		t.Fatalf("return to first page: %+v", prev)
	}
	if manual := resumeFlowCommand(t, e, "alias-page2", "/ws list 2"); manual.Reply != second.Reply {
		t.Fatalf("explicit page differs: %s", manual.Reply)
	}
	for i, command := range []string{"/ws list 3", "/ws list 0", "/ws list text"} {
		if got := resumeFlowCommand(t, e, fmt.Sprintf("bad-page-%d", i), command); got.Card != nil || !strings.Contains(got.Reply, "页") {
			t.Fatalf("invalid page got %+v", got)
		}
	}
}

func TestWorkspaceAliasCardDeletePreservesCurrentRun(t *testing.T) {
	e, x, replies, l, history := resumeFlowFixture(t, 1, true)
	scope := workspaceKey(e.Config, history[0].Source, "nas")
	const name = "工作 一"
	alias := Workspace{ProjectID: history[0].Target.ProjectID, Path: "/srv/first"}
	remap := Workspace{ProjectID: "second", Path: "/srv/second"}
	setAlias := func(w Workspace) {
		t.Helper()
		if err := e.Store.update(func(st *State) error {
			st.WorkspaceNames = map[string]map[string]Workspace{scope: {name: w, "保留": alias}}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	setAlias(alias)
	open := func(id string) *SessionCommand { return resumeFlowCommand(t, e, id, "/ws list") }
	card := open("alias-before-change")
	body := replies.replies[len(replies.replies)-1]
	if !strings.Contains(body, `"confirm":`) || !strings.Contains(body, "确认删除「"+name+"」") {
		t.Fatalf("missing native confirmation: %s", body)
	}
	action := commandCardClick(t, card, body, "删除别名："+name, "alias-stale-delete")
	wrongUser := action
	wrongUser.Operator.OpenID = "other"
	clickCommandCard(t, l, wrongUser)
	if len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("card accepted a different user")
	}
	clickCommandCard(t, l, action)
	setAlias(remap)
	if stale := consumeResumeFlowMessage(t, e, "card:alias-stale-delete"); stale == nil || !strings.Contains(stale.Reply, "已变化") {
		t.Fatalf("stale card deletion returned %+v", stale)
	}
	if st := e.Store.snapshot(); st.WorkspaceNames[scope][name] != remap || st.Commands[key(card.Source, card.Source.ID)].Card.Used {
		t.Fatal("stale card deleted a changed alias or consumed the card")
	}
	card = open("alias-fresh-list")
	body = replies.replies[len(replies.replies)-1]
	action = commandCardClick(t, card, body, "删除别名："+name, "alias-delete")
	before := e.Store.snapshot()
	clickCommandCard(t, l, action)
	if result := consumeResumeFlowMessage(t, e, "card:alias-delete"); result == nil || !strings.Contains(result.Reply, "已删除") {
		t.Fatalf("delete did not produce a receipt: %+v", result)
	}
	after := e.Store.snapshot()
	if _, exists := after.WorkspaceNames[scope][name]; exists || after.WorkspaceNames[scope]["保留"] != alias || !after.Commands[key(card.Source, card.Source.ID)].Card.Used {
		t.Fatal("delete did not remove just the chosen alias and consume the card")
	}
	if !reflect.DeepEqual(before.Current, after.Current) || !reflect.DeepEqual(before.Workspaces, after.Workspaces) || !reflect.DeepEqual(before.Tasks, after.Tasks) || x.stops != 0 {
		t.Fatal("delete changed a workspace, session or run")
	}
	setAlias(remap)
	clickCommandCard(t, l, action)
	action.EventID = "alias-replay"
	clickCommandCard(t, l, action)
	if st := e.Store.snapshot(); len(st.Inbox) != 0 || st.WorkspaceNames[scope][name] != remap {
		t.Fatal("consumed card could delete a recreated alias")
	}
	if result := resumeFlowCommand(t, e, "alias-missing", "/ws remove 不存在"); !strings.Contains(result.Reply, "没有这个工作区别名") {
		t.Fatalf("text delete falsely succeeded: %s", result.Reply)
	}
	if result := resumeFlowCommand(t, e, "alias-text-delete", "/ws rm "+name); !strings.Contains(result.Reply, "已删除") {
		t.Fatalf("text delete required a project API: %s", result.Reply)
	}
}
