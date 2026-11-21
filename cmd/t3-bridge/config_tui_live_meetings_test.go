package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
)

func TestConfigTUILiveMeetingsKeyboard(t *testing.T) {
	m := newConfigTUI(tuiTestSource(t))
	tuiType(m, "a")
	for _, value := range []string{"cli_meetings", "ou_sample", "/sample/meeting.secret", "/sample/meeting"} {
		tuiType(m, value)
		tuiKeys(m, tea.KeyTab)
	}
	tuiKeys(m, tea.KeyRight, tea.KeyCtrlS, tea.KeyDown)
	draft, accountIndex, row := m.draft, m.account, m.row
	m.profiles.registry = profileRegistry{root: t.TempDir()}
	press := func(key rune) tea.Cmd {
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
		return cmd
	}
	checkConfirmation := func(number string) {
		t.Helper()
		if m.screen != tuiLiveMeetingsConfirm || !strings.Contains(m.View(), "lark/cli_meetings") || !strings.Contains(m.View(), number) {
			t.Fatalf("confirmation omitted the selected App or meeting: %s", m.View())
		}
	}

	// H schedules an asynchronous status lookup for the selected account.
	if !strings.Contains(m.View(), "H 在线会议") || press('H') == nil || m.screen != tuiLiveMeetingsLoading {
		t.Fatal("H did not open the online meetings status")
	}
	tuiKeys(m, tea.KeyEsc)
	account := receiverProcessAccount{Tenant: "lark", AppID: "cli_meetings"}
	const processID = "0123456789abcdef0123456789abcdef"
	accepted := bridge.LocalMeetingEntry{Number: "111222333", Phase: "join_accepted", Origin: "local", Revision: strings.Repeat("a", 32)}
	pending := bridge.LocalMeetingEntry{Number: "444555666", Phase: "leave_pending", Origin: "local", Revision: strings.Repeat("b", 32)}
	view := bridge.LocalMeetingView{Enabled: true, Available: true, Meetings: []bridge.LocalMeetingEntry{accepted, pending}}
	var requests []bridge.LocalMeetingRequest
	status := bridge.LocalMeetingRequest{Action: "status"}
	cmd := m.openLiveMeetings(func(selected receiverProcessAccount) (receiverProcess, error) {
		if selected != account {
			t.Fatalf("lookup used the wrong account: %+v", selected)
		}
		return receiverProcess{ID: processID}, nil
	}, func(_ context.Context, process, tenant, appID string, req bridge.LocalMeetingRequest) (bridge.LocalMeetingView, error) {
		if process != processID || tenant != account.Tenant || appID != account.AppID {
			t.Fatalf("request used the wrong target: %s %s/%s", process, tenant, appID)
		}
		requests = append(requests, req)
		return view, nil
	})
	if len(requests) != 0 {
		t.Fatal("status ran before its Bubble Tea command")
	}
	m.Update(cmd())
	if !slices.Equal(requests, []bridge.LocalMeetingRequest{status}) {
		t.Fatalf("unexpected initial status request: %+v", requests)
	}

	// Join validates the exact number, and only y schedules one write.
	press('j')
	tuiType(m, "0012345678")
	tuiKeys(m, tea.KeyEnter)
	if m.screen != tuiLiveMeetingsJoin {
		t.Fatal("join accepted a ten-digit meeting number")
	}
	tuiKeys(m, tea.KeyBackspace, tea.KeyEnter)
	const joinNumber = "001234567"
	checkConfirmation(joinNumber)
	tuiKeys(m, tea.KeyEnter)
	if len(requests) != 1 {
		t.Fatal("Enter submitted the join request")
	}
	cmd = press('y')
	if cmd == nil || press('y') != nil || len(requests) != 1 {
		t.Fatal("join confirmation must schedule exactly one asynchronous command")
	}
	m.Update(cmd())
	join := bridge.LocalMeetingRequest{Action: "join", Number: joinNumber}
	if !slices.Equal(requests, []bridge.LocalMeetingRequest{status, join}) {
		t.Fatalf("unexpected join request: %+v", requests)
	}

	// An accepted row offers both operations and each uses the selected revision.
	if menu := m.View(); !strings.Contains(menu, "l 离会") || !strings.Contains(menu, "c 确认已离会") {
		t.Fatalf("accepted row did not offer both actions: %s", menu)
	}
	press('l')
	checkConfirmation(accepted.Number)
	if !strings.Contains(m.View(), "发送离会请求") {
		t.Fatalf("leave confirmation did not describe the action: %s", m.View())
	}
	m.Update(press('y')())
	if press('c') != nil || len(requests) != 3 {
		t.Fatal("confirm-left submitted without confirmation")
	}
	checkConfirmation(accepted.Number)
	if !strings.Contains(m.View(), "仅关闭本机回执，不调用飞书接口") {
		t.Fatalf("confirm-left did not disclose its local-only effect: %s", m.View())
	}
	tuiKeys(m, tea.KeyEsc)
	press('c')
	checkConfirmation(accepted.Number)
	m.Update(press('y')())
	tuiKeys(m, tea.KeyDown)
	if press('l') != nil || m.screen != tuiLiveMeetingsPick {
		t.Fatal("a pending row offered leave")
	}
	press('c')
	checkConfirmation(pending.Number)
	m.Update(press('y')())
	leave := bridge.LocalMeetingRequest{Action: "leave", Number: accepted.Number, Revision: accepted.Revision}
	closeAccepted := bridge.LocalMeetingRequest{Action: "confirm-left", Number: accepted.Number, Revision: accepted.Revision}
	confirmLeft := bridge.LocalMeetingRequest{Action: "confirm-left", Number: pending.Number, Revision: pending.Revision}
	if !slices.Equal(requests, []bridge.LocalMeetingRequest{status, join, leave, closeAccepted, confirmLeft}) {
		t.Fatalf("unexpected row requests: %+v", requests)
	}

	// Refresh reads status and preserves the selected meeting. Esc restores the draft view.
	view.Meetings = []bridge.LocalMeetingEntry{pending, accepted}
	m.Update(press('r')())
	if !slices.Equal(requests, []bridge.LocalMeetingRequest{status, join, leave, closeAccepted, confirmLeft, status}) || !strings.Contains(m.View(), "› "+pending.Number) {
		t.Fatalf("refresh lost the selection or requested a write: %s, %+v", m.View(), requests)
	}
	tuiKeys(m, tea.KeyEsc)
	if m.screen != tuiBrowse || m.draft != draft || m.account != accountIndex || m.row != row {
		t.Fatal("Esc did not preserve the draft and overview selection")
	}
}

func TestConfigTUIMeetingPermissionsWithoutReceiver(t *testing.T) {
	m := newConfigTUI(tuiTestSource(t))
	tuiType(m, "a")
	for _, value := range []string{"cli_meetings", "ou_sample", "/sample/meeting.secret", "/sample/meeting"} {
		tuiType(m, value)
		tuiKeys(m, tea.KeyTab)
	}
	tuiKeys(m, tea.KeyRight, tea.KeyCtrlS)
	path := filepath.Join(t.TempDir(), "meetings.json")
	raw := []byte(m.draft + "\n")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := configFileSource(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.loadSource(source)
	tuiKeys(m, tea.KeyTab, tea.KeyDown)
	m.draft += "\n"
	draft, row := m.draft, m.row
	press := func(key rune) tea.Cmd {
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
		return cmd
	}
	checkSource := func(got webSource, appID, tenant string) {
		t.Helper()
		if got.Path != path || got.Revision != configRevision(raw) || string(got.JSON) != string(source.JSON) || appID != "cli_meetings" || tenant != "lark" {
			t.Fatalf("checker used the wrong saved source or App: %s %s %s/%s", got.Path, got.Revision, tenant, appID)
		}
	}
	probeCalls := 0
	m.liveMeetings.checkAvailability = func(ctx context.Context, got webSource, appID, tenant string) (bridge.MeetingPermissionReport, error) {
		probeCalls++
		checkSource(got, appID, tenant)
		switch probeCalls {
		case 1:
			return bridge.MeetingPermissionReport{}, context.DeadlineExceeded
		case 3:
			if ctx.Err() != context.Canceled {
				t.Fatalf("Esc did not cancel the availability retry: %v", ctx.Err())
			}
		}
		return bridge.MeetingPermissionReport{AppID: appID, Tenant: tenant, Availability: "not-in-beta"}, nil
	}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 15})
	if !strings.Contains(m.View(), "H 在线会议") {
		t.Fatal("online meetings shortcut is missing at 40 columns")
	}
	status := press('H')
	if status == nil {
		t.Fatal("H was blocked without a receiver registry")
	}
	m.Update(status())
	if m.screen != tuiLiveMeetingsPick || m.liveMeetings.process != "" || !strings.Contains(m.View(), "p 查权限") {
		t.Fatalf("permissions shortcut is missing with receiver unavailable: %s", m.View())
	}
	unavailable := m.status
	if press('v') != nil || m.screen != tuiLiveMeetingsPick {
		t.Fatal("availability check was offered before the permission report")
	}
	const scope = "vc:meeting.bot.join:write"
	const authorizationURL = "https://open.larksuite.com/app/cli_meetings/auth?q=vc%3Ameeting.message%3Awrite"
	const versionID = "1234567890123456789"
	const invited = "vc.bot.meeting_invited_v1"
	const activity = "vc.bot.meeting_activity_v1"
	const ended = "vc.bot.meeting_ended_v1"
	calls := 0
	m.liveMeetings.checkPermissions = func(ctx context.Context, got webSource, appID, tenant string) (bridge.MeetingPermissionReport, error) {
		calls++
		checkSource(got, appID, tenant)
		if calls == 1 && ctx.Err() != context.Canceled {
			t.Fatalf("Esc did not cancel the first check: %v", ctx.Err())
		}
		return bridge.MeetingPermissionReport{
			AppID: appID, Tenant: tenant, Enabled: true,
			PublishedVersionID: versionID, PublicationState: "verified",
			Events:      []string{invited, activity, ended},
			EventStates: map[string]string{invited: "subscribed", activity: "missing", ended: "unknown"},
			Scopes: []bridge.MeetingPermissionScope{
				{Scope: scope, State: "granted"},
				{Scope: "vc:meeting.message:write", State: "missing", AuthorizationURL: authorizationURL},
			},
		}, nil
	}
	first := press('p')
	if first == nil || m.screen != tuiMeetingPermissionsLoading || !strings.Contains(m.View(), "lark/cli_meetings") {
		t.Fatalf("check for selected App was not scheduled: %s", m.View())
	}
	tuiKeys(m, tea.KeyEsc)
	if m.screen != tuiLiveMeetingsPick || m.status != unavailable {
		t.Fatalf("Esc did not restore unavailable meetings: %s", m.View())
	}
	second := press('p')
	if second == nil || calls != 0 {
		t.Fatal("check must run asynchronously")
	}
	m.Update(first())
	if m.screen != tuiMeetingPermissionsLoading {
		t.Fatal("a canceled check replaced the reopened check")
	}
	m.Update(second())
	if m.screen != tuiMeetingPermissionsResult || probeCalls != 0 {
		t.Fatalf("p must show the permissions report without probing availability: %s", m.View())
	}
	for _, want := range []string{"lark/cli_meetings", "入会放行或实际推送", "已核对线上版本", "版本 ID · " + versionID, "应用权限项与状态", "已授权 · " + scope,
		"v 查接口", "查所选 App 所有者的活动会议，仅显示状态"} {
		if !strings.Contains(m.View(), want) {
			t.Fatalf("check omitted %q: %s", want, m.View())
		}
	}
	tuiKeys(m, tea.KeyPgDown)
	for _, want := range []string{"未授权", "事件订阅状态", invited + " · 已订阅", activity + " · 未订阅", ended + " · 未知"} {
		if !strings.Contains(m.View(), want) {
			t.Fatalf("scrolling did not show %q: %s", want, m.View())
		}
	}
	if !strings.Contains(m.View(), authorizationURL[:40]) {
		t.Fatalf("missing scope did not show its console URL: %s", m.View())
	}
	tuiKeys(m, tea.KeyRight, tea.KeyRight, tea.KeyRight, tea.KeyRight, tea.KeyRight)
	if !strings.Contains(m.View(), "q=vc%3Ameeting.message%3Awrite") {
		t.Fatalf("horizontal scrolling did not show the complete authorization query: %s", m.View())
	}
	probe := press('v')
	if probe == nil || m.screen != tuiMeetingPermissionsLoading || press('v') != nil || probeCalls != 0 {
		t.Fatalf("v should schedule one asynchronous availability check: %s", m.View())
	}
	m.Update(probe())
	if !strings.Contains(m.View(), "查询超时。请按 v 重试。") {
		t.Fatalf("availability timeout did not offer retry: %s", m.View())
	}
	m.source.Revision = "new-revision"
	retry := press('v')
	m.source.Revision = source.Revision
	if retry == nil {
		t.Fatal("v did not schedule an availability retry")
	}
	m.Update(retry())
	if m.screen != tuiMeetingPermissionsResult || !strings.Contains(m.View(), "平台暂未开放此接口") {
		t.Fatalf("missing availability result: %s", m.View())
	}
	canceled := press('v')
	if canceled == nil {
		t.Fatal("v did not schedule another availability check")
	}
	tuiKeys(m, tea.KeyEsc)
	m.Update(canceled())
	if probeCalls != 3 || m.screen != tuiLiveMeetingsPick || m.status != unavailable || m.draft != draft || m.account != 1 || m.row != row || m.source.Revision != source.Revision {
		t.Fatalf("permissions check changed the configuration or navigation state: %s", m.View())
	}
}
