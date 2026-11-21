package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
)

func TestConfigTUILiveAccessConfirmAndPreserveDraft(t *testing.T) {
	m := newConfigTUI(tuiTestSource(t))
	dir := t.TempDir()
	tuiType(m, "a")
	for _, value := range []string{"cli_access", "ou_sample", filepath.Join(dir, "extra.secret"), filepath.Join(dir, "extra")} {
		tuiType(m, value)
		tuiKeys(m, tea.KeyTab)
	}
	tuiKeys(m, tea.KeyRight, tea.KeyCtrlS)
	before := m.draft
	if m.account != 1 || !strings.Contains(m.View(), "F12 在线用户权限") {
		t.Fatalf("missing F12 on the additional account: %s", m.View())
	}
	m.profiles.registry = profileRegistry{root: t.TempDir()}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyF12})
	if cmd == nil || m.screen != tuiLiveAccessLoading {
		t.Fatal("F12 did not schedule a lookup")
	}
	tuiKeys(m, tea.KeyEsc)

	account := receiverProcessAccount{Tenant: "lark", AppID: "cli_access"}
	const id = "0123456789abcdef0123456789abcdef"
	processes := []receiverProcess{
		{ID: "unsupported", Accounts: []receiverProcessAccount{account}},
		{ID: "wrong-tenant", AccessPolicy: true, Accounts: []receiverProcessAccount{{Tenant: "feishu", AppID: account.AppID}}},
		{ID: id, AccessPolicy: true, Accounts: []receiverProcessAccount{{Tenant: "feishu", AppID: "cli_other"}, account}},
	}
	var requests []bridge.LocalAccessPolicyRequest
	view := bridge.LocalAccessPolicyView{
		OwnerID: "ou_owner", RouteIDs: []string{"Work.Team"},
		Entries: []bridge.LocalAccessPolicyEntry{
			{Kind: "user", UserID: "ou_member", Configured: true, Override: "inherit", Effective: true},
			{Kind: "admin", UserID: "ou_admin", Override: "allow", Effective: true},
		},
	}
	cmd = m.openLiveAccess(func() ([]receiverProcess, error) { return processes, nil }, func(ctx context.Context, process, tenant, appID string, req bridge.LocalAccessPolicyRequest) (bridge.LocalAccessPolicyView, error) {
		if _, timed := ctx.Deadline(); !timed || process != id || tenant != account.Tenant || appID != account.AppID {
			t.Fatalf("wrong target %s %s/%s", process, tenant, appID)
		}
		requests = append(requests, req)
		switch req.Kind {
		case "user":
			view.Entries[0].Override, view.Entries[0].Effective = req.Mode, false
		case "route":
			view.Entries = append(view.Entries, bridge.LocalAccessPolicyEntry{Kind: "route", UserID: req.UserID, RouteID: req.RouteID, Override: req.Mode, Effective: true})
		}
		return view, nil
	})
	m.Update(configTUILiveAccessResult{request: m.liveAccess.request - 1})
	if m.screen != tuiLiveAccessLoading {
		t.Fatal("a stale result finished the matching request")
	}
	m.Update(cmd())
	if len(requests) != 1 || requests[0] != (bridge.LocalAccessPolicyRequest{}) || m.screen != tuiLiveAccessPick {
		t.Fatalf("initial screen or request is wrong: %s, %+v", m.View(), requests)
	}
	for _, value := range []string{"配置 已列入", "覆盖 跟随配置", "生效 已授权", "管理员", "ou_owner"} {
		if !strings.Contains(m.View(), value) {
			t.Fatalf("read table omitted %q: %s", value, m.View())
		}
	}

	tuiKeys(m, tea.KeyEnter, tea.KeyDown, tea.KeyDown, tea.KeyEnter)
	for _, value := range []string{id, "lark/cli_access", "权限 · 用户", "ou_member", "覆盖 · 跟随配置 → 拒绝"} {
		if m.screen != tuiLiveAccessConfirm || !strings.Contains(m.View(), value) {
			t.Fatalf("confirmation omitted %q: %s", value, m.View())
		}
	}
	tuiKeys(m, tea.KeyEnter, tea.KeyEsc)
	if m.screen != tuiLiveAccessEdit || len(requests) != 1 {
		t.Fatal("navigation or cancellation submitted a write")
	}
	tuiKeys(m, tea.KeyEnter)
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil || len(requests) != 1 || m.screen != tuiLiveAccessLoading {
		t.Fatal("confirmation did not schedule an asynchronous update")
	}
	m.Update(cmd())
	wantUser := bridge.LocalAccessPolicyRequest{Kind: "user", UserID: "ou_member", Mode: "deny"}
	if len(requests) != 2 || requests[1] != wantUser || !strings.Contains(m.View(), "覆盖 拒绝 · 生效 未授权") {
		t.Fatalf("existing user write or readback is wrong: %+v, %s", requests, m.View())
	}

	tuiType(m, "a")
	tuiKeys(m, tea.KeyDown, tea.KeyDown, tea.KeyEnter)
	tuiType(m, "member")
	tuiKeys(m, tea.KeyEnter)
	if m.screen != tuiLiveAccessUser || m.status == "" {
		t.Fatal("an invalid Open ID reached the edit screen")
	}
	m.input.SetValue("ou_new")
	tuiKeys(m, tea.KeyEnter, tea.KeyDown, tea.KeyEnter)
	if m.screen != tuiLiveAccessConfirm || !strings.Contains(m.View(), "路由 · Work.Team") || !strings.Contains(m.View(), "ou_new") {
		t.Fatalf("new route rule confirmation omitted its target: %s", m.View())
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Fatal("confirming the route rule did not schedule an update")
	}
	m.Update(cmd())
	wantRoute := bridge.LocalAccessPolicyRequest{Kind: "route", UserID: "ou_new", RouteID: "Work.Team", Mode: "allow"}
	if len(requests) != 3 || requests[2] != wantRoute || !strings.Contains(m.View(), "Work.Team · ou_new") {
		t.Fatalf("new route write or readback is wrong: %+v, %s", requests, m.View())
	}

	tuiKeys(m, tea.KeyEsc)
	if m.screen != tuiBrowse || m.account != 1 || m.draft != before {
		t.Fatal("online access changed the startup draft or account")
	}
}

func TestConfigTUILiveAccessRequiresUniqueMatchingProcess(t *testing.T) {
	account := receiverProcessAccount{Tenant: "feishu", AppID: "cli_access"}
	for _, tc := range []struct {
		processes []receiverProcess
		message   string
	}{
		{message: "没有支持在线用户权限"},
		{processes: []receiverProcess{
			{ID: "first", AccessPolicy: true, Accounts: []receiverProcessAccount{account}},
			{ID: "second", AccessPolicy: true, Accounts: []receiverProcessAccount{account}},
		}, message: "config live-access -process"},
	} {
		id, err := liveAccessProcess(func() ([]receiverProcess, error) { return tc.processes, nil }, account)
		if id != "" || err == nil || !strings.Contains(err.Error(), tc.message) {
			t.Fatalf("expected %q, got %q and %v", tc.message, id, err)
		}
	}
}
