package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/larksuite/oapi-sdk-go/v3/scene/registration"
)

func TestConfigTUIReplacementBinding(t *testing.T) {
	for _, test := range []struct {
		name, appID string
		account     int
	}{
		{name: "primary", appID: "cli_mainfixture", account: 0},
		{name: "additional", appID: "cli_otherfixture", account: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			source := tuiTestSource(t)
			source.Path, source.Revision = filepath.Join(dir, "config.json"), "missing"
			m := newConfigTUI(source)
			raw := existingBindConfigFixture(t, dir)
			m.setDraft(raw)
			if test.account == 1 {
				tuiKeys(m, tea.KeyTab)
			}
			tuiKeys(m, tea.KeyF7)
			tuiType(m, "x")
			if m.screen != tuiBindReview || m.binding.replaceAppID != test.appID || m.binding.client != nil {
				t.Fatalf("replacement review did not select %s: %s", test.appID, m.status)
			}
			for _, hint := range []string{test.appID, "默认 T3 路由和连接", "独立状态目录", "仅授权扫码用户", "禁用接收"} {
				if !strings.Contains(m.View(), hint) {
					t.Errorf("replacement review omitted %q", hint)
				}
			}
			tuiType(m, "r")
			tuiType(m, "x")
			if m.binding.replaceAppID != "" || !m.binding.comments || m.draft != string(raw) {
				t.Fatal("switching back to ordinary binding changed the draft or the comments option")
			}
			tuiType(m, "x")

			const newAppID = "cli_replacementfixture"
			q := existingScanFixture(t, dir, &registration.RegisterAppResult{
				ClientID: newAppID, ClientSecret: "synthetic-replacement-secret",
				UserInfo: &registration.UserInfo{OpenID: "ou_replacementfixture", TenantBrand: "feishu"},
			})
			q.replaceAppID = m.binding.replaceAppID
			m.binding.client, m.binding.id, m.screen = q, q.id, tuiBindProgress
			tuiType(m, "x")
			tuiType(m, "r")
			tuiKeys(m, tea.KeyTab)
			m.Update(configTUIBindingResult{q.id, q.view})
			if m.account != test.account || !m.binding.comments || m.draft != string(raw) ||
				!strings.Contains(m.View(), test.appID+" → "+newAppID) {
				t.Fatalf("scan confirmation did not preserve the selected replacement: %s", m.View())
			}
			tuiKeys(m, tea.KeyEnter)
			if m.screen != tuiReview || m.reviewed == nil {
				t.Fatalf("replacement draft did not open the regular review: %s", m.status)
			}
			config, err := bridge.ParseConfig([]byte(m.draft))
			if err != nil {
				t.Fatal(err)
			}
			if got := config.Accounts()[test.account].AppID; got != newAppID {
				t.Fatalf("selected account has app %q, want %q", got, newAppID)
			}
			if _, err := os.Stat(source.Path); !os.IsNotExist(err) {
				t.Fatalf("scan confirmation should leave the configuration unsaved: %v", err)
			}
		})
	}
}

func TestConfigTUIReplacementBindingRejectsTemplate(t *testing.T) {
	m := newConfigTUI(tuiTestSource(t))
	draft := m.draft
	tuiKeys(m, tea.KeyF7)
	tuiType(m, "x")
	if m.screen != tuiBindReview || m.binding.replaceAppID != "" || m.binding.client != nil || m.status == "" || m.draft != draft {
		t.Fatalf("template App ID was eligible for replacement: %s", m.View())
	}
}

func TestConfigTUIReplacementBindingCancel(t *testing.T) {
	m := newConfigTUI(tuiTestSource(t))
	draft := existingBindConfigFixture(t, t.TempDir())
	m.setDraft(draft)
	tuiKeys(m, tea.KeyF7)
	tuiType(m, "x")
	if m.binding.replaceAppID != "cli_mainfixture" {
		t.Fatalf("replacement mode did not open: %s", m.status)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	m.binding.stop, m.binding.id, m.screen = cancel, "fixture-scan", tuiBindProgress
	result := configTUIBindingResult{m.binding.id, qrBindingView{State: "scanned", AppID: "cli_replacementfixture"}}
	tuiKeys(m, tea.KeyEsc)
	if _, cmd := m.Update(result); cmd != nil {
		t.Fatal("late scan result scheduled work after cancellation")
	}
	if ctx.Err() != context.Canceled || m.screen != tuiBrowse || m.binding.replaceAppID != "" || m.draft != string(draft) {
		t.Fatalf("cancellation did not discard the active scan: %s", m.status)
	}
}
