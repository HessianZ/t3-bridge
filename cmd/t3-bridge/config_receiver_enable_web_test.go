package main

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestConfigReceiverEnableWeb(t *testing.T) {
	const origin = "http://" + webTestHost
	const target = "/runtime/receiver-enable"
	const appID = "cli_disabled"
	dir := t.TempDir()
	path := filepath.Join(dir, "saved.json")
	enabledPath := filepath.Join(dir, "enabled.json")
	base, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	config, err := bridge.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	config.AppID, config.StateDir, config.ReceiverAuthorized = "cli_enabled", filepath.Join(dir, "enabled-state"), true
	disabled := config
	disabled.AppID, disabled.StateDir, disabled.ReceiverAuthorized = appID, filepath.Join(dir, "disabled-state"), false
	disabled.Tenant, disabled.ReceiverMachine = "lark", "draft-host"
	writeSource := func(path string, config bridge.Config) []byte {
		t.Helper()
		raw, err := json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	writeSource(enabledPath, config)
	config.AdditionalAccounts = []bridge.Config{disabled}
	raw := writeSource(path, config)
	revision := configRevision(raw)
	candidate, err := bridge.EditConfigJSON(raw, []byte(`[{"path":["additionalAccounts","0","receiverAuthorized"],"value":true}]`))
	if err != nil {
		t.Fatal(err)
	}
	sources := []webSource{
		{ID: "file", Label: "测试配置", Path: path, Revision: "startup-version"},
		{ID: "enabled", Label: "已启用配置", Path: enabledPath},
		{ID: "template", JSON: base},
		{ID: "missing", Path: filepath.Join(dir, "missing.json"), JSON: base},
	}
	report := bridge.ReceiverPreflight{
		AllAccounts: &bridge.ReceiverPreflightOutcome{},
		Accounts: []bridge.ReceiverPreflightAccount{{
			AppID: appID, Tenant: "lark",
			Checks: []bridge.ReceiverPreflightCheck{
				{Check: "feishu", Status: "ok"},
				{Check: "route", Route: "work", Status: "unknown", Detail: "无法核对所选路由"},
				{Check: "legacy-receiver-lock", Status: "failed", LockHolder: &bridge.ReceiverLockHolder{Profile: "old-bridge", PID: 123}},
			},
		}},
	}
	draft := receiverEnableDraft{Report: report}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	var prepareErr error
	handler := configReceiverEnableHandler(func() ([]webSource, error) { return sources, nil },
		func(gotCtx context.Context, gotPath, gotRevision, gotAppID string) (receiverEnableDraft, error) {
			calls++
			if gotCtx != ctx || gotPath != path || gotRevision != revision || gotAppID != appID {
				t.Fatal("prepare did not receive the selected saved App and revision")
			}
			return draft, prepareErr
		})
	serve := func(handler http.Handler, method, requestPath, requestOrigin, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, origin+requestPath, strings.NewReader(body)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if requestOrigin != "" {
			r.Header.Set("Origin", requestOrigin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	workspace := configWebHandler(webTestHost, sources)
	assertWebPage(t, serve(workspace, http.MethodGet, "/runtime/install?source=file", "", ""), http.StatusOK,
		`href="`+target+`?source=file"`)
	assertWebPage(t, serve(workspace, http.MethodGet, target+"?source=file", "", ""), http.StatusOK, "只读检查")
	selection := serve(handler, http.MethodGet, target+"?source=file", "", "")
	assertWebPage(t, selection, http.StatusOK, "已保存配置 · 测试配置", "Lark · cli_disabled · draft-host",
		`name="revision" value="`+revision+`"`, `action="`+target+`"`, "只读检查", "</html>")
	if strings.Contains(selection.Body.String(), "cli_enabled") || strings.Contains(selection.Body.String(), config.AppSecretFile) {
		t.Fatal("selection exposed an enabled App or a credential reference")
	}
	assertWebPage(t, serve(workspace, http.MethodGet, target+"?source=enabled", "", ""), http.StatusOK, "此配置没有待启用的 App。")
	if calls != 0 {
		t.Fatal("a GET ran the receiver checks")
	}

	form := url.Values{"source": {"file"}, "revision": {revision}, "appId": {appID}}
	valid := form.Encode()
	for _, tc := range []struct {
		name, query, requestOrigin, body string
		want                             int
	}{
		{"foreign origin", "", "https://other.test", valid, http.StatusForbidden},
		{"missing origin", "", "", valid, http.StatusForbidden},
		{"query override", "?source=enabled", origin, valid, http.StatusBadRequest},
		{"stale revision", "", origin, strings.Replace(valid, revision, "stale", 1), http.StatusConflict},
		{"different saved source", "", origin, strings.Replace(valid, "source=file", "source=enabled", 1), http.StatusConflict},
		{"template source", "", origin, strings.Replace(valid, "source=file", "source=template", 1), http.StatusConflict},
		{"missing source", "", origin, strings.Replace(valid, "source=file", "source=missing", 1), http.StatusConflict},
		{"enabled App", "", origin, strings.Replace(valid, appID, "cli_enabled", 1), http.StatusBadRequest},
		{"unknown App", "", origin, strings.Replace(valid, appID, "cli_unknown", 1), http.StatusBadRequest},
		{"duplicate App", "", origin, valid + "&appId=" + appID, http.StatusBadRequest},
		{"untrusted save action", "", origin, valid + "&action=save", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertWebPage(t, serve(handler, http.MethodPost, target+tc.query, tc.requestOrigin, tc.body), tc.want)
			if calls != 0 {
				t.Fatal("invalid request reached receiver checks")
			}
		})
	}

	blocked := serve(handler, http.MethodPost, target, origin, valid)
	assertWebPage(t, blocked, http.StatusOK, "检查未通过", "飞书应用身份", "T3 路由", "未确认", "无法核对所选路由",
		"本机旧桥接收锁", "失败", "old-bridge", `value="cli_disabled" selected`, "</html>")
	if strings.Contains(blocked.Body.String(), `name="config"`) {
		t.Fatal("blocked preflight offered a candidate")
	}
	prepareErr = errWorkspaceServiceConfigChanged
	assertWebPage(t, serve(handler, http.MethodPost, target, origin, valid), http.StatusConflict, "已保存的配置已更新")

	prepareErr = nil
	report.AllAccounts = &bridge.ReceiverPreflightOutcome{Passed: true}
	report.Accounts[0].Checks = []bridge.ReceiverPreflightCheck{{Check: "feishu", Status: "ok"}}
	draft = receiverEnableDraft{Candidate: candidate, Report: report}
	result := serve(handler, http.MethodPost, target, origin, valid)
	assertWebPage(t, result, http.StatusOK, "检查通过", `action="/?source=file"`, `name="action" value="check"`, "</html>")
	_, handoff, _ := strings.Cut(result.Body.String(), `action="/?source=file"`)
	match := regexp.MustCompile(`name="revision" value="([^"]*)"\s*>\s*<input type="hidden" name="config" value="([^"]*)"`).FindStringSubmatch(handoff)
	if calls != 3 || match == nil || match[1] != revision || html.UnescapeString(match[2]) != string(candidate) ||
		strings.Contains(result.Body.String(), `value="save"`) {
		t.Fatal("successful preflight did not offer the draft-only handoff")
	}
	review := url.Values{"config": {html.UnescapeString(match[2])}, "revision": {match[1]}, "action": {"check"}}
	assertWebPage(t, serve(workspace, http.MethodPost, "/?source=file", origin, review.Encode()), http.StatusOK,
		"与来源的差异", `+      "receiverAuthorized": true`, `name="revision" value="`+revision+`"`)
	saved, err := os.ReadFile(path)
	if err != nil || string(saved) != string(raw) {
		t.Fatal("receiver checks or editor review changed the saved configuration", err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 2 {
		t.Fatal("receiver checks or editor review created local files", files, err)
	}
}
