package bridge

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/scene/registration"
	application "github.com/larksuite/oapi-sdk-go/v3/service/application/v6"
)

// GroupMessageScope describes the app-identity permission to receive group messages.
type GroupMessageScope string

const (
	GroupMessageScopeUnknown GroupMessageScope = "unknown"
	GroupMessageScopeGranted GroupMessageScope = "granted"
	GroupMessageScopeMissing GroupMessageScope = "missing"

	groupMessageScopeName       = "im:message.group_msg"
	groupMessageGrantMaxSeconds = 600 // SDK default QR lifetime.
	groupMessageEventName       = "im.message.receive_v1"
)

func (l *Lark) groupMessageScope(ctx context.Context) GroupMessageScope {
	if l == nil || l.Channel == nil || l.Config.AppID == "" {
		return GroupMessageScopeUnknown
	}
	r := getFeishuApplication(ctx, l.Channel.RawClient(), l.Config.AppID)
	if r == nil {
		return GroupMessageScopeUnknown
	}
	return groupMessageScopeFromApp(r.Data.App, l.Config.AppID)
}

func groupMessageScopeFromApp(app *application.Application, appID string) GroupMessageScope {
	// An explicit empty scopes array is a complete list.
	if app.Scopes == nil || (app.AppId != nil && *app.AppId != appID) {
		return GroupMessageScopeUnknown
	}
	status := GroupMessageScopeMissing
	for _, scope := range app.Scopes {
		if scope == nil || value(scope.Scope) == "" {
			status = GroupMessageScopeUnknown
			continue
		}
		if *scope.Scope == groupMessageScopeName {
			if slices.Contains(scope.TokenTypes, "tenant") {
				return GroupMessageScopeGranted
			}
			if len(scope.TokenTypes) == 0 || slices.Contains(scope.TokenTypes, "") {
				status = GroupMessageScopeUnknown
			}
		}
	}
	return status
}

type groupMessagePlatformStatus struct {
	scope GroupMessageScope
	event string
}

func unknownGroupMessagePlatformStatus(includeEvent bool) groupMessagePlatformStatus {
	status := groupMessagePlatformStatus{scope: GroupMessageScopeUnknown}
	if includeEvent {
		status.event = "unknown"
	}
	return status
}

func (l *Lark) groupMessagePlatformStatus(ctx context.Context, includeEvent bool) groupMessagePlatformStatus {
	status := unknownGroupMessagePlatformStatus(includeEvent)
	// Read the app and its published version within the original permission probe budget.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client, appID := l.Channel.RawClient(), l.Config.AppID
	r := getFeishuApplication(ctx, client, appID)
	if r == nil {
		return status
	}
	app := r.Data.App
	status.scope = groupMessageScopeFromApp(app, appID)
	if !includeEvent || value(app.AppId) != appID || app.OnlineVersionId == nil || !feishuApplicationResponseOK(r.ApiResp) {
		return status
	}
	if *app.OnlineVersionId == "" {
		status.event = "unpublished"
	} else if version := readPublishedFeishuVersion(ctx, client, appID, *app.OnlineVersionId); version != nil {
		status.event = feishuVersionEventState(version, groupMessageEventName)
	}
	return status
}

func getFeishuApplication(ctx context.Context, client *lark.Client, appID string, opts ...larkcore.RequestOptionFunc) *application.GetApplicationResp {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req := application.NewGetApplicationReqBuilder().AppId(appID).Lang("zh_cn").UserIdType("open_id").Build()
	r, err := client.Application.V6.Application.Get(ctx, req, opts...)
	if err != nil || r == nil || r.StatusCode != http.StatusOK || !r.Success() || r.Data == nil || r.Data.App == nil {
		return nil
	}
	return r
}

type groupMessageGrant struct {
	URL       string
	ExpiresAt time.Time
}

func (l *Lark) beginGroupMessageGrant(ctx context.Context) (groupMessageGrant, error) {
	return l.beginGroupMessageGrantWith(ctx, registration.RegisterApp)
}

func (l *Lark) beginGroupMessageGrantWith(ctx context.Context, register func(context.Context, *registration.Options) (*registration.RegisterAppResult, error)) (groupMessageGrant, error) {
	grantErr := errors.New("无法生成群消息权限授权链接")
	if !userGroupsAppID.MatchString(l.Config.AppID) {
		return groupMessageGrant{}, grantErr
	}
	var accountsHost, openHost string
	switch l.Config.Tenant {
	case "", "feishu":
		accountsHost, openHost = "accounts.feishu.cn", "open.feishu.cn"
	case "lark":
		accountsHost, openHost = "accounts.larksuite.com", "open.larksuite.com"
	default:
		return groupMessageGrant{}, grantErr
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var grant groupMessageGrant
	preset := false
	_, _ = register(ctx, &registration.Options{
		Source: "t3-bridge",
		Domain: "https://" + accountsHost,
		AppID:  l.Config.AppID,
		Addons: &registration.AppAddons{
			Preset: &preset,
			Scopes: registration.AppAddonsScopes{Tenant: []string{groupMessageScopeName}},
		},
		OnQRCode: func(info *registration.QRCodeInfo) {
			// RegisterApp invokes the callback synchronously before polling for credentials.
			defer cancel()
			if ctx.Err() != nil || info == nil || info.ExpireIn <= 0 || info.ExpireIn > groupMessageGrantMaxSeconds {
				return
			}
			if validGroupMessageGrantURL(info.URL, l.Config.AppID, accountsHost, openHost) {
				grant = groupMessageGrant{URL: info.URL, ExpiresAt: time.Now().Add(time.Duration(info.ExpireIn) * time.Second)}
			}
		},
	})
	if grant.URL == "" {
		return groupMessageGrant{}, grantErr
	}
	return grant, nil
}

func validGroupMessageGrantURL(rawURL, appID, accountsHost, openHost string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" {
		return false
	}
	if host := strings.ToLower(u.Host); host != accountsHost && host != openHost {
		return false
	}
	query, err := url.ParseQuery(u.RawQuery)
	ids := query["clientID"]
	return err == nil && len(ids) == 1 && ids[0] == appID
}
