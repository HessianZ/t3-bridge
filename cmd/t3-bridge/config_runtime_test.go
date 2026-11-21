package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestConfigRuntimePage(t *testing.T) {
	profiles := profileRegistry{root: t.TempDir()}
	registry := localReceiverProcesses(profiles)
	stale, err := registry.register([]bridge.Config{{AppID: "cli_stale"}})
	if err != nil {
		t.Fatal(err)
	}
	stale.writeDiagnostics(stale.process.Accounts[0], receiverDiagnosticSample{
		At: time.Now().UTC(), Data: bridge.ReceiverDiagnostics{Inbox: 9951},
	})
	if err := stale.lock.Close(); err != nil {
		t.Fatal(err)
	}
	active, err := registry.register([]bridge.Config{
		{AppID: "cli_first"}, {Tenant: "lark", AppID: "cli_second"},
		{Tenant: "lark", AppID: "cli_retrying"}, {AppID: "cli_exhausted"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := active.close(); err != nil {
			t.Error(err)
		}
	})
	observed := time.Now().UTC()
	active.writeDiagnostics(active.process.Accounts[0], receiverDiagnosticSample{At: observed, Data: bridge.ReceiverDiagnostics{
		MCPVerified: true, FeishuReady: true, Inbox: 5, InFlight: 3, Reconcile: 2, DeliveryPending: 1,
		Capacity: &bridge.ReceiverRunCapacity{Limit: 2, Occupied: 2, Waiting: 1},
	}})
	retrying, exhausted := active.process.Accounts[2], active.process.Accounts[3]
	active.writeDiagnostics(retrying, receiverDiagnosticSample{At: observed, Data: bridge.ReceiverDiagnostics{FeishuReady: true, Inbox: 9952}})
	failedAt := time.Now().UTC()
	retryAt := failedAt.Add(sharedReceiverRetryDelay)
	const failureKind = "initial-connect-failed"
	active.writeWorkerStatus(retrying, &receiverWorkerSample{At: failedAt, receiverWorkerStatus: receiverWorkerStatus{
		State: receiverWorkerRetrying, Attempts: 2, Failure: failureKind, RetryAt: retryAt,
	}})
	active.writeWorkerStatus(exhausted, &receiverWorkerSample{At: failedAt, receiverWorkerStatus: receiverWorkerStatus{
		State: receiverWorkerExhausted, Attempts: 4,
	}})
	const diagnostic = "/private/fixture/service.json -token fixture-secret"
	handler := runtimeTestWorkspace(t, profiles, func() ([]serviceInventoryEntry, error) {
		return []serviceInventoryEntry{
			{name: "running-fixture", status: "running", pid: 42001, manage: func(string) error {
				t.Fatal("viewing runtime status invoked service management")
				return nil
			}},
			{name: "stopped-fixture", status: "installed, stopped"},
			{name: "broken-fixture", err: errors.New(diagnostic)},
		}, nil
	})
	index := httptest.NewRecorder()
	handler.ServeHTTP(index, httptest.NewRequest(http.MethodGet, "http://"+webTestHost+"/", nil))
	assertWebPage(t, index, http.StatusOK, `href="/runtime">运行状态`)

	page := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://"+webTestHost+"/runtime", nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	handler.ServeHTTP(page, req)
	assertWebPage(t, page, http.StatusOK, `href="/#runtime" aria-current="page"`, `href="/runtime/install">安装后台服务`, "受管实例", "当前用户的接收进程", "工作台版本 <code>"+bridgeVersion()+"</code>",
		"running-fixture</code></td><td>running</td><td>42001", "stopped-fixture</code></td><td>installed, stopped</td><td>—",
		"broken-fixture</code></td><td>状态不可用", "进程 ID <code>"+active.process.ID+"</code>", "PID <code>"+strconv.Itoa(active.process.PID)+"</code>", "<small><code>"+bridgeVersion()+"</code></small>",
		"4 个 App（多 App 共享）",
		"飞书 WebSocket 已就绪。T3 环境最近建连时核验通过。", "入站 5 · 在途 3 · 待核对 2 · 送达缺口 1",
		"并发额度 2 / 2 · 等待额度 1",
		`datetime="`+observed.Format(time.RFC3339)+`"`, "暂无最近观察到的变化。",
		`<form method="post" action="/runtime/action"`, `name="instance" value="running-fixture"`,
		`value="start">启动`, `value="stop">停止整个实例`, `value="restart">重启整个实例`, `value="review-unregister">注销整个实例`,
		"包括共享此实例的所有 App", "</html>")
	assertWebPage(t, page, http.StatusOK, `href="/runtime/logs?instance=running-fixture">查看日志`)
	for _, private := range []string{stale.process.ID, "cli_stale", "入站 9951 ·", "fixture-secret", "/private/fixture", "-token", failureKind} {
		if strings.Contains(page.Body.String(), private) {
			t.Fatalf("runtime page exposed %q", private)
		}
	}
	const exhaustedHint = "可使用上方的「重连此 App…」恢复接收。"
	for _, tc := range []struct {
		account      receiverProcessAccount
		want, absent []string
	}{
		{active.process.Accounts[0], []string{"入站 5 · 在途 3"}, []string{"下次自动重试", "自动重试已耗尽", "故障类别", exhaustedHint}},
		{active.process.Accounts[1], []string{"当前连接与任务状态未知。"}, []string{"下次自动重试", "自动重试已耗尽", "故障类别", exhaustedHint}},
		{retrying, []string{fmt.Sprintf(`下次自动重试 <time datetime="%s">%s</time> · 已失败 2 次 · 故障类别 初次建连失败`, retryAt.Format(time.RFC3339), retryAt.Local().Format(time.DateTime))},
			[]string{"飞书 WebSocket", "入站 ", "并发额度", "当前连接与任务状态未知。", "自动重试已耗尽", exhaustedHint}},
		{exhausted, []string{"自动重试已耗尽 · 已失败 4 次", exhaustedHint},
			[]string{"飞书 WebSocket", "入站 ", "并发额度", "当前连接与任务状态未知。", "下次自动重试", "故障类别"}},
	} {
		_, accountBody, found := strings.Cut(page.Body.String(), "<code>"+tc.account.AppID+"</code>")
		row, _, _ := strings.Cut(accountBody, "</div>")
		if !found {
			t.Fatalf("missing runtime row for %s", tc.account.AppID)
		}
		reconnectURL := strings.ReplaceAll((runtimeReconnectTarget{Process: active.process.ID, Account: tc.account}).URL(), "&", "&amp;")
		for _, marker := range append(tc.want, `href="`+reconnectURL+`">重连此 App…`) {
			if !strings.Contains(row, marker) {
				t.Fatalf("missing %q in runtime row for %s: %s", marker, tc.account.AppID, row)
			}
		}
		for _, marker := range tc.absent {
			if strings.Contains(row, marker) {
				t.Fatalf("unexpected %q in runtime row for %s: %s", marker, tc.account.AppID, row)
			}
		}
	}
	if page.Header().Get("Cache-Control") != "no-store" || !strings.Contains(page.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("runtime page is missing workspace headers")
	}
	if page.Header().Get("Referrer-Policy") != "same-origin" || strings.Count(page.Body.String(), `<form method="post"`) != 1 {
		t.Fatal("expected same-origin forms only for manageable services")
	}
	if strings.Count(page.Body.String(), `href="/runtime/logs?instance=`) != 1 {
		t.Fatal("unexpected log links for unavailable services")
	}
}

func TestConfigRuntimeDiagnosticHistoryByApp(t *testing.T) {
	profiles := profileRegistry{root: t.TempDir()}
	process, err := localReceiverProcesses(profiles).register([]bridge.Config{
		{AppID: "cli_first"}, {Tenant: "lark", AppID: "cli_second"}, {AppID: "cli_expired"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.close(); err != nil {
			t.Error(err)
		}
	})
	now := time.Now().UTC().Truncate(time.Second)
	process.process.StartedAt = now.Add(-time.Minute)
	registration, err := json.Marshal(process.process)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localfile.WritePrivateAtomic(filepath.Join(process.dir, "process.json"), registration, false); err != nil {
		t.Fatal(err)
	}
	first, second, expired := process.process.Accounts[0], process.process.Accounts[1], process.process.Accounts[2]
	process.writeDiagnostics(first, receiverDiagnosticSample{At: now, Data: bridge.ReceiverDiagnostics{MCPVerified: true, FeishuReady: true, ReconnectWaiting: true}, Events: []receiverDiagnosticEvent{
		{At: now.Add(-7 * time.Second), Kind: "delivery-pending", Count: 91},
		{At: now.Add(-6 * time.Second), Kind: "feishu-ready"},
		{At: now.Add(-5 * time.Second), Kind: "mcp-verified"},
		{At: now.Add(-4 * time.Second), Kind: "reconcile", Count: 2},
		{At: now.Add(-3 * time.Second), Kind: "delivery-pending", Count: 4},
		{At: now.Add(-2 * time.Second), Kind: "delivery-pending"},
		{At: now.Add(-time.Second), Kind: "reconcile"},
	}})
	process.writeDiagnostics(second, receiverDiagnosticSample{At: now, Events: []receiverDiagnosticEvent{
		{At: now.Add(-3 * time.Second), Kind: "mcp-unverified"},
		{At: now.Add(-2 * time.Second), Kind: "feishu-not-ready"},
	}})
	expiredAt := now.Add(-receiverDiagnosticsMaxAge - time.Second)
	process.writeDiagnostics(expired, receiverDiagnosticSample{At: expiredAt, Data: bridge.ReceiverDiagnostics{DeliveryPending: 901, ReconnectWaiting: true}, Events: []receiverDiagnosticEvent{
		{At: expiredAt, Kind: "delivery-pending", Count: 901},
	}})

	handler := runtimeTestWorkspace(t, profiles, func() ([]serviceInventoryEntry, error) { return nil, nil })
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "http://"+webTestHost+"/runtime", nil))
	assertWebPage(t, page, http.StatusOK, "3 个 App（多 App 共享）", "<summary>最近观察到的变化</summary>", "约每 5 秒采样一次，短暂变化可能未记录。")
	for _, tc := range []struct {
		account receiverProcessAccount
		labels  []string
		latest  time.Time
	}{
		{first, []string{"待核对归零", "待送达归零", "待送达 4", "待核对 2", "T3 环境核验通过", "飞书 WebSocket 已就绪"}, now.Add(-time.Second)},
		{second, []string{"飞书 WebSocket 未就绪", "T3 环境未核验"}, now.Add(-2 * time.Second)},
		{expired, nil, time.Time{}},
	} {
		_, accountBody, found := strings.Cut(page.Body.String(), "<code>"+tc.account.AppID+"</code>")
		row, _, _ := strings.Cut(accountBody, "</div>")
		if !found || strings.Count(row, "<li>") != len(tc.labels) {
			t.Fatalf("unexpected history for %s: %s", tc.account.AppID, row)
		}
		if waiting := strings.Contains(row, "此 App 已暂停新派发，等待在途 T3 运行收口后重连。"); waiting != (tc.account.AppID == first.AppID) {
			t.Fatalf("unexpected reconnect waiting status for %s: %s", tc.account.AppID, row)
		}
		if len(tc.labels) == 0 && !strings.Contains(row, "当前连接与任务状态未知。") {
			t.Fatalf("expired sample presented current task status: %s", row)
		}
		for i, label := range tc.labels {
			at := tc.latest.Add(-time.Duration(i) * time.Second)
			entry := fmt.Sprintf(`<li><time datetime="%s">%s</time> · %s</li>`, at.Format(time.RFC3339), at.Local().Format(time.DateTime), label)
			_, row, found = strings.Cut(row, entry)
			if !found {
				t.Fatalf("missing or out-of-order entry for %s: %s", tc.account.AppID, entry)
			}
		}
	}
}

func TestConfigRuntimeEmptyAndUnavailable(t *testing.T) {
	profiles := profileRegistry{root: t.TempDir()}
	registry := localReceiverProcesses(profiles)
	empty := runtimeTestWorkspace(t, profiles, func() ([]serviceInventoryEntry, error) { return nil, nil })
	page := httptest.NewRecorder()
	empty.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "http://"+webTestHost+"/runtime", nil))
	assertWebPage(t, page, http.StatusOK, "暂无受管实例。", "暂无已登记的接收进程。")
	if _, err := os.Stat(registry.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty view created a receiver registry: %v", err)
	}

	if err := os.WriteFile(registry.dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	unavailable := runtimeTestWorkspace(t, profiles, func() ([]serviceInventoryEntry, error) {
		return nil, errors.New("fixture-manager-diagnostic")
	})
	page = httptest.NewRecorder()
	unavailable.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "http://"+webTestHost+"/runtime", nil))
	assertWebPage(t, page, http.StatusOK, "服务清单暂不可用。", "接收进程清单暂不可用。")
	if strings.Contains(page.Body.String(), "fixture-manager-diagnostic") || strings.Contains(page.Body.String(), profiles.root) {
		t.Fatal("runtime page exposed an inventory error")
	}
}

func TestConfigRuntimeRejectsForeignAndWriteRequests(t *testing.T) {
	handler := runtimeTestWorkspace(t, profileRegistry{}, func() ([]serviceInventoryEntry, error) {
		t.Error("rejected request read the service inventory")
		return nil, nil
	})
	for _, tc := range []struct {
		method, host, origin, fetchSite string
		status                          int
	}{
		{http.MethodGet, "foreign.test", "", "", http.StatusForbidden},
		{http.MethodGet, webTestHost, "https://foreign.test", "", http.StatusForbidden},
		{http.MethodGet, webTestHost, "", "cross-site", http.StatusForbidden},
		{http.MethodHead, webTestHost, "", "same-site", http.StatusForbidden},
		{http.MethodPost, webTestHost, "http://" + webTestHost, "same-origin", http.StatusMethodNotAllowed},
	} {
		req := httptest.NewRequest(tc.method, "http://"+tc.host+"/runtime", nil)
		req.Header.Set("Origin", tc.origin)
		req.Header.Set("Sec-Fetch-Site", tc.fetchSite)
		page := httptest.NewRecorder()
		handler.ServeHTTP(page, req)
		if page.Code != tc.status {
			t.Errorf("%s host=%q origin=%q site=%q returned %d, want %d", tc.method, tc.host, tc.origin, tc.fetchSite, page.Code, tc.status)
		}
	}
}

func TestConfigRuntimeRejectsUnsafeActions(t *testing.T) {
	handler := runtimeTestWorkspace(t, profileRegistry{}, func() ([]serviceInventoryEntry, error) {
		t.Fatal("rejected request reached service inventory")
		return nil, nil
	})
	const form = "instance=work&action=stop"
	const unregister = "instance=work&action=unregister"
	const confirmed = unregister + "&confirm=unregister"
	const review = "instance=work&action=review-unregister"
	const origin = "http://" + webTestHost
	for _, tc := range []struct {
		name   string
		change func(*http.Request)
		body   string
		status int
	}{
		{"GET", func(r *http.Request) { r.Method = http.MethodGet }, form, http.StatusMethodNotAllowed},
		{"PUT", func(r *http.Request) { r.Method = http.MethodPut }, form, http.StatusMethodNotAllowed},
		{"host", func(r *http.Request) { r.Host = "foreign.test" }, form, http.StatusForbidden},
		{"no origin", func(r *http.Request) { r.Header.Del("Origin") }, form, http.StatusForbidden},
		{"null origin", func(r *http.Request) { r.Header.Set("Origin", "null") }, form, http.StatusForbidden},
		{"foreign origin", func(r *http.Request) { r.Header.Set("Origin", "https://foreign.test") }, form, http.StatusForbidden},
		{"different port", func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:47890") }, form, http.StatusForbidden},
		{"duplicate origin", func(r *http.Request) { r.Header.Add("Origin", origin) }, form, http.StatusForbidden},
		{"same-site", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") }, form, http.StatusForbidden},
		{"none", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "none") }, form, http.StatusForbidden},
		{"duplicate fetch site", func(r *http.Request) { r.Header.Add("Sec-Fetch-Site", "cross-site") }, form, http.StatusForbidden},
		{"query", func(r *http.Request) { r.URL.RawQuery = form }, form, http.StatusBadRequest},
		{"wrong content type", func(r *http.Request) { r.Header.Set("Content-Type", "application/json") }, form, http.StatusBadRequest},
		{"path", nil, "instance=..%2Fwork&action=stop", http.StatusBadRequest},
		{"path field", nil, form + "&path=%2Ftmp%2Fservice", http.StatusBadRequest},
		{"arguments", nil, form + "&args=--force", http.StatusBadRequest},
		{"script", nil, form + "&script=fixture", http.StatusBadRequest},
		{"no button action", nil, "instance=work", http.StatusBadRequest},
		{"two actions", nil, form + "&action=start", http.StatusBadRequest},
		{"two names", nil, form + "&instance=other", http.StatusBadRequest},
		{"install", nil, "instance=work&action=install", http.StatusBadRequest},
		{"unregister without confirmation", nil, unregister, http.StatusBadRequest},
		{"unregister with wrong confirmation", nil, unregister + "&confirm=Unregister", http.StatusBadRequest},
		{"unregister with duplicate confirmation", nil, confirmed + "&confirm=unregister", http.StatusBadRequest},
		{"unregister with extra field", nil, confirmed + "&other=1", http.StatusBadRequest},
		{"unregister with query confirmation", func(r *http.Request) { r.URL.RawQuery = "confirm=unregister" }, unregister, http.StatusBadRequest},
		{"unregister with foreign origin", func(r *http.Request) { r.Header.Set("Origin", "https://foreign.test") }, confirmed, http.StatusForbidden},
		{"review with cross-site origin", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, review, http.StatusForbidden},
		{"review with confirmation", nil, review + "&confirm=unregister", http.StatusBadRequest},
		{"stop with confirmation", nil, form + "&confirm=unregister", http.StatusBadRequest},
		{"long body", nil, form + "&extra=" + strings.Repeat("x", 1024), http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := runtimeTestActionRequest(tc.body)
			if tc.change != nil {
				tc.change(req)
			}
			page := httptest.NewRecorder()
			handler.ServeHTTP(page, req)
			if page.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", page.Code, tc.status, page.Body)
			}
		})
	}
}

func TestConfigRuntimeSkipsUnavailableAndUnlistedServices(t *testing.T) {
	const diagnostic = "PRIVATE-FIXTURE-DIAGNOSTIC"
	for _, tc := range []struct {
		name    string
		entries []serviceInventoryEntry
		listErr error
		status  int
		result  string
	}{
		{"missing", nil, nil, http.StatusNotFound, "未找到该受管实例"},
		{"unmanaged", []serviceInventoryEntry{{name: "work", err: errors.New(diagnostic), manage: func(string) error {
			t.Fatal("unmanaged service was invoked")
			return nil
		}}}, nil, http.StatusConflict, "该实例当前无法管理"},
		{"inventory unavailable", []serviceInventoryEntry{{name: "work", manage: func(string) error {
			t.Fatal("service with an unavailable inventory was invoked")
			return nil
		}}}, errors.New(diagnostic), http.StatusServiceUnavailable, "无法读取受管实例清单"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := runtimeTestWorkspace(t, profileRegistry{}, func() ([]serviceInventoryEntry, error) {
				return tc.entries, tc.listErr
			})
			for _, action := range []string{"stop", "review-unregister", "unregister&confirm=unregister"} {
				page := httptest.NewRecorder()
				handler.ServeHTTP(page, runtimeTestActionRequest("instance=work&action="+action))
				assertWebPage(t, page, tc.status, tc.result, "操作未执行")
				if strings.Contains(page.Body.String(), diagnostic) || strings.Contains(page.Body.String(), `<form method="post"`) {
					t.Fatalf("%s response exposed diagnostics or unavailable controls", action)
				}
			}
		})
	}
}

func TestConfigRuntimeReviewsAndCancelsUnregister(t *testing.T) {
	reads := 0
	handler := runtimeTestWorkspace(t, profileRegistry{}, func() ([]serviceInventoryEntry, error) {
		reads++
		return []serviceInventoryEntry{{name: "other"}, {name: "work", manage: func(string) error {
			t.Fatal("reviewing or canceling unregister invoked service management")
			return nil
		}}}, nil
	})
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, runtimeTestActionRequest("instance=work&action=review-unregister"))
	assertWebPage(t, page, http.StatusOK, `aria-describedby="runtime-unregister-impact"`, "确认注销实例 <code>work</code>",
		"注销会停止整个实例中所有 App 的接收并移除服务定义。", "配置、凭据、状态和日志会保留。", "T3 中已在途的 run 可能继续执行。",
		`name="instance" value="work"`, `name="action" value="unregister"`, `name="confirm" value="unregister">确认注销整个实例`, `href="/runtime">取消`)
	if reads != 1 {
		t.Fatalf("review read the service inventory %d times, want 1", reads)
	}

	page = httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "http://"+webTestHost+"/runtime", nil))
	assertWebPage(t, page, http.StatusOK, "work</code></td>", `value="review-unregister">注销整个实例`)
	if strings.Contains(page.Body.String(), `name="confirm"`) {
		t.Fatal("cancel left an active confirmation form")
	}
}

func TestConfigRuntimeReadsBackUnregisterResult(t *testing.T) {
	const diagnostic = "private stderr: /private/config.json TOKEN=fixture-secret"
	for _, tc := range []struct {
		name, result          string
		manageErr, refreshErr error
		stillListed           bool
		status                int
	}{
		{name: "removed", result: "已从刷新后的受管实例清单移除", status: http.StatusOK},
		{name: "still listed", result: "刷新后仍在受管实例清单中", stillListed: true, status: http.StatusBadGateway},
		{name: "management failed", result: "注销请求失败或结果未确认", manageErr: errors.New(diagnostic), stillListed: true, status: http.StatusBadGateway},
		{name: "refresh failed", result: "无法刷新受管实例清单，注销结果未确认", refreshErr: errors.New(diagnostic), status: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads, calls := 0, 0
			work := serviceInventoryEntry{name: "work", status: "active", manage: func(action string) error {
				calls++
				if action != "unregister" || reads != 1 {
					t.Fatalf("management action %q after %d reads", action, reads)
				}
				return tc.manageErr
			}}
			handler := runtimeTestWorkspace(t, profileRegistry{}, func() ([]serviceInventoryEntry, error) {
				reads++
				if reads == 1 {
					return []serviceInventoryEntry{{name: "other"}, work}, nil
				}
				entries := []serviceInventoryEntry{{name: "other", status: "running"}}
				if tc.stillListed {
					entries = append(entries, work)
				}
				return entries, tc.refreshErr
			})
			page := httptest.NewRecorder()
			handler.ServeHTTP(page, runtimeTestActionRequest("instance=work&action=unregister&confirm=unregister"))
			assertWebPage(t, page, tc.status, "<code>work</code> · ", tc.result, "other</code></td><td>running")
			if reads != 2 || calls != 1 {
				t.Fatalf("unregister made %d inventory reads and %d management calls, want 2 and 1", reads, calls)
			}
			body := page.Body.String()
			if strings.Contains(body, "work</code></td>") != tc.stillListed {
				t.Fatal("page did not display the refreshed inventory")
			}
			if strings.Contains(body, "fixture-secret") || strings.Contains(body, "/private/config.json") {
				t.Fatal("unregister response exposed private diagnostics")
			}
		})
	}
}

func TestConfigRuntimeManagedActions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX service definition fixtures")
	}
	for _, platform := range []string{"linux", "darwin"} {
		t.Run(platform, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", root)
			t.Setenv("XDG_CONFIG_HOME", root)
			instance := writeInventoryDefinition(t, platform, "work")
			active := false
			failStop := false
			const diagnostic = "private stderr: /private/config.json TOKEN=fixture-secret"
			manager := func(command string, args ...string) ([]byte, error) {
				switch command {
				case "systemctl":
					switch args[2] {
					case "show":
						state := "inactive"
						if active {
							state = "active"
						}
						return fmt.Appendf(nil, "LoadState=loaded\nFragmentPath=%s\nActiveState=%s\n", instance.file, state), nil
					case "start", "restart":
						active = true
					case "disable":
						if failStop {
							return []byte(diagnostic), errors.New(diagnostic)
						}
						active = false
					case "enable", "daemon-reload":
					default:
						t.Fatalf("unexpected systemctl arguments %q", args)
					}
				case "/bin/launchctl":
					switch args[0] {
					case "print":
						if !active {
							return fmt.Appendf(nil, "Could not find service %q", instance.id), errors.New("not loaded")
						}
						return fmt.Appendf(nil, "path = %s\nstate = running\n", instance.file), nil
					case "bootstrap":
						active = true
					case "bootout":
						if failStop {
							return []byte(diagnostic), errors.New(diagnostic)
						}
						active = false
					case "enable", "disable":
					default:
						t.Fatalf("unexpected launchctl arguments %q", args)
					}
				default:
					t.Fatalf("unexpected manager %q", command)
				}
				return nil, nil
			}
			running, stopped := "active", "inactive"
			if platform == "darwin" {
				running, stopped = "running", "installed, stopped"
			}
			reads := 0
			handler := runtimeTestWorkspace(t, profileRegistry{root: t.TempDir()}, func() ([]serviceInventoryEntry, error) {
				reads++
				return readServiceInventory(platform, manager)
			})
			for _, tc := range []struct {
				action, result, state string
				fail                  bool
				status                int
			}{
				{"start", "启动请求已提交", running, false, http.StatusOK},
				{"restart", "重启请求已提交", running, false, http.StatusOK},
				{"stop", "停止请求失败或结果未确认", running, true, http.StatusBadGateway},
				{"stop", "停止请求已提交", stopped, false, http.StatusOK},
			} {
				failStop, reads = tc.fail, 0
				page := httptest.NewRecorder()
				req := runtimeTestActionRequest("instance=work&action=" + tc.action)
				// A client without Fetch Metadata still sends the exact Origin.
				if tc.action == "start" {
					req.Header.Del("Sec-Fetch-Site")
				}
				handler.ServeHTTP(page, req)
				assertWebPage(t, page, tc.status, tc.result, "work</code></td><td>"+tc.state+"</td>")
				if reads != 2 || strings.Contains(page.Body.String(), "fixture-secret") || strings.Contains(page.Body.String(), "/private/config.json") {
					t.Fatalf("%s did not reread status or exposed private output: %d reads", tc.action, reads)
				}
			}
		})
	}
}

func runtimeTestActionRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "http://"+webTestHost+"/runtime/action", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://"+webTestHost)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	return req
}

func runtimeTestWorkspace(t *testing.T, profiles profileRegistry, listServices func() ([]serviceInventoryEntry, error)) http.Handler {
	t.Helper()
	sources, err := configWebSources("")
	if err != nil {
		t.Fatal(err)
	}
	return configWorkspaceHandlerWithRuntime(webTestHost, sources, profiles, configRuntimeHandler(profiles, listServices))
}
