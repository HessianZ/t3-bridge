package main

import (
	"encoding/json"
	"html"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestWorkbenchProfilePrivateBundleImport(t *testing.T) {
	exporter, dir, original, secrets := exportProfileFixture(t)
	plan, err := exporter.previewExport("work")
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "exports", "work & copy.json")
	otherSource := filepath.Join(dir, "exports", "other.json")
	for _, path := range []string{source, otherSource} {
		if err := exporter.exportFile("work", path, plan.Revision, true, false); err != nil {
			t.Fatal(err)
		}
	}
	registry := profileRegistry{root: filepath.Join(dir, "destination")}
	sources, err := configWebSources("")
	if err != nil {
		t.Fatal(err)
	}
	handler := configWorkspaceHandler(webTestHost, sources, registry)
	const path = "/profiles/import"
	const editPath = "/?source=profile-moved"
	destination := filepath.Join(registry.root, "profiles", "moved", "config.json")
	assertWebPage(t, requestProfileLifecycle(handler, "/profiles", nil, ""), http.StatusOK,
		"暂无已保存的命名配置", `href="/profiles/import"`)
	entry := requestProfileLifecycle(handler, path, nil, "")
	assertWebPage(t, entry, http.StatusOK, `type="text" name="name"`, `type="text" name="source"`, `type="text" name="patch"`, "绝对路径")
	if strings.Contains(entry.Body.String(), `type="file"`) || entry.Header().Get("Referrer-Policy") != "same-origin" {
		t.Fatal("import must accept a local path and support same-origin form submission")
	}
	spoofed := httptest.NewRecorder()
	handler.ServeHTTP(spoofed, httptest.NewRequest(http.MethodGet, "http://different.invalid"+path, nil))
	assertWebPage(t, spoofed, http.StatusForbidden)

	post := func(t *testing.T, form url.Values, origin string) *httptest.ResponseRecorder {
		t.Helper()
		response := requestProfileLifecycle(handler, path, form, origin)
		body := response.Body.String()
		for _, secret := range secrets {
			if strings.Contains(body, secret) {
				t.Fatal("import returned an App Secret")
			}
		}
		if strings.Contains(body, "synthetic-t3-token-outside-bundle") || strings.Contains(body, "schemaVersion") ||
			response.Header().Get("Content-Disposition") != "" {
			t.Fatal("import returned bundle or token contents")
		}
		return response
	}
	previewForm := url.Values{"name": {"moved"}, "source": {source}, "action": {"preview"}}
	for _, origin := range []string{"", "http://different.invalid"} {
		assertWebPage(t, post(t, previewForm, origin), http.StatusForbidden)
	}
	assertWebPage(t, post(t, url.Values{"name": {"moved"}, "source": {"relative.json"}, "action": {"preview"}},
		profileLifecycleTestOrigin), http.StatusBadRequest, "绝对路径")
	blankPatch := maps.Clone(previewForm)
	blankPatch.Set("patch", "")
	assertWebPage(t, post(t, blankPatch, profileLifecycleTestOrigin), http.StatusOK, "预览私有包导入")
	preview := func(t *testing.T) url.Values {
		t.Helper()
		response := post(t, previewForm, profileLifecycleTestOrigin)
		assertWebPage(t, response, http.StatusOK, source, destination, `包内配置 <strong>work</strong>`,
			`新配置 <strong>moved</strong>`, "2 个 App", "接收授权初始关闭", "T3 令牌继续使用包内配置的文件引用",
			`type="checkbox" name="confirm" value="yes" required`, `value="cancel"`)
		assertProfileLifecycleFileMissing(t, registry.root)
		return url.Values{"name": {"moved"}, "source": {source}, "action": {"confirm"},
			"review": {credentialWebToken(t, response)}, "confirm": {"yes"}}
	}
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, url.Values)
		status int
	}{
		{"changed name", func(_ *testing.T, form url.Values) { form.Set("name", "other") }, http.StatusConflict},
		{"changed source", func(_ *testing.T, form url.Values) { form.Set("source", otherSource) }, http.StatusConflict},
		{"client revision", func(_ *testing.T, form url.Values) { form.Set("revision", "client-value") }, http.StatusBadRequest},
		{"missing confirmation", func(_ *testing.T, form url.Values) { form.Del("confirm") }, http.StatusBadRequest},
		{"cancel", func(_ *testing.T, form url.Values) {
			form.Set("action", "cancel")
			form.Del("confirm")
		}, http.StatusSeeOther},
		{"changed bundle revision", func(t *testing.T, _ url.Values) {
			changeProfileLifecycleRevision(t, source)
		}, http.StatusConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := preview(t)
			changed := maps.Clone(original)
			test.mutate(t, changed)
			response := post(t, changed, profileLifecycleTestOrigin)
			assertWebPage(t, response, test.status)
			if test.status == http.StatusSeeOther && response.Header().Get("Location") != "/profiles" {
				t.Fatal("cancel did not return to named profiles")
			}
			assertWebPage(t, post(t, original, profileLifecycleTestOrigin), http.StatusConflict, "预览已失效")
			assertProfileLifecycleFileMissing(t, registry.root)
		})
	}

	form := preview(t)
	assertWebPage(t, post(t, form, profileLifecycleTestOrigin), http.StatusOK, "已创建命名配置",
		`href="/?source=profile-moved"`, "打开新配置")
	assertWebPage(t, post(t, form, profileLifecycleTestOrigin), http.StatusConflict, "预览已失效")
	assertWebPage(t, requestProfileLifecycle(handler, editPath, nil, ""), http.StatusOK, "命名配置 · moved")
	if active, err := registry.active(); err != nil || active != "" {
		t.Fatalf("import changed the default profile: %q, %v", active, err)
	}
	created, err := bridge.LoadConfig(destination)
	if err != nil || len(created.Accounts()) != len(secrets) {
		t.Fatalf("invalid imported configuration: %v", err)
	}
	for i, account := range created.Accounts() {
		old := original.Accounts()[i]
		if account.AppID != old.AppID || account.ReceiverAuthorized || account.MCP.TokenFile != old.MCP.TokenFile ||
			account.StateDir == old.StateDir || account.AppSecretFile == old.AppSecretFile {
			t.Fatal("unexpected identity, receiver state or file references in the new profile")
		}
		if value, err := bridge.Secret(account.AppSecretFile); err != nil || value != secrets[account.AppID] {
			t.Fatalf("import did not write the synthetic App Secret: %v", err)
		}
	}
}

func TestWorkbenchProfilePrivateBundleImportWithPatch(t *testing.T) {
	exporter, dir, _, _ := exportProfileFixture(t)
	plan, err := exporter.previewExport("work")
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "exports", "work.json")
	if err := exporter.exportFile("work", source, plan.Revision, true, false); err != nil {
		t.Fatal(err)
	}
	patch := filepath.Join(dir, "local & patch.json")
	tokenFile := filepath.Join(dir, "target-t3-token.json")
	const projectID = "synthetic-target-project"
	raw, err := json.Marshal([]map[string]any{
		{"path": []string{"mcp", "tokenFile"}, "value": tokenFile},
		{"path": []string{"routes", "work", "projectId"}, "value": projectID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localfile.WritePrivateAtomic(patch, raw, true); err != nil {
		t.Fatal(err)
	}
	registry := profileRegistry{root: filepath.Join(dir, "destination")}
	sources, err := configWebSources("")
	if err != nil {
		t.Fatal(err)
	}
	handler := configWorkspaceHandler(webTestHost, sources, registry)
	const path = "/profiles/import"
	post := func(form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		response := requestProfileLifecycle(handler, path, form, profileLifecycleTestOrigin)
		body := html.UnescapeString(response.Body.String())
		if strings.Contains(body, tokenFile) || strings.Contains(body, projectID) {
			t.Fatal("import response disclosed adjustment values")
		}
		return response
	}
	previewForm := url.Values{"name": {"moved"}, "source": {source}, "action": {"preview"}}
	previewForm.Set("patch", "relative.json")
	assertWebPage(t, post(previewForm), http.StatusBadRequest, "JSON 调整文件的绝对路径",
		`name="name" value="moved"`, `name="source" value="`+source+`"`, `name="patch" value="relative.json"`)
	previewForm["patch"] = []string{"", ""}
	assertWebPage(t, post(previewForm), http.StatusBadRequest, "操作参数无效")
	separator := string(filepath.Separator)
	previewForm.Set("patch", dir+separator+"."+separator+filepath.Base(patch))
	preview := func() url.Values {
		t.Helper()
		response := post(previewForm)
		assertWebPage(t, response, http.StatusOK, "<code>"+patch+"</code>",
			"<li><code>mcp.tokenFile</code></li>", "<li><code>routes.work.projectId</code></li>",
			`type="hidden" name="patch" value="`+patch+`"`)
		assertProfileLifecycleFileMissing(t, registry.root)
		return url.Values{"name": {"moved"}, "source": {source}, "patch": {patch}, "action": {"confirm"},
			"review": {credentialWebToken(t, response)}, "confirm": {"yes"}}
	}
	form := preview()
	tampered := maps.Clone(form)
	tampered.Set("patch", filepath.Join(dir, "different.json"))
	assertWebPage(t, post(tampered), http.StatusConflict, "预览已失效")
	assertWebPage(t, post(form), http.StatusConflict, "预览已失效")
	assertProfileLifecycleFileMissing(t, registry.root)

	form = preview()
	changeProfileLifecycleRevision(t, patch)
	assertWebPage(t, post(form), http.StatusConflict, errProfileBundleChanged.Error(), `name="patch" value="`+patch+`"`)
	assertWebPage(t, post(form), http.StatusConflict, "预览已失效")
	assertProfileLifecycleFileMissing(t, registry.root)

	form = preview()
	assertWebPage(t, post(form), http.StatusOK, "已创建命名配置", `href="/?source=profile-moved"`)
	assertWebPage(t, requestProfileLifecycle(handler, "/?source=profile-moved", nil, ""), http.StatusOK, "命名配置 · moved")
	created, err := bridge.LoadConfig(filepath.Join(registry.root, "profiles", "moved", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if created.MCP.TokenFile != tokenFile || created.Routes["work"].ProjectID != projectID {
		t.Fatal("target configuration did not apply the reviewed adjustments")
	}
}
