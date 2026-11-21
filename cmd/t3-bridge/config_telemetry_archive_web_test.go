package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestConfigWebTelemetryArchive(t *testing.T) {
	dir := t.TempDir()
	raw, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	config, err := bridge.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	config.Tenant, config.AppID = "", "cli_archive_primary"
	config.ReceiverAuthorized = false
	config.StateDir = filepath.Join(dir, "primary-state")
	config.AppSecretFile = filepath.Join(dir, "unused-secret")
	config.MCP.TokenFile = filepath.Join(dir, "unused-token")
	extra := config
	extra.Tenant, extra.AppID = "lark", "cli_archive_extra"
	extra.StateDir = filepath.Join(dir, "extra-state")
	config.AdditionalAccounts = []bridge.Config{extra}
	raw, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "saved.json")
	if _, err := localfile.WritePrivateAtomic(path, raw, true); err != nil {
		t.Fatal(err)
	}
	sources := []webSource{
		{ID: "template", Label: "模板", JSON: raw},
		{ID: "missing", Label: "草稿", Path: filepath.Join(dir, "missing.json"), JSON: raw},
		{ID: "file", Label: "已保存", Path: path, JSON: []byte("{}"), Revision: "startup-only"},
	}
	const day = "2026-02-20"
	archive := receiverTelemetryArchiveDir(extra.StateDir)
	if err := localfile.MkdirAllPrivate(archive); err != nil {
		t.Fatal(err)
	}
	var fixture strings.Builder
	for i := 0; i < 102; i++ {
		fmt.Fprintf(&fixture, `{"type":"receiver-metric","tenant":"lark","appId":"cli_archive_extra","at":"2026-02-20T12:00:00+08:00","name":"pool_active","value":%d,"content":"<script>private-fixture</script>"}`+"\n", i)
	}
	for _, sample := range []string{
		`{"type":"receiver-event","tenant":"feishu","appId":"cli_archive_extra","at":"2026-02-20T04:00:00Z","kind":"run-failed"}`,
		`{"type":"receiver-event","tenant":"lark","appId":"cli_archive_primary","at":"2026-02-20T04:00:00Z","kind":"run-failed"}`,
		`{"type":"receiver-event","tenant":"lark","appId":"cli_archive_extra","at":"2026-02-21T04:00:00Z","kind":"run-failed"}`,
	} {
		fixture.WriteString(sample + "\n")
	}
	if _, err := localfile.WritePrivateAtomic(filepath.Join(archive, receiverTelemetryArchiveName(day)), []byte(fixture.String()), true); err != nil {
		t.Fatal(err)
	}

	mounted := configWebHandler(webTestHost, sources)
	serve := func(handler http.Handler, method, path string, headers http.Header) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://"+webTestHost+path, nil)
		if headers != nil {
			req.Header = headers
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	const target = "/telemetry-archive"
	assertWebPage(t, serve(mounted, http.MethodGet, "/?source=file", nil), http.StatusOK, `href="/telemetry-archive?source=file"`)
	assertWebPage(t, serve(mounted, http.MethodGet, target+"?source=file&date="+day, nil), http.StatusOK,
		`value="cli_archive_primary" selected`, "feishu · cli_archive_primary · 2026-02-20 UTC", "没有归档记录")
	before := time.Now().UTC().Format(receiverTelemetryArchiveDate)
	defaultDay := serve(mounted, http.MethodGet, target, nil)
	after := time.Now().UTC().Format(receiverTelemetryArchiveDate)
	if strings.Contains(defaultDay.Body.String(), `name="date" value="`+before+`"`) {
		after = before
	}
	assertWebPage(t, defaultDay, http.StatusOK, `value="file" selected`, `value="cli_archive_primary" selected`,
		`type="date" name="date" value="`+after+`"`, "没有归档记录")
	const selection = "?source=file&appId=cli_archive_extra&date="
	const previous = target + selection + "2026-02-19"
	const next = target + selection + "2026-02-21"
	selected := serve(mounted, http.MethodGet, target+selection+day, nil)
	assertWebPage(t, selected, http.StatusOK, `value="file" selected`, `value="cli_archive_extra" selected`,
		`method="get"`, `name="date" value="`+day+`"`, "lark · cli_archive_extra · 2026-02-20 UTC",
		"最多显示最近 100 条记录", `href="`+previous+`"`, `href="`+next+`"`)
	if !strings.Contains(selected.Body.String(), `<pre>{&#34;type&#34;:`) {
		t.Fatal("archive is not escaped preformatted text")
	}
	body := html.UnescapeString(selected.Body.String())
	if strings.Count(body, `"type":"receiver-metric"`) != 100 ||
		!strings.Contains(body, `"at":"2026-02-20T04:00:00Z","name":"pool_active","value":2}`) ||
		!strings.Contains(body, `"value":101}`) {
		t.Fatal("archive is missing the last 100 matching records in UTC")
	}
	for _, hidden := range []string{dir, "private-fixture", `"value":0}`, `"value":1}`, "run-failed", `value="missing"`, `value="template"`, `method="post"`} {
		if strings.Contains(body, hidden) {
			t.Errorf("archive exposed %q", hidden)
		}
	}
	for _, date := range []string{"2026-02-19", "2026-02-21"} {
		assertWebPage(t, serve(mounted, http.MethodGet, target+selection+date, nil), http.StatusOK,
			`value="file" selected`, `value="cli_archive_extra" selected`, `name="date" value="`+date+`"`, "没有归档记录")
	}
	for _, method := range []string{http.MethodPost, http.MethodHead} {
		result := serve(mounted, method, target+selection+day, nil)
		assertWebPage(t, result, http.StatusMethodNotAllowed)
		if result.Header().Get("Allow") != http.MethodGet {
			t.Errorf("%s returned Allow %q", method, result.Header().Get("Allow"))
		}
	}

	reads := 0
	guarded := configTelemetryArchiveHandler(func() ([]webSource, error) { return sources, nil },
		func(string, receiverProcessAccount, time.Time, int) ([]byte, error) {
			reads++
			return nil, errors.New(archive)
		})
	for _, query := range []string{"?date=2026-02-30", "?date=../2026-02-20", "?date=2026-2-20", "?date=", "?date=%",
		"?source=", "?appId=", "?source=file&source=file", "?appId=a&appId=b", "?date=" + day + "&date=" + day,
		"?path=other.json", "?tenant=lark", "?lines=1", "?source=file&appId=cli_unknown"} {
		assertWebPage(t, serve(guarded, http.MethodGet, target+query, nil), http.StatusBadRequest)
	}
	for _, id := range []string{"template", "missing", "unknown", path} {
		assertWebPage(t, serve(guarded, http.MethodGet, target+"?source="+url.QueryEscape(id), nil), http.StatusConflict)
	}
	for _, headers := range []http.Header{
		{"Sec-Fetch-Site": {"cross-site"}}, {"Origin": {"https://example.invalid"}},
		{"Origin": {"http://" + webTestHost, "https://example.invalid"}}, {"Sec-Fetch-Site": {"same-origin", "cross-site"}},
	} {
		assertWebPage(t, serve(guarded, http.MethodGet, target+selection+day, headers), http.StatusForbidden)
	}
	if reads != 0 {
		t.Fatal("rejected request read an archive")
	}
	failure := serve(guarded, http.MethodGet, target+selection+day, nil)
	assertWebPage(t, failure, http.StatusUnprocessableEntity, "无法读取所选日期的遥测归档")
	if reads != 1 || strings.Contains(failure.Body.String(), dir) {
		t.Fatal("archive error exposed a local path or skipped the read")
	}
	if saved, err := os.ReadFile(path); err != nil || !bytes.Equal(saved, raw) {
		t.Fatalf("saved config changed: %v", err)
	}
	if _, err := os.Stat(config.StateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read created the absent primary state directory: %v", err)
	}
}
