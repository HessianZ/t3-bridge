package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
)

func TestConfigTUIUserGroupsUsesCurrentAccountAndPreservesDraft(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	original := tuiTestSource(t).JSON
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := configFileSource(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := newConfigTUI(source)
	m.profiles.registry = profileRegistry{root: dir}
	tuiSetField(t, m, "账号 · appId", "cli_primary")
	tuiType(m, "a")
	for _, value := range []string{"cli_extra", "ou_sample", filepath.Join(dir, "extra.secret"), filepath.Join(dir, "extra")} {
		tuiType(m, value)
		tuiKeys(m, tea.KeyTab)
	}
	tuiKeys(m, tea.KeyRight, tea.KeyCtrlS, tea.KeyTab)
	if view := m.View(); !strings.Contains(view, "F8 选择飞书群") || !strings.Contains(view, "F9 在线群权限") || !strings.Contains(view, "F10 我的群") {
		t.Fatalf("group shortcuts are not visible together: %s", view)
	}
	tuiKeys(m, tea.KeyF10)
	if m.screen != tuiUserGroupsLoading || m.userGroups.account != (receiverProcessAccount{Tenant: "feishu", AppID: "cli_primary"}) {
		t.Fatalf("primary-account lookup did not start: %s", m.View())
	}
	oldRequest := m.userGroups.request
	tuiKeys(m, tea.KeyEsc, tea.KeyTab)
	tuiSetField(t, m, "ACL · chats", `["oc_manual"]`)
	before := m.draft
	tuiKeys(m, tea.KeyF10)
	m.Update(configTUIUserGroupsResult{request: oldRequest, status: bridge.UserGroupsStatus{ListAuthorized: true}})
	if m.screen != tuiUserGroupsLoading || m.userGroups.action != "status" || m.userGroups.account != (receiverProcessAccount{Tenant: "lark", AppID: "cli_extra"}) {
		t.Fatalf("late primary-account result replaced the secondary lookup: %s", m.View())
	}
	// Supply synthetic responses without executing the commands returned by Bubble Tea.
	deliver := func(msg configTUIUserGroupsResult) {
		t.Helper()
		if m.screen != tuiUserGroupsLoading || m.userGroups.cancel == nil {
			t.Fatalf("no request is pending: %s", m.View())
		}
		m.userGroups.cancel()
		msg.request = m.userGroups.request
		m.Update(msg)
	}
	deliver(configTUIUserGroupsResult{})
	tuiType(m, "i")
	challenge := bridge.UserGroupsChallenge{Ticket: "opaque-ticket", URL: "https://accounts.larksuite.com/example", ExpiresAt: time.Now().Add(time.Minute)}
	deliver(configTUIUserGroupsResult{challenge: challenge})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	if view := m.View(); !strings.Contains(view, challenge.URL) || strings.Contains(view, challenge.Ticket) {
		t.Fatalf("expected the authorization URL in the compact view: %s", view)
	}
	tuiKeys(m, tea.KeyEnter)
	deliver(configTUIUserGroupsResult{err: context.DeadlineExceeded})
	if m.screen != tuiUserGroupsAuthorize || !strings.Contains(m.status, "超时") {
		t.Fatalf("authorization timeout did not allow manual retry: %s", m.View())
	}
	tuiKeys(m, tea.KeyEnter)
	deliver(configTUIUserGroupsResult{status: bridge.UserGroupsStatus{ListAuthorized: true, InviteAuthorized: true}})
	page := bridge.UserGroupsPage{NextPageToken: "next", Groups: []bridge.UserGroup{{ID: "oc_target", Name: "Example group"}}}
	deliver(configTUIUserGroupsResult{page: page})
	_, shortcut := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if shortcut != nil || m.screen != tuiUserGroupsPick || m.status != userGroupLiveUnavailable || strings.Contains(m.View(), "a 在线群授权") || m.draft != before {
		t.Fatalf("opening online permissions with unknown membership was allowed: %s", m.View())
	}
	tuiKeys(m, tea.KeyEnter)
	if m.draft != before || !strings.Contains(m.status, "确认机器人已入群") {
		t.Fatal("adding a group with unknown membership was allowed")
	}
	tuiType(m, "]")
	if !slices.Equal(m.userGroups.pages, []string{"", "next"}) {
		t.Fatal("next page did not use the next page token")
	}
	deliver(configTUIUserGroupsResult{})
	tuiType(m, "[")
	if !slices.Equal(m.userGroups.pages, []string{""}) {
		t.Fatal("previous page did not restore its token")
	}
	deliver(configTUIUserGroupsResult{page: page})
	tuiType(m, "/")
	tuiType(m, "Example")
	tuiKeys(m, tea.KeyEnter)
	if m.userGroups.query != "Example" || len(m.userGroups.pages) != 1 {
		t.Fatal("search did not reset pagination")
	}
	deliver(configTUIUserGroupsResult{page: page})
	tuiType(m, "v")
	if view := m.View(); m.screen != tuiUserGroupsConfirm || !strings.Contains(view, "lark/cli_extra") || !strings.Contains(view, "Example group") || !strings.Contains(view, "oc_target") {
		t.Fatalf("confirmation is missing the app or group: %s", view)
	}
	tuiKeys(m, tea.KeyEnter)
	send, _ := m.handleKey("y")
	repeat, _ := m.handleKey("y")
	if send == nil || repeat != nil || m.userGroups.action != "invite" {
		t.Fatal("unexpected confirmation or in-flight invitation handling")
	}
	deliver(configTUIUserGroupsResult{invitation: bridge.UserGroupInvitation{State: "pending"}})
	tuiKeys(m, tea.KeyEnter)
	if m.draft != before || m.screen != tuiUserGroupsPick {
		t.Fatal("an invitation that is pending changed the draft")
	}
	tuiType(m, "r")
	page.BotKnown, page.Groups[0].BotJoined = true, true
	deliver(configTUIUserGroupsResult{page: page})
	tuiKeys(m, tea.KeyEnter, tea.KeyEnter)
	config, err := bridge.ParseConfig([]byte(m.draft))
	if err != nil || !slices.Equal(config.AdditionalAccounts[0].Chats, []string{"oc_manual", "oc_target"}) || slices.Contains(config.Chats, "oc_target") {
		t.Fatalf("confirmed group did not append once to the selected draft: %s, %v", m.draft, err)
	}
	if raw, err := os.ReadFile(path); err != nil || !bytes.Equal(raw, original) {
		t.Fatalf("the configuration file was changed without saving: %v", err)
	}
}
