package main

import (
	"context"
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

func TestConfigWebLegacySessionImport(t *testing.T) {
	dir := t.TempDir()
	path, legacyPath := filepath.Join(dir, "target.json"), filepath.Join(dir, "old.json")
	t.Setenv("LARK_CHANNEL_HOME", dir)
	detectedPath := filepath.Join(dir, "config.json")
	stateDir, projectPath := filepath.Join(dir, "state"), filepath.Join(dir, "project")
	const appID, chatID, userID, threadID = "cli_session", "oc_private", "ou_session", "old-thread"
	const topicChatID, topicID, topicThreadID = "oc_topic_only", "omt_old", "topic-thread"
	const otherTopicID, otherTopicThreadID = "omt_other", "other-topic-thread"
	const summary, policy = "synthetic private transcript", "synthetic policy fingerprint"
	const secret = "synthetic-app-secret"
	write := func(path string, raw []byte) {
		t.Helper()
		if _, err := localfile.WritePrivateAtomic(path, raw, true); err != nil {
			t.Fatal(err)
		}
	}
	jsonBytes := func(value any) []byte {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	raw, err := loadConfigTemplate("generic")
	if err != nil {
		t.Fatal(err)
	}
	raw, err = bridge.EditConfigJSON(raw, jsonBytes([]legacyImportEdit{
		{[]string{"appId"}, appID}, {[]string{"stateDir"}, stateDir},
		{[]string{"users"}, []string{userID}}, {[]string{"directChats"}, []string{chatID}},
		{[]string{"chats"}, []string{topicChatID}},
		{[]string{"routes", "work", "users"}, []string{userID}},
		{[]string{"routes", "work", "environmentId"}, "env-session"},
		{[]string{"routes", "work", "projectId"}, "project-session"},
		{[]string{"routes", "work", "projectPath"}, projectPath},
	}))
	if err != nil {
		t.Fatal(err)
	}
	write(path, raw)
	legacyRaw := []byte(`{"schemaVersion":2,"profiles":{"work":{"schemaVersion":2,"agentKind":"t3","accounts":{"app":{"id":"cli_session","secret":"` + secret + `"}}}}}`)
	write(legacyPath, legacyRaw)
	catalogPath := filepath.Join(dir, "profiles", "work", "sessions.json.catalog.json")
	if err := localfile.MkdirAllPrivate(filepath.Dir(catalogPath)); err != nil {
		t.Fatal(err)
	}
	entry := func(scope, thread, fingerprint string, updatedAt int64) map[string]any {
		return map[string]any{
			"key":     strings.Join([]string{scope, "t3", projectPath, fingerprint}, "\x1f"),
			"scopeId": scope, "agentId": "t3", "cwdRealpath": projectPath, "policyFingerprint": fingerprint,
			"status": "active", "threadId": thread, "updatedAt": updatedAt, "lastSummary": summary,
		}
	}
	catalogRaw := jsonBytes([]map[string]any{
		entry(chatID, threadID, policy, 1735689600123),
		entry(chatID, "older-thread", "older-policy", 1735603200123),
		entry(topicChatID+":"+topicID, topicThreadID, policy, 1735603200123),
		entry(topicChatID+":"+otherTopicID, otherTopicThreadID, policy, 1735516800123),
	})
	write(catalogPath, catalogRaw)

	revision := configRevision(raw)
	sources := []webSource{
		{ID: "file", Label: "目标配置", Path: path, Revision: "startup-version"},
		{ID: "generic", Label: "模板", JSON: raw},
		{ID: "missing", Label: "未保存", Path: filepath.Join(dir, "missing.json")},
	}
	handler := configWebHandler(webTestHost, sources)
	const target = "/legacy-session-import"
	origin := "http://" + webTestHost
	serveHandler := func(h http.Handler, method, requestPath, requestOrigin string, form url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, origin+requestPath, strings.NewReader(form.Encode()))
		if method == http.MethodPost {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", requestOrigin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	serve := func(method, requestPath, requestOrigin string, form url.Values) *httptest.ResponseRecorder {
		return serveHandler(handler, method, requestPath, requestOrigin, form)
	}
	assertWebPage(t, serve(http.MethodGet, "/?source=file", "", nil), http.StatusOK, `href="/legacy-session-import?source=file">转接旧会话</a>`)
	assertWebPage(t, serve(http.MethodGet, target+"?source=file", "", nil), http.StatusOK, `name="legacyConfig" value=""`)
	write(detectedPath, []byte("synthetic non-JSON"))
	initial := serve(http.MethodGet, target+"?source=file", "", nil)
	assertWebPage(t, initial, http.StatusOK, `<option value="file" selected>`, "停止原接收端", "目标状态目录须处于离线状态",
		"目录查询和普通预览只读取本机文件", "仅在目标状态中登记转接元数据", "沿用现有授权",
		`<form method="post" action="/legacy-session-import">`, `value="check">预览并核验 T3</button>`,
		`name="revision" value="`+revision+`"`, `value="prepare-route">采用原线程执行配置并审阅</button>`,
		`name="legacyConfig" value="`+detectedPath+`"`, `value="inspect" formnovalidate`,
		`<option value="p2p" selected>私聊</option>`, `<option value="group">群聊</option>`, `name="topicId" value=""`,
		"留空使用已保存配置的主 App", "留空匹配同 App、同租户的唯一 T3 V2 profile", "</html>")
	if initial.Header().Get("Referrer-Policy") != "same-origin" || strings.Contains(initial.Body.String(), `value="apply"`) ||
		strings.Contains(initial.Body.String(), `value="generic"`) || strings.Contains(initial.Body.String(), `value="missing"`) {
		t.Fatal("initial page allowed an unsaved source or an unreviewed write, or omitted the policy for native form posts")
	}
	form := url.Values{
		"source": {"file"}, "appId": {appID}, "legacyConfig": {legacyPath}, "legacyProfile": {"work"},
		"threadId": {threadID}, "chatId": {chatID}, "userId": {userID}, "routeId": {"work"}, "action": {"apply"}, "review": {""},
	}
	submit := func() *httptest.ResponseRecorder { return serve(http.MethodPost, target, origin, form) }
	assertWebPage(t, submit(), http.StatusBadRequest, "请先预览旧会话转接")
	form.Set("action", "preview")
	for _, field := range []string{"chatType", "topicId", "chatId", "unknown"} {
		t.Run("malformed/"+field, func(t *testing.T) {
			invalid, err := url.ParseQuery(form.Encode())
			if err != nil {
				t.Fatal(err)
			}
			switch field {
			case "unknown":
				invalid.Set(field, "unexpected")
			case "chatId":
				invalid.Add(field, chatID)
			default:
				invalid[field] = []string{"", ""}
			}
			assertWebPage(t, serve(http.MethodPost, target, origin, invalid), http.StatusBadRequest, "转接表单无效")
		})
	}
	assertWebPage(t, serve(http.MethodPost, target+"?topicId="+topicID, origin, form), http.StatusBadRequest, "转接表单无效")
	form.Set("source", "generic")
	assertWebPage(t, submit(), http.StatusConflict, "所选配置不可用")
	form.Set("source", "file")
	form.Set("legacyConfig", "")
	assertWebPage(t, submit(), http.StatusUnprocessableEntity, "旧配置的绝对路径", `name="legacyConfig" value=""`)
	form.Set("legacyConfig", legacyPath)
	form.Set("action", "inspect")
	form.Set("review", "previous-review")
	form.Set("threadId", "manual-thread")
	form.Set("userId", "")
	form.Set("routeId", "")
	assertWebPage(t, serve(http.MethodPost, target, "https://other.test", form), http.StatusForbidden)
	assertWebPage(t, serve(http.MethodPost, target, "", form), http.StatusForbidden)
	form.Set("chatId", "")
	form.Set("appId", "")
	form.Set("legacyProfile", "")
	form.Set("legacyConfig", "")
	assertWebPage(t, submit(), http.StatusUnprocessableEntity, "旧配置的绝对路径")
	form.Set("legacyConfig", legacyPath)
	inventory := submit()
	assertWebPage(t, inventory, http.StatusOK, "旧目录聊天范围", "旧目录无法证明聊天类型或参与者",
		"目标 App <code>"+appID+"</code> · 旧 profile <code>work</code>",
		"聊天 <code>"+chatID+"</code> · 2 个活动 T3 线程 · 更新于 2025-01-01 00:00:00 UTC",
		"聊天 <code>"+topicChatID+"</code> · 话题 <code>"+topicID+"</code> · 1 个活动 T3 线程 · 更新于 2024-12-31 00:00:00 UTC",
		`name="chatId" value="" list="legacy-session-chats"`, `<datalist id="legacy-session-chats">`, `<option value="`+chatID+`"></option>`,
		`name="review" value=""`, "</html>")
	for _, forbidden := range []string{`<option value="` + topicChatID + `"`, "legacy-session-candidates", threadID, "older-thread", topicThreadID, otherTopicThreadID,
		projectPath, summary, policy, secret, "previous-review", "转接预览", `value="apply"`} {
		if strings.Contains(inventory.Body.String(), forbidden) {
			t.Errorf("inventory response exposed %q", forbidden)
		}
	}
	form.Set("appId", appID)
	form.Set("legacyProfile", "work")
	form.Set("chatId", topicChatID+":"+topicID)
	assertWebPage(t, submit(), http.StatusUnprocessableEntity)
	form.Set("chatId", "oc_privat")
	assertWebPage(t, submit(), http.StatusOK, "没有符合条件的旧 T3 线程")
	form.Set("chatId", chatID)
	lookup := submit()
	assertWebPage(t, lookup, http.StatusOK, "旧 T3 线程查询结果", `name="legacyConfig" value="`+legacyPath+`"`,
		`name="appId" value="`+appID+`"`, `name="legacyProfile" value="work"`, `name="chatId" value="`+chatID+`"`,
		"私聊 <code>"+chatID+"</code>",
		`name="threadId" value="manual-thread" list="legacy-session-candidates"`,
		`<option value="`+threadID+`" label="`+projectPath+`">`, "2025-01-01 00:00:00 UTC", `name="review" value=""`, "</html>")
	candidate := regexp.MustCompile(`<datalist id="legacy-session-candidates">\s*<option value="([^"]+)"`).FindStringSubmatch(lookup.Body.String())
	if candidate == nil {
		t.Fatal("lookup omitted a selectable thread")
	}
	for _, forbidden := range []string{summary, policy, secret, topicThreadID, "previous-review", "转接预览", `value="apply"`} {
		if strings.Contains(lookup.Body.String(), forbidden) {
			t.Errorf("lookup response exposed %q", forbidden)
		}
	}
	if _, err := os.Stat(stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lookup touched the target state directory: %v", err)
	}
	assertInputsUnchanged := func() {
		t.Helper()
		for input, want := range map[string][]byte{path: raw, legacyPath: legacyRaw, catalogPath: catalogRaw, detectedPath: []byte("synthetic non-JSON")} {
			got, err := os.ReadFile(input)
			if err != nil || string(got) != string(want) {
				t.Fatalf("transfer changed input %q: %v", input, err)
			}
		}
	}
	assertInputsUnchanged()
	form.Set("action", "preview")
	form.Set("review", "")
	form.Set("threadId", candidate[1])
	form.Set("userId", userID)
	form.Set("routeId", "work")
	form.Set("appId", "")
	form.Set("legacyProfile", "")
	for _, field := range []string{"chatId", "userId", "threadId", "routeId"} {
		value := form.Get(field)
		form.Set(field, "")
		assertWebPage(t, submit(), http.StatusUnprocessableEntity)
		form.Set(field, value)
	}
	preview := submit()
	assertWebPage(t, preview, http.StatusOK, "转接预览", appID, chatID, userID, threadID, "env-session", "project-session", projectPath,
		"才会读取 T3 并核验原线程", "登记待核验的转接", `value="apply"`, `name="legacyConfig" value="`+legacyPath+`"`, "</html>")
	reviewField := regexp.MustCompile(`name="review" value="([^"]+)"`)
	match := reviewField.FindStringSubmatch(preview.Body.String())
	if match == nil || strings.Contains(preview.Body.String(), summary) || strings.Contains(preview.Body.String(), "T3 核验报告") {
		t.Fatal("offline preview omitted its review or showed unexpected content")
	}
	routeCandidate, err := bridge.EditConfigJSON(raw, []byte(`[{"path":["routes","work","model"],"value":"original-thread-model"}]`))
	if err != nil {
		t.Fatal(err)
	}
	checkCalls, prepareCalls, blocked := 0, 0, true
	wantRequest := legacySessionRequest{LegacyConfig: legacyPath, ThreadID: threadID, ChatID: chatID, UserID: userID, RouteID: "work"}
	actionHandler := configLegacySessionHandlerWithActions(func() ([]webSource, error) { return sources, nil },
		func(_ context.Context, source webSource, request legacySessionRequest) (bridge.LegacySessionImportResult, string, []string, error) {
			checkCalls++
			if source.ID != "file" || source.Path != path || source.Revision != revision || request != wantRequest {
				t.Fatalf("check received an unexpected saved source or mapping: %+v %+v", source, request)
			}
			result, review, err := previewLegacySession(source, request)
			if blocked {
				return result, "", []string{"原线程仍在运行。", "实际项目 <project>&"}, err
			}
			return result, review, []string{"原线程空闲。", "实际项目 <project>&"}, err
		}, func(_ context.Context, source webSource, request legacySessionRequest) ([]byte, error) {
			prepareCalls++
			if source.ID != "file" || source.Path != path || source.Revision != revision || request != wantRequest {
				t.Errorf("preparation received an unexpected saved source or mapping: %+v %+v", source, request)
			}
			return routeCandidate, nil
		})
	form.Set("action", "check")
	form.Set("chatType", "p2p")
	form.Set("review", match[1])
	if rejected := serveHandler(actionHandler, http.MethodPost, target, "https://other.test", form); rejected.Code != http.StatusForbidden || checkCalls != 0 {
		t.Fatal("cross-origin request reached verification")
	}
	assertActionPage := func(t *testing.T, page *httptest.ResponseRecorder, status int, markers ...string) {
		t.Helper()
		if page.Code != status {
			t.Fatalf("action page returned %d, want %d: %s", page.Code, status, page.Body.String())
		}
		for _, marker := range markers {
			if !strings.Contains(page.Body.String(), marker) {
				t.Errorf("action page missing %q", marker)
			}
		}
	}
	blockedCheck := serveHandler(actionHandler, http.MethodPost, target, origin, form)
	assertActionPage(t, blockedCheck, http.StatusOK, "转接预览", "T3 核验报告", "原线程仍在运行。", "实际项目 &lt;project&gt;&amp;",
		"当前不可确认写入", `name="review" value=""`, "</html>")
	if strings.Contains(blockedCheck.Body.String(), `value="apply"`) || strings.Contains(blockedCheck.Body.String(), match[1]) {
		t.Fatal("blocked check retained the previous review or its write action")
	}
	blocked = false
	checked := serveHandler(actionHandler, http.MethodPost, target, origin, form)
	assertActionPage(t, checked, http.StatusOK, "T3 核验报告", "原线程空闲。", "实际项目 &lt;project&gt;&amp;",
		"本次 T3 核验通过", "仍会读取 T3 并核验原线程", `name="review" value="`+match[1]+`"`, `value="apply"`, "</html>")
	if checkCalls != 2 {
		t.Fatalf("expected two explicit checks, got %d", checkCalls)
	}
	t.Run("prepare-route", func(t *testing.T) {
		prepare, err := url.ParseQuery(form.Encode())
		if err != nil {
			t.Fatal(err)
		}
		prepare.Set("action", "prepare-route")
		post := func(requestOrigin string) *httptest.ResponseRecorder {
			return serveHandler(actionHandler, http.MethodPost, target, requestOrigin, prepare)
		}
		assertActionPage(t, post(origin), http.StatusBadRequest, "请重新打开已保存的目标配置")
		prepare.Set("revision", "startup-version")
		assertActionPage(t, post(origin), http.StatusConflict, "已保存配置版本已变化")
		prepare.Set("revision", revision)
		prepare.Add("revision", revision)
		assertActionPage(t, post(origin), http.StatusBadRequest, "转接表单无效")
		prepare.Set("revision", revision)
		assertActionPage(t, post("https://other.test"), http.StatusForbidden)
		if prepareCalls != 0 {
			t.Fatal("preparation ran without an explicit action and a valid same-origin revision")
		}
		prepared := post(origin)
		assertActionPage(t, prepared, http.StatusOK, "原线程执行配置的候选 JSON 已就绪", `name="action" value="check">校验并查看差异`, `name="review" value=""`)
		body := prepared.Body.String()
		handoff := regexp.MustCompile(`<form method="post" action="/\?source=file">\s*<input type="hidden" name="revision" value="([^"]+)">\s*<input type="hidden" name="config" value="([^"]*)">`).FindStringSubmatch(body)
		if handoff == nil || handoff[1] != revision || html.UnescapeString(handoff[2]) != string(routeCandidate) || prepareCalls != 1 || checkCalls != 2 {
			t.Fatal("preparation did not hand the candidate and saved revision to the editor")
		}
		if strings.Contains(body, match[1]) || strings.Contains(body, `value="apply"`) || strings.Contains(body, `value="save"`) {
			t.Fatal("preparation offered to write or exposed the previous migration review")
		}
		assertWebPage(t, serve(http.MethodPost, "/?source=file", origin, url.Values{
			"action": {"check"}, "revision": {handoff[1]}, "config": {html.UnescapeString(handoff[2])},
		}), http.StatusOK, "与来源的差异", "original-thread-model", `name="revision" value="`+revision+`"`)
		if _, err := os.Stat(stateDir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("preparation or diff review touched the target state directory: %v", err)
		}
	})
	assertInputsUnchanged()
	form.Set("action", "preview")
	form.Set("review", "")
	form.Set("chatType", "group")
	assertWebPage(t, submit(), http.StatusUnprocessableEntity, "not authorized")
	form.Set("chatType", "p2p")
	form.Set("topicId", "")
	form.Set("revision", revision)
	assertWebPage(t, submit(), http.StatusOK, `name="review" value="`+match[1]+`"`)
	form.Del("revision")
	form.Del("chatType")
	form.Del("topicId")
	form.Set("action", "apply")
	form.Set("review", match[1])
	assertWebPage(t, serve(http.MethodPost, target, "https://other.test", form), http.StatusForbidden)
	form.Set("threadId", "other-thread")
	assertWebPage(t, submit(), http.StatusConflict, "请重新预览")
	if _, err := os.Stat(stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preview or rejected write touched the target state directory: %v", err)
	}
	form.Set("threadId", threadID)
	confirmed := submit()
	assertWebPage(t, confirmed, http.StatusOK, "转接已登记，等待新私聊消息核验", `name="legacyConfig" value="`+legacyPath+`"`, "</html>")
	if strings.Contains(confirmed.Body.String(), summary) || strings.Contains(confirmed.Body.String(), `value="apply"`) {
		t.Fatal("confirmation exposed legacy content or left the write action available")
	}

	statePath := filepath.Join(stateDir, "state.json")
	readState := func() []byte {
		t.Helper()
		data, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	privateState := readState()
	form.Set("action", "inspect")
	form.Set("review", "")
	form.Set("chatType", "group")
	form.Set("chatId", topicChatID)
	form.Set("threadId", "")
	assertWebPage(t, submit(), http.StatusOK, "群聊主会话", "没有符合条件的旧 T3 线程")
	form.Del("chatType")
	form.Set("topicId", topicID)
	assertWebPage(t, submit(), http.StatusUnprocessableEntity)
	form.Set("chatType", "room")
	assertWebPage(t, submit(), http.StatusUnprocessableEntity)
	form.Set("chatType", "group")
	form.Set("topicId", topicID+":"+otherTopicID)
	assertWebPage(t, submit(), http.StatusUnprocessableEntity)
	form.Set("topicId", topicID)
	groupLookup := submit()
	assertWebPage(t, groupLookup, http.StatusOK, "旧 T3 线程查询结果", "群聊 <code>"+topicChatID+"</code> · 话题 <code>"+topicID+"</code>",
		`<option value="group" selected>群聊</option>`, `name="topicId" value="`+topicID+`"`,
		`<option value="`+topicThreadID+`" label="`+projectPath+`">`, "</html>")
	for _, forbidden := range []string{otherTopicThreadID, summary, policy, secret, `value="apply"`} {
		if strings.Contains(groupLookup.Body.String(), forbidden) {
			t.Errorf("group lookup exposed %q", forbidden)
		}
	}
	form.Set("action", "preview")
	form.Set("threadId", topicThreadID)
	groupPreview := submit()
	const proof = "逐条核对原 T3 线程的所有旧输入及其对应的飞书原消息"
	assertWebPage(t, groupPreview, http.StatusOK, "转接预览", "<dt>群聊 ID</dt><dd><code>"+topicChatID+"</code></dd>",
		"<dt>话题 ID</dt><dd><code>"+topicID+"</code></dd>", "且已获授权的新群聊消息", proof,
		"全部通过后才会续聊", "发现其他发言人或缺少证明时，核验失败", `value="apply"`, "</html>")
	match = reviewField.FindStringSubmatch(groupPreview.Body.String())
	if match == nil || strings.Contains(groupPreview.Body.String(), summary) {
		t.Fatal("group preview omitted its review or exposed legacy content")
	}
	form.Set("action", "apply")
	form.Set("review", match[1])
	form.Set("topicId", otherTopicID)
	assertWebPage(t, submit(), http.StatusConflict, "请重新预览")
	form.Set("topicId", topicID)
	form.Set("chatType", "p2p")
	assertWebPage(t, submit(), http.StatusConflict, "请重新预览")
	if string(readState()) != string(privateState) {
		t.Fatal("lookup, preview or rejected confirmation changed the target state")
	}
	form.Set("chatType", "group")
	groupConfirmed := submit()
	assertWebPage(t, groupConfirmed, http.StatusOK, "转接已登记，等待新群聊消息核验",
		"<dt>群聊 ID</dt><dd><code>"+topicChatID+"</code></dd>", "<dt>话题 ID</dt><dd><code>"+topicID+"</code></dd>", proof, "</html>")
	if strings.Contains(groupConfirmed.Body.String(), summary) || strings.Contains(groupConfirmed.Body.String(), `value="apply"`) {
		t.Fatal("group confirmation exposed legacy content or left the write action available")
	}

	var before, after bridge.State
	if err := json.Unmarshal(privateState, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(readState(), &after); err != nil {
		t.Fatal(err)
	}
	if len(before.LegacySessions) != 1 || len(after.LegacySessions) != 2 {
		t.Fatalf("unexpected pending transfer count before %d and after %d", len(before.LegacySessions), len(after.LegacySessions))
	}
	for key, entry := range after.LegacySessions {
		if entry.ThreadID == topicThreadID && entry.ProjectPath == projectPath && entry.ChatType == "group" {
			delete(after.LegacySessions, key)
		}
	}
	if string(jsonBytes(before)) != string(jsonBytes(after)) {
		t.Fatal("group confirmation changed state beyond the pending transfer metadata")
	}
	assertInputsUnchanged()
}
