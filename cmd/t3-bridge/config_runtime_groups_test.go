package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestConfigRuntimeGroupsDispatchesOnlyToSelectedAccount(t *testing.T) {
	const processID = "0123456789abcdef0123456789abcdef"
	const legacyID = "fedcba9876543210fedcba9876543210"
	requests := []bridge.GroupAccessRequest{}
	scope := bridge.GroupMessageScopeGranted
	messageEvent := "subscribed"
	list := func() ([]receiverProcess, error) {
		return []receiverProcess{
			{ID: processID, PID: 31, GroupAccess: true, Accounts: []receiverProcessAccount{{Tenant: "feishu", AppID: "cli_first"}, {Tenant: "lark", AppID: "cli_second"}}},
			{ID: legacyID, PID: 32, Accounts: []receiverProcessAccount{{Tenant: "lark", AppID: "cli_old"}}},
		}, nil
	}
	handler := configRuntimeGroupsHandler(list, func(ctx context.Context, process, tenant, app string, req bridge.GroupAccessRequest) (bridge.GroupAccessView, error) {
		if _, timed := ctx.Deadline(); !timed || process != processID || tenant != "lark" || app != "cli_second" {
			t.Fatal("operation used the wrong account or had no deadline")
		}
		requests = append(requests, req)
		return bridge.GroupAccessView{DefaultRequiresMention: true, MessageScope: scope, MessageEvent: messageEvent, Groups: []bridge.GroupAccessEntry{{
			ID: "oc_work", Name: "<群名称>", Allowed: true, MentionOverride: "off",
		}}}, nil
	})
	base := "http://" + webTestHost
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, base+path, nil))
		return w
	}
	first := get("/runtime/groups")
	assertWebPage(t, first, http.StatusOK, "在线群授权", "cli_first", "cli_second")
	if len(requests) != 0 || strings.Contains(first.Body.String(), "cli_old") {
		t.Fatal("initial view queried a receiver or offered an unsupported receiver")
	}
	wrong := (runtimeGroupTarget{ProcessID: processID, Tenant: "feishu", AppID: "cli_second"}).URL("")
	assertWebPage(t, get(wrong), http.StatusNotFound, "账号已不可用")
	if len(requests) != 0 {
		t.Fatal("queried a receiver with a mismatched tenant")
	}
	target := runtimeGroupTarget{ProcessID: processID, Tenant: "lark", AppID: "cli_second"}
	selected := target.URL("")
	page := get(selected)
	assertWebPage(t, page, http.StatusOK, "<群名称>", "oc_work", "单群覆盖", `name="appId" value="cli_second"`, "飞书无 @ 群消息权限 · 已授权")
	if !strings.Contains(page.Body.String(), "&lt;群名称&gt;") || strings.Contains(page.Body.String(), "<群名称>") || len(requests) != 1 || !reflect.DeepEqual(requests[0], bridge.GroupAccessRequest{}) {
		t.Fatal("selected read was unexpected or the group name was not escaped")
	}
	form := url.Values{"process": {processID}, "tenant": {"lark"}, "appId": {"cli_second"}, "chatId": {"oc_work"}, "access": {"deny"}, "mention": {"inherit"}}
	post := httptest.NewRequest(http.MethodPost, base+"/runtime/groups", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("Origin", base)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, post)
	want := bridge.GroupAccessRequest{ChatID: "oc_work", Access: "deny", Mention: "inherit"}
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != selected+"&updated=1" || len(requests) != 2 || !reflect.DeepEqual(requests[1], want) {
		t.Fatalf("unexpected mutation or redirect: %d %s, %+v", w.Code, w.Header().Get("Location"), requests)
	}
	assertWebPage(t, get(w.Header().Get("Location")), http.StatusOK, "群权限已更新。", `name="chatId" value="oc_work"`, "应用默认 @ 规则")
	for _, tc := range []struct {
		name          string
		scope         bridge.GroupMessageScope
		scopeStatus   string
		scopeGuidance bool
		event         string
		eventStatus   string
		eventGuidance bool
	}{
		{"subscribed", bridge.GroupMessageScopeMissing, "缺失", true, "subscribed", "已订阅", false},
		{"missing", bridge.GroupMessageScopeGranted, "已授权", false, "missing", "未订阅", true},
		{"unpublished", bridge.GroupMessageScopeGranted, "已授权", false, "unpublished", "应用未发布", true},
		{"unknown", bridge.GroupMessageScopeUnknown, "未知", false, "unknown", "未知", false},
		{"legacy receiver", "", "未知", false, "", "未知", false},
		{"unrecognized event state", bridge.GroupMessageScopeGranted, "已授权", false, "unrecognized", "未知", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope, messageEvent = tc.scope, tc.event
			page := get(selected)
			assertWebPage(t, page, http.StatusOK, "飞书无 @ 群消息权限 · "+tc.scopeStatus,
				"线上消息事件订阅 · "+tc.eventStatus, "im.message.receive_v1", "实际投递需要用测试消息单独确认")
			body := page.Body.String()
			if strings.Contains(body, "管理员私聊发送 /config mention authorize") != tc.scopeGuidance {
				t.Fatalf("unexpected scope guidance: %s", body)
			}
			if strings.Contains(body, "核对事件订阅与应用发布状态") != tc.eventGuidance {
				t.Fatalf("unexpected event guidance: %s", body)
			}
		})
	}
}

func TestConfigRuntimeGroupsFocusFiltersByExactAccountAndChat(t *testing.T) {
	const processID = "0123456789abcdef0123456789abcdef"
	const otherID = "fedcba9876543210fedcba9876543210"
	const groupID = "oc_work"
	const appID = "cli_second"
	list := func() ([]receiverProcess, error) {
		return []receiverProcess{
			{ID: processID, GroupAccess: true, Accounts: []receiverProcessAccount{{Tenant: "feishu", AppID: "cli_first"}, {Tenant: "feishu", AppID: appID}}},
			{ID: otherID, GroupAccess: true, Accounts: []receiverProcessAccount{{Tenant: "lark", AppID: appID}}},
		}, nil
	}
	for _, tc := range []struct {
		name, query, process, tenant string
		status                       int
	}{
		{"default tenant", "chatId=oc_work&appId=cli_second&tenant=", processID, "feishu", http.StatusOK},
		{"lark", "chatId=oc_work&appId=cli_second&tenant=lark", otherID, "lark", http.StatusOK},
		{"explicit process and ignored write fields", "chatId=oc_work&appId=cli_second&tenant=feishu&process=" + processID + "&access=deny&mention=off", processID, "feishu", http.StatusOK},
		{"wrong process", "chatId=oc_work&appId=cli_second&tenant=feishu&process=" + otherID, "", "", http.StatusNotFound},
		{"wrong tenant", "chatId=oc_work&appId=cli_first&tenant=lark", "", "", http.StatusNotFound},
		{"missing app", "chatId=oc_work&tenant=feishu", "", "", http.StatusBadRequest},
		{"duplicate app", "chatId=oc_work&appId=cli_second&appId=cli_first", "", "", http.StatusBadRequest},
		{"duplicate chat", "chatId=oc_work&chatId=oc_other&appId=cli_second", "", "", http.StatusBadRequest},
		{"malformed chat escape", "chatId=%zz&appId=cli_second&tenant=feishu&process=" + processID, "", "", http.StatusBadRequest},
		{"empty chat", "chatId=&appId=cli_second&tenant=feishu&process=" + processID, "", "", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			handler := configRuntimeGroupsHandler(list, func(_ context.Context, process, tenant, app string, req bridge.GroupAccessRequest) (bridge.GroupAccessView, error) {
				calls++
				if process != tc.process || tenant != tc.tenant || app != appID || !reflect.DeepEqual(req, bridge.GroupAccessRequest{}) {
					t.Fatalf("unexpected receiver read: %q %q %q %+v", process, tenant, app, req)
				}
				return bridge.GroupAccessView{JoinedAvailable: true, MessageEvent: "unpublished", Groups: []bridge.GroupAccessEntry{
					{ID: "oc_work_other", Name: "同名群"},
					{ID: groupID, Name: "同名群", Allowed: true, Joined: true, AccessOverride: "allow"},
				}}, nil
			})
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://"+webTestHost+"/runtime/groups?"+tc.query, nil))
			assertWebPage(t, w, tc.status)
			body := w.Body.String()
			if tc.status == http.StatusOK {
				assertWebPage(t, w, tc.status, "已授权 （运行覆盖）", "机器人已入群", "查看完整在线群授权", "飞书无 @ 群消息权限", `name="returnToGroup" value="1"`,
					"线上消息事件订阅 · 应用未发布", "im.message.receive_v1", "核对事件订阅与应用发布状态",
					`name="chatId" value="oc_work"`, `name="appId" value="cli_second"`, `name="process" value="`+tc.process+`"`, `name="tenant" value="`+tc.tenant+`"`)
				if calls != 1 || strings.Count(body, `<form method="post"`) != 1 || strings.Contains(body, "cli_first") || strings.Contains(body, "oc_work_other") {
					t.Fatalf("focused view contains another target or group: %s", body)
				}
			} else if calls != 0 || strings.Contains(body, `<form method="post"`) {
				t.Fatalf("invalid focus dispatched or exposed a form: %s", body)
			}
		})
	}
}

func TestConfigRuntimeGroupsFocusRequiresProcessAndAvailableGroup(t *testing.T) {
	const firstID = "0123456789abcdef0123456789abcdef"
	const secondID = "fedcba9876543210fedcba9876543210"
	const appID = "cli_shared"
	accounts := []receiverProcessAccount{{Tenant: "feishu", AppID: appID}}
	list := func() ([]receiverProcess, error) {
		return []receiverProcess{
			{ID: firstID, GroupAccess: true, Accounts: accounts},
			{ID: secondID, GroupAccess: true, Accounts: accounts},
		}, nil
	}
	var requestedProcesses []string
	handler := configRuntimeGroupsHandler(list, func(_ context.Context, process, tenant, app string, req bridge.GroupAccessRequest) (bridge.GroupAccessView, error) {
		if tenant != "feishu" || app != appID || !reflect.DeepEqual(req, bridge.GroupAccessRequest{}) {
			t.Fatalf("unexpected receiver read: %q %q %+v", tenant, app, req)
		}
		requestedProcesses = append(requestedProcesses, process)
		return bridge.GroupAccessView{Groups: []bridge.GroupAccessEntry{{ID: "oc_configured", Allowed: true}}}, nil
	})
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://"+webTestHost+path, nil))
		return w
	}
	first := runtimeGroupTarget{ProcessID: firstID, Tenant: "feishu", AppID: appID}
	second := runtimeGroupTarget{ProcessID: secondID, Tenant: "feishu", AppID: appID}
	ambiguous := get("/runtime/groups?appId=" + appID + "&chatId=oc_configured")
	assertWebPage(t, ambiguous, http.StatusOK, "请明确选择", "查看完整在线群授权",
		first.URL("oc_configured"), second.URL("oc_configured"))
	if len(requestedProcesses) != 0 || strings.Contains(ambiguous.Body.String(), `<form method="post"`) {
		t.Fatal("ambiguous focus selected a receiver or rendered a form")
	}
	missing := get(second.URL("oc_missing"))
	assertWebPage(t, missing, http.StatusNotFound, "未找到该群", "查看完整在线群授权")
	if strings.Contains(missing.Body.String(), `<form method="post"`) || strings.Contains(missing.Body.String(), "oc_configured") {
		t.Fatal("missing focus fell back to a different group")
	}
	configured := get(second.URL("oc_configured"))
	assertWebPage(t, configured, http.StatusOK, "已授权", "机器人入群状态未确认", `name="chatId" value="oc_configured"`)
	if strings.Contains(configured.Body.String(), "机器人未入群") || len(requestedProcesses) != 2 || requestedProcesses[0] != secondID || requestedProcesses[1] != secondID {
		t.Fatalf("unexpected membership or receiver selection: %v", requestedProcesses)
	}
}

func TestConfigRuntimeGroupsRejectsUntrustedAndInvalidMutations(t *testing.T) {
	const processID = "0123456789abcdef0123456789abcdef"
	const form = "process=" + processID + "&tenant=feishu&appId=cli_synthetic&mention=off"
	base := "http://" + webTestHost
	calls := 0
	handler := configRuntimeGroupsHandler(func() ([]receiverProcess, error) {
		return []receiverProcess{{ID: processID, GroupAccess: true, Accounts: []receiverProcessAccount{{Tenant: "feishu", AppID: "cli_synthetic"}}}}, nil
	}, func(context.Context, string, string, string, bridge.GroupAccessRequest) (bridge.GroupAccessView, error) {
		calls++
		return bridge.GroupAccessView{}, errors.New("private receiver details")
	})
	for _, tc := range []struct {
		name, origin, body string
		status             int
	}{
		{"missing origin", "", form, http.StatusForbidden},
		{"foreign origin", "http://foreign.test", form, http.StatusForbidden},
		{"duplicate action", base, form + "&mention=on", http.StatusBadRequest},
		{"group return without group", base, form + "&returnToGroup=1", http.StatusBadRequest},
		{"unknown field", base, form + "&credential=sample", http.StatusBadRequest},
		{"invalid command", base, strings.Replace(form, "=off", "=everything", 1), http.StatusBadRequest},
		{"different app", base, strings.Replace(form, "appId=cli_synthetic", "appId=cli_other", 1) + "&chatId=oc_work", http.StatusNotFound},
		{"different tenant", base, strings.Replace(form, "tenant=feishu", "tenant=lark", 1) + "&chatId=oc_work", http.StatusNotFound},
		{"unknown outcome", base, form, http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := calls
			r := httptest.NewRequest(http.MethodPost, base+"/runtime/groups", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.status || (calls != before) != (tc.status == http.StatusBadGateway) || strings.Contains(w.Body.String(), "private receiver details") {
				t.Fatalf("unexpected command dispatch: %d, %s", w.Code, w.Body)
			}
		})
	}
}

func TestConfigRuntimeGroupsBatchGrantReviewsExactSelection(t *testing.T) {
	const processID = "0123456789abcdef0123456789abcdef"
	const otherID = "fedcba9876543210fedcba9876543210"
	const appID = "cli_synthetic"
	const fullID = "oc_0123456789abcdef0123456789abcdef"
	const base = "http://" + webTestHost
	account := receiverProcessAccount{Tenant: "feishu", AppID: appID}
	list := func() ([]receiverProcess, error) {
		return []receiverProcess{
			{ID: processID, GroupAccess: true, GroupBatchGrant: true, Accounts: []receiverProcessAccount{account, {Tenant: "lark", AppID: appID}, {Tenant: "feishu", AppID: "cli_other"}}},
			{ID: otherID, GroupAccess: true, GroupBatchGrant: true, Accounts: []receiverProcessAccount{account}},
		}, nil
	}
	view := bridge.GroupAccessView{JoinedAvailable: true, Groups: []bridge.GroupAccessEntry{
		{ID: fullID, Name: "<研发群>", Joined: true},
		{ID: fullID + "_other", Name: "未选中的群", Joined: true},
		{ID: "oc_release", Name: "发布群", Joined: true, AccessOverride: "deny"},
		{ID: "oc_allowed", Joined: true, Allowed: true},
		{ID: "oc_not_joined"},
	}}
	var requests []bridge.GroupAccessRequest
	handler := configRuntimeGroupsHandler(list, func(_ context.Context, process, tenant, app string, req bridge.GroupAccessRequest) (bridge.GroupAccessView, error) {
		if process != processID || tenant != account.Tenant || app != appID {
			t.Fatalf("dispatched to another target: %q %q %q", process, tenant, app)
		}
		requests = append(requests, req)
		return view, nil
	})
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, base+path, nil))
		return w
	}
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, base+"/runtime/groups", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", base)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	target := runtimeGroupTarget{ProcessID: processID, Tenant: account.Tenant, AppID: appID}
	checkbox := `type="checkbox" form="runtime-group-batch" name="groupId" value="`
	page := get(target.URL(""))
	assertWebPage(t, page, http.StatusOK, "预览批量授权", checkbox+fullID+`"`, checkbox+`oc_release"`)
	if strings.Count(page.Body.String(), checkbox) != 3 {
		t.Fatal("batch selection included an ineligible group or omitted an eligible group")
	}
	if strings.Contains(get(target.URL(fullID)).Body.String(), checkbox) {
		t.Fatal("focused group page offered batch selection")
	}
	prefix := "process=" + processID + "&tenant=feishu&appId=" + appID
	selection := "&groupId=" + fullID + "&groupId=oc_release"
	apply := prefix + "&batch=apply" + selection
	assertWebPage(t, post(apply), http.StatusConflict, "请重新预览")
	if len(requests) != 2 {
		t.Fatal("apply without preview invoked the receiver")
	}
	view.JoinedAvailable = false
	if strings.Contains(get(target.URL("")).Body.String(), checkbox) {
		t.Fatal("batch selection was offered without confirmed membership")
	}
	assertWebPage(t, post(prefix+"&batch=preview"+selection), http.StatusConflict, "无法确认所选群")
	view.JoinedAvailable = true
	for _, id := range []string{"oc_allowed", "oc_not_joined", "oc_missing"} {
		w := post(prefix + "&batch=preview&groupId=" + id)
		assertWebPage(t, w, http.StatusConflict, "无法确认所选群")
		if strings.Contains(w.Body.String(), `name="batch" value="apply"`) {
			t.Fatal("ineligible group produced a confirmation form")
		}
	}
	beforePreview := len(requests)
	preview := post(prefix + "&batch=preview" + selection)
	hiddenID := `type="hidden" name="groupId" value="`
	assertWebPage(t, preview, http.StatusOK, "确认授权 2 个群", "<研发群>", "发布群", `<code>`+fullID+`</code>`, `<code>oc_release</code>`,
		`name="batch" value="apply"`, hiddenID+fullID+`"`, hiddenID+`oc_release"`)
	if strings.Count(preview.Body.String(), hiddenID) != 2 || strings.Contains(preview.Body.String(), "未选中的群") ||
		!strings.Contains(preview.Body.String(), "&lt;研发群&gt;") {
		t.Fatal("preview did not show the exact selection with escaped names")
	}
	readCount := len(requests)
	for _, changed := range []string{
		strings.Replace(apply, processID, otherID, 1),
		strings.Replace(apply, "tenant=feishu", "tenant=lark", 1),
		strings.Replace(apply, appID, "cli_other", 1),
		strings.Replace(apply, fullID, fullID+"_other", 1),
		strings.TrimSuffix(apply, "&groupId=oc_release"),
	} {
		assertWebPage(t, post(changed), http.StatusConflict, "请重新预览")
	}
	if len(requests) != readCount || readCount != beforePreview+1 {
		t.Fatalf("unexpected receiver calls before confirmation: %+v", requests)
	}
	for _, req := range requests {
		if !reflect.DeepEqual(req, bridge.GroupAccessRequest{}) {
			t.Fatalf("preview or page read dispatched a write: %+v", req)
		}
	}
	w := post(apply)
	want := bridge.GroupAccessRequest{ChatIDs: []string{fullID, "oc_release"}, Access: "allow"}
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != target.URL("")+"&updated=1" ||
		len(requests) != readCount+1 || !reflect.DeepEqual(requests[readCount], want) {
		t.Fatalf("unexpected batch request or redirect: %d %q, %+v", w.Code, w.Header().Get("Location"), requests)
	}
	assertWebPage(t, post(apply), http.StatusConflict, "请重新预览")
	if len(requests) != readCount+1 {
		t.Fatal("replayed confirmation invoked the receiver")
	}
}

func TestConfigRuntimeGroupsBatchGrantRejectsInvalidFormsAndLegacyReceiver(t *testing.T) {
	const processID = "0123456789abcdef0123456789abcdef"
	const base = "http://" + webTestHost
	const path = "/runtime/groups"
	const target = "process=" + processID + "&tenant=feishu&appId=cli_synthetic"
	const preview = target + "&batch=preview&groupId=oc_selected"
	oversizedSelection := target + "&batch=preview"
	for i := 0; i <= bridge.MaxGroupGrantBatch; i++ {
		oversizedSelection += fmt.Sprintf("&groupId=oc_%d", i)
	}
	handler := configRuntimeGroupsHandler(func() ([]receiverProcess, error) {
		return []receiverProcess{{ID: processID, GroupAccess: true, Accounts: []receiverProcessAccount{{Tenant: "feishu", AppID: "cli_synthetic"}}}}, nil
	}, func(context.Context, string, string, string, bridge.GroupAccessRequest) (bridge.GroupAccessView, error) {
		t.Fatal("invalid batch invoked the receiver")
		return bridge.GroupAccessView{}, nil
	})
	for _, tc := range []struct {
		name, path, origin, body string
		status                   int
	}{
		{"missing origin", path, "", preview, http.StatusForbidden},
		{"foreign origin", path, "http://foreign.test", preview, http.StatusForbidden},
		{"query mixed with body", path + "?batch=apply", base, preview, http.StatusBadRequest},
		{"unknown action", path, base, target + "&batch=allow&groupId=oc_selected", http.StatusBadRequest},
		{"duplicate action", path, base, preview + "&batch=apply", http.StatusBadRequest},
		{"duplicate target field", path, base, preview + "&tenant=feishu", http.StatusBadRequest},
		{"legacy access field", path, base, preview + "&access=allow", http.StatusBadRequest},
		{"legacy mention field", path, base, preview + "&mention=off", http.StatusBadRequest},
		{"singular chat ID", path, base, preview + "&chatId=oc_selected", http.StatusBadRequest},
		{"missing selection", path, base, target + "&batch=preview", http.StatusBadRequest},
		{"duplicate group", path, base, preview + "&groupId=oc_selected", http.StatusBadRequest},
		{"malformed group ID", path, base, preview + "&groupId=%20oc_other", http.StatusBadRequest},
		{"too many groups", path, base, oversizedSelection, http.StatusBadRequest},
		{"oversized body", path, base, preview + strings.Repeat("x", 4096), http.StatusBadRequest},
		{"legacy receiver preview", path, base, preview, http.StatusConflict},
		{"legacy receiver apply", path, base, strings.Replace(preview, "=preview", "=apply", 1), http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, base+tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.status, w.Body)
			}
			if tc.status == http.StatusConflict {
				assertWebPage(t, w, tc.status, "此接收进程不支持批量群授权")
			}
		})
	}
}

func TestConfigRuntimeGroupsReadsLargeViewThroughReceiver(t *testing.T) {
	const appID = "cli_synthetic"
	registry := localReceiverProcesses(profileRegistry{root: t.TempDir()})
	process, err := registry.register([]bridge.Config{{AppID: appID}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-stopped
		if err := process.close(); err != nil {
			t.Error(err)
		}
	})
	account := newReceiverGroupAccount("feishu", appID)
	pending := receiverGroupPendingPath(process.dir, account)
	groups := make([]bridge.GroupAccessEntry, 300)
	for i := range groups {
		groups[i] = bridge.GroupAccessEntry{ID: fmt.Sprintf("oc_group_%d", i), Name: fmt.Sprintf("合成群组 %d", i), Joined: true}
	}
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				process.serveGroupAccess(ctx, account, pending, func(context.Context, bridge.GroupAccessRequest) (bridge.GroupAccessView, error) {
					return bridge.GroupAccessView{JoinedAvailable: true, Groups: groups}, nil
				})
			}
		}
	}()
	target := runtimeGroupTarget{ProcessID: process.process.ID, Tenant: "feishu", AppID: appID}
	w := httptest.NewRecorder()
	configRuntimeGroupsHandler(registry.list, registry.groupAccess).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://"+webTestHost+target.URL(""), nil))
	assertWebPage(t, w, http.StatusOK, "oc_group_0", "oc_group_299", "机器人已入群")
}
