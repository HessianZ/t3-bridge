package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
)

func TestConfigTUIGroupFilterKeepsSelections(t *testing.T) {
	m := newConfigTUI(tuiTestSource(t))
	m.screen = tuiGroupsLoading
	m.groups.chats = []string{"oc_manual"}
	m.Update(configTUIFeishuGroupsResult{groups: []bridge.FeishuGroup{
		{ID: "oc_a", Name: "Quality"}, {ID: "oc_review", Name: "Review"}, {ID: "oc_unselected", Name: "Others"},
	}})
	tuiType(m, "/")
	tuiType(m, "QUALITY")
	tuiKeys(m, tea.KeyEnter)
	if view := m.View(); m.screen != tuiGroupsPick || !strings.Contains(view, "Quality") || strings.Contains(view, "Others") {
		t.Fatalf("name filter did not select the group: %s", view)
	}
	tuiKeys(m, tea.KeySpace)
	tuiType(m, "/")
	m.input.SetValue("oc_review")
	tuiKeys(m, tea.KeyEnter, tea.KeySpace, tea.KeyEnter)
	config, err := bridge.ParseConfig([]byte(m.draft))
	if err != nil || m.screen != tuiBrowse || !slices.Equal(config.Chats, []string{"oc_manual", "oc_a", "oc_review"}) {
		t.Fatalf("filter discarded selection, changed account, or lost manual group: %v, %v", config.Chats, err)
	}
}
