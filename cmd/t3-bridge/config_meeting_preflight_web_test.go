package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestConfigWebMeetingPreflight(t *testing.T) {
	const target = "/meeting-preflight"
	const origin = "http://" + webTestHost
	const appID = "cli_meeting_preflight"
	const authorizationURL = "https://open.larksuite.com/app/" + appID + "/auth?q=vc:meeting.meetingevent:read"
	base, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	data, err := bridge.EditConfigJSON(base, []byte(`[{"path":["appId"],"value":"`+appID+`"},{"path":["tenant"],"value":"lark"}]`))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if _, err := localfile.WritePrivateAtomic(path, data, true); err != nil {
		t.Fatal(err)
	}
	revision := configRevision(data)
	sources := []webSource{
		{ID: "file", Label: "会议配置", Path: path, Revision: "startup-only"},
		{ID: "generic", Label: "模板", JSON: base},
		{ID: "missing", Label: "未保存", Path: filepath.Join(dir, "missing.json"), JSON: data},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	availabilityCalls := 0
	var checkErr error
	publicationState, versionID := "verified", "published-test-version"
	eventStates := map[string]string{
		"vc.bot.meeting_invited_v1":  "subscribed",
		"vc.bot.meeting_activity_v1": "missing",
		"vc.bot.meeting_ended_v1":    "unknown",
	}
	check := func(gotCtx context.Context, source webSource, app, tenant string) (bridge.MeetingPermissionReport, error) {
		if gotCtx != ctx || source.ID != "file" || source.Path != path || source.Revision != revision || app != appID || tenant != "lark" {
			t.Fatal("check did not receive the selected saved application and request context")
		}
		return bridge.MeetingPermissionReport{
			AppID: app, Tenant: tenant, Enabled: false,
			Scopes: []bridge.MeetingPermissionScope{
				{Scope: "vc:meeting.bot.join:write", State: "granted"},
				{Scope: "vc:meeting.meetingevent:read", State: "missing"},
				{Scope: "vc:meeting.message:write", State: "unknown"},
			},
			Events:             []string{"vc.bot.meeting_invited_v1", "vc.bot.meeting_activity_v1", "vc.bot.meeting_ended_v1"},
			PublishedVersionID: versionID,
			PublicationState:   publicationState,
			EventStates:        eventStates,
		}, checkErr
	}
	handler := configMeetingPreflightHandler(func() ([]webSource, error) { return sources, nil },
		func(ctx context.Context, source webSource, app, tenant string) (bridge.MeetingPermissionReport, error) {
			calls++
			return check(ctx, source, app, tenant)
		},
		func(ctx context.Context, source webSource, app, tenant string) (bridge.MeetingPermissionReport, error) {
			availabilityCalls++
			report, err := check(ctx, source, app, tenant)
			report.Availability = "readable"
			report.Scopes[0].AuthorizationURL = authorizationURL
			report.Scopes[1].AuthorizationURL = authorizationURL
			return report, err
		})
	serve := func(handler http.Handler, method, endpoint, requestOrigin, site, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, origin+endpoint, strings.NewReader(body)).WithContext(ctx)
		if method == http.MethodPost {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if requestOrigin != "" {
			r.Header.Set("Origin", requestOrigin)
		}
		if site != "" {
			r.Header.Set("Sec-Fetch-Site", site)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	get := func(endpoint string) *httptest.ResponseRecorder {
		return serve(handler, http.MethodGet, endpoint, "", "", "")
	}
	initial := get(target + "?source=file")
	assertWebPage(t, initial, http.StatusOK, `name="revision" value="`+revision+`"`, `<option value="0">`+appID+` · lark`,
		`<button type="submit">开始只读检查</button>`, `<button type="submit" name="probe" value="availability">同时检查活动会议接口</button>`,
		"会额外查询所选应用所有者的活动会议，结果仅显示接口状态。", "事件实际投递、平台会议功能资格和实际会议准入均未验证。", "</html>")
	if initial.Header().Get("Referrer-Policy") != "same-origin" || strings.Contains(initial.Body.String(), `value="generic"`) || strings.Contains(initial.Body.String(), `value="missing"`) {
		t.Fatal("expected native same-origin forms and only saved source options")
	}
	assertWebPage(t, get(target+"?source=generic"), http.StatusConflict)
	assertWebPage(t, get(target+"?source=file&source=missing"), http.StatusBadRequest)
	assertWebPage(t, get(target+"?source=file&probe=availability"), http.StatusBadRequest)
	form := url.Values{"source": {"file"}, "revision": {revision}, "choice": {"0"}}.Encode()
	availabilityForm := form + "&probe=availability"
	for _, tc := range []struct {
		name, endpoint, requestOrigin, site, body string
		status                                    int
	}{
		{"foreign origin", target, "https://other.test", "", form, http.StatusForbidden},
		{"missing origin", target, "", "", form, http.StatusForbidden},
		{"foreign fetch site", target, origin, "same-site", form, http.StatusForbidden},
		{"query override", target + "?source=file", origin, "", form, http.StatusBadRequest},
		{"extra tenant", target, origin, "", form + "&tenant=feishu", http.StatusBadRequest},
		{"duplicate choice", target, origin, "", form + "&choice=0", http.StatusBadRequest},
		{"unknown choice", target, origin, "", strings.Replace(form, "choice=0", "choice=1", 1), http.StatusBadRequest},
		{"stale revision", target, origin, "", strings.Replace(form, revision, "startup-only", 1), http.StatusConflict},
		{"probe with foreign origin", target, "https://other.test", "", availabilityForm, http.StatusForbidden},
		{"probe in query", target + "?probe=availability", origin, "", form, http.StatusBadRequest},
		{"empty probe", target, origin, "", form + "&probe=", http.StatusBadRequest},
		{"unknown probe", target, origin, "", form + "&probe=Availability", http.StatusBadRequest},
		{"duplicate probe", target, origin, "", availabilityForm + "&probe=availability", http.StatusBadRequest},
		{"probe with extra app", target, origin, "", availabilityForm + "&appId=" + appID, http.StatusBadRequest},
		{"probe with duplicate source", target, origin, "", availabilityForm + "&source=file", http.StatusBadRequest},
		{"probe with unknown choice", target, origin, "", strings.Replace(availabilityForm, "choice=0", "choice=1", 1), http.StatusBadRequest},
		{"probe with stale revision", target, origin, "", strings.Replace(availabilityForm, revision, "startup-only", 1), http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertWebPage(t, serve(handler, http.MethodPost, tc.endpoint, tc.requestOrigin, tc.site, tc.body), tc.status)
		})
	}
	if calls != 0 || availabilityCalls != 0 {
		t.Fatal("GET or rejected POST invoked a meeting check")
	}
	submit := func() *httptest.ResponseRecorder {
		return serve(handler, http.MethodPost, target, origin, "same-origin", form)
	}
	permissions := submit()
	assertWebPage(t, permissions, http.StatusOK, "未启用 <code>meetingEnabled</code>", `<option value="0" selected>`+appID+` · lark`,
		"<code>vc:meeting.bot.join:write</code> · 已授权", "<code>vc:meeting.meetingevent:read</code> · 未授权", "<code>vc:meeting.message:write</code> · 未知",
		"已发布版本的事件订阅配置", "<dt>发布状态</dt><dd>"+meetingPublicationStateText("verified")+"</dd>",
		"<dt>核对的版本 ID</dt><dd><code>published-test-version</code></dd>",
		"<li><code>vc.bot.meeting_invited_v1</code> · "+meetingEventStateText("subscribed")+"</li>"+
			"<li><code>vc.bot.meeting_activity_v1</code> · "+meetingEventStateText("missing")+"</li>"+
			"<li><code>vc.bot.meeting_ended_v1</code> · "+meetingEventStateText("unknown")+"</li>", "</html>")
	if availabilityCalls != 0 || strings.Contains(permissions.Body.String(), "<dt>活动会议接口状态</dt>") || strings.Contains(permissions.Body.String(), "在开放平台申请") {
		t.Fatal("default submission showed availability or a link without a backend URL")
	}
	availability := serve(handler, http.MethodPost, target, origin, "same-origin", availabilityForm)
	assertWebPage(t, availability, http.StatusOK, `<option value="0" selected>`+appID+` · lark`,
		"<dt>活动会议接口状态</dt><dd>"+meetingAvailabilityText("readable")+"</dd>",
		`<code>vc:meeting.meetingevent:read</code> · 未授权 · <a href="`+authorizationURL+`" target="_blank" rel="noopener noreferrer">在开放平台申请</a>`,
		"已发布版本的事件订阅配置")
	if calls != 1 || availabilityCalls != 1 || strings.Count(availability.Body.String(), "在开放平台申请") != 1 {
		t.Fatal("opt-in invoked the wrong check or showed a link for a non-missing scope")
	}

	versionID = ""
	for event := range eventStates {
		eventStates[event] = "unknown"
	}
	for _, state := range []string{"unknown", "unpublished"} {
		t.Run(state, func(t *testing.T) {
			publicationState = state
			page := submit()
			assertWebPage(t, page, http.StatusOK, "<dt>发布状态</dt><dd>"+meetingPublicationStateText(state)+"</dd>",
				"<li><code>vc.bot.meeting_invited_v1</code> · "+meetingEventStateText("unknown")+"</li>",
				"<li><code>vc.bot.meeting_activity_v1</code> · "+meetingEventStateText("unknown")+"</li>",
				"<li><code>vc.bot.meeting_ended_v1</code> · "+meetingEventStateText("unknown")+"</li>")
			if strings.Contains(page.Body.String(), "核对的版本 ID") {
				t.Fatal("page showed a version ID field without a checked version")
			}
		})
	}

	checkErr = errors.New("synthetic private error")
	failed := submit()
	assertWebPage(t, failed, http.StatusUnprocessableEntity, "无法读取会议权限")
	if strings.Contains(failed.Body.String(), checkErr.Error()) || strings.Contains(failed.Body.String(), "会议权限检查结果") {
		t.Fatal("failed check exposed an internal error or report")
	}
	checkErr = errMeetingPermissionSourceChanged
	stale := submit()
	assertWebPage(t, stale, http.StatusConflict, errMeetingPermissionSourceChanged.Error())
	if strings.Contains(stale.Body.String(), `name="choice"`) || strings.Contains(stale.Body.String(), "会议权限检查结果") {
		t.Fatal("revision conflict showed a result or let the user submit again")
	}
	if calls != 5 {
		t.Fatalf("permission check invoked %d times, want 5 explicit submissions", calls)
	}

	workspace := configWebHandler(webTestHost, sources)
	assertWebPage(t, serve(workspace, http.MethodGet, "/?source=file", "", "", ""), http.StatusOK, `href="/meeting-preflight?source=file"`)
	assertWebPage(t, serve(workspace, http.MethodGet, target+"?source=file", "", "", ""), http.StatusOK, "会议权限预检")
	assertWebPage(t, serve(workspace, http.MethodPost, target, origin, "", ""), http.StatusBadRequest, "会议权限预检表单无效")
	for _, id := range []string{"generic", "missing"} {
		page := serve(workspace, http.MethodGet, "/?source="+id, "", "", "")
		assertWebPage(t, page, http.StatusOK)
		if strings.Contains(page.Body.String(), `href="/meeting-preflight`) {
			t.Errorf("workbench exposed saved-config navigation for %s", id)
		}
	}
}
