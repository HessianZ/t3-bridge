package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
	tea "github.com/charmbracelet/bubbletea"
)

func TestConfigTUIToolIdentity(t *testing.T) {
	const probeDiagnostic = "专用飞书工具 CLI 的服务端用户身份核对失败"
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	want := workspaceToolIdentity{AppID: "cli_selected", RouteID: "Work.Tools", Identity: "user-default"}
	cfg, err := bridge.ParseConfig(tuiTestSource(t).JSON)
	if err != nil {
		t.Fatal(err)
	}
	extra := cfg
	extra.AppID = want.AppID
	extra.StateDir = filepath.Join(dir, "state")
	extra.AppSecretFile = filepath.Join(dir, "app.secret")
	extra.Tenant = "feishu"
	extra.Users = []string{"ou_selected"}
	target := cfg.Routes["work"]
	target.ProviderID, target.Users = "provider-selected", extra.Users
	target.HostName, target.Aliases = "", nil
	target.ToolContext = &bridge.ToolContext{
		CLIPath: filepath.Join(dir, "lark-cli"), EnvironmentID: target.EnvironmentID, Identity: want.Identity,
		BotBinding: &bridge.ToolBotBinding{
			AppID: want.AppID, Tenant: extra.Tenant, BotOpenID: "ou_bot", Profile: "personal", ConfigDir: dir,
		},
		UserBinding: &bridge.ToolUserBinding{
			AppID: want.AppID, Tenant: extra.Tenant, UserOpenID: "ou_selected", Profile: "personal", ConfigDir: dir,
		},
	}
	extra.Routes = map[string]bridge.Target{"work": cfg.Routes["work"], want.RouteID: target}
	cfg.AdditionalAccounts = []bridge.Config{extra}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localfile.WritePrivateAtomic(path, raw, true); err != nil {
		t.Fatal(err)
	}
	source, err := configFileSource(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := newConfigTUI(source)
	calls, probes := 0, 0
	m.toolIdentity.check = func(ctx context.Context, got webSource, appID, routeID string) (workspaceToolIdentity, error) {
		calls++
		if got.Path != path || got.Revision != configRevision(raw) || appID != want.AppID || routeID != want.RouteID {
			t.Fatalf("checker received the wrong saved selection: %q %q %q %q", got.Path, got.Revision, appID, routeID)
		}
		if (calls == 1) != (ctx.Err() == context.Canceled) {
			t.Fatalf("unexpected cancellation for check %d: %v", calls, ctx.Err())
		}
		return verifySavedToolIdentity(ctx, got, appID, routeID, func(_ context.Context, account bridge.Config, routeID string) error {
			probes++
			if account.AppID != want.AppID || routeID != want.RouteID || account.Routes[routeID].ToolContext.Identity != want.Identity {
				t.Fatal("probe received the wrong saved identity")
			}
			if probes == 2 {
				return toolIdentityProbeFailure{reason: probeDiagnostic}
			}
			return nil
		})
	}
	pressV := func() tea.Cmd {
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'V'}})
		return cmd
	}
	if cmd := pressV(); cmd != nil || m.screen != tuiBrowse || !strings.Contains(m.status, "先打开") {
		t.Fatalf("verification opened outside a route: %s", m.View())
	}
	tuiKeys(m, tea.KeyTab)
	tuiSelect(t, m, `路由 · "Work.Tools"`)
	tuiKeys(m, tea.KeyEnter)
	if !strings.Contains(m.View(), "V 核验工具身份") {
		t.Fatal("route verification shortcut is missing")
	}
	tuiSetField(t, m, "目标 · model", "draft-model")
	dirty := m.draft
	if cmd := pressV(); cmd != nil || m.screen != tuiBrowse || m.draft != dirty || !strings.Contains(m.status, "保存") {
		t.Fatalf("dirty draft was not blocked and preserved: %s", m.View())
	}
	m.setDraft(source.JSON)
	row := m.row
	first := pressV()
	if first == nil || m.screen != tuiToolIdentityLoading || !strings.Contains(m.View(), "飞书只读检查") {
		t.Fatalf("verification was not scheduled: %s", m.View())
	}
	tuiKeys(m, tea.KeyEsc)
	second := pressV()
	if second == nil || calls != 0 {
		t.Fatal("verification did not run asynchronously")
	}
	m.Update(first())
	if m.screen != tuiToolIdentityLoading || probes != 0 {
		t.Fatal("a canceled check replaced the pending check or ran its probe")
	}
	m.Update(second())
	if m.screen != tuiToolIdentityResult || probes != 1 {
		t.Fatalf("missing verification result: %s", m.View())
	}
	for _, value := range []string{want.AppID, want.RouteID, want.Identity, "核验通过", "飞书只读检查"} {
		if !strings.Contains(m.View(), value) {
			t.Fatalf("result omitted %q: %s", value, m.View())
		}
	}
	tuiKeys(m, tea.KeyEsc)
	if m.screen != tuiBrowse || m.account != 1 || m.route != want.RouteID || m.row != row || m.draft != string(source.JSON) {
		t.Fatal("verification changed the selection or draft")
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("verification modified the saved configuration: %v", err)
	}
	cmd := pressV()
	if cmd == nil {
		t.Fatal("failure check was not scheduled")
	}
	m.Update(cmd())
	if view := m.View(); m.screen != tuiToolIdentityResult || !strings.Contains(view, want.Identity) || !strings.Contains(view, probeDiagnostic) {
		t.Fatalf("probe diagnostic or identity mode missing: %s", view)
	}
	tuiKeys(m, tea.KeyEsc)
	if _, err := localfile.WritePrivateAtomic(path, append(bytes.Clone(raw), '\n'), false); err != nil {
		t.Fatal(err)
	}
	cmd = pressV()
	if cmd == nil {
		t.Fatal("revision check was not scheduled")
	}
	m.Update(cmd())
	if m.screen != tuiToolIdentityResult || probes != 2 || !strings.Contains(m.View(), "重新打开配置") {
		t.Fatalf("changed revision was not blocked: %s", m.View())
	}
}
