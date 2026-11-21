package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	im "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// CheckFeishuApp reads the bot identity without opening the long connection.
func CheckFeishuApp(ctx context.Context, appID, secretFile, tenant string) error {
	if err := validateFeishuAppConnection(appID, secretFile, tenant); err != nil {
		return err
	}
	secret, err := Secret(secretFile)
	if err != nil {
		return err
	}
	// Channel.GetBotIdentity logs to stdout, including when the query succeeds.
	// Use the same SDK request directly so config commands can emit clean JSON.
	client := newFeishuProbeClient(appID, secret, tenant, privateHTTP(10*time.Second))
	return checkFeishuAppIdentity(ctx, client, appID, secret)
}

func newFeishuProbeClient(appID, secret, tenant string, httpClient larkcore.HttpClient) *lark.Client {
	domain := lark.FeishuBaseUrl
	if tenant == "lark" {
		domain = lark.LarkBaseUrl
	}
	return lark.NewClient(appID, secret, lark.WithOpenBaseUrl(domain), lark.WithEnableTokenCache(false),
		lark.WithHttpClient(httpClient), lark.WithLogger(feishuProbeLogger{}))
}

func checkFeishuAppIdentity(ctx context.Context, client *lark.Client, appID, secret string) error {
	token, err := feishuAppTenantToken(ctx, client, appID, secret)
	if err != nil {
		return err
	}
	resp, err := client.Get(ctx, "/open-apis/bot/v3/info", nil, larkcore.AccessTokenTypeTenant, larkcore.WithTenantAccessToken(token))
	var result struct {
		Code *int `json:"code"`
		Bot  struct {
			OpenID string `json:"open_id"`
		} `json:"bot"`
	}
	if err != nil || resp == nil || resp.StatusCode != http.StatusOK ||
		json.Unmarshal(resp.RawBody, &result) != nil || result.Code == nil || *result.Code != 0 || result.Bot.OpenID == "" {
		return errors.New("应用 API 核验失败。请检查租户、应用、密钥、机器人能力和网络")
	}
	return nil
}

func feishuAppTenantToken(ctx context.Context, client *lark.Client, appID, secret string) (string, error) {
	// Always verify the current secret. The SDK cache is keyed by App ID and can
	// otherwise mask a bad secret after reconfiguring an existing app.
	body := larkcore.SelfBuiltAppAccessTokenReq{AppID: appID, AppSecret: secret}
	resp, err := client.Post(ctx, larkcore.TenantAccessTokenInternalUrlPath, body, larkcore.AccessTokenTypeNone)
	var token struct {
		Code  *int   `json:"code"`
		Value string `json:"tenant_access_token"`
	}
	if err != nil || resp == nil || resp.StatusCode != http.StatusOK ||
		json.Unmarshal(resp.RawBody, &token) != nil || token.Code == nil || *token.Code != 0 || token.Value == "" {
		return "", errors.New("无法取得应用令牌。请检查租户、应用 ID、密钥和网络")
	}
	return token.Value, nil
}

type feishuProbeLogger struct{}

func (feishuProbeLogger) Debug(context.Context, ...any) {}
func (feishuProbeLogger) Info(context.Context, ...any)  {}
func (feishuProbeLogger) Warn(context.Context, ...any)  {}
func (feishuProbeLogger) Error(context.Context, ...any) {}

type FeishuGroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListFeishuGroups reads the bot's joined groups without opening the long connection.
func ListFeishuGroups(ctx context.Context, appID, secretFile, tenant string) ([]FeishuGroup, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client, tenantAuth, err := feishuAppReadClient(ctx, appID, secretFile, tenant)
	if err != nil {
		return nil, err
	}
	return listFeishuGroups(ctx, client, tenantAuth)
}

// Saved-config probes use HTTP only. The Channel SDK initializes an event
// dispatcher that logs to stdout even before the long connection is started.
func feishuAppReadClient(ctx context.Context, appID, secretFile, tenant string) (*lark.Client, larkcore.RequestOptionFunc, error) {
	if err := validateFeishuAppConnection(appID, secretFile, tenant); err != nil {
		return nil, nil, err
	}
	secret, err := Secret(secretFile)
	if err != nil {
		return nil, nil, err
	}
	client := newFeishuProbeClient(appID, secret, tenant, privateHTTP(5*time.Second))
	token, err := feishuAppTenantToken(ctx, client, appID, secret)
	if err != nil {
		return nil, nil, err
	}
	return client, larkcore.WithTenantAccessToken(token), nil
}

func validateFeishuAppConnection(appID, secretFile, tenant string) error {
	if appID == "" || !filepath.IsAbs(secretFile) || (tenant != "" && tenant != "feishu" && tenant != "lark") {
		return errors.New("请填写应用 ID、密钥文件的绝对路径，并选择飞书或 Lark")
	}
	return nil
}

func listFeishuGroups(ctx context.Context, client *lark.Client, options ...larkcore.RequestOptionFunc) ([]FeishuGroup, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	list := client.Im.V1.Chat.List
	groups := []FeishuGroup{}
	token := ""
	seenGroups := map[string]bool{}
	seenTokens := map[string]bool{}
	for page := 0; page < 10; page++ {
		r, err := list(ctx, im.NewListChatReqBuilder().UserIdType("open_id").PageSize(100).PageToken(token).Build(), options...)
		if err != nil || r == nil || !r.Success() || r.Data == nil {
			return nil, errors.New("cannot list joined groups")
		}
		for _, chat := range r.Data.Items {
			if chat == nil || value(chat.ChatId) == "" {
				return nil, errors.New("invalid joined group")
			}
			id := *chat.ChatId
			if !seenGroups[id] {
				seenGroups[id] = true
				groups = append(groups, FeishuGroup{ID: id, Name: value(chat.Name)})
			}
		}
		if r.Data.HasMore == nil || !*r.Data.HasMore {
			return groups, nil
		}
		token = value(r.Data.PageToken)
		if token == "" || seenTokens[token] {
			break
		}
		seenTokens[token] = true
	}
	return nil, errors.New("joined groups pagination outside bounds")
}
