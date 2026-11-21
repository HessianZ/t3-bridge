package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
	tea "github.com/charmbracelet/bubbletea"
)

func tuiLifecycleProfileModel(t *testing.T) *configTUI {
	t.Helper()
	configHome := t.TempDir()
	t.Setenv("HOME", configHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("AppData", configHome)
	return tuiProfileModel(t)
}

func TestConfigTUIProfilePurge(t *testing.T) {
	for _, defaultAfter := range []string{"无", "other"} {
		t.Run(defaultAfter, func(t *testing.T) {
			m := tuiLifecycleProfileModel(t)
			m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
			r := m.profiles.registry
			for _, name := range []string{"work", "other"} {
				if name == "other" && defaultAfter == "无" {
					continue
				}
				if err := r.run([]string{"create", name}, io.Discard); err != nil {
					t.Fatal(err)
				}
			}
			if err := r.run([]string{"use", "work"}, io.Discard); err != nil {
				t.Fatal(err)
			}
			m.openProfile("work")
			path := m.source.Path
			cfg, err := bridge.ParseConfig(m.source.JSON)
			if err != nil {
				t.Fatal(err)
			}
			if err := localfile.MkdirAllPrivate(cfg.StateDir); err != nil {
				t.Fatal(err)
			}
			tuiSetField(t, m, "账号 · receiverMachine", "unsaved")
			draft := m.draft
			tuiKeys(m, tea.KeyF4)
			x := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}}
			_, canceled := m.Update(x)
			tuiKeys(m, tea.KeyEsc)
			_, preview := m.Update(x)
			if preview == nil || canceled == nil || m.screen != tuiProfilePreviewLoading {
				t.Fatal("purge preview did not produce an asynchronous command")
			}
			m.Update(canceled())
			if m.screen != tuiProfilePreviewLoading {
				t.Fatal("canceled preview was accepted during a new request")
			}
			m.Update(preview())
			view := m.View()
			for _, expected := range []string{"确认永久清除", strconv.Quote(filepath.Dir(path)), "清除路径", strconv.Quote(cfg.StateDir),
				"保留项", strconv.Quote(cfg.AppSecretFile), "完成后默认配置 · " + defaultAfter, "丢弃当前草稿"} {
				if m.screen != tuiConfirmProfilePurge || !strings.Contains(view, expected) {
					t.Fatalf("preview missing %q: %s", expected, view)
				}
			}
			tuiType(m, "other")
			if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil || m.screen != tuiConfirmProfilePurge {
				t.Fatal("wrong name confirmed purge")
			}
			tuiKeys(m, tea.KeyEsc)
			if _, err := os.Stat(path); err != nil || m.screen != tuiProfiles || m.draft != draft {
				t.Fatalf("canceling purge changed the saved profile or draft: %v", err)
			}
			_, preview = m.Update(x)
			m.Update(preview())
			tuiType(m, "work")
			_, apply := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			if _, err := os.Stat(path); err != nil || apply == nil || m.screen != tuiProfileApplying {
				t.Fatalf("purge was applied synchronously or not requested: %v", err)
			}
			m.Update(apply())
			expectedPath := ""
			var expectedEntries []string
			if defaultAfter == "other" {
				expectedPath = filepath.Join(r.root, "profiles", "other", "config.json")
				expectedEntries = []string{"other"}
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) || m.source.Path != expectedPath || m.screen != tuiProfiles ||
				!slices.Equal(m.profiles.entries, expectedEntries) || m.draft != string(m.source.JSON) {
				t.Fatalf("purge did not refresh the list and open the default or template: %s (%v)", m.View(), err)
			}
		})
	}
}

func TestConfigTUIProfilePurgeFailureRefreshes(t *testing.T) {
	m := tuiLifecycleProfileModel(t)
	r := m.profiles.registry
	if err := r.run([]string{"create", "work"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	m.openProfile("work")
	path := m.source.Path
	tuiSetField(t, m, "账号 · receiverMachine", "unsaved")
	draft := m.draft
	tuiKeys(m, tea.KeyF4)
	_, preview := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m.Update(preview())
	if m.screen != tuiConfirmProfilePurge {
		t.Fatalf("could not prepare purge preview: %s", m.View())
	}
	lock, err := lockProfileRuntime(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	if err := r.run([]string{"create", "other"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	tuiType(m, "work")
	_, apply := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if apply == nil {
		t.Fatalf("could not confirm purge: %s", m.View())
	}
	m.Update(apply())
	if m.screen != tuiProfiles || !strings.Contains(m.status, "操作失败") || m.source.Path != path || m.draft != draft ||
		!slices.Equal(m.profiles.entries, []string{"other", "work"}) || m.profiles.row != 1 {
		t.Fatalf("failed purge did not refresh while retaining the selection and draft: %s", m.View())
	}
}

func TestConfigTUIProfilePrivateExport(t *testing.T) {
	m := tuiLifecycleProfileModel(t)
	r, dir, _, secrets := exportProfileFixture(t)
	m.profiles.registry = r
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 24})
	m.openProfile("work")
	tuiSetField(t, m, "账号 · appId", "cli_unsaved")
	draft := m.draft
	tuiKeys(m, tea.KeyF4)
	dest := filepath.Join(dir, "exports", "work.bundle.json")
	openPreview := func() {
		t.Helper()
		tuiType(m, "e")
		tuiType(m, dest)
		_, preview := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if preview == nil || m.screen != tuiProfilePreviewLoading {
			t.Fatalf("export preview was not requested: %s", m.View())
		}
		m.Update(preview())
		view := m.View()
		if m.screen != tuiConfirmProfileExport || !strings.Contains(view, strconv.Quote(dest)) || !strings.Contains(view, "App 数量 · 2") || !strings.Contains(view, "已保存版本") {
			t.Fatalf("export confirmation is incomplete: %s", view)
		}
		for _, secret := range secrets {
			if strings.Contains(view, secret) {
				t.Fatal("export preview displayed a credential")
			}
		}
	}
	openPreview()
	tuiKeys(m, tea.KeyEnter, tea.KeyEsc)
	if _, err := os.Stat(dest); !os.IsNotExist(err) || m.screen != tuiProfiles || m.draft != draft {
		t.Fatalf("canceling export created a file or changed the draft: %v", err)
	}
	openPreview()
	y := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}
	_, apply := m.Update(y)
	if _, err := os.Stat(dest); !os.IsNotExist(err) || apply == nil || m.screen != tuiProfileApplying {
		t.Fatalf("export was applied synchronously or not requested: %v", err)
	}
	m.Update(apply())
	if m.screen != tuiProfiles || m.draft != draft || !strings.Contains(m.status, "私有文件已导出") {
		t.Fatalf("export did not return to the list with the draft intact: %s", m.View())
	}
	bundle, _, err := readProfileBundle(dest)
	if err != nil || len(bundle.AppSecrets) != len(secrets) || bytes.Contains(bundle.Config, []byte("cli_unsaved")) {
		t.Fatalf("export did not bundle the saved profile: %v", err)
	}
	for _, item := range bundle.AppSecrets {
		if item.Secret != secrets[item.AppID] || strings.Contains(m.View(), item.Secret) {
			t.Fatal("export failed to keep the credential in the bundle only")
		}
	}
	before, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	openPreview()
	_, apply = m.Update(y)
	result := apply().(configTUIProfileApplied)
	m.Update(result)
	if after, err := os.ReadFile(dest); err != nil || result.err == nil || !bytes.Equal(before, after) || m.screen != tuiProfiles || m.draft != draft {
		t.Fatalf("existing export was overwritten or the draft changed: %v", err)
	}
}
