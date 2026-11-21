package bridge

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	channel "github.com/larksuite/channel-sdk-go"
	"github.com/larksuite/oapi-sdk-go/v3/scene/registration"
)

const groupMessageFixtureAppID = "cli_groupmessage"

func TestGroupMessageScope(t *testing.T) {
	appResponse := func(scopes string) string {
		return `{"code":0,"data":{"app":{"app_id":"` + groupMessageFixtureAppID + `","scopes":` + scopes + `}}}`
	}
	for _, tc := range []struct {
		name, body string
		want       GroupMessageScope
	}{
		{"tenant", appResponse(`[{"scope":"im:message.group_msg","token_types":["tenant"]}]`), GroupMessageScopeGranted},
		{"both identities", appResponse(`[{"scope":"im:message.group_msg","token_types":["user","tenant"]}]`), GroupMessageScopeGranted},
		{"user only", appResponse(`[{"scope":"im:message.group_msg","token_types":["user"]}]`), GroupMessageScopeMissing},
		{"other scope", appResponse(`[{"scope":"im:message:send_as_bot","token_types":["tenant"]}]`), GroupMessageScopeMissing},
		{"explicit empty list", appResponse(`[]`), GroupMessageScopeMissing},
		{"absent token types", appResponse(`[{"scope":"im:message.group_msg"}]`), GroupMessageScopeUnknown},
		{"empty token types", appResponse(`[{"scope":"im:message.group_msg","token_types":[]}]`), GroupMessageScopeUnknown},
		{"malformed token types", appResponse(`[{"scope":"im:message.group_msg","token_types":[null]}]`), GroupMessageScopeUnknown},
		{"malformed item", appResponse(`[null]`), GroupMessageScopeUnknown},
		{"unknown scope name", appResponse(`[{"token_types":["tenant"]}]`), GroupMessageScopeUnknown},
		{"null list", appResponse(`null`), GroupMessageScopeUnknown},
		{"absent list", `{"code":0,"data":{"app":{}}}`, GroupMessageScopeUnknown},
		{"wrong app", `{"code":0,"data":{"app":{"app_id":"cli_other","scopes":[]}}}`, GroupMessageScopeUnknown},
		{"absent app", `{"code":0,"data":{}}`, GroupMessageScopeUnknown},
		{"API error", `{"code":999,"msg":"synthetic API error"}`, GroupMessageScopeUnknown},
		{"transport error", "", GroupMessageScopeUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queried := false
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				res := httptest.NewRecorder()
				res.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/open-apis/auth/v3/tenant_access_token/internal":
					io.WriteString(res, `{"code":0,"tenant_access_token":"synthetic-token","expire":7200}`)
				case "/open-apis/application/v6/applications/" + groupMessageFixtureAppID:
					queried = true
					query := r.URL.Query()
					if r.Method != http.MethodGet || query.Get("lang") != "zh_cn" || query.Get("user_id_type") != "open_id" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
						t.Fatalf("unexpected application request: %s %s", r.Method, r.URL)
					}
					if deadline, ok := r.Context().Deadline(); !ok || time.Until(deadline) > 5*time.Second {
						t.Fatal("application request must have a five-second deadline")
					}
					if tc.body == "" {
						return nil, errors.New("synthetic transport error")
					}
					io.WriteString(res, tc.body)
				default:
					t.Fatalf("unexpected API request: %s %s", r.Method, r.URL.Path)
				}
				return res.Result(), nil
			})
			ch, err := channel.New(groupMessageFixtureAppID, "synthetic-secret", channel.WithEnableTokenCache(true),
				channel.WithHTTPClient(&http.Client{Transport: transport}))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { ch.Stop(context.Background()) })
			l := &Lark{Channel: ch, Config: Config{AppID: groupMessageFixtureAppID}}
			if got := l.groupMessageScope(context.Background()); !queried || got != tc.want {
				t.Fatalf("queried=%t, got %q, want %q", queried, got, tc.want)
			}
		})
	}
}

func TestBeginGroupMessageGrantOptionsAndCancellation(t *testing.T) {
	for _, tc := range []struct{ tenant, domain, host string }{
		{"", "https://accounts.feishu.cn", "accounts.feishu.cn"},
		{"feishu", "https://accounts.feishu.cn", "open.feishu.cn"},
		{"lark", "https://accounts.larksuite.com", "accounts.larksuite.com"},
		{"lark", "https://accounts.larksuite.com", "open.larksuite.com"},
	} {
		t.Run(tc.host, func(t *testing.T) {
			l := &Lark{Config: Config{AppID: groupMessageFixtureAppID, Tenant: tc.tenant}}
			target := "https://" + tc.host + "/page/launcher?clientID=" + groupMessageFixtureAppID
			before := time.Now()
			grant, err := l.beginGroupMessageGrantWith(context.Background(), func(ctx context.Context, opts *registration.Options) (*registration.RegisterAppResult, error) {
				if opts.AppID != groupMessageFixtureAppID || opts.Domain != tc.domain || opts.CreateOnly || opts.AppPreset != nil {
					t.Fatalf("unexpected registration options: %+v", opts)
				}
				addons := opts.Addons
				if addons == nil || addons.Preset == nil || *addons.Preset ||
					!slices.Equal(addons.Scopes.Tenant, []string{"im:message.group_msg"}) || len(addons.Scopes.User) != 0 ||
					len(addons.Events.Items.Tenant) != 0 || len(addons.Events.Items.User) != 0 || len(addons.Callbacks.Items) != 0 {
					t.Fatalf("unexpected registration addons: %+v", addons)
				}
				if err := ctx.Err(); err != nil {
					t.Fatalf("registration canceled before callback: %v", err)
				}
				opts.OnQRCode(&registration.QRCodeInfo{URL: target, ExpireIn: 600})
				if ctx.Err() != context.Canceled {
					t.Fatal("poll context must be canceled before the callback returns")
				}
				return nil, ctx.Err()
			})
			if err != nil || grant.URL != target || grant.ExpiresAt.Before(before.Add(10*time.Minute)) || grant.ExpiresAt.After(time.Now().Add(10*time.Minute)) {
				t.Fatalf("unexpected grant: %+v (%v)", grant, err)
			}
		})
	}
}

func TestBeginGroupMessageGrantRejectsUnexpectedCallback(t *testing.T) {
	const origin = "https://accounts.feishu.cn/page/launcher"
	const query = "?clientID=" + groupMessageFixtureAppID
	for _, tc := range []struct {
		name, target string
		expireIn     int
	}{
		{"http", "http://accounts.feishu.cn/page/launcher" + query, 600},
		{"lookalike host", "https://accounts.feishu.cn.example.org/page/launcher" + query, 600},
		{"userinfo", "https://example.org@accounts.feishu.cn/page/launcher" + query, 600},
		{"port", "https://accounts.feishu.cn:8443/page/launcher" + query, 600},
		{"wrong tenant", "https://accounts.larksuite.com/page/launcher" + query, 600},
		{"missing clientID", origin, 600},
		{"wrong clientID", origin + "?clientID=cli_other", 600},
		{"duplicate clientID", origin + query + "&clientID=cli_other", 600},
		{"malformed query", origin + query + "&ticket=%zz", 600},
		{"no expiry", origin + query, 0},
		{"excessive expiry", origin + query, 601},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := &Lark{Config: Config{AppID: groupMessageFixtureAppID, Tenant: "feishu"}}
			grant, err := l.beginGroupMessageGrantWith(context.Background(), func(ctx context.Context, opts *registration.Options) (*registration.RegisterAppResult, error) {
				opts.OnQRCode(&registration.QRCodeInfo{URL: tc.target, ExpireIn: tc.expireIn})
				if ctx.Err() != context.Canceled {
					t.Fatal("poll context must be canceled for rejected callbacks")
				}
				return nil, ctx.Err()
			})
			if err == nil || grant != (groupMessageGrant{}) {
				t.Fatalf("unexpected grant: %+v (%v)", grant, err)
			}
		})
	}
}

func TestBeginGroupMessageGrantRejectsInvalidAppAndSanitizesSDKError(t *testing.T) {
	for _, appID := range []string{"", "cli_example&clientID=cli_other", groupMessageFixtureAppID} {
		l := &Lark{Config: Config{AppID: appID}}
		called := false
		grant, err := l.beginGroupMessageGrantWith(context.Background(), func(context.Context, *registration.Options) (*registration.RegisterAppResult, error) {
			called = true
			return nil, errors.New("synthetic private SDK detail")
		})
		if called != (appID == groupMessageFixtureAppID) || grant != (groupMessageGrant{}) || err == nil || err.Error() != "无法生成群消息权限授权链接" {
			t.Fatalf("app %q, called=%t, grant=%+v, error=%v", appID, called, grant, err)
		}
	}
}
