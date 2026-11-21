package main

import (
	"errors"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConfigRuntimeLogsRestrictsInstanceAndEscapesContents(t *testing.T) {
	const privateError = "synthetic /private/manager/credential"
	const rawLogs = "first line\n<script>alert(1)</script>\n"
	const selectedLogs = `{"type":"receiver-metric","tenant":"feishu","appId":"cli_Work","at":"2026-11-20T00:00:00Z","name":"pool_active","value":1}` + "\n" +
		`{"type":"receiver-metric","tenant":"feishu","appId":"cli_Work","at":"2026-11-20T00:00:01Z","name":"pool_waiting","value":2}` + "\n"
	const telemetryLogs = selectedLogs +
		`{"type":"receiver-metric","tenant":"lark","appId":"cli_Work","at":"2026-11-20T00:00:02Z","name":"pool_active","value":3}` + "\n" +
		`{"type":"receiver-metric","tenant":"feishu","appId":"cli_WorkExtra","at":"2026-11-20T00:00:03Z","name":"pool_active","value":4}` + "\n" +
		`{"type":"receiver-metric","tenant":"feishu","appId":"cli_work","at":"2026-11-20T00:00:04Z","name":"pool_active","value":5}` + "\n"
	const telemetryQuery = "instance=work&format=telemetry"
	const telemetryURL = `/runtime/logs?instance=work&amp;format=telemetry`
	const emptyFilter = "最近读取的日志中没有该 App 的遥测记录。"
	calls := 0
	handler := configRuntimeLogsHandler(func(name string, telemetry bool) (string, error) {
		calls++
		switch name {
		case "work":
			if telemetry {
				return telemetryLogs, nil
			}
			return rawLogs, nil
		case "unavailable":
			return "partial private output", errors.New(privateError)
		default:
			t.Fatalf("unexpected instance %q", name)
			return "", nil
		}
	})
	request := func(t *testing.T, query, origin, site string, status, reads int) string {
		t.Helper()
		before := calls
		page := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://"+webTestHost+"/runtime/logs?"+query, nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Sec-Fetch-Site", site)
		handler.ServeHTTP(page, req)
		if page.Code != status || page.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" {
			t.Fatalf("status=%d, headers=%v", page.Code, page.Header())
		}
		if calls-before != reads {
			t.Fatalf("log reader called %d times, want %d", calls-before, reads)
		}
		body := page.Body.String()
		if strings.Contains(body, "<script>") || strings.Contains(body, privateError) || strings.Contains(body, "partial private output") {
			t.Fatal("response rendered executable text or partial diagnostic information")
		}
		return body
	}
	for _, tc := range []struct {
		name, query, origin, site string
		status                    int
	}{
		{"foreign origin", "instance=work", "https://foreign.test", "", http.StatusForbidden},
		{"cross site", telemetryQuery + "&account=feishu:cli_Work", "", "cross-site", http.StatusForbidden},
		{"absent instance", "", "", "", http.StatusBadRequest},
		{"relative path", "instance=..%2Fwork", "", "", http.StatusBadRequest},
		{"override", "instance=work&path=/private", "", "", http.StatusBadRequest},
		{"unknown format", "instance=work&format=unsupported", "", "", http.StatusBadRequest},
		{"duplicate format", "instance=work&format=telemetry&format=telemetry", "", "", http.StatusBadRequest},
		{"duplicate", "instance=work&instance=unavailable", "", "", http.StatusBadRequest},
		{"malformed", "instance=%", "", "", http.StatusBadRequest},
		{"long query", telemetryQuery + "&account=feishu:cli_" + strings.Repeat("A", serviceLogViewerMaxQueryBytes), "", "", http.StatusBadRequest},
		{"unknown telemetry field", telemetryQuery + "&extra=all", "", "", http.StatusBadRequest},
		{"extra telemetry field", telemetryQuery + "&account=all&extra=all", "", "", http.StatusBadRequest},
		{"invalid format with account", "instance=work&format=raw&account=all", "", "", http.StatusBadRequest},
		{"raw account", "instance=work&account=feishu:cli_Work", "", "", http.StatusBadRequest},
		{"raw all", "instance=work&account=all", "", "", http.StatusBadRequest},
		{"duplicate account", telemetryQuery + "&account=all&%61ccount=feishu:cli_Work", "", "", http.StatusBadRequest},
		{"nonexact account key", telemetryQuery + "&Account=all", "", "", http.StatusBadRequest},
		{"empty account", telemetryQuery + "&account=", "", "", http.StatusBadRequest},
		{"bare account", telemetryQuery + "&account", "", "", http.StatusBadRequest},
		{"empty query field", telemetryQuery + "&&account=all", "", "", http.StatusBadRequest},
		{"invalid account escape", telemetryQuery + "&account=feishu%3Gcli_Work", "", "", http.StatusBadRequest},
		{"invalid all case", telemetryQuery + "&account=ALL", "", "", http.StatusBadRequest},
		{"invalid tenant case", telemetryQuery + "&account=Feishu:cli_Work", "", "", http.StatusBadRequest},
		{"invalid app prefix", telemetryQuery + "&account=feishu:CLI_Work", "", "", http.StatusBadRequest},
		{"account without tenant", telemetryQuery + "&account=cli_Work", "", "", http.StatusBadRequest},
		{"account without app", telemetryQuery + "&account=feishu:", "", "", http.StatusBadRequest},
		{"account with extra colon", telemetryQuery + "&account=feishu:cli_Work:extra", "", "", http.StatusBadRequest},
		{"account with whitespace", telemetryQuery + "&account=feishu:cli_Work+", "", "", http.StatusBadRequest},
		{"app over limit", telemetryQuery + "&account=feishu:cli_" + strings.Repeat("A", 125), "", "", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request(t, tc.query, tc.origin, tc.site, tc.status, 0)
		})
	}
	t.Run("owned raw", func(t *testing.T) {
		body := request(t, "instance=work", "http://"+webTestHost, "same-origin", http.StatusOK, 1)
		if !strings.Contains(body, `<pre aria-label="实例日志内容">`+html.EscapeString(rawLogs)+`</pre>`) ||
			!strings.Contains(body, `href="/runtime/logs?instance=work">刷新日志`) ||
			!strings.Contains(body, `href="`+telemetryURL+`">查看遥测`) ||
			strings.Contains(body, `name="account"`) {
			t.Fatal("raw log content or navigation changed")
		}
	})
	t.Run("unavailable", func(t *testing.T) {
		request(t, "instance=unavailable", "", "", http.StatusBadGateway, 1)
	})
	for _, tc := range []struct {
		name, query, refresh, selected, logs string
	}{
		{"legacy telemetry URL", "", "", "all", telemetryLogs},
		{"all apps", "&account=all", "&amp;account=all", "all", telemetryLogs},
		{"exact pair", "&account=feishu%3Acli_Work", "&amp;account=feishu%3acli_Work", "feishu:cli_Work", selectedLogs},
		{"missing pair", "&account=lark:cli_Missing", "&amp;account=lark%3acli_Missing", "lark:cli_Missing", ""},
		{"maximum size app", "&account=feishu%3Acli_" + strings.Repeat("%41", 124), "&amp;account=feishu%3acli_" + strings.Repeat("A", 124), "feishu:cli_" + strings.Repeat("A", 124), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := request(t, telemetryQuery+tc.query, "http://"+webTestHost, "same-origin", http.StatusOK, 1)
			for _, want := range []string{
				"服务遥测", "每个输出流最多读取最近 100 行", `href="/runtime/logs?instance=work">查看完整日志`,
				`href="` + telemetryURL + tc.refresh + `">刷新遥测`,
				`<form method="get" action="/runtime/logs">`, `name="instance" value="work"`,
				`name="format" value="telemetry"`, `name="account"`, `<button type="submit">应用筛选</button>`,
				`value="` + tc.selected + `" selected`,
			} {
				if !strings.Contains(body, want) {
					t.Fatalf("response missing %q", want)
				}
			}
			_, options, _ := strings.Cut(body, `<select id="telemetry-account" name="account">`)
			options, _, _ = strings.Cut(options, "</select>")
			if !strings.Contains(options, `value="all"`) || strings.Count(options, " selected") != 1 {
				t.Fatal("account list missing the all-apps option or a single selection")
			}
			for _, pair := range []string{"feishu:cli_Work", "lark:cli_Work", "feishu:cli_WorkExtra", "feishu:cli_work"} {
				if strings.Count(options, `value="`+pair+`"`) != 1 || !strings.Contains(options, `>`+pair+`</option>`) {
					t.Fatalf("account option missing or repeated for %q", pair)
				}
			}
			if tc.logs == "" {
				if !strings.Contains(body, emptyFilter) || strings.Contains(body, `<pre aria-label="实例日志内容">`) ||
					!strings.Contains(body, `>`+tc.selected+`</option>`) {
					t.Fatal("empty filtered page lost the selected app or displayed records")
				}
			} else if !strings.Contains(body, `<pre aria-label="实例日志内容">`+html.EscapeString(tc.logs)+`</pre>`) {
				t.Fatal("telemetry response did not preserve the expected records")
			}
		})
	}
}

func TestConfigRuntimeLogsRouteRemainsReadOnly(t *testing.T) {
	handler := runtimeTestWorkspace(t, profileRegistry{}, func() ([]serviceInventoryEntry, error) {
		t.Fatal("invalid log request accessed service inventory")
		return nil, nil
	})
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "http://"+webTestHost+"/runtime/logs?path=private", nil))
	assertWebPage(t, page, http.StatusBadRequest, "请选择有效的受管实例")

	page = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://"+webTestHost+"/runtime/logs?instance=work", nil)
	req.Header.Set("Origin", "http://"+webTestHost)
	handler.ServeHTTP(page, req)
	assertWebPage(t, page, http.StatusMethodNotAllowed)
}
