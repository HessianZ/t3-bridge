package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestRuntimeMeetingsReviewFlow(t *testing.T) {
	const path = "/runtime/meetings"
	const origin = "http://" + webTestHost
	const process = "0123456789abcdef0123456789abcdef"
	const appID = "cli_meetings"
	const revision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const number = "123456789"
	const pendingNumber = "234567890"
	const confirmButton = `name="confirm" value="yes"`
	processes := []receiverProcess{
		{ID: process, PID: 1234, Meetings: true, Accounts: []receiverProcessAccount{{Tenant: "lark", AppID: appID}, {Tenant: "feishu", AppID: appID}}},
		{ID: revision, Accounts: []receiverProcessAccount{{Tenant: "lark", AppID: "cli_legacy"}}},
	}
	view := bridge.LocalMeetingView{Enabled: true, Available: true, Meetings: []bridge.LocalMeetingEntry{
		{Number: number, Phase: "join_accepted", Origin: "local", Revision: revision},
		{Number: pendingNumber, Phase: "join_pending", Origin: "invite", Revision: revision},
		{Number: "345678901", Phase: "leave_pending", Origin: "chat", Revision: revision},
		{Number: "456789012", Phase: "join_rejected", Origin: "invite", Revision: revision},
	}}
	var calls []bridge.LocalMeetingRequest
	var operationErr error
	handler := configRuntimeMeetingsHandler(func() ([]receiverProcess, error) { return processes, nil },
		func(ctx context.Context, id, tenant, app string, request bridge.LocalMeetingRequest) (bridge.LocalMeetingView, error) {
			if _, timed := ctx.Deadline(); !timed || id != process || tenant != "lark" || app != appID {
				t.Fatal("meeting operation did not receive the exact target with a timeout")
			}
			calls = append(calls, request)
			return view, operationErr
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
	page := request(http.MethodGet, path, "", "")
	assertWebPage(t, page, http.StatusOK, "选择接收应用", appID)
	if len(calls) != 0 || strings.Contains(page.Body.String(), "cli_legacy") {
		t.Fatal("target list included an unsupported receiver or invoked a meeting operation")
	}
	form := url.Values{"process": {process}, "tenant": {"lark"}, "appId": {appID}}
	selection := path + "?" + form.Encode()
	page = request(http.MethodGet, selection, "", "")
	assertWebPage(t, page, http.StatusOK, "飞书已受理入会", "参会名单和等候室", "本机", "飞书邀请", "飞书私聊", "飞书已拒绝自动入会")
	body := page.Body.String()
	if !slices.Equal(calls, []bridge.LocalMeetingRequest{{Action: "status"}}) ||
		strings.Count(body, `name="action" value="leave"`) != 1 ||
		strings.Count(body, `name="action" value="confirm-left"`) != 3 || strings.Contains(body, confirmButton) {
		t.Fatal("status must read once, offer only eligible actions and require review")
	}
	assertWebPage(t, request(http.MethodGet, selection+"&action=join", "", ""), http.StatusBadRequest)

	review := func(action, meeting, expected string, withRevision bool) string {
		t.Helper()
		form.Set("action", action)
		form.Set("number", meeting)
		if withRevision {
			form.Set("revision", revision)
		} else {
			form.Del("revision")
		}
		before := len(calls)
		page := request(http.MethodPost, path, form.Encode(), origin)
		assertWebPage(t, page, http.StatusOK, meeting, confirmButton, expected)
		_, after, ok := strings.Cut(page.Body.String(), `name="review" value="`)
		token, _, _ := strings.Cut(after, `"`)
		if !ok || token == "" || len(calls) != before {
			t.Fatal("review must issue a confirmation without invoking the receiver")
		}
		return form.Encode() + "&confirm=yes&review=" + url.QueryEscape(token)
	}

	form.Set("action", "join")
	form.Set("number", number)
	assertWebPage(t, request(http.MethodPost, path, form.Encode(), "https://foreign.test"), http.StatusForbidden)
	assertWebPage(t, request(http.MethodPost, path, form.Encode()+"&revision="+revision, origin), http.StatusBadRequest)
	assertWebPage(t, request(http.MethodPost, path, form.Encode()+"&confirm=yes&review=fake", origin), http.StatusConflict)
	if len(calls) != 1 {
		t.Fatal("rejected review or confirmation invoked the receiver")
	}

	join := review("join", number, "参会者可在会中看到机器人", false)
	assertWebPage(t, request(http.MethodPost, path, join, ""), http.StatusForbidden)
	assertWebPage(t, request(http.MethodPost, path, strings.Replace(join, "tenant=lark", "tenant=feishu", 1), origin), http.StatusConflict)
	join = review("join", number, "确认请求入会", false)
	view.Outcome = "joined"
	assertWebPage(t, request(http.MethodPost, path, join, origin), http.StatusOK, "飞书已受理入会。", "刷新状态")
	assertWebPage(t, request(http.MethodPost, path, join, origin), http.StatusConflict)

	leave := review("leave", number, "确认请求离会", true)
	processes[0].Meetings = false
	assertWebPage(t, request(http.MethodPost, path, leave, origin), http.StatusNotFound, "刷新状态")
	processes[0].Meetings = true
	assertWebPage(t, request(http.MethodPost, path, leave, origin), http.StatusConflict)

	confirmAccepted := review("confirm-left", number, "确认两处都没有该机器人", true)
	assertWebPage(t, request(http.MethodPost, path, strings.Replace(confirmAccepted, "revision="+revision, "revision="+process, 1), origin), http.StatusConflict)
	confirmAccepted = review("confirm-left", number, "确认后只关闭 Bridge 的本地会议回执", true)
	view.Outcome = "confirmed"
	assertWebPage(t, request(http.MethodPost, path, confirmAccepted, origin), http.StatusOK, "已根据你的确认关闭本地会议回执。")

	confirmLeft := review("confirm-left", pendingNumber, "确认机器人不在会中", true)
	operationErr = errors.New("synthetic private failure")
	page = request(http.MethodPost, path, confirmLeft, origin)
	assertWebPage(t, page, http.StatusBadGateway, "会议操作结果未确认", `href="`+selection+`">刷新状态`)
	if body := page.Body.String(); strings.Contains(body, "synthetic private failure") || strings.Contains(body, `method="post"`) {
		t.Fatal("unknown outcome must hide transport details and offer only a read-only refresh")
	}
	assertWebPage(t, request(http.MethodPost, path, confirmLeft, origin), http.StatusConflict)
	if !slices.Equal(calls, []bridge.LocalMeetingRequest{
		{Action: "status"},
		{Action: "join", Number: number},
		{Action: "confirm-left", Number: number, Revision: revision},
		{Action: "confirm-left", Number: pendingNumber, Revision: revision},
	}) {
		t.Fatalf("unexpected meeting calls: %+v", calls)
	}
}
