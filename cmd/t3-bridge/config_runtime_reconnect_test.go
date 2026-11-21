package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestRuntimeReconnectConfirmationFlow(t *testing.T) {
	const path = "/runtime/reconnect"
	const origin = "http://" + webTestHost
	const process = "0123456789abcdef0123456789abcdef"
	const legacy = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const unsupported = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const appID = "cli_reconnect"
	processes := []receiverProcess{
		{ID: process, PID: 1234, Reconnect: true, ReconnectWait: true, Accounts: []receiverProcessAccount{{Tenant: "lark", AppID: appID}, {Tenant: "feishu", AppID: "cli_peer"}}},
		{ID: legacy, Reconnect: true, Accounts: []receiverProcessAccount{{Tenant: "lark", AppID: "cli_legacy"}}},
		{ID: unsupported, Accounts: []receiverProcessAccount{{Tenant: "lark", AppID: appID}}},
	}
	calls := 0
	var calledProcess, calledTenant, calledApp string
	var calledAction bridge.ReconnectAction
	var timed bool
	var result bridge.LocalReconnectView
	var operationErr error
	handler := configRuntimeReconnectHandler(func() ([]receiverProcess, error) { return processes, nil },
		func(ctx context.Context, id, tenant, app string, action bridge.ReconnectAction) (bridge.LocalReconnectView, error) {
			calls++
			_, timed = ctx.Deadline()
			calledProcess, calledTenant, calledApp, calledAction = id, tenant, app, action
			return result, operationErr
		})
	request := func(method, endpoint, body, requestOrigin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, origin+endpoint, strings.NewReader(body))
		if requestOrigin != "" {
			r.Header.Set("Origin", requestOrigin)
		}
		if method == http.MethodPost {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	form := url.Values{"process": {process}, "tenant": {"lark"}, "appId": {appID}}
	selection := path + "?" + form.Encode()
	legacyForm := url.Values{"process": {legacy}, "tenant": {"lark"}, "appId": {"cli_legacy"}}
	const confirmation = `name="confirm" value="reconnect"`
	const nowButton = `name="action" value="now"`
	const waitButton = `name="action" value="wait"`
	const cancelButton = `name="action" value="cancel"`
	page := request(http.MethodGet, path, "", "")
	assertWebPage(t, page, http.StatusOK, "选择接收应用", "cli_peer", "cli_legacy", appID)
	if strings.Contains(page.Body.String(), unsupported) || strings.Contains(page.Body.String(), confirmation) {
		t.Fatal("the target list included an unsupported process or a confirmation without a selection")
	}
	page = request(http.MethodGet, selection, "", "")
	assertWebPage(t, page, http.StatusOK, process, appID, "lark", "PID 1234", "任务回执会保留", "申请恢复（需新版接收端）", confirmation, nowButton, waitButton, cancelButton)
	if strings.Contains(page.Body.String(), "cli_peer") {
		t.Fatal("the confirmation included another app")
	}
	page = request(http.MethodGet, path+"?"+legacyForm.Encode(), "", "")
	assertWebPage(t, page, http.StatusOK, legacy, "cli_legacy", confirmation, nowButton)
	if strings.Contains(page.Body.String(), waitButton) || strings.Contains(page.Body.String(), cancelButton) {
		t.Fatal("the legacy receiver displayed unsupported actions")
	}
	for _, action := range []string{"now", "wait", "cancel"} {
		assertWebPage(t, request(http.MethodGet, selection+"&action="+action, "", ""), http.StatusBadRequest)
	}
	if calls != 0 {
		t.Fatal("GET invoked reconnect")
	}
	missingConfirmation := form.Encode()
	form.Set("confirm", "reconnect")
	confirmed := form.Encode()
	legacyForm.Set("confirm", "reconnect")
	legacyConfirmed := legacyForm.Encode()
	for _, tc := range []struct {
		name, endpoint, body, origin string
		status                       int
	}{
		{"missing origin", path, confirmed, "", http.StatusForbidden},
		{"foreign origin wait", path, confirmed + "&action=wait", "https://foreign.test", http.StatusForbidden},
		{"foreign origin cancel", path, confirmed + "&action=cancel", "https://foreign.test", http.StatusForbidden},
		{"missing confirmation", path, missingConfirmation + "&action=wait", origin, http.StatusBadRequest},
		{"duplicate confirmation", path, confirmed + "&confirm=reconnect", origin, http.StatusBadRequest},
		{"duplicate target field", path, confirmed + "&tenant=lark", origin, http.StatusBadRequest},
		{"duplicate action", path, confirmed + "&action=wait&action=wait", origin, http.StatusBadRequest},
		{"invalid action", path, confirmed + "&action=restart", origin, http.StatusBadRequest},
		{"empty action", path, confirmed + "&action=", origin, http.StatusBadRequest},
		{"unknown field", path, confirmed + "&action=wait&unexpected=1", origin, http.StatusBadRequest},
		{"query on submit", path + "?action=wait", confirmed, origin, http.StatusBadRequest},
		{"wrong tenant", path, strings.Replace(confirmed, "tenant=lark", "tenant=feishu", 1) + "&action=wait", origin, http.StatusNotFound},
		{"wrong process", path, strings.Replace(confirmed, process, legacy, 1) + "&action=cancel", origin, http.StatusNotFound},
		{"wrong app", path, strings.Replace(confirmed, appID, "cli_peer", 1) + "&action=wait", origin, http.StatusNotFound},
		{"unsupported receiver", path, strings.Replace(confirmed, process, unsupported, 1), origin, http.StatusNotFound},
		{"legacy wait", path, legacyConfirmed + "&action=wait", origin, http.StatusBadRequest},
		{"legacy cancel", path, legacyConfirmed + "&action=cancel", origin, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertWebPage(t, request(http.MethodPost, tc.endpoint, tc.body, tc.origin), tc.status)
			if calls != 0 {
				t.Fatal("rejected request invoked reconnect")
			}
		})
	}
	processes[0].Reconnect = false
	assertWebPage(t, request(http.MethodPost, path, confirmed, origin), http.StatusNotFound)
	if calls != 0 {
		t.Fatal("reconnect was invoked after the capability was removed")
	}
	processes[0].Reconnect = true
	processes[0].ReconnectWait = false
	assertWebPage(t, request(http.MethodPost, path, confirmed+"&action=wait", origin), http.StatusBadRequest, "此接收进程仅支持立即重连。")
	if calls != 0 {
		t.Fatal("wait was invoked after the capability was removed")
	}
	processes[0].ReconnectWait = true
	for _, tc := range []struct {
		body, process, app string
		action             bridge.ReconnectAction
		state              string
		err                error
		status             int
		marker             string
	}{
		{confirmed, process, appID, bridge.ReconnectNow, bridge.ReconnectStarted, nil, http.StatusOK, "T3 MCP 已核验，飞书 WebSocket 正在连接。"},
		{confirmed + "&action=now", process, appID, bridge.ReconnectNow, bridge.ReconnectRecoveryRequested, nil, http.StatusOK, "已受理恢复请求。请在运行状态中核对连接。"},
		{confirmed + "&action=wait", process, appID, bridge.ReconnectAfterRuns, bridge.ReconnectWorkerOffline, nil, http.StatusConflict, "此 App 离线，等待设置未变。请返回运行状态，选择立即重连。"},
		{confirmed + "&action=cancel", process, appID, bridge.ReconnectCancelWait, bridge.ReconnectWorkerOffline, nil, http.StatusConflict, "此 App 离线，等待设置未变。请返回运行状态，选择立即重连。"},
		{legacyConfirmed, legacy, "cli_legacy", bridge.ReconnectNow, bridge.ReconnectStarted, nil, http.StatusOK, "T3 MCP 已核验，飞书 WebSocket 正在连接。"},
		{legacyConfirmed + "&action=now", legacy, "cli_legacy", bridge.ReconnectNow, bridge.ReconnectPending, nil, http.StatusOK, "此 App 已在等待重连，原等待继续有效。"},
		{confirmed + "&action=wait", process, appID, bridge.ReconnectAfterRuns, bridge.ReconnectScheduled, nil, http.StatusOK, "已登记等待重连。"},
		{confirmed + "&action=wait", process, appID, bridge.ReconnectAfterRuns, bridge.ReconnectPending, nil, http.StatusOK, "原等待继续有效。"},
		{confirmed + "&action=cancel", process, appID, bridge.ReconnectCancelWait, bridge.ReconnectCancelled, nil, http.StatusOK, "已取消等待重连"},
		{confirmed + "&action=cancel", process, appID, bridge.ReconnectCancelWait, bridge.ReconnectIdle, nil, http.StatusOK, "当前没有待取消的重连。"},
		{confirmed, process, appID, bridge.ReconnectNow, bridge.ReconnectUnconfirmed, nil, http.StatusBadGateway, "请先查看运行状态"},
		{confirmed + "&action=cancel", process, appID, bridge.ReconnectCancelWait, "", errors.New("synthetic private error"), http.StatusBadGateway, "请先查看运行状态"},
	} {
		result, operationErr = bridge.LocalReconnectView{State: tc.state}, tc.err
		before := calls
		page = request(http.MethodPost, path, tc.body, origin)
		assertWebPage(t, page, tc.status, tc.marker, `href="/runtime">返回运行状态`)
		body := page.Body.String()
		if calls != before+1 || !timed || calledProcess != tc.process || calledTenant != "lark" || calledApp != tc.app || calledAction != tc.action {
			t.Fatalf("unexpected reconnect call for %q: %d calls to %s/%s/%s with action %s and deadline %t", tc.body, calls-before, calledProcess, calledTenant, calledApp, calledAction, timed)
		}
		if strings.Contains(body, confirmation) || strings.Contains(body, "WebSocket 已就绪") || strings.Contains(body, "synthetic private error") {
			t.Fatalf("unexpected reconnect response: %d calls, %s", calls-before, body)
		}
	}
}
