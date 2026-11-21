package main

import (
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestConfigTUICatalogOptionsKeyboard(t *testing.T) {
	m := newConfigTUI(tuiTestSource(t))
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	tuiSelect(t, m, `路由 · "work"`)
	tuiKeys(m, tea.KeyEnter)
	tuiSetField(t, m, "目标 · environmentId", "target")
	tuiSetField(t, m, "目标 · providerId", "provider-sample")
	tuiSetField(t, m, "目标 · model", "model-sample")
	tuiSetField(t, m, "执行 · options", `{"Mixed.Flag":true,"Effort":"fast","Stale":"retired","Remove":"present","Hidden":9007199254740993}`)
	before := m.draft

	model := bridge.T3Model{
		ID: "model-sample", Label: "Model Example",
		Options: []bridge.T3ModelOption{
			{ID: "Mixed.Flag", Values: []any{true, false}},
			{ID: "Effort", Values: []any{"fast", "deep"}},
			{ID: "Stale", Values: []any{"available"}},
			{ID: "Remove", Values: []any{"present"}},
		},
	}
	result := bridge.T3Catalog{
		EnvironmentID: "target", LocalEnvironmentID: "local", LinkAccess: "read-only", OperateAccess: "missing",
		Projects: []bridge.Project{{ID: "project-sample", Title: "Example", Root: "/example"}},
		T3Capabilities: bridge.T3Capabilities{
			RuntimeMode: "read-only",
			Providers: []bridge.T3Provider{{
				ID: "provider-sample", Driver: "codex",
				Models: []bridge.T3Model{{ID: "other-model"}, model},
			}},
		},
	}
	tuiKeys(m, tea.KeyF5, tea.KeyEnter)
	m.catalog.cancel()
	m.Update(configTUICatalogResult{request: m.catalog.request, result: result})
	tuiKeys(m, tea.KeyEnter, tea.KeyDown, tea.KeyEnter)
	view := m.View()
	for _, want := range []string{
		"› Mixed.Flag = true", `Effort = "fast"`, `保留现值（目录未提供） "retired"`,
		catalogOperateAccessWarning, "关联权限 read-only", "←→", "Esc 返回模型",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q in option editor:\n%s", want, view)
		}
	}
	lines := strings.Split(view, "\n")
	if len(lines) > 12 {
		t.Fatalf("option editor exceeds window height:\n%s", view)
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > 80 {
			t.Fatalf("option editor exceeds window width: %q", line)
		}
	}
	edit := []tea.KeyType{tea.KeyRight, tea.KeyDown, tea.KeyRight, tea.KeyEnd, tea.KeyLeft}
	tuiKeys(m, edit...)
	if m.draft != before || !strings.Contains(m.View(), "Mixed.Flag = false") ||
		!strings.Contains(m.View(), `Effort = "deep"`) || !strings.Contains(m.View(), "› Remove = 默认") {
		t.Fatalf("keyboard edits did not stay in the option editor:\n%s", m.View())
	}
	tuiKeys(m, tea.KeyEsc)
	if m.screen != tuiCatalogPick || m.draft != before || !strings.Contains(m.View(), "› Model Example") {
		t.Fatalf("Esc did not restore the selected model and prior draft:\n%s", m.View())
	}
	tuiKeys(m, tea.KeyEnter)
	if view := m.View(); !strings.Contains(view, "› Mixed.Flag = true") || !strings.Contains(view, `Effort = "fast"`) ||
		!strings.Contains(view, `Remove = "present"`) {
		t.Fatalf("reopened option editor did not read the original values:\n%s", view)
	}
	tuiKeys(m, edit...)
	tuiKeys(m, tea.KeyEnter)
	config, err := bridge.ParseConfig([]byte(m.draft))
	target := config.Routes["work"]
	if err != nil || m.screen != tuiBrowse || target.ProjectID != "project-sample" || target.Model != "model-sample" ||
		len(target.Options) != 4 || target.Options["Mixed.Flag"] != false || target.Options["Effort"] != "deep" ||
		target.Options["Stale"] != "retired" || !strings.Contains(m.draft, `"Hidden": 9007199254740993`) {
		t.Fatalf("incorrect applied target: %+v, %v", target, err)
	}
}
