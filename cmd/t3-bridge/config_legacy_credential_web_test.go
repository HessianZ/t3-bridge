package main

import (
	"context"
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
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

const legacyWebFixture = `{
	"schemaVersion":2, "activeProfile":"Team.Work",
	"profiles":{"Team.Work":{
		"schemaVersion":2,
		"accounts":{"app":{"id":"cli_imported","secret":"synthetic_source_secret_marker"}},
		"access":{"allowedUsers":["ou_imported"]}
	}}
}`

const legacyWebInlineSecret = "synthetic_inline_secret_marker"
const legacyWebInlineFixture = `{
	"schemaVersion":1,
	"app":{"id":"cli_imported","tenant":"lark","secret":"` + legacyWebInlineSecret + `"},
	"preferences":{"access":{"allowedUsers":["ou_imported"]}}
}`

type legacyCredentialWebFixture struct {
	form    url.Values
	handler http.Handler
	config  string
	legacy  *legacyCredentialWeb
}

func newLegacyCredentialWebFixture(t *testing.T, legacy *legacyCredentialWeb) legacyCredentialWebFixture {
	t.Helper()
	dir := t.TempDir()
	old := filepath.Join(dir, "old.json")
	path := filepath.Join(dir, "target.json")
	if err := os.WriteFile(old, []byte(legacyWebFixture), 0600); err != nil {
		t.Fatal(err)
	}
	sources, err := configWebSources(path)
	if err != nil {
		t.Fatal(err)
	}
	profiles := profileRegistry{}
	return legacyCredentialWebFixture{
		form: url.Values{
			"action":              {"preview-legacy-credential"},
			"revision":            {"missing"},
			"config":              {string(sources[0].JSON)},
			"edits":               {`[{"path":["routes","work","users"],"value":["ou_route"]},{"path":["routes","work","model"],"value":"draft-model"},{"path":["receiverAuthorized"],"value":true}]`},
			"legacySourcePath":    {old},
			"legacyStateDir":      {filepath.Join(dir, "new-state")},
			"legacyAppSecretFile": {filepath.Join(dir, "credentials", "private.secret")},
		},
		config: path,
		legacy: legacy,
		handler: configWorkspaceHandlerWithLegacy(webTestHost, sources, profiles,
			configRuntimeHandler(profiles, readHostServiceInventory), legacy),
	}
}

func (fixture legacyCredentialWebFixture) post(action string, address, origin string) *httptest.ResponseRecorder {
	fixture.form.Set("action", action)
	req := httptest.NewRequest(http.MethodPost, address, strings.NewReader(fixture.form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	fixture.handler.ServeHTTP(w, req)
	return w
}

func (fixture legacyCredentialWebFixture) submit(action string) *httptest.ResponseRecorder {
	return fixture.post(action, "http://"+webTestHost+"/?source=file", "http://"+webTestHost)
}

var legacyWebToken = regexp.MustCompile(`name="legacyReview" value="([A-Z2-7]+)"`)
var legacyWebCandidate = regexp.MustCompile(`(?s)<textarea [^>]*name="config"[^>]*>\n(.*?)</textarea>`)

func legacyWebPageField(t *testing.T, page *httptest.ResponseRecorder, field *regexp.Regexp) string {
	t.Helper()
	match := field.FindStringSubmatch(page.Body.String())
	if match == nil {
		t.Fatalf("missing form field %s in status %d", field, page.Code)
	}
	return html.UnescapeString(match[1])
}

func (fixture legacyCredentialWebFixture) preview(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	page := fixture.submit("preview-legacy-credential")
	assertWebPage(t, page, http.StatusOK, "与来源的差异", "确认复制 App Secret", "Team.Work", "cli_imported")
	fixture.form.Set("legacyReview", legacyWebPageField(t, page, legacyWebToken))
	fixture.form.Set("config", legacyWebPageField(t, page, legacyWebCandidate))
	fixture.form.Del("edits")
	return page
}

func assertLegacyWebNoWrite(t *testing.T, fixture legacyCredentialWebFixture) {
	t.Helper()
	for _, path := range []string{fixture.config, fixture.form.Get("legacyAppSecretFile"), fixture.form.Get("legacyStateDir")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unexpected change at %s: %v", path, err)
		}
	}
}

func TestConfigWebLegacyCredentialCopyAndRetry(t *testing.T) {
	const secret = "synthetic_provider_secret_marker"
	provider := filepath.Join(t.TempDir(), "synthetic-provider")
	legacy := &legacyCredentialWeb{provider: provider}
	fixture := newLegacyCredentialWebFixture(t, legacy)
	var calls int
	legacy.copy = func(_ context.Context, plan legacyCredentialPlan, executable string, candidate []byte) error {
		calls++
		if executable != provider || plan.SourcePath != fixture.form.Get("legacySourcePath") ||
			plan.Profile != "Team.Work" || plan.AppID != "cli_imported" || plan.Destination != fixture.form.Get("legacyAppSecretFile") ||
			string(candidate) != fixture.form.Get("config") {
			t.Fatal("copy did not receive the host executable and reviewed plan and candidate")
		}
		if calls == 1 {
			return errors.New("合成复制失败，可重试")
		}
		return writeAppSecret(plan.Destination, secret)
	}
	preview := fixture.preview(t)
	if preview.Header().Get("Referrer-Policy") != "same-origin" || calls != 0 {
		t.Fatal("preview did not preserve native form Origin behavior or attempted a copy")
	}
	assertLegacyWebNoWrite(t, fixture)
	legacyPath := fixture.form.Get("legacySourcePath")
	fixture.form.Set("legacySourcePath", "")
	assertWebPage(t, fixture.submit("confirm-legacy-credential"), http.StatusUnprocessableEntity,
		"导入表单已变化", `name="legacySourcePath" value=""`)
	fixture.form.Set("legacySourcePath", legacyPath)
	fixture.form.Set("legacyBridge", filepath.Join(t.TempDir(), "browser-supplied-executable"))
	failure := fixture.submit("confirm-legacy-credential")
	assertWebPage(t, failure, http.StatusUnprocessableEntity, "合成复制失败，可重试", "确认复制 App Secret")
	if legacyWebPageField(t, failure, legacyWebToken) != fixture.form.Get("legacyReview") ||
		legacyWebPageField(t, failure, legacyWebCandidate) != fixture.form.Get("config") {
		t.Fatal("copy failure discarded the draft or review")
	}
	assertLegacyWebNoWrite(t, fixture)
	copied := fixture.submit("confirm-legacy-credential")
	assertWebPage(t, copied, http.StatusOK, "App Secret 已复制", `name="legacySourcePath" value="`+legacyPath+`"`,
		`name="legacyStateDir" value="`+fixture.form.Get("legacyStateDir")+`"`,
		`name="legacyAppSecretFile" value="`+fixture.form.Get("legacyAppSecretFile")+`"`)
	candidate := legacyWebPageField(t, copied, legacyWebCandidate)
	parsed, err := bridge.ParseConfig([]byte(candidate))
	if err != nil || parsed.AppID != "cli_imported" || parsed.AppSecretFile != fixture.form.Get("legacyAppSecretFile") ||
		parsed.ReceiverAuthorized || parsed.Routes["work"].Model != "draft-model" || candidate != fixture.form.Get("config") {
		t.Fatalf("copy changed the reviewed candidate: %v", err)
	}
	for _, page := range []*httptest.ResponseRecorder{preview, failure, copied} {
		for _, marker := range []string{secret, "synthetic_source_secret_marker", provider} {
			if strings.Contains(page.Body.String(), marker) {
				t.Fatalf("credential page exposed %q", marker)
			}
		}
	}
	if legacyWebToken.MatchString(copied.Body.String()) {
		t.Fatal("successful copy rendered a reusable review")
	}
	assertWebPage(t, fixture.submit("confirm-legacy-credential"), http.StatusUnprocessableEntity, "请先预览")
	if _, err := os.Stat(fixture.config); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("copy saved the config: %v", err)
	}
	if got, err := os.ReadFile(parsed.AppSecretFile); err != nil || string(got) != secret+"\n" || calls != 2 {
		t.Fatalf("copy wrote unexpected content or was invoked %d times: %v", calls, err)
	}
}

func TestConfigWebLegacyCredentialCopiesSingleAccountLiteralWithoutProvider(t *testing.T) {
	fixture := newLegacyCredentialWebFixture(t, &legacyCredentialWeb{})
	if err := os.WriteFile(fixture.form.Get("legacySourcePath"), []byte(legacyWebInlineFixture), 0600); err != nil {
		t.Fatal(err)
	}
	page := httptest.NewRecorder()
	fixture.handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "http://"+webTestHost+"/?source=file", nil))
	assertWebPage(t, page, http.StatusOK, `value="preview-legacy-credential"`)
	if page.Header().Get("Referrer-Policy") != "same-origin" {
		t.Fatal("single-account copy form requires a non-opaque Origin header")
	}
	preview := fixture.submit("preview-legacy-credential")
	assertWebPage(t, preview, http.StatusOK, "与来源的差异", "确认复制 App Secret", "单账号配置", "cli_imported", `name="legacyProfile" value=""`)
	assertLegacyWebNoWrite(t, fixture)
	candidate := legacyWebPageField(t, preview, legacyWebCandidate)
	destination := fixture.form.Get("legacyAppSecretFile")
	parsed, err := bridge.ParseConfig([]byte(candidate))
	if err != nil || parsed.AppID != "cli_imported" || parsed.Tenant != "lark" || parsed.AppSecretFile != destination ||
		parsed.ReceiverAuthorized || parsed.Routes["work"].Model != "draft-model" {
		t.Fatalf("preview did not preserve the import and draft edits: %v", err)
	}
	fixture.form.Set("legacyReview", legacyWebPageField(t, preview, legacyWebToken))
	fixture.form.Set("config", candidate)
	fixture.form.Del("edits")
	copied := fixture.submit("confirm-legacy-credential")
	assertWebPage(t, copied, http.StatusOK, "App Secret 已复制", destination)
	if got, err := bridge.Secret(destination); err != nil || got != legacyWebInlineSecret {
		t.Fatalf("confirmation did not copy the expected private credential: %v", err)
	}
	if legacyWebPageField(t, copied, legacyWebCandidate) != candidate ||
		strings.Contains(preview.Body.String(), legacyWebInlineSecret) || strings.Contains(copied.Body.String(), legacyWebInlineSecret) {
		t.Fatal("credential copy changed the reviewed draft or exposed the secret")
	}
	for _, path := range []string{fixture.config, fixture.form.Get("legacyStateDir")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("copy touched %s: %v", path, err)
		}
	}
}

func TestConfigWebLegacyCredentialReferenceAndCancel(t *testing.T) {
	for _, test := range []struct{ name, source, message string }{
		{"V2 without a provider", legacyWebFixture, "找不到可用的旧桥程序"},
		{"single account with a credential reference", strings.Replace(legacyWebInlineFixture, legacyWebInlineSecret, "${SYNTHETIC_LEGACY_SECRET}", 1), "未保存可直接复制的 App Secret"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLegacyCredentialWebFixture(t, &legacyCredentialWeb{})
			if err := os.WriteFile(fixture.form.Get("legacySourcePath"), []byte(test.source), 0600); err != nil {
				t.Fatal(err)
			}
			preview := fixture.submit("preview-legacy-credential")
			assertWebPage(t, preview, http.StatusUnprocessableEntity, test.message, "已有私有凭据文件", `value="import-legacy"`, "draft-model")
			if legacyWebToken.MatchString(preview.Body.String()) {
				t.Fatal("rejected preview offered a confirmation")
			}
			fixture.form.Set("config", legacyWebPageField(t, preview, legacyWebCandidate))
			fixture.form.Del("edits")
			imported := fixture.submit("import-legacy")
			assertWebPage(t, imported, http.StatusOK, "与来源的差异", "cli_imported", "draft-model", fixture.form.Get("legacyAppSecretFile"))
			assertLegacyWebNoWrite(t, fixture)
		})
	}
	t.Run("cancel preserves the current draft", func(t *testing.T) {
		fixture := newLegacyCredentialWebFixture(t, &legacyCredentialWeb{provider: "synthetic-provider"})
		assertWebPage(t, fixture.submit("confirm-legacy-credential"), http.StatusUnprocessableEntity, "请先预览")
		fixture.preview(t)
		fixture.form.Set("edits", `[{"path":["routes","work","model"],"value":"edited-after-preview"}]`)
		cancel := fixture.submit("cancel-legacy-credential")
		assertWebPage(t, cancel, http.StatusOK, "cli_imported", "edited-after-preview")
		if legacyWebToken.MatchString(cancel.Body.String()) {
			t.Fatal("cancel kept the review on the page")
		}
		fixture.form.Del("edits")
		assertWebPage(t, fixture.submit("confirm-legacy-credential"), http.StatusUnprocessableEntity, "请先预览")
		assertLegacyWebNoWrite(t, fixture)
	})
	t.Run("preview again after cancel", func(t *testing.T) {
		fixture := newLegacyCredentialWebFixture(t, &legacyCredentialWeb{provider: "synthetic-provider"})
		fixture.preview(t)
		oldReview := fixture.form.Get("legacyReview")
		assertWebPage(t, fixture.submit("cancel-legacy-credential"), http.StatusOK, "cli_imported")
		fixture.form.Del("legacyReview")
		fixture.preview(t)
		if fixture.form.Get("legacyReview") == oldReview {
			t.Fatal("preview reused the canceled review")
		}
		assertLegacyWebNoWrite(t, fixture)
	})
}

func TestConfigWebLegacyCredentialRejectsStaleApproval(t *testing.T) {
	for _, test := range []struct {
		name, message string
		change        func(*testing.T, legacyCredentialWebFixture)
	}{
		{"draft", "候选配置已变化", func(_ *testing.T, fixture legacyCredentialWebFixture) {
			fixture.form.Set("edits", `[{"path":["routes","work","model"],"value":"changed"}]`)
		}},
		{"import fields", "导入表单已变化", func(_ *testing.T, fixture legacyCredentialWebFixture) {
			fixture.form.Set("legacyProfile", "other")
		}},
		{"posted revision", "所选配置来源或版本已变化", func(_ *testing.T, fixture legacyCredentialWebFixture) {
			fixture.form.Set("revision", "stale")
		}},
		{"saved revision", "所选配置来源或版本已变化", func(t *testing.T, fixture legacyCredentialWebFixture) {
			if err := os.WriteFile(fixture.config, []byte(fixture.form.Get("config")), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"expired", "请先预览", func(_ *testing.T, fixture legacyCredentialWebFixture) {
			fixture.legacy.pending.expires = time.Now().Add(-time.Second)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLegacyCredentialWebFixture(t, &legacyCredentialWeb{provider: "synthetic-provider"})
			fixture.preview(t)
			test.change(t, fixture)
			assertWebPage(t, fixture.submit("confirm-legacy-credential"), http.StatusUnprocessableEntity, test.message)
			if _, err := os.Stat(fixture.form.Get("legacyAppSecretFile")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("stale confirmation touched the destination: %v", err)
			}
		})
	}
	t.Run("legacy source drift", func(t *testing.T) {
		provider := filepath.Join(t.TempDir(), "nonexistent-provider")
		fixture := newLegacyCredentialWebFixture(t, &legacyCredentialWeb{provider: provider, copy: func(ctx context.Context, plan legacyCredentialPlan, binary string, candidate []byte) error {
			return plan.copy(ctx, binary, candidate)
		}})
		fixture.preview(t)
		oldReview := fixture.form.Get("legacyReview")
		if err := os.WriteFile(fixture.form.Get("legacySourcePath"), []byte(legacyWebFixture+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		assertWebPage(t, fixture.submit("confirm-legacy-credential"), http.StatusUnprocessableEntity, "旧配置内容已变化")
		fixture.preview(t)
		if fixture.form.Get("legacyReview") == oldReview {
			t.Fatal("refresh kept the stale review")
		}
		fixture.form.Set("legacyReview", oldReview)
		assertWebPage(t, fixture.submit("confirm-legacy-credential"), http.StatusUnprocessableEntity, "请先预览")
		assertLegacyWebNoWrite(t, fixture)
	})
}

func TestConfigWebLegacyCredentialConfirmRequiresHostAndOrigin(t *testing.T) {
	fixture := newLegacyCredentialWebFixture(t, &legacyCredentialWeb{provider: "synthetic-provider"})
	fixture.preview(t)
	const origin = "http://" + webTestHost
	const address = origin + "/?source=file"
	for _, test := range []struct{ address, origin string }{
		{address, ""}, {address, "null"}, {address, "http://external.example"},
		{"http://external.example/?source=file", "http://external.example"},
	} {
		assertWebPage(t, fixture.post("confirm-legacy-credential", test.address, test.origin), http.StatusForbidden)
	}
	assertWebPage(t, fixture.post("confirm-legacy-credential", origin+"/?source=generic", origin),
		http.StatusUnprocessableEntity, "所选配置来源或版本已变化")
	assertLegacyWebNoWrite(t, fixture)
}
