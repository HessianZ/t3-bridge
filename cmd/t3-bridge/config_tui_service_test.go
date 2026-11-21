package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
)

func TestConfigTUIServiceInstallAndStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receiver.json")
	if err := os.WriteFile(path, tuiTestSource(t).JSON, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := configFileSource(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := newConfigTUI(source)
	m.Update(tea.WindowSizeMsg{Width: 180, Height: 14})
	var preflightScopes []bool
	installCalls, startCalls := 0, 0
	var preflightErr error
	m.service.preflight = func(_ context.Context, gotPath, revision string, includeDisabled bool) (bridge.ReceiverPreflight, error) {
		preflightScopes = append(preflightScopes, includeDisabled)
		if gotPath != path || revision != source.Revision {
			t.Fatal("preflight lost the saved configuration revision")
		}
		if includeDisabled {
			return bridge.ReceiverPreflight{
				Passed:      true,
				AllAccounts: &bridge.ReceiverPreflightOutcome{Passed: false},
				Accounts: []bridge.ReceiverPreflightAccount{
					{Tenant: "lark", AppID: "cli_disabled", Checks: []bridge.ReceiverPreflightCheck{{Check: "feishu", Status: "failed"}}},
				},
			}, preflightErr
		}
		return bridge.ReceiverPreflight{Accounts: []bridge.ReceiverPreflightAccount{
			{Tenant: "feishu", AppID: "cli_enabled", Enabled: true, Checks: []bridge.ReceiverPreflightCheck{
				{Check: "feishu", Status: "ok"},
				{Check: "t3", Status: "failed", Detail: "环境核验失败"},
				{Check: "route", Route: "default", Status: "skipped"},
			}},
			{Tenant: "lark", AppID: "cli_disabled", Checks: []bridge.ReceiverPreflightCheck{
				{Check: "legacy-receiver-lock", Status: "failed", LockHolder: &bridge.ReceiverLockHolder{Profile: "Work.Codex", PID: 1234}},
			}},
		}}, preflightErr
	}
	var startErr error
	m.service.install = func(instance string, source webSource) error {
		installCalls++
		if instance != "sample" || source.Path != path || source.Revision != m.source.Revision {
			t.Fatal("installation lost the confirmed instance or saved configuration")
		}
		return nil
	}
	m.service.run = func(args []string, output io.Writer) error {
		startCalls++
		if !slices.Equal(args, []string{"start", "-instance", "sample"}) || output != io.Discard {
			t.Fatalf("unexpected start request %v", args)
		}
		return startErr
	}
	m.draft += "\n"
	tuiType(m, "I")
	if m.screen != tuiBrowse || !strings.Contains(m.status, "保存") {
		t.Fatal("modified draft was accepted for installation")
	}
	m.draft = string(source.JSON)
	tuiType(m, "I")
	tuiKeys(m, tea.KeyEnter)
	if m.screen != tuiServiceName || m.status == "" {
		t.Fatal("empty instance name was accepted")
	}
	tuiType(m, "sample")
	tuiKeys(m, tea.KeyEnter, tea.KeyEsc, tea.KeyEnter)
	if view := m.View(); m.screen != tuiServiceConfirm || !strings.Contains(view, "receiver.json") ||
		!strings.Contains(view, "实例名 · sample") || !strings.Contains(view, "p 检查启动条件 · P 预检全部账号") {
		t.Fatalf("confirmation did not show the saved file, instance and preflight shortcuts: %s", view)
	}
	serviceKey := func(key string) tea.Cmd {
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		return cmd
	}
	if cmd := serviceKey("s"); cmd != nil || len(preflightScopes) != 0 || installCalls != 0 || startCalls != 0 {
		t.Fatal("service action ran before confirmation")
	}
	preflight := serviceKey("p")
	if preflight == nil || m.screen != tuiServicePreflightLoading || !strings.Contains(m.View(), "只读 API") ||
		len(preflightScopes) != 0 || serviceKey("p") != nil || serviceKey("y") != nil {
		t.Fatal("preflight did not queue a separate read-only check")
	}
	m.Update(preflight())
	if m.screen != tuiServicePreflightResult || !slices.Equal(preflightScopes, []bool{false}) {
		t.Fatal("lowercase p did not show the enabled-only preflight")
	}
	view := m.View()
	for _, want := range []string{"启动预检未通过", "接收连接和消息收发请在启动后核对", "feishu · cli_enabled", "飞书应用身份 · 通过", "T3 环境 · 失败 · 环境核验失败"} {
		if !strings.Contains(view, want) {
			t.Fatalf("preflight result missing %q: %s", want, view)
		}
	}
	if strings.Contains(view, "cli_disabled") || strings.Contains(view, "全部账号预检") {
		t.Fatalf("unexpected first page of enabled-only preflight: %s", view)
	}
	tuiKeys(m, tea.KeyPgDown)
	if view := m.View(); !strings.Contains(view, "T3 路由 default · 跳过") ||
		!strings.Contains(view, "lark · cli_disabled") || !strings.Contains(view, "接收授权未启用 · 跳过飞书和 T3 核验") ||
		!strings.Contains(view, "本机旧桥接收锁 · 失败") || !strings.Contains(view, "旧锁记录 · profile Work.Codex · PID 1234") {
		t.Fatalf("could not scroll to the remaining checks: %s", view)
	}
	if serviceKey("y") != nil || serviceKey("s") != nil || installCalls != 0 || startCalls != 0 {
		t.Fatal("preflight triggered a service action")
	}
	tuiKeys(m, tea.KeyEsc)
	if m.screen != tuiServiceConfirm || !strings.Contains(m.View(), "实例名 · sample") {
		t.Fatalf("preflight did not return to the same confirmation: %s", m.View())
	}
	allAccounts := serviceKey("P")
	if allAccounts == nil || m.screen != tuiServicePreflightLoading || len(preflightScopes) != 1 {
		t.Fatal("confirmation did not queue the all-accounts preflight")
	}
	m.Update(allAccounts())
	view = m.View()
	if m.screen != tuiServicePreflightResult || !slices.Equal(preflightScopes, []bool{false, true}) ||
		!strings.Contains(view, "启动预检通过") || !strings.Contains(view, "全部账号预检未通过") ||
		!strings.Contains(view, "接收授权未启用 · 纳入预检") || !strings.Contains(view, "飞书应用身份 · 失败") {
		t.Fatalf("all-accounts result did not distinguish startup readiness and disabled-account checks: %s", view)
	}
	if serviceKey("y") != nil || serviceKey("s") != nil || installCalls != 0 || startCalls != 0 {
		t.Fatal("all-accounts preflight triggered a service action")
	}
	tuiKeys(m, tea.KeyEsc)
	if m.screen != tuiServiceConfirm || m.service.instance != "sample" {
		t.Fatal("all-accounts preflight did not return to the same confirmation")
	}
	preflightErr = errWorkspaceServiceConfigChanged
	m.Update(serviceKey("p")())
	if view := m.View(); !strings.Contains(view, "配置文件已更新或不可用") || strings.Contains(view, "cli_enabled") {
		t.Fatalf("preflight showed stale results: %s", view)
	}
	tuiKeys(m, tea.KeyEsc)
	install := serviceKey("y")
	if install == nil || installCalls != 0 || serviceKey("y") != nil {
		t.Fatal("installation was not queued exactly once")
	}
	m.Update(install())
	if m.screen != tuiServiceInstalled || installCalls != 1 || startCalls != 0 {
		t.Fatal("installation did not present the separate start action")
	}
	startErr = errors.New("synthetic-private-manager-output")
	start := serviceKey("s")
	if start == nil || startCalls != 0 {
		t.Fatal("startup was not queued")
	}
	m.Update(start())
	if m.screen != tuiServiceInstalled || !strings.Contains(m.status, "启动未确认") || strings.Contains(m.View(), startErr.Error()) {
		t.Fatalf("startup failure was not safely reported: %s", m.View())
	}
	startErr = nil
	m.Update(serviceKey("s")())
	if m.screen != tuiBrowse || startCalls != 2 || !strings.Contains(m.status, "已提交启动请求") {
		t.Fatal("retry did not submit a start request")
	}
}

func TestConfigTUIServicePreflightFromBrowse(t *testing.T) {
	source := tuiTestSource(t)
	source.Path, source.Revision = "synthetic-config.json", "saved"
	m := newConfigTUI(source)
	tuiKeys(m, tea.KeyDown)
	row := m.row
	calls := 0
	m.service.preflight = func(_ context.Context, path, revision string, includeDisabled bool) (bridge.ReceiverPreflight, error) {
		calls++
		if path != source.Path || revision != source.Revision || !includeDisabled {
			t.Fatalf("unexpected direct preflight target: %q at %q, includeDisabled=%v", path, revision, includeDisabled)
		}
		return bridge.ReceiverPreflight{AllAccounts: &bridge.ReceiverPreflightOutcome{Passed: true}}, nil
	}
	if view := m.View(); !strings.Contains(view, "P 预检全部账号") {
		t.Fatalf("browse shortcut is missing: %s", view)
	}
	m.service.instance = "prior-instance"
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}})
	if view := m.View(); cmd == nil || m.screen != tuiServicePreflightLoading || calls != 0 || strings.Contains(view, "prior-instance") {
		t.Fatalf("browse did not queue preflight directly: %s", view)
	}
	m.Update(cmd())
	if view := m.View(); calls != 1 || m.screen != tuiServicePreflightResult ||
		!strings.Contains(view, "启动预检未通过") || !strings.Contains(view, "全部账号预检通过") {
		t.Fatalf("browse did not show the all-accounts outcome: %s", view)
	}
	tuiKeys(m, tea.KeyEsc)
	if m.screen != tuiBrowse || m.row != row || m.draft != string(source.JSON) {
		t.Fatal("preflight did not return to the same browse view")
	}
}

func TestConfigTUIServicePreflightSelectedApp(t *testing.T) {
	source := tuiReceiverEnableSource(t)
	m := newConfigTUI(source)
	calls := 0
	m.service.preflightApp = func(_ context.Context, path, revision, appID string, includeDisabled bool) (bridge.ReceiverPreflight, error) {
		calls++
		if path != source.Path || revision != source.Revision || appID != "cli_additional" || !includeDisabled {
			t.Fatalf("unexpected selected preflight target: %q at %q, app %q, includeDisabled=%v", path, revision, appID, includeDisabled)
		}
		return bridge.ReceiverPreflight{
			AllAccounts: &bridge.ReceiverPreflightOutcome{Passed: true},
			Accounts: []bridge.ReceiverPreflightAccount{
				{Tenant: "feishu", AppID: appID, Checks: []bridge.ReceiverPreflightCheck{{Check: "feishu", Status: "ok"}}},
			},
		}, nil
	}
	key := func() tea.Cmd {
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'A'}})
		return cmd
	}
	tuiKeys(m, tea.KeyTab, tea.KeyDown)
	row := m.row
	m.draft += "\n"
	dirty := m.draft
	if cmd := key(); cmd != nil || calls != 0 || m.screen != tuiBrowse || m.draft != dirty ||
		!strings.Contains(m.status, "配置尚未保存") || strings.Contains(m.View(), "A 预检所选 App") {
		t.Fatalf("A accepted an unsaved draft: %s", m.View())
	}
	m.draft = string(source.JSON)
	if view := m.View(); !strings.Contains(view, "A 预检所选 App") || !strings.Contains(view, "E 准备启用接收") {
		t.Fatalf("saved overview omitted receiver shortcuts: %s", view)
	}
	cmd := key()
	if view := m.View(); cmd == nil || calls != 0 || m.screen != tuiServicePreflightLoading ||
		!strings.Contains(view, "所选 App · cli_additional") {
		t.Fatalf("A did not queue the selected App preflight: %s", view)
	}
	tuiKeys(m, tea.KeyTab)
	m.Update(cmd())
	view := m.View()
	if calls != 1 || m.screen != tuiServicePreflightResult || strings.Contains(view, "cli_primary") ||
		strings.Contains(view, "全部账号") {
		t.Fatalf("preflight did not show the scoped result: %s", view)
	}
	for _, want := range []string{"所选 App 预检通过", "启动未就绪", "接收授权未启用", "feishu · cli_additional", "飞书应用身份 · 通过"} {
		if !strings.Contains(view, want) {
			t.Fatalf("selected App result omitted %q: %s", want, view)
		}
	}
	tuiKeys(m, tea.KeyEsc)
	if m.screen != tuiBrowse || m.account != 1 || m.row != row || m.draft != string(source.JSON) || m.reviewed != nil {
		t.Fatalf("preflight changed the draft or overview selection: %s", m.View())
	}
	if raw, err := os.ReadFile(source.Path); err != nil || string(raw) != string(source.JSON) {
		t.Fatalf("preflight changed the saved configuration: %v", err)
	}
}

func TestConfigTUIServiceRequiresSavedFile(t *testing.T) {
	template := tuiTestSource(t)
	missing, err := configFileSource(filepath.Join(t.TempDir(), "new.json"), template.JSON)
	if err != nil {
		t.Fatal(err)
	}
	noRevision := missing
	noRevision.Revision = ""
	dirty := missing
	dirty.Revision = "saved"
	for _, source := range []webSource{template, missing, noRevision, dirty} {
		for _, key := range []rune{'I', 'P'} {
			m := newConfigTUI(source)
			if source.Revision == "saved" {
				m.draft += "\n"
			}
			_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
			if cmd != nil || m.screen != tuiBrowse || !strings.Contains(m.status, "保存") {
				t.Fatalf("%c accepted an unsaved source: %s", key, m.View())
			}
		}
	}
}
