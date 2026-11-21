package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestConfigWebMessageReceipt(t *testing.T) {
	dir := t.TempDir()
	raw, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	primary, err := bridge.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	primary.AppID, primary.StateDir = "cli_receipt_primary", filepath.Join(dir, "primary-state")
	primary.AppSecretFile = filepath.Join(dir, "unused-app-secret")
	primary.MCP.TokenFile = filepath.Join(dir, "unused-t3-token")
	secondary := primary
	secondary.AppID, secondary.StateDir = "cli_receipt_secondary", filepath.Join(dir, "secondary-state")
	primary.AdditionalAccounts = []bridge.Config{secondary}
	writeJSON := func(path string, value any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := localfile.WritePrivateAtomic(path, data, true); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "config.json")
	writeJSON(path, primary)
	const privateInput = "synthetic private input"
	const privateResult = "synthetic private result"
	const threadURL = "https://t3.example.invalid/threads/env-receipt/thread-primary"
	for _, account := range primary.Accounts() {
		if err := localfile.MkdirAllPrivate(account.StateDir); err != nil {
			t.Fatal(err)
		}
		task := &bridge.Task{
			ID: "task-primary", ReceiverAppID: account.AppID, RouteID: "work", ThreadID: "thread-primary",
			Link:           threadURL,
			Target:         bridge.Target{EnvironmentID: "env-receipt", ProjectID: "project-receipt"},
			DispatchSource: bridge.Message{ID: "om_second", Text: privateInput},
			Phase:          "dispatching", RequestID: "request-new", NeedsReconcile: true, Result: privateResult,
			Replies: []bridge.ReplyRound{
				{InputID: "om_legacy", Source: bridge.Message{ID: "om_legacy"}, CardID: "om_reply_original", Phase: "completed"},
				{InputID: "om_first", Source: bridge.Message{ID: "om_first", Text: privateInput},
					RunID: "run-original", RequestID: "request-original", CardID: "om_reply_original", Phase: "completed",
					Result: privateResult, DeliveryState: bridge.ReplyDeliveryConfirmed},
			},
		}
		if account.AppID == secondary.AppID {
			task = &bridge.Task{
				ID: "task-secondary", ReceiverAppID: account.AppID, ThreadID: "thread-secondary",
				DispatchSource: bridge.Message{ID: "om_first", Text: privateInput},
				Phase:          "running", RunID: "run-secondary",
			}
		}
		writeJSON(filepath.Join(account.StateDir, "state.json"), bridge.State{
			Version: 1, AccountID: account.AppID, Tasks: map[string]*bridge.Task{task.ID: task},
		})
	}

	handler := configWebHandler(webTestHost, []webSource{
		{ID: "file", Label: "保存的测试配置", Path: path},
		{ID: "generic", Label: "模板", JSON: raw},
		{ID: "missing", Label: "未保存的测试配置", Path: filepath.Join(dir, "missing.json")},
	})
	origin := "http://" + webTestHost
	const target = "/message-receipt"
	serve := func(method, path, requestOrigin string, form url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, origin+path, strings.NewReader(form.Encode()))
		if method == http.MethodPost {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if requestOrigin != "" {
			r.Header.Set("Origin", requestOrigin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	assertAbsent := func(response *httptest.ResponseRecorder, markers ...string) {
		t.Helper()
		for _, marker := range markers {
			if strings.Contains(response.Body.String(), marker) {
				t.Errorf("response unexpectedly contains %q", marker)
			}
		}
	}
	assertWebPage(t, serve(http.MethodGet, "/?source=file", "", nil), http.StatusOK, `href="/message-receipt?source=file"`)
	initial := serve(http.MethodGet, target+"?source=file&messageId=om_first", "", nil)
	assertWebPage(t, initial, http.StatusOK, `<option value="file" selected>`, `method="post" action="/message-receipt"`, "留空使用主账号", "</html>")
	assertAbsent(initial, `value="generic"`, `value="missing"`, "回执详情", "run-original")
	if got := initial.Header().Get("Referrer-Policy"); got != "same-origin" {
		t.Errorf("referrer policy = %q, want same-origin for the native form", got)
	}

	form := url.Values{"source": {"file"}, "messageId": {"om_first"}}
	submit := func() *httptest.ResponseRecorder { return serve(http.MethodPost, target, origin, form) }
	threadLink := `<a href="` + threadURL + `" target="_blank" rel="noopener noreferrer">` + threadURL + `</a>`
	const deliveryNote = "此状态依据本机持有的飞书 API 回执，卡片 ID 复用与客户端阅读状态需分别核对。"
	historical := submit()
	assertWebPage(t, historical, http.StatusOK, "回执详情", "cli_receipt_primary", "om_first", "task-primary", "<code>work</code>",
		"env-receipt", "project-receipt", "completed", "thread-primary", "request-original", "run-original", "om_reply_original",
		threadLink, "历史轮次", "<dt>待核对</dt><dd>否</dd>",
		"<dt>终态回复投递</dt><dd>已获飞书确认<br>"+deliveryNote+"</dd>", "</html>")
	assertAbsent(historical, privateInput, privateResult, "run-secondary", primary.StateDir, primary.AppSecretFile)

	form.Set("messageId", "om_legacy")
	legacy := submit()
	assertWebPage(t, legacy, http.StatusOK, "历史轮次", "<dt>终态回复投递</dt><dd>未知</dd>",
		"<dt>飞书回复 ID</dt><dd><code>om_reply_original</code></dd>")
	assertAbsent(legacy, "已获飞书确认", deliveryNote)

	form.Set("appId", "")
	form.Set("messageId", "om_second")
	current := submit()
	assertWebPage(t, current, http.StatusOK, "cli_receipt_primary", "thread-primary", "request-new", "当前轮次", "dispatching",
		"<dt>终态回复投递</dt><dd>等待终态回复</dd>",
		threadLink, "<dt>T3 Run ID</dt><dd>未记录</dd>", "<dt>飞书回复 ID</dt><dd>未记录</dd>", "<dt>待核对</dt><dd>是</dd>")
	assertAbsent(current, privateInput, privateResult, "run-original", deliveryNote)
	form.Set("appId", secondary.AppID)
	form.Set("messageId", "om_first")
	other := submit()
	assertWebPage(t, other, http.StatusOK, "cli_receipt_secondary", "thread-secondary", "run-secondary", "当前轮次")
	assertAbsent(other, "T3 线程链接", threadURL, "cli_receipt_primary", "run-original", privateInput)
	form.Set("messageId", "om_second")
	assertWebPage(t, submit(), http.StatusNotFound, "所选账号中未找到")
	form.Set("appId", "cli_not_configured")
	assertWebPage(t, submit(), http.StatusUnprocessableEntity, "无法查询回执")
	form.Set("appId", "")
	form.Set("source", path)
	assertWebPage(t, submit(), http.StatusConflict, "所选配置不可用")
	form.Set("source", "file")
	assertWebPage(t, serve(http.MethodPost, target, "", form), http.StatusForbidden)
	assertWebPage(t, serve(http.MethodPost, target, "https://other.test", form), http.StatusForbidden)
	assertWebPage(t, serve(http.MethodPost, target+"?stateDir="+url.QueryEscape(dir), origin, form), http.StatusBadRequest)
	for _, field := range []string{"stateDir", "text"} {
		form.Set(field, "unapproved-value")
		assertWebPage(t, submit(), http.StatusBadRequest)
		form.Del(field)
	}
	form.Add("messageId", "om_first")
	assertWebPage(t, submit(), http.StatusBadRequest)

	t.Run("CLI thread URL", func(t *testing.T) {
		for _, tc := range []struct{ appID, wantURL string }{
			{primary.AppID, threadURL},
			{secondary.AppID, ""},
		} {
			var output strings.Builder
			if err := runConfigReceipt([]string{"-config", path, "-app-id", tc.appID, "-message-id", "om_first"}, &output); err != nil {
				t.Fatal(err)
			}
			var receipt map[string]any
			if err := json.Unmarshal([]byte(output.String()), &receipt); err != nil {
				t.Fatal(err)
			}
			address, present := receipt["threadUrl"]
			if tc.wantURL == "" {
				if present {
					t.Errorf("app %s unexpectedly included threadUrl: %v", tc.appID, address)
				}
			} else if address != tc.wantURL {
				t.Errorf("app %s threadUrl = %v, want %q", tc.appID, address, tc.wantURL)
			}
		}
	})
}
