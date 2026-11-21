package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestConfigWebWorkspaceImport(t *testing.T) {
	dir := t.TempDir()
	oldRoot := filepath.Join(dir, "old")
	t.Setenv("LARK_CHANNEL_HOME", oldRoot)
	configPath := filepath.Join(dir, "config.json")
	legacyPath := filepath.Join(oldRoot, "profiles", "dev", "workspaces.json")
	stateDir, projectPath := filepath.Join(dir, "state"), filepath.Join(dir, "project")
	raw, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	edits, err := json.Marshal([]legacyImportEdit{
		{[]string{"appId"}, "cli_web_import"},
		{[]string{"stateDir"}, stateDir},
		{[]string{"users"}, []string{"ou_web"}},
		{[]string{"chats"}, []string{"oc_web"}},
		{[]string{"routes", "work", "users"}, []string{"ou_web"}},
		{[]string{"routes", "work", "projectId"}, "project-web"},
		{[]string{"routes", "work", "projectPath"}, projectPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = bridge.EditConfigJSON(raw, edits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localfile.WritePrivateAtomic(configPath, raw, true); err != nil {
		t.Fatal(err)
	}
	const sourceKey = "dev\x1fowner\x1foc_old:omt_old\x1fImported"
	legacy, err := json.Marshal(map[string]any{"named": map[string]string{sourceKey: projectPath, "Unmapped": projectPath}})
	if err != nil {
		t.Fatal(err)
	}
	if err := localfile.MkdirAllPrivate(filepath.Dir(legacyPath)); err != nil {
		t.Fatal(err)
	}
	legacyConfig := []byte(`{"schemaVersion":2,"profiles":{"dev":{"schemaVersion":2,"accounts":{"app":{"id":"cli_web_import","tenant":"feishu"}}}}}`)
	if _, err := localfile.WritePrivateAtomic(filepath.Join(oldRoot, "config.json"), legacyConfig, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	resolvedPath, err := filepath.EvalSymlinks(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	sources, err := configWebSources(configPath)
	if err != nil {
		t.Fatal(err)
	}
	handler := configWebHandler(webTestHost, sources)
	baseURL := "http://" + webTestHost
	target := baseURL + "/workspace-import"
	get := func(address string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, address, nil))
		return w
	}
	assertWebPage(t, get(baseURL+"/?source=file"), http.StatusOK, `href="/workspace-import?source=file"`, `value="import-legacy"`)
	initial := get(target + "?" + (url.Values{"source": {"file"}, "action": {"inspect"}, "appId": {"cli_web_import"}, "legacyPath": {legacyPath}}).Encode())
	assertWebPage(t, initial, http.StatusOK, "旧 workspaces.json 的本机绝对路径", "按目标 App 和租户", "唯一 V2 profile",
		"可省略 <code>projectId</code>", `<option value="file" selected>`, `value="inspect" formnovalidate`, `\u001f`, "</html>")
	if body := initial.Body.String(); strings.Contains(body, `value="generic"`) || strings.Contains(body, `value="apply"`) || strings.Contains(body, "Imported") {
		t.Fatal("GET exposed an unsaved template, a write, or inspected aliases")
	}

	const mapping = `[{"sourceKey":"dev\u001fowner\u001foc_old:omt_old\u001fImported","userId":"ou_web","chatId":"oc_web","chatType":"group","topicId":"omt_web","routeId":"work"}]`
	form := url.Values{
		"source": {"file"}, "appId": {"cli_web_import"}, "legacyPath": {legacyPath},
		"mapping": {mapping}, "action": {"apply"}, "review": {""},
	}
	submit := func(origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	form.Set("action", "inspect")
	form.Set("mapping", "")
	assertWebPage(t, submit("https://other.test"), http.StatusForbidden)
	inspected := submit(baseURL)
	assertWebPage(t, inspected, http.StatusOK, "旧工作区来源 <code>"+legacyPath+"</code>", "无效的旧别名 0 个",
		"Imported · "+projectPath, "Unmapped · "+projectPath, "需填话题 ID", `<option value="work">work</option>`,
		`name="userId"`, `name="chatId"`, `value="p2p"`, `value="group"`, `name="topicId"`, `name="projectId"`, `value="add" formnovalidate`, "</html>")
	selected := regexp.MustCompile(`<option value="([^"]+)">Imported · `).FindStringSubmatch(inspected.Body.String())
	if selected == nil {
		t.Fatal("inspect did not render a selectable scoped alias")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(selected[1])
	if err != nil || string(decoded) != sourceKey || strings.Contains(inspected.Body.String(), "\x1f") {
		t.Fatalf("scoped key did not round-trip through the HTML option: %q, %v", decoded, err)
	}

	const currentDraft = `[{"sourceKey":"Unmapped","userId":"ou_web","chatId":"oc_web","chatType":"p2p","routeId":"work"}]`
	form.Set("action", "add")
	form.Set("mapping", currentDraft)
	form.Set("review", "stale-review")
	builder := map[string]string{
		"sourceKey": selected[1], "userId": "ou_web", "chatId": "oc_web", "chatType": "group",
		"topicId": "omt_web", "routeId": "work", "projectId": "project-web",
	}
	for key, value := range builder {
		form.Set(key, value)
	}
	mappingValue := func(w *httptest.ResponseRecorder) string {
		t.Helper()
		field := regexp.MustCompile(`(?s)<textarea name="mapping"[^>]*>\n(.*?)</textarea>`).FindStringSubmatch(w.Body.String())
		if field == nil {
			t.Fatal("page did not render a mapping textarea")
		}
		return html.UnescapeString(field[1])
	}
	assertWebPage(t, submit("https://other.test"), http.StatusForbidden)
	form.Set("routeId", "missing")
	invalidRoute := submit(baseURL)
	assertWebPage(t, invalidRoute, http.StatusUnprocessableEntity, "请选择目标应用已配置的路由")
	if value := mappingValue(invalidRoute); value != currentDraft {
		t.Fatalf("rejected mapping changed the draft: %q", value)
	}
	form.Set("routeId", "work")
	form.Set("topicId", "")
	assertWebPage(t, submit(baseURL), http.StatusUnprocessableEntity, "旧话题别名需要填写目标话题 ID")
	form.Set("topicId", "omt_web")
	if err := os.WriteFile(legacyPath, []byte(`{"named":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	assertWebPage(t, submit(baseURL), http.StatusUnprocessableEntity, "请选择当前来源中的旧别名")
	if err := os.WriteFile(legacyPath, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	draft := submit(baseURL)
	assertWebPage(t, draft, http.StatusOK, "已追加一条映射到草稿", `name="legacyPath" value="`+legacyPath+`"`,
		`name="review" value=""`, "</html>")
	if strings.Contains(draft.Body.String(), `value="apply"`) {
		t.Fatal("adding a draft mapping offered a write without a new preview")
	}
	updated := mappingValue(draft)
	var entries []workspaceImportDraftEntry
	if err := json.Unmarshal([]byte(updated), &entries); err != nil || len(entries) != 2 ||
		entries[0] != (workspaceImportDraftEntry{SourceKey: "Unmapped", UserID: "ou_web", ChatID: "oc_web", ChatType: "p2p", RouteID: "work"}) ||
		entries[1] != (workspaceImportDraftEntry{SourceKey: sourceKey, UserID: "ou_web", ChatID: "oc_web", ChatType: "group", TopicID: "omt_web", RouteID: "work", ProjectID: "project-web"}) {
		t.Fatalf("draft did not preserve and append exactly one mapping: %s, %v", updated, err)
	}
	if _, err := os.Stat(stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspection or draft update touched the target state directory: %v", err)
	}
	form.Set("mapping", updated)
	form.Set("action", "preview")
	form.Set("review", "")
	for key := range builder {
		form.Set(key, "")
	}
	assertWebPage(t, submit(baseURL), http.StatusOK, "未映射的旧别名 0 个", "<td>Unmapped</td>", "<td>Imported</td>", `value="apply"`)
	for key := range builder {
		form.Del(key)
	}
	form.Set("mapping", mapping)
	form.Set("action", "apply")
	assertWebPage(t, submit(baseURL), http.StatusBadRequest, "请先预览工作区迁移")
	form.Set("action", "preview")
	form.Set("source", "generic")
	assertWebPage(t, submit(baseURL), http.StatusConflict, "所选配置不可用")
	form.Set("source", "file")
	assertWebPage(t, submit(baseURL), http.StatusOK, "旧工作区来源 <code>"+legacyPath+"</code>")
	form.Set("legacyPath", "")
	preview := submit(baseURL)
	assertWebPage(t, preview, http.StatusOK, "迁移预览", "未映射的旧别名 1 个", "<td>Imported</td>", "<code>"+projectPath+"</code>",
		"<code>work</code>", "<code>project-web</code>", "<code>ou_web</code>", "<code>oc_web</code>", "<code>omt_web</code>",
		"旧工作区来源 <code>"+resolvedPath+"</code>", `name="legacyPath" value="" spellcheck="false"`,
		"<td>新增</td>", "暂停目标实例的接收端", `value="apply"`, "</html>")
	match := regexp.MustCompile(`name="review" value="([^"]+)"`).FindStringSubmatch(preview.Body.String())
	if match == nil {
		t.Fatal("preview did not return a review")
	}

	form.Set("action", "apply")
	form.Set("review", match[1])
	assertWebPage(t, submit("https://other.test"), http.StatusForbidden)
	form.Set("mapping", " "+mapping)
	assertWebPage(t, submit(baseURL), http.StatusConflict, "请重新预览")
	if _, err := os.Stat(stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preview or rejected write touched the target state directory: %v", err)
	}
	form.Set("mapping", mapping)
	assertWebPage(t, submit(baseURL), http.StatusOK, "已写入目标状态", "旧工作区来源 <code>"+resolvedPath+"</code>", "</html>")
	form.Set("action", "preview")
	assertWebPage(t, submit(baseURL), http.StatusOK, "保留已有别名")
	for file, want := range map[string][]byte{configPath: raw, legacyPath: legacy} {
		if after, err := os.ReadFile(file); err != nil || !bytes.Equal(want, after) {
			t.Fatalf("migration modified input %s: %v", file, err)
		}
	}
}
