package main

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestConfigTUILiveGroupsConfirmAndPreserveDraft(t *testing.T) {
	m := newConfigTUI(tuiTestSource(t))
	tuiType(m, "a")
	for _, value := range []string{"cli_example", "ou_sample", "/sample/extra.secret", "/sample/extra"} {
		tuiType(m, value)
		tuiKeys(m, tea.KeyTab)
	}
	tuiKeys(m, tea.KeyRight, tea.KeyCtrlS)
	tuiSetField(t, m, "ACL · chats", `["oc_draft"]`)
	before := m.draft
	if m.account != 1 || !strings.Contains(m.View(), "F9 在线群权限") {
		t.Fatalf("missing F9 for the additional account: %s", m.View())
	}
	m.profiles.registry = profileRegistry{root: t.TempDir()}
	_, scheduled := m.Update(tea.KeyMsg{Type: tea.KeyF9})
	if scheduled == nil || m.screen != tuiLiveGroupsLoading {
		t.Fatal("F9 did not schedule a lookup")
	}
	tuiKeys(m, tea.KeyEsc)
	account := receiverProcessAccount{Tenant: "lark", AppID: "cli_example"}
	const id = "0123456789abcdef0123456789abcdef"
	matches := []receiverProcess{
		{ID: "unsupported", Accounts: []receiverProcessAccount{account}},
		{ID: "wrong-tenant", GroupAccess: true, Accounts: []receiverProcessAccount{{Tenant: "feishu", AppID: account.AppID}}},
		{ID: "wrong-app", GroupAccess: true, Accounts: []receiverProcessAccount{{Tenant: account.Tenant, AppID: "cli_other"}}},
		{ID: id, GroupAccess: true, Accounts: []receiverProcessAccount{account}},
	}
	var requests []bridge.GroupAccessRequest
	view := bridge.GroupAccessView{DefaultRequiresMention: true, MessageScope: bridge.GroupMessageScopeMissing, MessageEvent: "missing", Groups: []bridge.GroupAccessEntry{
		{ID: "oc_alpha", Name: "Alpha", Allowed: true, RequiresMention: true},
		{ID: "oc_beta", Name: "Beta", Allowed: true, RequiresMention: true},
	}}
	cmd := m.openLiveGroups(func() ([]receiverProcess, error) { return matches, nil }, func(ctx context.Context, process, tenant, appID string, req bridge.GroupAccessRequest) (bridge.GroupAccessView, error) {
		if _, ok := ctx.Deadline(); !ok || process != id || tenant != account.Tenant || appID != account.AppID {
			t.Fatalf("lookup used wrong target: %s %s/%s", process, tenant, appID)
		}
		requests = append(requests, req)
		if req.Access == "deny" {
			view.Groups[1].Allowed, view.Groups[1].AccessOverride = false, "deny"
			view.MessageScope = bridge.GroupMessageScopeUnknown
			view.MessageEvent = "subscribed"
		}
		return view, nil
	})
	if len(requests) != 0 || m.screen != tuiLiveGroupsLoading {
		t.Fatal("query must wait for Bubble Tea to execute its command")
	}
	m.Update(configTUILiveGroupsResult{request: m.liveGroups.request - 1})
	if m.screen != tuiLiveGroupsLoading {
		t.Fatal("result from a canceled lookup replaced the current lookup")
	}
	m.Update(cmd())
	if len(requests) != 1 || !reflect.DeepEqual(requests[0], bridge.GroupAccessRequest{}) || m.screen != tuiLiveGroupsPick ||
		!strings.Contains(m.View(), "lark/cli_example") || !strings.Contains(m.View(), "暂无法读取机器人所在群") ||
		!strings.Contains(m.View(), "飞书无 @ 群消息权限 · 缺失") || !strings.Contains(m.View(), "管理员私聊发送 /config mention authorize") ||
		!strings.Contains(m.View(), "线上消息事件订阅 · 未订阅") {
		t.Fatalf("unexpected query view or request: %s, %+v", m.View(), requests)
	}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	tuiType(m, "/")
	tuiType(m, "absent")
	tuiKeys(m, tea.KeyEnter)
	if display := m.View(); strings.Count(display, "\n") >= 12 ||
		!strings.Contains(display, "线上消息事件订阅 · 未订阅") ||
		!strings.Contains(display, "飞书无 @ 群消息权限 · 缺失") ||
		!strings.Contains(display, "/config mention authorize") ||
		!strings.Contains(display, "入群未知 · 无可显示群 · 搜索 · absent") ||
		!strings.Contains(display, "› 应用默认 · 需要 @") || !strings.Contains(display, "Esc 返回草稿") {
		t.Fatalf("filtered empty list and controls must fit 40×12: %s", display)
	}
	tuiType(m, "/")
	tuiKeys(m, tea.KeyCtrlU)
	tuiType(m, "beta")
	tuiKeys(m, tea.KeyEnter, tea.KeyDown, tea.KeyEnter, tea.KeyDown)
	if !strings.Contains(m.View(), "目标 · oc_beta") || !strings.Contains(m.View(), id) || !strings.Contains(m.View(), "› 撤销群授权") {
		t.Fatalf("confirmation does not identify the target and operation: %s", m.View())
	}
	tuiKeys(m, tea.KeyEnter, tea.KeyEsc)
	if len(requests) != 1 || m.screen != tuiLiveGroupsPick || m.draft != before {
		t.Fatal("navigation or cancellation submitted an operation")
	}
	tuiKeys(m, tea.KeyEnter, tea.KeyDown)
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil || len(requests) != 1 || m.screen != tuiLiveGroupsLoading {
		t.Fatal("confirmation did not schedule the write asynchronously")
	}
	m.Update(cmd())
	if want := (bridge.GroupAccessRequest{ChatID: "oc_beta", Access: "deny"}); len(requests) != 2 || !reflect.DeepEqual(requests[1], want) ||
		!strings.Contains(m.View(), "未授权(覆盖)") || !strings.Contains(m.View(), "飞书无 @ 群消息权限 · 未知") ||
		!strings.Contains(m.View(), "线上消息事件订阅 · 已订阅") ||
		strings.Contains(m.View(), "/config mention authorize") || m.draft != before {
		t.Fatalf("unexpected group update: %s, %+v", m.View(), requests)
	}
	tuiKeys(m, tea.KeyHome, tea.KeyEnter, tea.KeyEnd, tea.KeyDown, tea.KeyDown)
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m.Update(cmd())
	tuiKeys(m, tea.KeyEsc)
	if want := (bridge.GroupAccessRequest{Mention: "inherit"}); len(requests) != 3 || !reflect.DeepEqual(requests[2], want) ||
		m.screen != tuiBrowse || m.account != 1 || m.draft != before {
		t.Fatalf("unexpected default update or lost draft: %s, %+v", m.View(), requests)
	}
}

func TestConfigTUILiveGroupsBatchKeyboard(t *testing.T) {
	m := newConfigTUI(tuiTestSource(t))
	tuiSetField(t, m, "账号 · appId", "cli_example")
	before := m.draft
	account := receiverProcessAccount{Tenant: "feishu", AppID: "cli_example"}
	const processID = "0123456789abcdef0123456789abcdef"
	const alphaName = "Alpha with a group name wider than forty columns"
	view := bridge.GroupAccessView{JoinedAvailable: true, Groups: []bridge.GroupAccessEntry{
		{ID: "oc_allowed", Joined: true, Allowed: true},
		{ID: "oc_left"},
		{ID: "oc_alpha", Name: alphaName, Joined: true},
		{ID: "oc_beta", Name: "Beta", Joined: true},
	}}
	var requests []bridge.GroupAccessRequest
	cmd := m.openLiveGroups(func() ([]receiverProcess, error) {
		return []receiverProcess{{ID: processID, GroupAccess: true, GroupBatchGrant: true, Accounts: []receiverProcessAccount{account}}}, nil
	}, func(_ context.Context, process, tenant, app string, req bridge.GroupAccessRequest) (bridge.GroupAccessView, error) {
		if process != processID || tenant != account.Tenant || app != account.AppID {
			t.Fatalf("unexpected target %s %s/%s", process, tenant, app)
		}
		requests = append(requests, req)
		return view, nil
	})
	m.Update(cmd())
	tuiKeys(m, tea.KeySpace, tea.KeyDown, tea.KeySpace, tea.KeyDown, tea.KeySpace)
	tuiType(m, "b")
	if m.screen != tuiLiveGroupsPick || len(m.liveGroups.marked) != 0 {
		t.Fatal("the application row and ineligible groups must not be marked")
	}
	tuiKeys(m, tea.KeyDown, tea.KeySpace)
	tuiType(m, "/")
	tuiType(m, "beta")
	tuiKeys(m, tea.KeyEnter, tea.KeyDown, tea.KeySpace)
	tuiType(m, "b")
	if m.screen != tuiLiveGroupsConfirm || len(requests) != 1 || !reflect.DeepEqual(requests[0], bridge.GroupAccessRequest{}) {
		t.Fatalf("batch preview must follow one read and write nothing: %s", m.View())
	}

	for _, width := range []int{80, 40} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 12})
		seen := ""
		for range 12 {
			display := m.View()
			if len(strings.Split(display, "\n")) > 12 || !strings.Contains(display, "y 授权 · Esc 返回") {
				t.Fatalf("preview and shortcuts must fit %d×12: %s", width, display)
			}
			seen += display + "\n"
			tuiKeys(m, tea.KeyDown)
		}
		for _, want := range []string{"批量授权 · 2 个群", processID, "群名 · Alpha", "forty columns", "群 ID · oc_alpha", "群名 · Beta", "群 ID · oc_beta"} {
			if !strings.Contains(seen, want) {
				t.Fatalf("%q was not browsable at width %d: %s", want, width, seen)
			}
		}
		tuiKeys(m, tea.KeyPgUp, tea.KeyPgUp, tea.KeyPgUp)
	}

	tuiKeys(m, tea.KeyEnter, tea.KeyEsc)
	if m.screen != tuiLiveGroupsPick || m.liveGroups.filter != "beta" || !strings.Contains(m.View(), "[x]") ||
		!strings.Contains(m.View(), "b 预览(2)") || len(requests) != 1 {
		t.Fatalf("returning from the preview lost selection or wrote a request: %s", m.View())
	}
	tuiKeys(m, tea.KeySpace)
	if !strings.Contains(m.View(), "b 预览(1)") {
		t.Fatalf("Space did not unmark the visible group: %s", m.View())
	}
	tuiKeys(m, tea.KeySpace)
	tuiType(m, "b")
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil || m.screen != tuiLiveGroupsLoading || len(requests) != 1 {
		t.Fatal("y must schedule one asynchronous batch write")
	}
	tuiKeys(m, tea.KeyEsc)
	if m.screen != tuiLiveGroupsLoading {
		t.Fatal("the submitted write must remain pending until the receiver responds")
	}
	m.Update(cmd())
	want := bridge.GroupAccessRequest{ChatIDs: []string{"oc_alpha", "oc_beta"}, Access: "allow"}
	if len(requests) != 2 || !reflect.DeepEqual(requests[1], want) || m.screen != tuiLiveGroupsPick ||
		len(m.liveGroups.marked) != 0 || m.draft != before {
		t.Fatalf("unexpected batch write or final state: %+v, %s", requests, m.View())
	}
}

func TestConfigTUILiveGroupsBatchCapabilityAndLimit(t *testing.T) {
	account := receiverProcessAccount{Tenant: "feishu", AppID: "cli_example"}
	for _, tc := range []struct {
		name, target string
		event, label string
		batchGrant   bool
		joined       bool
		want         int
	}{
		{name: "old receiver", joined: true, label: "未知"},
		{name: "membership unknown", batchGrant: true, event: "unknown", label: "未知"},
		{name: "My Groups", target: "oc_0", batchGrant: true, joined: true, event: "subscribed", label: "已订阅"},
		{name: "batch limit", batchGrant: true, joined: true, want: bridge.MaxGroupGrantBatch, event: "unpublished", label: "应用未发布"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newConfigTUI(tuiTestSource(t))
			cmd := m.startLiveGroups(account, tc.target, func() ([]receiverProcess, error) {
				return []receiverProcess{{ID: "process", GroupAccess: true, GroupBatchGrant: tc.batchGrant, Accounts: []receiverProcessAccount{account}}}, nil
			}, func(context.Context, string, string, string, bridge.GroupAccessRequest) (bridge.GroupAccessView, error) {
				view := bridge.GroupAccessView{JoinedAvailable: tc.joined, MessageEvent: tc.event}
				for i := range bridge.MaxGroupGrantBatch + 1 {
					view.Groups = append(view.Groups, bridge.GroupAccessEntry{ID: fmt.Sprintf("oc_%d", i), Joined: true})
				}
				return view, nil
			})
			m.Update(cmd())
			m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
			tuiKeys(m, tea.KeyHome)
			for range bridge.MaxGroupGrantBatch + 1 {
				tuiKeys(m, tea.KeyDown, tea.KeySpace)
			}
			display := m.View()
			if strings.Count(display, "\n") >= 12 || !strings.Contains(display, "线上消息事件订阅 · "+tc.label) ||
				!strings.Contains(display, fmt.Sprintf("oc_%d", bridge.MaxGroupGrantBatch)) || !strings.Contains(display, "Enter") {
				t.Fatalf("end of the group list and controls must fit 40×12: %s", display)
			}
			for _, line := range strings.Split(display, "\n") {
				if ansi.StringWidth(line) > 40 {
					t.Fatalf("group list line exceeds terminal width: %q", line)
				}
			}
			if len(m.liveGroups.marked) != tc.want || strings.Contains(m.View(), "Space 标记") != (tc.want > 0) {
				t.Fatalf("unexpected marked groups or batch controls: %s", m.View())
			}
			if tc.want > 0 && !strings.Contains(m.status, "最多选择 32 个群") {
				t.Fatalf("the extra selection was not explained: %s", m.View())
			}
			if tc.want == 0 {
				tuiType(m, "b")
				if m.screen != tuiLiveGroupsPick {
					t.Fatalf("batch review opened without support: %s", m.View())
				}
				tuiKeys(m, tea.KeyEnter)
				if m.screen != tuiLiveGroupsConfirm || m.liveGroups.batchReview {
					t.Fatalf("single-group review is unavailable: %s", m.View())
				}
			}
		})
	}
}

func TestConfigTUILiveGroupsRequiresUniqueMatchingProcess(t *testing.T) {
	m := newConfigTUI(tuiTestSource(t))
	tuiSetField(t, m, "账号 · appId", "cli_example")
	account := receiverProcessAccount{Tenant: "feishu", AppID: "cli_example"}
	for _, tc := range []struct {
		processes []receiverProcess
		message   string
	}{
		{message: "没有支持在线群权限"},
		{
			processes: []receiverProcess{
				{ID: "first", GroupAccess: true, Accounts: []receiverProcessAccount{account}},
				{ID: "second", GroupAccess: true, Accounts: []receiverProcessAccount{account}},
			},
			message: "config live-groups -process",
		},
	} {
		cmd := m.openLiveGroups(func() ([]receiverProcess, error) { return tc.processes, nil }, func(context.Context, string, string, string, bridge.GroupAccessRequest) (bridge.GroupAccessView, error) {
			t.Fatal("an unmatched or ambiguous process must never be queried")
			return bridge.GroupAccessView{}, nil
		})
		m.Update(cmd())
		if m.screen != tuiBrowse || !strings.Contains(m.status, tc.message) {
			t.Fatalf("expected %q: %s", tc.message, m.View())
		}
	}
}

func TestConfigTUIUserGroupsOpensExactLiveGroup(t *testing.T) {
	r, receiver := testGroupAccessRegistry(t)
	other, err := r.register([]bridge.Config{{AppID: "cli_shared"}, {Tenant: "lark", AppID: "cli_other"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.close() })
	m := newConfigTUI(tuiTestSource(t))
	m.profiles.registry = profileRegistry{root: filepath.Dir(r.dir)}
	tuiSetField(t, m, "账号 · appId", "cli_primary")
	tuiType(m, "a")
	for _, value := range []string{"cli_shared", "ou_sample", "/sample/extra.secret", "/sample/extra"} {
		tuiType(m, value)
		tuiKeys(m, tea.KeyTab)
	}
	tuiKeys(m, tea.KeyRight, tea.KeyCtrlS)
	tuiSetField(t, m, "ACL · chats", `["oc_draft"]`)
	before := m.draft
	account := receiverProcessAccount{Tenant: "lark", AppID: "cli_shared"}
	tuiKeys(m, tea.KeyF10)
	deliver := func(msg configTUIUserGroupsResult) {
		t.Helper()
		if m.screen != tuiUserGroupsLoading || m.userGroups.cancel == nil {
			t.Fatalf("no user group query is pending: %s", m.View())
		}
		m.userGroups.cancel()
		msg.request = m.userGroups.request
		m.Update(msg)
	}
	deliver(configTUIUserGroupsResult{status: bridge.UserGroupsStatus{ListAuthorized: true}})
	deliver(configTUIUserGroupsResult{})
	tuiType(m, "/")
	tuiType(m, "Example")
	tuiKeys(m, tea.KeyEnter)
	deliver(configTUIUserGroupsResult{page: bridge.UserGroupsPage{NextPageToken: "next"}})
	tuiType(m, "]")
	const chatID = "oc_target"
	deliver(configTUIUserGroupsResult{page: bridge.UserGroupsPage{BotKnown: true, Groups: []bridge.UserGroup{
		{ID: "oc_other", Name: "Example", BotJoined: true},
		{ID: chatID, Name: "Example", BotJoined: true},
	}}})
	tuiKeys(m, tea.KeyDown)
	assertReturned := func(status string) {
		t.Helper()
		g := m.userGroups
		if m.screen != tuiUserGroupsPick || m.status != status || m.account != 1 || m.draft != before || g.query != "Example" ||
			!slices.Equal(g.pages, []string{"", "next"}) || g.row != 1 || len(g.page.Groups) != 2 || g.page.Groups[g.row].ID != chatID {
			t.Fatalf("the original group selection or draft was lost: %s", m.View())
		}
	}
	if view := m.View(); !strings.Contains(view, "a 在线群授权") || !strings.Contains(view, "我的群 · lark/cli_shared") {
		t.Fatalf("missing shortcut or selected app: %s", view)
	}
	open := func() tea.Cmd {
		t.Helper()
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
		if cmd == nil || m.screen != tuiLiveGroupsLoading {
			t.Fatalf("shortcut did not schedule a live query: %s", m.View())
		}
		return cmd
	}
	duplicate, err := r.register([]bridge.Config{{Tenant: account.Tenant, AppID: account.AppID}})
	if err != nil {
		t.Fatal(err)
	}
	cmd := open()
	result := cmd().(configTUILiveGroupsResult)
	if err := duplicate.close(); err != nil {
		t.Fatal(err)
	}
	if result.err == nil || result.process != "" {
		t.Fatalf("ambiguous receivers were accepted: %+v", result)
	}
	m.Update(result)
	assertReturned(userGroupLiveUnavailable)

	canceled := open()
	tuiKeys(m, tea.KeyEsc)
	assertReturned("")
	cmd = open()
	m.Update(canceled())
	if m.screen != tuiLiveGroupsLoading {
		t.Fatal("canceled lookup replaced the active lookup")
	}
	pending := receiverGroupPendingPath(receiver.dir, account)
	complete := func(cmd tea.Cmd, want bridge.GroupAccessRequest, view bridge.GroupAccessView) {
		t.Helper()
		if cmd == nil {
			t.Fatal("no command scheduled")
		}
		done := make(chan tea.Msg, 1)
		go func() { done <- cmd() }()
		command := waitGroupAccessPending(t, r, pending)
		want.IncludeMessageEvent = true
		if command.Instance != receiver.process.ID || command.Account != account || command.Request == nil || !reflect.DeepEqual(*command.Request, want) {
			t.Fatalf("unexpected live permissions request: %+v", command)
		}
		receiver.serveGroupAccess(t.Context(), account, pending, func(context.Context, bridge.GroupAccessRequest) (bridge.GroupAccessView, error) {
			return view, nil
		})
		m.Update(<-done)
	}
	view := bridge.GroupAccessView{JoinedAvailable: true, Groups: []bridge.GroupAccessEntry{
		{ID: "oc_target_suffix", Name: "Example", Joined: true},
		{ID: chatID, Name: "Example", Joined: true},
		{ID: "oc_other", Name: "Example", Joined: true},
	}}
	complete(cmd, bridge.GroupAccessRequest{}, view)
	if display := m.View(); m.screen != tuiLiveGroupsPick || m.liveGroups.row != 2 || !strings.Contains(display, "lark/cli_shared") || !strings.Contains(display, "Esc 返回我的群") {
		t.Fatalf("the exact group or running app is missing: %s", display)
	}
	_, enter := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if display := m.View(); enter != nil || m.screen != tuiLiveGroupsConfirm || m.liveGroups.target.ID != chatID || !strings.Contains(display, receiver.process.ID) {
		t.Fatalf("Enter did not preview the exact group without writing: %s", display)
	}
	_, back := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if back != nil || m.screen != tuiLiveGroupsPick || m.draft != before {
		t.Fatal("canceling the confirmation wrote or lost the draft")
	}
	tuiKeys(m, tea.KeyEnter)
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	view.Groups[1].Allowed, view.Groups[1].AccessOverride = true, "allow"
	complete(cmd, bridge.GroupAccessRequest{ChatID: chatID, Access: "allow"}, view)
	if m.screen != tuiLiveGroupsPick || m.liveGroups.row != 2 || !strings.Contains(m.View(), "在线群权限已更新") {
		t.Fatalf("the confirmed update did not preserve the selection: %s", m.View())
	}
	tuiKeys(m, tea.KeyEsc)
	assertReturned("")
}

func TestConfigTUILiveGroupsDoesNotFallBackFromUserGroup(t *testing.T) {
	const chatID = "oc_target"
	for _, tc := range []struct {
		name string
		view bridge.GroupAccessView
		err  error
	}{
		{name: "receiver unavailable", err: errReceiverGroupUnsupported},
		{name: "membership unknown", view: bridge.GroupAccessView{Groups: []bridge.GroupAccessEntry{{ID: chatID, Joined: true}}}},
		{name: "bot left", view: bridge.GroupAccessView{JoinedAvailable: true, Groups: []bridge.GroupAccessEntry{{ID: chatID, Allowed: true}}}},
		{name: "exact group missing", view: bridge.GroupAccessView{JoinedAvailable: true, Groups: []bridge.GroupAccessEntry{{ID: "oc_target_suffix", Joined: true}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newConfigTUI(tuiTestSource(t))
			account := receiverProcessAccount{Tenant: "feishu", AppID: "cli_example"}
			cmd := m.startLiveGroups(account, chatID, func() ([]receiverProcess, error) {
				return []receiverProcess{{ID: "process", GroupAccess: true, Accounts: []receiverProcessAccount{account}}}, nil
			}, func(_ context.Context, _, _, _ string, req bridge.GroupAccessRequest) (bridge.GroupAccessView, error) {
				if !reflect.DeepEqual(req, bridge.GroupAccessRequest{}) {
					t.Fatalf("navigation is not a read-only query: %+v", req)
				}
				return tc.view, tc.err
			})
			m.Update(cmd())
			if m.screen != tuiUserGroupsPick || m.status != userGroupLiveUnavailable {
				t.Fatalf("expected a fixed prompt on return to My Groups, got screen %d, status %q", m.screen, m.status)
			}
		})
	}
}
