package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestRuntimeSettingsSubmitsOnlyChangedFields(t *testing.T) {
	const process = "0123456789abcdef0123456789abcdef"
	const appID = "cli_settings"
	const origin = "http://" + webTestHost
	procs := []receiverProcess{
		{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Accounts: []receiverProcessAccount{{Tenant: "feishu", AppID: "cli_legacy"}}},
		{ID: process, PID: 1234, RunSettings: true, Accounts: []receiverProcessAccount{{Tenant: "lark", AppID: appID}}},
	}
	current := bridge.RunSettingsView{Progress: true, ReplyMode: "card", ToolActivity: "default", ProcessMode: "stream-brief"}
	calls := 0
	handler := configRuntimeSettingsHandler(func() ([]receiverProcess, error) { return procs, nil },
		func(ctx context.Context, id, tenant, app string, patch bridge.RunSettingsPatch) (bridge.RunSettingsView, error) {
			calls++
			if _, timed := ctx.Deadline(); !timed || id != process || tenant != "lark" || app != appID {
				t.Fatal("request did not bind the selected process, tenant and app")
			}
			if patch != (bridge.RunSettingsPatch{}) {
				if patch.MaxConcurrent == nil || *patch.MaxConcurrent != 4 || patch.ToolActivity == nil || *patch.ToolActivity != "brief" ||
					patch.ProcessMode == nil || *patch.ProcessMode != "stream-detailed" || patch.Progress != nil ||
					patch.ReplyMode == nil || *patch.ReplyMode != "markdown" || patch.IdleTimeoutMinutes != nil {
					t.Fatalf("request included the wrong changes: %+v", patch)
				}
				current.MaxConcurrent, current.ToolActivity, current.ProcessMode = *patch.MaxConcurrent, *patch.ToolActivity, *patch.ProcessMode
				current.ReplyMode = *patch.ReplyMode
			}
			return current, nil
		})
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, origin+path, nil))
		return w
	}
	page := get("/runtime/settings")
	assertWebPage(t, page, http.StatusOK, appID, `name="before-progress" value="on"`, `value="default" selected`,
		`name="maxConcurrent" type="number" min="0" max="50"`,
		"过程显示", `<option value="card">本卡正文</option>`, `<option value="stream">独立正文预览</option>`,
		`<option value="stream-brief" selected>独立正文＋简略工具</option>`, `<option value="stream-detailed">独立正文＋详细工具</option>`,
		"本卡正文仅用于卡片回复。纯文本或 Markdown 可选独立正文预览。", `<option value="markdown">终态 Markdown</option>`)
	if strings.Contains(page.Body.String(), "cli_legacy") {
		t.Fatal("an old receiver without the capability appeared as a target")
	}

	values := url.Values{"process": {process}, "tenant": {"lark"}, "appId": {appID}}
	for _, field := range runtimeSettingsFields(current) {
		values.Set(field.Name, field.Value)
		values.Set("before-"+field.Name, field.Value)
	}
	values.Set("maxConcurrent", "4")
	values.Set("toolActivity", "brief")
	values.Set("processMode", "stream-detailed")
	values.Set("replyMode", "markdown")
	current.Progress = false // A different entry updated this field after the form loaded.
	post := func(requestOrigin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, origin+"/runtime/settings", strings.NewReader(values.Encode()))
		r.Header.Set("Origin", requestOrigin)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := post("https://foreign.test"); w.Code != http.StatusForbidden || calls != 1 {
		t.Fatalf("foreign request reached the receiver: %d, %d calls", w.Code, calls)
	}
	values.Set("appId", "cli_other")
	if w := post(origin); w.Code != http.StatusNotFound || calls != 1 {
		t.Fatalf("unknown account reached the receiver: %d, %d calls", w.Code, calls)
	}
	values.Set("appId", appID)
	values.Set("maxConcurrent", "51")
	if w := post(origin); w.Code != http.StatusBadRequest || calls != 1 {
		t.Fatalf("invalid value reached the receiver: %d, %d calls", w.Code, calls)
	}
	values.Set("maxConcurrent", "4")
	values.Set("toolActivity", "stream")
	if w := post(origin); w.Code != http.StatusBadRequest || calls != 1 {
		t.Fatalf("stream tool activity reached the receiver: %d, %d calls", w.Code, calls)
	}
	values.Set("toolActivity", "brief")
	w := post(origin)
	if w.Code != http.StatusSeeOther || calls != 2 {
		t.Fatalf("save result: %d, %d calls, %s", w.Code, calls, w.Body)
	}
	page = get(w.Header().Get("Location"))
	assertWebPage(t, page, http.StatusOK, "运行设置已保存。", `name="before-progress" value="off"`,
		`name="before-maxConcurrent" value="4"`, `value="brief" selected`, `<option value="stream-detailed" selected>独立正文＋详细工具</option>`, `value="markdown" selected`)
}
