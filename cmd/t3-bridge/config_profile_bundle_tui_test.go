package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
	tea "github.com/charmbracelet/bubbletea"
)

func TestConfigTUIProfileBundleImport(t *testing.T) {
	m := tuiLifecycleProfileModel(t)
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
	exporter, dir, _, secrets := exportProfileFixture(t)
	source := filepath.Join(dir, "exports", strings.Repeat("long folder ", 18)+"a", strings.Repeat("long folder ", 10)+"b", "work.bundle.json")
	if err := exporter.run([]string{"export", "work", "-include-secrets", "-yes", "-output", source}, io.Discard); err != nil {
		t.Fatal(err)
	}
	tuiSetField(t, m, "账号 · receiverMachine", "unsaved")
	draft, editing := m.draft, m.source.ID
	registry := m.profiles.registry
	name := "work-copy"
	dest := filepath.Join(registry.root, "profiles", name, "config.json")
	y := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}
	enter := tea.KeyMsg{Type: tea.KeyEnter}
	assertWaiting := func(screen configTUIScreen) {
		t.Helper()
		for _, key := range []tea.KeyMsg{y, enter, {Type: tea.KeyEsc}, {Type: tea.KeyCtrlC}, {Type: tea.KeyF4}} {
			if _, cmd := m.Update(key); cmd != nil || m.screen != screen {
				t.Fatalf("key %q interrupted or repeated a pending operation", key.String())
			}
		}
	}
	openPreview := func() {
		t.Helper()
		tuiType(m, "i")
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(source), Paste: true})
		if m.screen != tuiProfileBundlePath || m.input.Value() != source {
			t.Fatal("bundle path input truncated the pasted path")
		}
		tuiKeys(m, tea.KeyEnter)
		tuiType(m, name)
		_, preview := m.Update(enter)
		if preview == nil || m.screen != tuiProfileBundleReading {
			t.Fatalf("preview did not start asynchronously: %s", m.View())
		}
		assertWaiting(tuiProfileBundleReading)
		m.Update(preview())
		if m.screen != tuiProfileBundleReview {
			t.Fatalf("bundle preview failed: %s", m.View())
		}
		m.Update(tea.WindowSizeMsg{Width: 768, Height: 24})
		view := m.View()
		for _, expected := range []string{strconv.Quote(source), "包内配置 work", "新配置 " + name, strconv.Quote(dest),
			"App 数量 · 2", "私有 App Secret 文件", "独立状态目录", "接收授权关闭", "T3 令牌文件继续使用原引用"} {
			if !strings.Contains(view, expected) {
				t.Fatalf("preview is missing %q", expected)
			}
		}
		for _, secret := range secrets {
			if strings.Contains(view, secret) {
				t.Fatal("preview displayed a credential")
			}
		}
		if strings.Contains(view, "调整文件") || strings.Contains(view, "调整字段") {
			t.Fatal("preview displayed adjustments without a patch")
		}
		assertTUIBundleProfileAbsent(t, dest)
	}

	tuiKeys(m, tea.KeyF4)
	if len(m.profiles.entries) != 0 || !strings.Contains(m.View(), "i 导入私有包") {
		t.Fatalf("empty profile list is missing the import shortcut: %s", m.View())
	}
	openPreview()
	if _, cmd := m.Update(enter); cmd != nil || m.screen != tuiProfileBundleReview {
		t.Fatal("Enter submitted the preview")
	}
	tuiKeys(m, tea.KeyEsc)
	assertTUIBundleProfileAbsent(t, dest)
	if m.screen != tuiProfiles || m.draft != draft {
		t.Fatal("canceling import did not return to the list with the draft intact")
	}

	if err := registry.run([]string{"create", "keep"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	openPreview()
	_, apply := m.Update(y)
	if apply == nil || m.screen != tuiProfileBundleApplying {
		t.Fatal("confirmation did not schedule the import")
	}
	assertWaiting(tuiProfileBundleApplying)
	assertTUIBundleProfileAbsent(t, dest)
	m.Update(apply())
	if m.screen != tuiProfiles || m.draft != draft || m.source.ID != editing || len(m.profiles.entries) != 2 ||
		m.profiles.entries[m.profiles.row] != name || !strings.Contains(m.status, "已导入") {
		t.Fatalf("import did not refresh the selection with the draft intact: %s", m.View())
	}
	tuiKeys(m, tea.KeyEnter)
	if m.screen != tuiConfirmProfileOpen || m.draft != draft {
		t.Fatal("opening the imported profile did not retain the draft confirmation")
	}
	tuiType(m, "d")
	if m.screen != tuiBrowse || m.source.Path != dest {
		t.Fatal("could not open the imported profile")
	}
}

func TestConfigTUIProfileBundleImportRequiresCurrentPreview(t *testing.T) {
	m := tuiLifecycleProfileModel(t)
	exporter, dir, _, _ := exportProfileFixture(t)
	source := filepath.Join(dir, "exports", "work.bundle.json")
	if err := exporter.run([]string{"export", "work", "-include-secrets", "-yes", "-output", source}, io.Discard); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(m.profiles.registry.root, "profiles", "retry", "config.json")
	enter := tea.KeyMsg{Type: tea.KeyEnter}
	y := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}
	preview := func() {
		t.Helper()
		_, cmd := m.Update(enter)
		if cmd == nil || m.screen != tuiProfileBundleReading {
			t.Fatal("could not start bundle preview")
		}
		m.Update(cmd())
	}

	tuiKeys(m, tea.KeyF4)
	tuiType(m, "i")
	tuiType(m, filepath.Join(dir, "missing.json"))
	tuiKeys(m, tea.KeyEnter)
	tuiType(m, "retry")
	preview()
	if m.screen != tuiProfileBundlePath || !strings.Contains(m.status, "预览失败") {
		t.Fatalf("unreadable source did not return to path input: %s", m.View())
	}
	assertTUIBundleProfileAbsent(t, dest)
	tuiKeys(m, tea.KeyF2)
	tuiType(m, source)
	tuiKeys(m, tea.KeyTab)
	if m.input.Value() != "retry" {
		t.Fatal("correcting the source lost the new profile name")
	}
	preview()
	if m.screen != tuiProfileBundleReview {
		t.Fatalf("corrected preview failed: %s", m.View())
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localfile.WritePrivateAtomic(source, append(raw, '\n'), false); err != nil {
		t.Fatal(err)
	}
	_, apply := m.Update(y)
	if apply == nil {
		t.Fatal("could not confirm bundle import")
	}
	m.Update(apply())
	assertTUIBundleProfileAbsent(t, dest)
	if m.screen != tuiProfileBundlePath || m.input.Value() != source || !strings.Contains(m.status, "重新预览") {
		t.Fatalf("changed source did not require a new preview: %s", m.View())
	}
	tuiKeys(m, tea.KeyEnter)
	preview()
	tuiType(m, "e")
	if m.screen != tuiProfileBundlePath || m.input.Value() != source {
		t.Fatal("review could not return to the editable path")
	}
	tuiKeys(m, tea.KeyEnter)
	preview()
	_, apply = m.Update(y)
	if apply == nil {
		t.Fatal("could not confirm the current preview")
	}
	m.Update(apply())
	if m.screen != tuiProfiles || len(m.profiles.entries) != 1 || m.profiles.entries[0] != "retry" {
		t.Fatalf("current preview did not import: %s", m.View())
	}
}

func TestConfigTUIProfileBundleImportWithPatch(t *testing.T) {
	m := tuiLifecycleProfileModel(t)
	m.Update(tea.WindowSizeMsg{Width: 768, Height: 24})
	exporter, dir, _, _ := exportProfileFixture(t)
	source := filepath.Join(dir, "exports", "work.bundle.json")
	if err := exporter.run([]string{"export", "work", "-include-secrets", "-yes", "-output", source}, io.Discard); err != nil {
		t.Fatal(err)
	}
	const name = "work-patched"
	const model = "synthetic-adjusted-model"
	const secondaryModel = "synthetic-secondary-adjusted-model"
	patch := filepath.Join(dir, "adjustment.json")
	if _, err := localfile.WritePrivateAtomic(patch, []byte(`[
		{"path":["routes","work","model"],"value":"`+model+`"},
		{"path":["additionalAccounts","0","routes","work","model"],"value":"`+secondaryModel+`"}
	]`), true); err != nil {
		t.Fatal(err)
	}
	preview := func() string {
		t.Helper()
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd == nil || m.screen != tuiProfileBundleReading {
			t.Fatalf("could not start bundle preview: %s", m.View())
		}
		m.Update(cmd())
		if m.screen != tuiProfileBundleReview {
			t.Fatalf("bundle preview failed: %s", m.View())
		}
		return m.View()
	}

	tuiKeys(m, tea.KeyF4)
	tuiType(m, "i")
	tuiType(m, source)
	tuiKeys(m, tea.KeyEnter)
	tuiType(m, name)
	if !strings.Contains(m.View(), "Ctrl+E 编辑调整路径") {
		t.Fatal("name input is missing the patch shortcut")
	}
	tuiKeys(m, tea.KeyCtrlE)
	if m.screen != tuiProfileBundlePatch || m.input.Value() != "" {
		t.Fatal("patch input did not open empty")
	}
	missing := filepath.Join(dir, "missing-patch.json")
	tuiType(m, missing)
	tuiKeys(m, tea.KeyTab, tea.KeyCtrlE)
	if m.screen != tuiProfileBundlePatch || m.input.Value() != missing {
		t.Fatal("patch input did not retain the previous path")
	}
	tuiKeys(m, tea.KeyF2, tea.KeyEnter)
	if m.screen != tuiProfileBundleName || m.input.Value() != name {
		t.Fatal("clearing the patch path lost the name")
	}
	if view := preview(); strings.Contains(view, "调整文件") {
		t.Fatal("preview retained the cleared patch path")
	}

	tuiType(m, "e")
	if m.screen != tuiProfileBundlePath || m.input.Value() != source {
		t.Fatal("editing the review lost the source")
	}
	tuiKeys(m, tea.KeyEnter, tea.KeyCtrlE)
	// Leave a dot segment in the input to distinguish it from the reviewed path.
	tuiType(m, strings.Join([]string{dir, ".", filepath.Base(patch)}, string(filepath.Separator)))
	tuiKeys(m, tea.KeyEnter)
	view := preview()
	for _, expected := range []string{strconv.Quote(source), "新配置 " + name, "调整文件 " + strconv.Quote(patch),
		"调整字段", "  routes.work.model", "  additionalAccounts.0.routes.work.model"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("patched preview is missing %q: %s", expected, view)
		}
	}
	if strings.Contains(view, model) || strings.Contains(view, secondaryModel) {
		t.Fatal("preview displayed patch values")
	}

	dest := filepath.Join(m.profiles.registry.root, "profiles", name, "config.json")
	assertTUIBundleProfileAbsent(t, dest)
	_, apply := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if apply == nil || m.screen != tuiProfileBundleApplying {
		t.Fatal("could not confirm the patched preview")
	}
	m.Update(apply())
	if m.screen != tuiProfiles || len(m.profiles.entries) != 1 || m.profiles.entries[0] != name {
		t.Fatalf("patched import did not refresh the profile list: %s", m.View())
	}
	cfg, err := bridge.LoadConfig(dest)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Routes["work"].Model != model || len(cfg.AdditionalAccounts) != 1 ||
		cfg.AdditionalAccounts[0].Routes["work"].Model != secondaryModel || cfg.ReceiverAuthorized || cfg.AdditionalAccounts[0].ReceiverAuthorized {
		t.Fatal("import did not persist the reviewed adjustments with receiver authorization disabled")
	}
}

func assertTUIBundleProfileAbsent(t *testing.T, configPath string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Dir(configPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("import created a profile before successful confirmation: %v", err)
	}
}
