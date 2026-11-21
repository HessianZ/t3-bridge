package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestConfigServiceInstallUsesSavedRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	raw, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	write := func(raw []byte) {
		t.Helper()
		if _, err := localfile.WritePrivateAtomic(path, raw, false); err != nil {
			t.Fatal(err)
		}
	}
	write(raw)
	revision := configRevision(raw)
	installed := 0
	var checked []bool
	handler := configServiceInstallHandler(func() ([]webSource, error) {
		return []webSource{
			{ID: "file", Label: "fixture", Path: path, Revision: "previous"},
			{ID: "generic", Label: "template"},
		}, nil
	}, func(instance string, source webSource) error {
		installed++
		if instance != "office" || source.Path != path || source.Revision != revision {
			t.Fatalf("install selected %q with %+v", instance, source)
		}
		return nil
	}, func(_ context.Context, selectedPath, selectedRevision string, includeDisabled bool) (bridge.ReceiverPreflight, error) {
		checked = append(checked, includeDisabled)
		if selectedPath != path || selectedRevision != revision {
			t.Fatalf("preflight selected %s at %s", selectedPath, selectedRevision)
		}
		report := bridge.ReceiverPreflight{Accounts: []bridge.ReceiverPreflightAccount{
			{Tenant: "feishu", AppID: "synthetic", Enabled: true,
				Checks: []bridge.ReceiverPreflightCheck{{Check: "route", Route: "work", Status: "failed", Detail: "project unavailable"}}},
			{Tenant: "lark", AppID: "synthetic_disabled",
				Checks: []bridge.ReceiverPreflightCheck{
					{Check: "legacy-receiver-lock", Status: "failed", LockHolder: &bridge.ReceiverLockHolder{Profile: "Work.Codex", PID: 1234}},
					{Check: "state-lock", Status: "failed"},
				}},
		}}
		if includeDisabled {
			report.Passed = true
			report.Accounts[0].Checks[0] = bridge.ReceiverPreflightCheck{Check: "route", Route: "work", Status: "ok"}
			report.Accounts[1].Checks = append(report.Accounts[1].Checks, bridge.ReceiverPreflightCheck{Check: "feishu", Status: "failed"})
			report.AllAccounts = &bridge.ReceiverPreflightOutcome{Passed: false}
		}
		return report, nil
	}, func(context.Context, string, string, string, bool) (bridge.ReceiverPreflight, error) {
		t.Fatal("existing install or preflight request performed a selected App check")
		return bridge.ReceiverPreflight{}, nil
	})
	target := "http://" + webTestHost + "/runtime/install"
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, target+"?source=file&action=preflight-all", nil))
	assertInstallPage(t, page, http.StatusOK, `name="revision" value="`+revision+`"`)
	assertInstallPage(t, page, http.StatusOK, `name="action" value="preflight-all" formnovalidate`)
	if strings.Contains(page.Body.String(), `value="generic"`) {
		t.Fatal("install preview offers an unsaved template")
	}

	form := url.Values{"source": {"file"}, "revision": {revision}, "instance": {"office"}, "action": {"install"}}
	for _, tc := range []struct {
		name, origin, body string
		status             int
	}{
		{"missing origin", "", form.Encode(), http.StatusForbidden},
		{"foreign origin", "https://example.invalid", form.Encode(), http.StatusForbidden},
		{"foreign origin for all accounts", "https://example.invalid", strings.Replace(form.Encode(), "action=install", "action=preflight-all", 1), http.StatusForbidden},
		{"path parameter", "http://" + webTestHost, form.Encode() + "&path=arbitrary.json", http.StatusBadRequest},
		{"unsaved template", "http://" + webTestHost, strings.Replace(form.Encode(), "source=file", "source=generic", 1), http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			assertInstallPage(t, response, tc.status, "")
		})
	}
	if installed != 0 || len(checked) != 0 {
		t.Fatal("preview or rejected request performed an action")
	}

	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://"+webTestHost)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	form.Set("action", "preflight")
	form.Set("instance", "")
	preflight := post()
	assertInstallPage(t, preflight, http.StatusOK, "已启用账号启动预检未通过")
	assertInstallPage(t, preflight, http.StatusOK, "接收授权未启用。跳过飞书和 T3 核验。")
	if strings.Contains(preflight.Body.String(), "<strong>全账号预检") {
		t.Fatal("default preflight showed an all-accounts verdict")
	}
	assertInstallPage(t, preflight, http.StatusOK, "本机旧桥接收锁")
	assertInstallPage(t, preflight, http.StatusOK, "旧锁记录 · profile <code>Work.Codex</code> · PID <code>1234</code>")
	assertInstallPage(t, preflight, http.StatusOK, "状态目录锁")
	form.Set("action", "preflight-all")
	all := post()
	assertInstallPage(t, all, http.StatusOK, "已启用账号启动预检通过")
	assertInstallPage(t, all, http.StatusOK, "全账号预检未通过")
	assertInstallPage(t, all, http.StatusOK, "接收授权未启用。仅参与本次核验。")
	assertInstallPage(t, all, http.StatusOK, "飞书应用身份")
	assertInstallPage(t, all, http.StatusOK, "本机旧桥接收锁")
	if strings.Contains(all.Body.String(), "跳过飞书和 T3 核验") {
		t.Fatal("expanded preflight said the disabled account was skipped")
	}
	if installed != 0 || !slices.Equal(checked, []bool{false, true}) {
		t.Fatalf("preflight actions ran install %d times and checked scopes %v", installed, checked)
	}
	form.Set("action", "install")
	form.Set("instance", "office")
	assertInstallPage(t, post(), http.StatusOK, "后台服务已安装。可到运行页启动。")
	write(append(raw, '\n'))
	assertInstallPage(t, post(), http.StatusConflict, "配置文件已更新或不可用")
	form.Set("action", "preflight")
	assertInstallPage(t, post(), http.StatusConflict, "配置文件已更新或不可用")
	form.Set("action", "preflight-all")
	assertInstallPage(t, post(), http.StatusConflict, "配置文件已更新或不可用")
	if installed != 1 || !slices.Equal(checked, []bool{false, true}) {
		t.Fatalf("stale config performed actions: installed %d, checked scopes %v", installed, checked)
	}
}

func TestConfigServiceInstallPreflightSelectedApp(t *testing.T) {
	const origin = "http://" + webTestHost
	const target = origin + "/runtime/install"
	const enabledID, disabledID = "cli_enabled", "cli_disabled"
	dir := t.TempDir()
	path := filepath.Join(dir, "saved.json")
	base, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	config, err := bridge.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	config.AppID, config.StateDir, config.ReceiverAuthorized = enabledID, filepath.Join(dir, "enabled-state"), true
	disabled := config
	disabled.AppID, disabled.StateDir, disabled.ReceiverAuthorized = disabledID, filepath.Join(dir, "disabled-state"), false
	disabled.Tenant = "lark"
	config.AdditionalAccounts = []bridge.Config{disabled}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localfile.WritePrivateAtomic(path, raw, false); err != nil {
		t.Fatal(err)
	}
	revision := configRevision(raw)
	var checked []string
	handler := configServiceInstallHandler(func() ([]webSource, error) {
		return []webSource{{ID: "file", Path: path}, {ID: "template", JSON: base}}, nil
	}, func(string, webSource) error {
		t.Fatal("selected App check attempted installation")
		return nil
	}, func(context.Context, string, string, bool) (bridge.ReceiverPreflight, error) {
		t.Fatal("selected App check ran unscoped preflight")
		return bridge.ReceiverPreflight{}, nil
	}, func(_ context.Context, gotPath, gotRevision, appID string, includeDisabled bool) (bridge.ReceiverPreflight, error) {
		checked = append(checked, appID)
		if gotPath != path || gotRevision != revision || !includeDisabled {
			t.Fatalf("unexpected preflight scope: %s at %s, include disabled %t", gotPath, gotRevision, includeDisabled)
		}
		enabled := appID == enabledID
		return bridge.ReceiverPreflight{
			Passed:      enabled,
			AllAccounts: &bridge.ReceiverPreflightOutcome{Passed: true},
			Accounts:    []bridge.ReceiverPreflightAccount{{AppID: appID, Enabled: enabled}},
		}, nil
	})
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, target+"?source=file", nil))
	assertInstallPage(t, page, http.StatusOK, `name="appId" required`, `value="cli_enabled">飞书 · cli_enabled · 已启用接收`,
		`value="cli_disabled">Lark · cli_disabled · 未启用接收`, `name="action" value="preflight-app"`, `name="revision" value="`+revision+`"`)

	post := func(target, requestOrigin, site, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if requestOrigin != "" {
			req.Header.Set("Origin", requestOrigin)
		}
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, req)
		return result
	}
	form := url.Values{"source": {"file"}, "revision": {revision}, "appId": {disabledID}, "action": {"preflight-app"}}
	valid := form.Encode()
	for _, tc := range []struct {
		name, query, origin, site, body string
		status                          int
	}{
		{"cross origin", "", "https://other.invalid", "", valid, http.StatusForbidden},
		{"missing origin", "", "", "", valid, http.StatusForbidden},
		{"cross site", "", origin, "cross-site", valid, http.StatusForbidden},
		{"query App", "?appId=" + enabledID, origin, "", valid, http.StatusBadRequest},
		{"stale revision", "", origin, "", strings.Replace(valid, revision, "stale", 1), http.StatusConflict},
		{"template source", "", origin, "", strings.Replace(valid, "source=file", "source=template", 1), http.StatusConflict},
		{"unknown App", "", origin, "", strings.Replace(valid, disabledID, "cli_unknown", 1), http.StatusBadRequest},
		{"case changed", "", origin, "", strings.Replace(valid, disabledID, "CLI_DISABLED", 1), http.StatusBadRequest},
		{"duplicate App", "", origin, "", valid + "&appId=" + enabledID, http.StatusBadRequest},
		{"empty App", "", origin, "", strings.Replace(valid, disabledID, "", 1), http.StatusBadRequest},
		{"legacy form with App", "", origin, "", strings.Replace(valid, "action=preflight-app", "action=preflight&instance=office", 1), http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertInstallPage(t, post(target+tc.query, tc.origin, tc.site, tc.body), tc.status)
			if len(checked) != 0 {
				t.Fatal("invalid request reached the selected App check")
			}
		})
	}
	for _, appID := range []string{enabledID, disabledID} {
		form.Set("appId", appID)
		result := post(target, origin, "same-origin", form.Encode())
		assertInstallPage(t, result, http.StatusOK, "所选 App 预检通过", "<code>"+appID+"</code>", `value="`+appID+`" selected`)
		body := result.Body.String()
		if strings.Count(body, "<h3>") != 1 || strings.Contains(body, "<strong>全账号预检") || strings.Contains(body, "<strong>已启用账号启动预检") {
			t.Fatal("selected App result rendered a broader scope")
		}
		if appID == disabledID {
			assertInstallPage(t, result, http.StatusOK, "启动未就绪。接收授权未启用。仅参与本次核验。")
		}
	}
	if !slices.Equal(checked, []string{enabledID, disabledID}) {
		t.Fatalf("checked Apps %v", checked)
	}
	saved, err := os.ReadFile(path)
	if err != nil || string(saved) != string(raw) {
		t.Fatal("selected App preflight changed the saved config", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("selected App preflight created local files", entries, err)
	}
}

func assertInstallPage(t *testing.T, page *httptest.ResponseRecorder, status int, markers ...string) {
	t.Helper()
	if page.Code != status {
		t.Fatalf("got status %d, want %d: %s", page.Code, status, page.Body.String())
	}
	for _, marker := range markers {
		if !strings.Contains(page.Body.String(), marker) {
			t.Fatalf("page missing %q: %s", marker, page.Body.String())
		}
	}
}
