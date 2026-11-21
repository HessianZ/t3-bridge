package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestConfigWebReceiverReadiness(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	other := filepath.Join(dir, "other.json")
	raw := []byte("{}\n")
	for _, file := range []string{path, other} {
		if _, err := localfile.WritePrivateAtomic(file, raw, true); err != nil {
			t.Fatal(err)
		}
	}
	sources := []webSource{
		{ID: "generic", Label: "模板"},
		{ID: "other", Label: "另一份配置", Path: other},
		{ID: "file", Label: "已保存配置", Path: path, Revision: "startup-only"},
		{ID: "missing", Label: "未保存", Path: filepath.Join(dir, "missing.json")},
	}

	at := time.Date(2026, 11, 20, 2, 0, 0, 0, time.UTC)
	states := []string{"disabled", "offline", "ambiguous", "revision-unknown", "different-revision",
		"waiting-sample", "retrying", "exhausted", "reconnect-wait", "connecting", "ready"}
	report := receiverReadinessReport{Revision: configRevision(raw)}
	for _, state := range states {
		row := receiverReadinessAccount{
			receiverProcessAccount: receiverProcessAccount{Tenant: "feishu", AppID: "cli_" + state},
			State:                  state,
		}
		switch state {
		case "revision-unknown":
			row.Process, row.PID, row.Version = "old-process", 101, "v0.1.0"
		case "different-revision":
			row.Process, row.ProcessRevision = "different-process", "other-revision"
		case "retrying":
			row.Worker = &receiverWorkerSample{At: at, receiverWorkerStatus: receiverWorkerStatus{
				State: state, Attempts: 2, Failure: "initial-connect-failed", RetryAt: at.Add(time.Minute),
			}}
		case "exhausted":
			row.Worker = &receiverWorkerSample{At: at, receiverWorkerStatus: receiverWorkerStatus{
				State: state, Attempts: sharedReceiverMaxRetries + 1, Failure: "initial-connect-failed",
			}}
		case "ready":
			row.Sample = &receiverDiagnosticSample{At: at, Data: bridge.ReceiverDiagnostics{
				FeishuReady: true, MCPVerified: true, Inbox: 3, InFlight: 4,
			}}
		}
		report.Accounts = append(report.Accounts, row)
	}

	calls := 0
	var readErr error
	handler := configReceiverReadinessHandler(func() ([]webSource, error) { return sources, nil },
		func(gotPath, revision string) (receiverReadinessReport, error) {
			calls++
			if gotPath != path || revision != report.Revision {
				t.Error("readiness did not receive the selected path and fresh revision")
			}
			return report, readErr
		})
	const target = "/runtime/receiver-readiness"
	serve := func(handler http.Handler, method, query, site string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://"+webTestHost+target+query, nil)
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	mounted := configWebHandler(webTestHost, sources)
	assertWebPage(t, serve(mounted, http.MethodPost, "?source=file", ""), http.StatusMethodNotAllowed)
	assertWebPage(t, serve(mounted, http.MethodHead, "?source=file", ""), http.StatusMethodNotAllowed)
	assertWebPage(t, serve(mounted, http.MethodGet, "?source=file&path=other.json", ""), http.StatusBadRequest)
	for _, query := range []string{"?source=", "?source=file&source=generic", "?source=file&revision=client", "?source=%"} {
		assertWebPage(t, serve(handler, http.MethodGet, query, ""), http.StatusBadRequest)
	}
	for _, id := range []string{"generic", "missing", path} {
		assertWebPage(t, serve(handler, http.MethodGet, "?source="+url.QueryEscape(id), ""), http.StatusConflict)
	}
	assertWebPage(t, serve(handler, http.MethodGet, "?source=file", "cross-site"), http.StatusForbidden)
	if calls != 0 {
		t.Fatal("rejected requests invoked readiness")
	}

	page := serve(handler, http.MethodGet, "?source=file", "")
	assertWebPage(t, page, http.StatusOK, `value="file" selected`, `method="get"`, "刷新状态",
		"接收未就绪", report.Revision, "old-process", "PID 101", "v0.1.0", "other-revision",
		"2026-11-20T02:00:00Z", "2026-11-20T02:01:00Z", "初次建连失败",
		"飞书 WebSocket 已就绪", "入站 3 · 在途 4", "</html>")
	for _, state := range states {
		assertWebPage(t, page, http.StatusOK, "<code>cli_"+state+"</code>", "<strong>"+receiverReadinessStatusText(state)+"</strong>")
	}
	body := page.Body.String()
	if calls != 1 || strings.Contains(body, `method="post"`) ||
		strings.Contains(body, `value="missing"`) || strings.Count(body, "下次自动重试") != 1 {
		t.Fatal("unexpected source, form method, or retry details")
	}

	raw = append(raw, '\n')
	if _, err := localfile.WritePrivateAtomic(path, raw, false); err != nil {
		t.Fatal(err)
	}
	report.Revision, report.Ready = configRevision(raw), true
	assertWebPage(t, serve(handler, http.MethodGet, "?source=file", ""), http.StatusOK, report.Revision, "已启用的 App 均已就绪")
	readErr = errWorkspaceServiceConfigChanged
	stale := serve(handler, http.MethodGet, "?source=file", "")
	assertWebPage(t, stale, http.StatusConflict, "请刷新状态或重新选择配置", "刷新状态", "</html>")
	if strings.Contains(stale.Body.String(), `id="readiness-result"`) {
		t.Fatal("published a readiness report for stale configuration")
	}
}
