package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
)

func TestConfigTUIFeishuGroupsSelection(t *testing.T) {
	original := tuiTestSource(t).JSON
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := configFileSource(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := newConfigTUI(source)
	tuiType(m, "a")
	for _, value := range []string{"cli_extra", "ou_sample", "/sample/extra.secret", "/sample/extra"} {
		tuiType(m, value)
		tuiKeys(m, tea.KeyTab)
	}
	tuiKeys(m, tea.KeyRight, tea.KeyCtrlS)
	tuiSetField(t, m, "账号 · appSecretFile", "/sample/draft.secret")
	tuiSetField(t, m, "ACL · chats", `["oc_manual","oc_existing"]`)
	before := m.draft
	if !strings.Contains(m.View(), "F8 选择飞书群") || m.account != 1 {
		t.Fatalf("expected the group shortcut on the additional account: %s", m.View())
	}
	cmd := m.queryFeishuGroups(func(ctx context.Context, appID, secretFile, tenant string) ([]bridge.FeishuGroup, error) {
		if _, ok := ctx.Deadline(); !ok || appID != "cli_extra" || secretFile != "/sample/draft.secret" || tenant != "lark" {
			t.Fatalf("query did not use the additional account draft: %q %q %q", appID, secretFile, tenant)
		}
		return []bridge.FeishuGroup{
			{ID: "oc_existing", Name: "Existing"},
			{ID: "oc_new_a", Name: "New A"},
			{ID: "oc_new_b", Name: "New B"},
			{ID: "oc_new_a", Name: "New A again"},
		}, nil
	})
	m.Update(cmd())
	if m.screen != tuiGroupsPick || !strings.Contains(m.View(), "[已在草稿] Existing · oc_existing") {
		t.Fatalf("existing group not shown in picker: %s", m.View())
	}
	tuiKeys(m, tea.KeySpace, tea.KeyEnter)
	if m.screen != tuiGroupsPick || m.draft != before {
		t.Fatal("selecting an existing group changed the draft")
	}
	tuiKeys(m, tea.KeyDown, tea.KeySpace, tea.KeyDown, tea.KeySpace)
	if m.draft != before || !strings.Contains(m.View(), "已选 2") {
		t.Fatal("expected two staged groups before confirmation")
	}
	tuiKeys(m, tea.KeyEnter)
	after, err := bridge.ParseConfig([]byte(m.draft))
	if err != nil {
		t.Fatal(err)
	}
	initial, err := bridge.ParseConfig([]byte(before))
	if err != nil {
		t.Fatal(err)
	}
	if m.screen != tuiBrowse || !slices.Equal(after.Chats, initial.Chats) ||
		!slices.Equal(after.AdditionalAccounts[0].Chats, []string{"oc_manual", "oc_existing", "oc_new_a", "oc_new_b"}) ||
		after.AdditionalAccounts[0].ReceiverAuthorized != initial.AdditionalAccounts[0].ReceiverAuthorized {
		t.Fatalf("unexpected draft after confirmation: %s", m.draft)
	}
	if raw, err := os.ReadFile(path); err != nil || !bytes.Equal(raw, original) {
		t.Fatalf("confirmation changed the config file: %v", err)
	}
}

func TestConfigTUIFeishuGroupsCancelAndErrors(t *testing.T) {
	m := newConfigTUI(tuiTestSource(t))
	before := m.draft
	// F8 schedules the real command. Supply synthetic messages without running it.
	tuiKeys(m, tea.KeyF8)
	if m.screen != tuiGroupsLoading {
		t.Fatal("F8 did not start the lookup")
	}
	stale := m.groups.request
	tuiKeys(m, tea.KeyEsc, tea.KeyF8)
	result := configTUIFeishuGroupsResult{request: stale, groups: []bridge.FeishuGroup{{ID: "oc_new"}}}
	m.Update(result)
	if m.screen != tuiGroupsLoading {
		t.Fatal("a canceled lookup changed the current view")
	}
	m.groups.cancel()
	result.request = m.groups.request
	m.Update(result)
	tuiKeys(m, tea.KeySpace, tea.KeyEsc)
	if m.screen != tuiBrowse || m.draft != before {
		t.Fatal("canceling a selection changed the draft")
	}
	for _, tc := range []struct {
		err    error
		status string
	}{
		{nil, "机器人当前没有可选择的群"},
		{errors.New("无法读取群列表"), "无法读取群列表"},
	} {
		cmd := m.queryFeishuGroups(func(context.Context, string, string, string) ([]bridge.FeishuGroup, error) {
			return nil, tc.err
		})
		m.Update(cmd())
		if m.screen != tuiBrowse || m.draft != before || m.status != tc.status {
			t.Fatalf("unexpected empty or failed query behavior: %s", m.View())
		}
	}
}
