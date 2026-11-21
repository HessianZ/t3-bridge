package main

import (
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

const webTestHost = "127.0.0.1:41123"

func TestConfigWebCandidate(t *testing.T) {
	sources, err := configWebSources("")
	if err != nil {
		t.Fatal(err)
	}
	original := strings.NewReplacer(
		`"defaultRoute": "work"`, `"defaultRoute": "Work.Team"`,
		`"work":`, `"Work.Team":`,
		`"options": {}`, `"options": {"X.Y": true, "x.y": false, "Number": 9007199254740993}`,
	).Replace(string(sources[0].JSON))
	path := filepath.Join(t.TempDir(), "existing.json")
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	sources, err = configWebSources(path)
	if err != nil {
		t.Fatal(err)
	}
	handler := configWebHandler(webTestHost, sources)
	for _, source := range sources {
		req := httptest.NewRequest(http.MethodGet, "http://"+webTestHost+"/?source="+source.ID, nil)
		page := httptest.NewRecorder()
		handler.ServeHTTP(page, req)
		assertWebPage(t, page, http.StatusOK, "访问控制", "账号联通", "未检查", "应用 ID", "执行权限", "添加路由", "下载候选 JSON", "</html>",
			`data-route-summary-project>已绑定项目</small>`, `data-target-results hidden`, `data-target-apply hidden disabled`,
			`<select data-key="meetingSummaryTarget">`, `<option value="" selected>关闭</option>`, `<option value="origin">原私聊</option>`, `<option value="owner">应用所有者私聊</option>`,
			`data-groups-list`, `data-groups-add`, `name="action" value="import-legacy"`)
	}
	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "http://"+webTestHost+"/config.js", nil))
	assertWebPage(t, asset, http.StatusOK, "data-key", "addRoute", "fieldJSON")

	edits := `[{"path":["appId"],"value":"cli_candidate"},{"path":["routes","Work.Team","model"],"value":"candidate_model"},{"path":["routes","Work.Team","interactionMode"],"value":"plan"},{"path":["meetingEnabled"],"value":true},{"path":["meetingRoute"],"value":"Work.Team"},{"path":["meetingRoomReplies"],"value":true},{"path":["meetingAutoJoinUsers"],"value":["ou_sample"]}]`
	form := url.Values{"config": {string(sources[0].JSON)}, "edits": {edits}, "action": {"check"}}
	page := submitWebConfig(handler, form)
	assertWebPage(t, page, http.StatusOK, "与来源的差异", `-  "appId": "REPLACE_APP_ID"`, `+  "appId": "cli_candidate"`,
		"配置何时生效", "在线生效", "按 App 重连", "重启实例", "首次初始化",
		`data-route="Work.Team"`, `data-key="meetingEnabled" checked`, `data-key="meetingRoute" value="Work.Team"`, `data-key="meetingRoomReplies" checked`, `data-key="meetingAutoJoinUsers"`, `"model": "candidate_model"`, "X.Y", "x.y", "9007199254740993")
	match := regexp.MustCompile(`name="reviewed" value="([a-f0-9]{64})"`).FindStringSubmatch(page.Body.String())
	if match == nil {
		t.Fatal("valid candidate did not become downloadable")
	}

	form.Set("reviewed", match[1])
	form.Set("action", "download")
	download := submitWebConfig(handler, form)
	if download.Code != http.StatusOK ||
		download.Header().Get("Content-Disposition") != `attachment; filename="t3-bridge.candidate.json"` {
		t.Fatalf("unexpected download: status=%d", download.Code)
	}
	parsed, err := bridge.ParseConfig(download.Body.Bytes())
	route := parsed.Routes["Work.Team"]
	if err != nil || !parsed.MeetingEnabled || !parsed.MeetingRoomReplies || parsed.MeetingRoute != "Work.Team" || len(parsed.MeetingAutoJoinUsers) != 1 || parsed.MeetingAutoJoinUsers[0] != "ou_sample" ||
		route.Options["X.Y"] != true || route.Options["x.y"] != false ||
		route.Model != "candidate_model" || !strings.Contains(download.Body.String(), "9007199254740993") {
		t.Fatalf("download lost execution values: %v", err)
	}
	saved, err := os.ReadFile(path)
	if err != nil || string(saved) != original {
		t.Fatal("source file changed", err)
	}

	form.Set("edits", `[{"path":["routes","Work.Team"],"delete":true}]`)
	assertWebPage(t, submitWebConfig(handler, form), http.StatusUnprocessableEntity, "未通过", `value="download" disabled`)
	form.Set("edits", `[{"path":["appId"],"value":"different"}]`)
	assertWebPage(t, submitWebConfig(handler, form), http.StatusConflict, "候选配置已修改", "different")
	form.Set("edits", "")
	form.Set("config", `{"appId":"safe","appId":"duplicate"}`)
	assertWebPage(t, submitWebConfig(handler, form), http.StatusUnprocessableEntity, "未通过", `value="download" disabled`)
}

func TestConfigWebRejectsForeignRequests(t *testing.T) {
	sources, err := configWebSources("")
	if err != nil {
		t.Fatal(err)
	}
	handler := configWebHandler(webTestHost, sources)
	req := httptest.NewRequest(http.MethodGet, "http://other.test/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "REPLACE_APP_ID") {
		t.Fatal("foreign host received config")
	}
	req = httptest.NewRequest(http.MethodPost, "http://"+webTestHost+"/", nil)
	req.Header.Set("Origin", "https://other.test")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("foreign origin received status %d", w.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "http://"+webTestHost+"/feishu/groups", nil)
	req.Header.Set("Origin", "https://other.test")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("foreign origin reached group query, status %d", w.Code)
	}
}

func TestConfigWebSaveRequiresReviewedCurrentRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	sources, err := configWebSources(path)
	if err != nil {
		t.Fatal(err)
	}
	handler := configWebHandler(webTestHost, sources)
	const preflightLink = `href="/runtime/install?source=file">接收预检`
	workbench := "http://" + webTestHost + "/?source=file"
	missingPage := httptest.NewRecorder()
	handler.ServeHTTP(missingPage, httptest.NewRequest(http.MethodGet, workbench, nil))
	assertWebPage(t, missingPage, http.StatusOK)
	if strings.Contains(missingPage.Body.String(), preflightLink) {
		t.Fatal("workbench offered preflight for an unsaved file")
	}
	edits := `[{"path":["appId"],"value":"synthetic_saved"},{"path":["routes","work","interactionMode"],"value":"plan"},{"path":["meetingEnabled"],"value":true},{"path":["meetingRoute"],"value":"work"},{"path":["meetingSummaryTarget"],"value":"owner"}]`
	form := url.Values{
		"config":   {string(sources[0].JSON)},
		"revision": {"missing"},
		"edits":    {strings.Replace(edits, `"owner"`, `"room"`, 1)},
		"action":   {"save"},
	}
	assertWebPage(t, submitWebConfig(handler, form), http.StatusUnprocessableEntity,
		"meetingSummaryTarget must be empty, origin or owner", `<option value="room" selected disabled>`, `value="save" disabled`)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid meeting summary target touched the source: %v", err)
	}
	form.Set("edits", edits)
	assertWebPage(t, submitWebConfig(handler, form), http.StatusConflict, "候选配置已修改")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unreviewed save touched the source: %v", err)
	}
	candidate, err := bridge.EditConfigJSON(sources[0].JSON, []byte(form.Get("edits")))
	if err != nil {
		t.Fatal(err)
	}
	form.Set("action", "check")
	page := submitWebConfig(handler, form)
	assertWebPage(t, page, http.StatusOK, `<option value="owner" selected>应用所有者私聊</option>`)
	form.Set("reviewed", configRevision(candidate))
	form.Set("action", "save")
	response := submitWebConfig(handler, form)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/?source=file&saved=1" {
		t.Fatalf("unexpected save response %d: %s", response.Code, response.Body.String())
	}
	savedPage := httptest.NewRecorder()
	handler.ServeHTTP(savedPage, httptest.NewRequest(http.MethodGet, "http://"+webTestHost+response.Header().Get("Location"), nil))
	assertWebPage(t, savedPage, http.StatusOK, preflightLink)
	reopenedPage := httptest.NewRecorder()
	handler.ServeHTTP(reopenedPage, httptest.NewRequest(http.MethodGet, workbench, nil))
	assertWebPage(t, reopenedPage, http.StatusOK, preflightLink)
	installPage := httptest.NewRecorder()
	handler.ServeHTTP(installPage, httptest.NewRequest(http.MethodGet, "http://"+webTestHost+"/runtime/install?source=file", nil))
	assertWebPage(t, installPage, http.StatusOK, `name="revision" value="`+configRevision(candidate)+`"`)
	assertWebPage(t, submitWebConfig(handler, form), http.StatusConflict, "config revision conflict")
	stored, err := bridge.LoadConfig(path)
	if err != nil || stored.AppID != "synthetic_saved" || stored.MeetingSummaryTarget != "owner" {
		t.Fatalf("save did not publish candidate: %v", err)
	}
	newer, err := bridge.EditConfigJSON(candidate, []byte(`[{"path":["appId"],"value":"synthetic_newer"}]`))
	if err != nil {
		t.Fatal(err)
	}
	revision, err := saveConfigCandidate(path, newer, configRevision(candidate))
	if err != nil {
		t.Fatal(err)
	}
	preview := submitWebConfig(handler, url.Values{"action": {"load-backup"}, "revision": {revision}})
	assertWebPage(t, preview, http.StatusOK, "与来源的差异", "synthetic_saved", "synthetic_newer")
	raw, err := os.ReadFile(path)
	if err != nil || configRevision(raw) != revision {
		t.Fatalf("backup preview touched the on-disk configuration: %v", err)
	}
}

func TestConfigWebAddExistingAppAsDraft(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	sources, err := configWebSources(path)
	if err != nil {
		t.Fatal(err)
	}
	handler := configWebHandler(webTestHost, sources)
	page := submitWebConfig(handler, url.Values{
		"config":           {string(sources[0].JSON)},
		"revision":         {"missing"},
		"action":           {"add-account"},
		"newAppId":         {"cli_synthetic_aux"},
		"newTenant":        {"lark"},
		"newAppSecretFile": {"/unused/aux.secret"},
		"newStateDir":      {"/unused/state"},
		"newUser":          {"ou_aux"},
		"newRoute":         {"work"},
	})
	assertWebPage(t, page, http.StatusOK, "cli_synthetic_aux", `data-account="0"`, "与来源的差异")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("draft changed the source: %v", err)
	}
}

func TestConfigWebImportLegacyDraft(t *testing.T) {
	dir := t.TempDir()
	legacyPath, path := filepath.Join(dir, "legacy.json"), filepath.Join(dir, "config.json")
	legacyHome := t.TempDir()
	t.Setenv("LARK_CHANNEL_HOME", legacyHome)
	detectedPath := filepath.Join(legacyHome, "config.json")
	stateDir, secretFile := filepath.Join(dir, "new-state"), filepath.Join(dir, "private.secret")
	const secretMarker = "synthetic_legacy_secret_marker"
	const legacy = `{
		"schemaVersion":2, "activeProfile":"default",
		"profiles":{"default":{
			"schemaVersion":2,
			"accounts":{"app":{"id":"cli_imported","secret":"` + secretMarker + `"}},
			"access":{"allowedUsers":["ou_imported"]}
		}}
	}`
	if err := os.WriteFile(legacyPath, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	sources, err := configWebSources(path)
	if err != nil {
		t.Fatal(err)
	}
	handler := configWebHandler(webTestHost, sources)
	get := func() *httptest.ResponseRecorder {
		page := httptest.NewRecorder()
		handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "http://"+webTestHost+"/?source=file", nil))
		return page
	}
	const closedImport = "<details class=\"panel advanced-row\" data-workbench-page=\"advanced\">\n      <summary>导入旧 lark-channel-bridge 配置</summary>"
	assertWebPage(t, get(), http.StatusOK, closedImport, `name="legacySourcePath" value=""`)
	if err := os.WriteFile(detectedPath, []byte("synthetic non-JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	assertWebPage(t, get(), http.StatusOK, closedImport, `name="legacySourcePath" value="`+detectedPath+`"`)
	const manualPath = "  custom & relative.json  "
	form := url.Values{
		"action":              {"check"},
		"revision":            {"missing"},
		"config":              {strings.Replace(string(sources[0].JSON), `"receiverMachine": "bridge"`, `"receiverMachine": "draft-machine"`, 1)},
		"edits":               {`[{"path":["routes","work","users"],"value":["ou_route"]},{"path":["routes","work","model"],"value":"draft-model"},{"path":["receiverAuthorized"],"value":true}]`},
		"legacySourcePath":    {manualPath},
		"legacyProfile":       {"absent"},
		"legacyStateDir":      {stateDir},
		"legacyAppSecretFile": {secretFile},
	}
	assertWebPage(t, submitWebConfig(handler, form), http.StatusOK, closedImport, `name="legacySourcePath" value="`+manualPath+`"`)
	form.Set("action", "import-legacy")
	form.Set("legacySourcePath", "")
	assertWebPage(t, submitWebConfig(handler, form), http.StatusUnprocessableEntity, "旧配置必须填写绝对 JSON 文件路径", `name="legacySourcePath" value=""`)
	form.Set("legacySourcePath", legacyPath)
	assertWebPage(t, submitWebConfig(handler, form), http.StatusUnprocessableEntity,
		`name="legacySourcePath" value="`+legacyPath+`"`, `name="legacyProfile" value="absent"`,
		`name="legacyStateDir" value="`+stateDir+`"`, `name="legacyAppSecretFile" value="`+secretFile+`"`,
		`"receiverMachine": "draft-machine"`, `"model": "draft-model"`, `"appId": "REPLACE_APP_ID"`, `value="save" disabled`)
	form.Del("legacyProfile")
	preview := submitWebConfig(handler, form)
	assertWebPage(t, preview, http.StatusOK, "与来源的差异", `+  "appId": "cli_imported"`, closedImport, `name="legacySourcePath" value="`+legacyPath+`"`)
	body := preview.Body.String()
	if strings.Contains(body, secretMarker) {
		t.Fatal("legacy credential appeared in the preview")
	}
	draft := regexp.MustCompile(`(?s)<textarea [^>]*name="config"[^>]*>\n(.*?)</textarea>`).FindStringSubmatch(body)
	if draft == nil {
		t.Fatal("preview did not render a candidate draft")
	}
	candidate := html.UnescapeString(draft[1])
	parsed, err := bridge.ParseConfig([]byte(candidate))
	if err != nil || parsed.ReceiverMachine != "draft-machine" || parsed.Routes["work"].Model != "draft-model" ||
		parsed.AppID != "cli_imported" || parsed.ReceiverAuthorized || parsed.AppSecretFile != secretFile {
		t.Fatalf("preview did not preserve draft edits and apply the import: %v", err)
	}
	reviewed := configRevision([]byte(candidate))
	assertWebPage(t, preview, http.StatusOK, `name="reviewed" value="`+reviewed+`"`)
	form.Set("action", "save")
	form.Set("config", candidate)
	form.Del("edits")
	assertWebPage(t, submitWebConfig(handler, form), http.StatusConflict, "候选配置已修改")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("import or unreviewed save created the destination: %v", err)
	}
	form.Set("reviewed", reviewed)
	if response := submitWebConfig(handler, form); response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/?source=file&saved=1" {
		t.Fatalf("reviewed save returned status %d and location %q", response.Code, response.Header().Get("Location"))
	}
	if saved, err := os.ReadFile(path); err != nil || string(saved) != candidate {
		t.Fatalf("reviewed save did not write the selected destination: %v", err)
	}
	if raw, err := os.ReadFile(legacyPath); err != nil || string(raw) != legacy {
		t.Fatalf("legacy source changed: %v", err)
	}
}

func submitWebConfig(handler http.Handler, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "http://"+webTestHost+"/?source=file", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func assertWebPage(t *testing.T, page *httptest.ResponseRecorder, status int, markers ...string) {
	t.Helper()
	if page.Code != status || page.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d, want %d; cache control %q", page.Code, status, page.Header().Get("Cache-Control"))
	}
	body := html.UnescapeString(page.Body.String())
	for _, marker := range markers {
		if !strings.Contains(body, marker) {
			t.Errorf("page missing %q", marker)
		}
	}
}
