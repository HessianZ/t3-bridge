package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestConfigTUIFirstRunGuidance(t *testing.T) {
	template := tuiTestSource(t)
	if view := newConfigTUI(template).View(); strings.Contains(view, "首次配置 ·") {
		t.Fatalf("template without a save path shows first-run guidance: %s", view)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	source, err := configFileSource(path, template.JSON)
	if err != nil {
		t.Fatal(err)
	}
	m := newConfigTUI(source)
	const width, height = 40, 12
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	view := m.View()
	for _, hint := range []string{"首次配置 · F7", "T 本机 T3 · F6", "F5 选目标 · K 登记项目", "p 预览 · s 保存", "A 预检 · E 审阅启用后 s 保存", `› 路由 · "work"`} {
		if !strings.Contains(view, hint) {
			t.Errorf("first-run view is missing %q: %s", hint, view)
		}
	}
	lines := strings.Split(view, "\n")
	if len(lines) > height {
		t.Errorf("first-run view exceeds terminal height: %s", view)
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > width {
			t.Errorf("first-run line exceeds terminal width: %q", line)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("opening the TUI should leave the config absent: %v", err)
	}
	tuiKeys(m, tea.KeyEnter)
	if m.route == "" || strings.Contains(m.View(), "首次配置 ·") {
		t.Fatalf("expected the route view without first-run guidance: %s", m.View())
	}
	tuiKeys(m, tea.KeyEsc)
	tuiType(m, "p")
	tuiType(m, "s")
	if m.screen != tuiBrowse || m.source.Revision == "missing" || strings.Contains(m.View(), "首次配置 ·") {
		t.Fatalf("successful save should dismiss first-run guidance: %s", m.View())
	}
}
