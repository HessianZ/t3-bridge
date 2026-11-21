package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestConfigWebDrainCheck(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LARK_CHANNEL_HOME", dir)
	raw, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	path, otherPath := filepath.Join(dir, "receiver.json"), filepath.Join(dir, "other.json")
	for _, file := range []string{path, otherPath} {
		if _, err := localfile.WritePrivateAtomic(file, raw, true); err != nil {
			t.Fatal(err)
		}
	}
	sources := []webSource{
		{ID: "other", Label: "另一份配置", Path: otherPath},
		{ID: "file", Label: "测试接收配置", Path: path, Revision: "startup-only"},
		{ID: "generic", Label: "模板", JSON: raw},
		{ID: "missing", Label: "未保存", Path: filepath.Join(dir, "missing.json"), JSON: raw},
	}
	revision := configRevision(raw)
	const replyID = "om_reply_input"
	const processID = "om_process_input"
	result := bridge.DrainReport{Accounts: []bridge.DrainAccount{
		{AppID: "cli_clear", Status: "drained"},
		{
			AppID: "cli_pending", Status: "blocked",
			Blockers: []bridge.DrainBlocker{{Code: "active", Count: 2}, {Code: "delivery", Count: 5}},
			DeliverySamples: []bridge.DrainDeliverySample{
				{MessageID: replyID, Kind: "reply"},
				{MessageID: processID, Kind: "process"},
			},
		},
		{AppID: "cli_busy", Status: "busy"},
		{
			AppID: "cli_secondary", Status: "blocked",
			Blockers:        []bridge.DrainBlocker{{Code: "delivery", Count: 1}},
			DeliverySamples: []bridge.DrainDeliverySample{{MessageID: replyID, Kind: "process"}},
		},
	}}
	calls := 0
	var checkErr error
	handler := configDrainHandler(func() ([]webSource, error) { return sources, nil },
		func(gotPath, gotRevision string) (bridge.DrainReport, error) {
			calls++
			if gotPath != path || gotRevision != revision {
				t.Error("check did not receive the selected saved path and revision")
			}
			return result, checkErr
		})
	const target = "/drain-check"
	const origin = "http://" + webTestHost
	serve := func(handler http.Handler, method, target, requestOrigin, site, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, origin+target, strings.NewReader(body))
		if method == http.MethodPost {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if requestOrigin != "" {
			req.Header.Set("Origin", requestOrigin)
		}
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	initial := serve(handler, http.MethodGet, target+"?source=file", "", "", "")
	assertWebPage(t, initial, http.StatusOK, `value="file" selected`, `name="revision" value="`+revision+`"`,
		"测试接收配置", path, "请先停止", "状态快照", "</html>")
	if initial.Header().Get("Referrer-Policy") != "same-origin" || strings.Contains(initial.Body.String(), `id="drain-result"`) {
		t.Fatal("opening the page must permit a native same-origin form and omit results")
	}
	for _, id := range []string{"generic", "missing"} {
		if strings.Contains(initial.Body.String(), `value="`+id+`"`) {
			t.Errorf("offered unsaved source %s", id)
		}
	}
	form := func(source, rev string) string {
		return url.Values{"source": {source}, "revision": {rev}}.Encode()
	}
	valid := form("file", revision)
	for _, tc := range []struct {
		name, query, origin, site, body string
		status                          int
	}{
		{name: "cross origin", origin: "https://other.test", body: valid, status: http.StatusForbidden},
		{name: "missing origin", body: valid, status: http.StatusForbidden},
		{name: "cross site", origin: origin, site: "cross-site", body: valid, status: http.StatusForbidden},
		{name: "query override", query: "?source=other", origin: origin, body: valid, status: http.StatusBadRequest},
		{name: "missing revision", origin: origin, body: "source=file", status: http.StatusBadRequest},
		{name: "duplicate source", origin: origin, body: valid + "&source=other", status: http.StatusBadRequest},
		{name: "extra path", origin: origin, body: valid + "&path=" + url.QueryEscape(path), status: http.StatusBadRequest},
		{name: "free file path as source", origin: origin, body: form(path, revision), status: http.StatusConflict},
		{name: "unsaved source", origin: origin, body: form("missing", revision), status: http.StatusConflict},
		{name: "stale revision", origin: origin, body: form("file", "startup-only"), status: http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertWebPage(t, serve(handler, http.MethodPost, target+tc.query, tc.origin, tc.site, tc.body), tc.status)
			if calls != 0 {
				t.Fatal("GET or a rejected POST invoked the drain check")
			}
		})
	}
	submit := func() *httptest.ResponseRecorder {
		return serve(handler, http.MethodPost, target, origin, "same-origin", valid)
	}
	response := submit()
	assertWebPage(t, response, http.StatusOK, "未能确认全部 App 已排空", "测试接收配置", path,
		"<code>cli_clear</code>", "<td>"+drainStatusText("drained")+"</td>",
		"<code>cli_pending</code>", "<td>"+drainStatusText("blocked")+"</td>",
		"<li>"+drainBlockerText("active")+"（2）</li>", "<li>"+drainBlockerText("delivery")+"（5）</li>",
		"<code>cli_busy</code>", "<td>"+drainStatusText("busy")+"</td>", "总数以待核对计数为准", "</html>")
	const receiptForm = `<form method="post" action="/message-receipt">`
	body := response.Body.String()
	if got := strings.Count(body, receiptForm); got != 3 {
		t.Fatalf("receipt forms = %d, want one per sample", got)
	}
	type receiptSample struct{ id, label string }
	for _, account := range []struct {
		appID   string
		samples []receiptSample
	}{
		{"cli_pending", []receiptSample{{replyID, "结果回复"}, {processID, "过程卡"}}},
		{"cli_secondary", []receiptSample{{replyID, "过程卡"}}},
	} {
		_, row, found := strings.Cut(body, "<code>"+account.appID+"</code>")
		row, _, _ = strings.Cut(row, "</tr>")
		forms := strings.Split(row, receiptForm)[1:]
		if !found || len(forms) != len(account.samples) {
			t.Fatalf("App %s has %d receipt forms, want %d", account.appID, len(forms), len(account.samples))
		}
		for i, sample := range account.samples {
			form, _, _ := strings.Cut(forms[i], "</form>")
			if strings.Count(form, "<input ") != 3 {
				t.Errorf("App %s has unexpected receipt inputs: %s", account.appID, form)
			}
			for _, want := range []string{
				`<input type="hidden" name="source" value="file">`,
				`<input type="hidden" name="appId" value="` + account.appID + `">`,
				`<input type="hidden" name="messageId" value="` + sample.id + `">`,
				sample.label + " · <code>" + sample.id + "</code>",
			} {
				if !strings.Contains(form, want) {
					t.Errorf("App %s receipt for %s missing %q", account.appID, sample.id, want)
				}
			}
		}
	}
	checkErr = errWorkspaceServiceConfigChanged
	assertConflict := func(response *httptest.ResponseRecorder) {
		t.Helper()
		assertWebPage(t, response, http.StatusConflict, "请重新打开配置后检查")
		if body := response.Body.String(); strings.Contains(body, `id="drain-result"`) || strings.Contains(body, `name="revision"`) {
			t.Fatal("a revision conflict rendered a result or a stale check form")
		}
	}
	assertConflict(submit())
	if _, err := localfile.WritePrivateAtomic(path, append(raw, '\n'), false); err != nil {
		t.Fatal(err)
	}
	assertConflict(submit())
	if calls != 2 {
		t.Fatalf("check called %d times, want two valid submissions", calls)
	}

	workspace := configWebHandler(webTestHost, sources)
	assertWebPage(t, serve(workspace, http.MethodGet, "/?source=file", "", "", ""), http.StatusOK, `href="/drain-check?source=file"`)
	assertWebPage(t, serve(workspace, http.MethodGet, target+"?source=file", "", "", ""), http.StatusOK, "接收端排空检查")
	assertWebPage(t, serve(workspace, http.MethodPost, target, origin, "same-origin", ""), http.StatusBadRequest, "排空检查表单无效")
}
