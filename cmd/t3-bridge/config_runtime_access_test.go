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

func TestRuntimeAccessBindsSelectedAppAndRequiresAnExplicitChange(t *testing.T) {
	const processID = "0123456789abcdef0123456789abcdef"
	const origin = "http://" + webTestHost
	account := receiverProcessAccount{Tenant: "lark", AppID: "cli_selected"}
	procs := []receiverProcess{
		{ID: processID, PID: 412, AccessPolicy: true, Accounts: []receiverProcessAccount{
			account, {Tenant: "feishu", AppID: "cli_other"},
		}},
		{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Accounts: []receiverProcessAccount{{Tenant: "lark", AppID: "cli_old"}}},
	}
	target := runtimeAccessTarget{Process: processID, Account: account, PID: 412}
	current := bridge.LocalAccessPolicyView{
		OwnerID:  "ou_owner",
		RouteIDs: []string{"EmptyRoute", "Team.Work"},
		Entries: []bridge.LocalAccessPolicyEntry{
			{Kind: "route", UserID: "ou_member", RouteID: "Team.Work", Configured: true, Override: "inherit", Effective: true},
		},
	}
	var writes []bridge.LocalAccessPolicyRequest
	fail := false
	handler := configRuntimeAccessHandler(func() ([]receiverProcess, error) { return procs, nil },
		func(ctx context.Context, process, tenant, appID string, request bridge.LocalAccessPolicyRequest) (bridge.LocalAccessPolicyView, error) {
			if _, timed := ctx.Deadline(); !timed || process != processID || tenant != account.Tenant || appID != account.AppID {
				t.Fatal("authorization request did not retain its selected target")
			}
			if request != (bridge.LocalAccessPolicyRequest{}) {
				writes = append(writes, request)
				if fail {
					return bridge.LocalAccessPolicyView{}, errors.New("private receiver diagnostic")
				}
				current.Entries[0].Override, current.Entries[0].Effective = request.Mode, request.Mode != "deny"
			}
			return current, nil
		})
	get := func(path string) *httptest.ResponseRecorder {
		path, _, _ = strings.Cut(path, "#")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, origin+path, nil))
		return w
	}
	page := get(target.editURL(current.Entries[0]))
	assertWebPage(t, page, http.StatusOK, "ou_owner", "cli_selected", "启动配置", "运行覆盖", "本层生效",
		`name="userId" value="ou_member"`, `value="Team.Work" selected`, `value="EmptyRoute"`,
		`<td>允许</td>`, `<td>继承</td>`, "#edit-access")
	if strings.Contains(page.Body.String(), "cli_old") || len(writes) != 0 {
		t.Fatal("the read exposed an unsupported process or issued a mutation")
	}

	form := url.Values{
		"process": {processID}, "appId": {account.AppID}, "tenant": {account.Tenant},
		"kind": {"route"}, "userId": {"ou_member"}, "routeId": {"Team.Work"}, "mode": {"deny"},
	}
	post := func(requestOrigin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, origin+"/runtime/access", strings.NewReader(form.Encode()))
		req.Header.Set("Origin", requestOrigin)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	if w := post("https://foreign.test"); w.Code != http.StatusForbidden {
		t.Fatalf("foreign origin returned %d", w.Code)
	}
	form.Set("tenant", "feishu")
	if w := post(origin); w.Code != http.StatusNotFound {
		t.Fatalf("wrong app and tenant pair returned %d", w.Code)
	}
	form.Set("tenant", account.Tenant)
	form["mode"] = []string{"allow", "deny"}
	if w := post(origin); w.Code != http.StatusBadRequest {
		t.Fatalf("duplicate mode returned %d", w.Code)
	}
	form.Set("mode", "deny")
	form.Set("kind", "admin")
	if w := post(origin); w.Code != http.StatusBadRequest || len(writes) != 0 {
		t.Fatalf("ambiguous layer returned %d and %d writes", w.Code, len(writes))
	}
	form.Set("kind", "route")
	w := post(origin)
	want := bridge.LocalAccessPolicyRequest{Kind: "route", UserID: "ou_member", RouteID: "Team.Work", Mode: "deny"}
	if w.Code != http.StatusSeeOther || len(writes) != 1 || writes[0] != want {
		t.Fatalf("expected one explicit change, got %d and %+v", w.Code, writes)
	}
	assertWebPage(t, get(w.Header().Get("Location")), http.StatusOK, "在线权限已保存。", "<td>撤销</td>", "<td>未授权</td>")
	fail = true
	w = post(origin)
	assertWebPage(t, w, http.StatusBadGateway, "授权保存未获确认")
	if strings.Contains(w.Body.String(), "private receiver diagnostic") || len(writes) != 2 {
		t.Fatal("the error disclosed receiver details or retried the change")
	}
}
