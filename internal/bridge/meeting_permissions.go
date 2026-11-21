package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	application "github.com/larksuite/oapi-sdk-go/v3/service/application/v6"
)

type MeetingPermissionReport struct {
	AppID              string                   `json:"appId"`
	Tenant             string                   `json:"tenant"`
	Enabled            bool                     `json:"enabled"`
	Scopes             []MeetingPermissionScope `json:"scopes"`
	Events             []string                 `json:"events"`
	PublishedVersionID string                   `json:"publishedVersionId,omitempty"`
	PublicationState   string                   `json:"publicationState"`
	EventStates        map[string]string        `json:"eventStates"`
	Availability       string                   `json:"availability,omitempty"`
}

type MeetingPermissionScope struct {
	Scope            string `json:"scope"`
	State            string `json:"state"`
	AuthorizationURL string `json:"authorizationUrl,omitempty"`
}

var feishuPublishedVersionID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// CheckMeetingPermissions checks tenant scopes and published subscriptions independently of receiver state.
func CheckMeetingPermissions(ctx context.Context, cfg Config) (MeetingPermissionReport, error) {
	return checkMeetingPermissions(ctx, cfg, privateHTTP(10*time.Second))
}

func checkMeetingPermissions(ctx context.Context, cfg Config, httpClient larkcore.HttpClient) (MeetingPermissionReport, error) {
	return probeMeetingPermissions(ctx, cfg, httpClient, false)
}

func probeMeetingPermissions(ctx context.Context, cfg Config, httpClient larkcore.HttpClient, includeAvailability bool) (MeetingPermissionReport, error) {
	if err := validateFeishuAppConnection(cfg.AppID, cfg.AppSecretFile, cfg.Tenant); err != nil {
		return MeetingPermissionReport{}, err
	}
	secret, err := Secret(cfg.AppSecretFile)
	if err != nil {
		return MeetingPermissionReport{}, err
	}
	tenant := cfg.Tenant
	if tenant == "" {
		tenant = "feishu"
	}
	client := newFeishuProbeClient(cfg.AppID, secret, tenant, httpClient)
	token, err := feishuAppTenantToken(ctx, client, cfg.AppID, secret)
	if err != nil {
		return MeetingPermissionReport{}, err
	}

	report := MeetingPermissionReport{
		AppID: cfg.AppID, Tenant: tenant, Enabled: cfg.MeetingEnabled,
		Scopes: []MeetingPermissionScope{
			{Scope: "vc:meeting.bot.join:write", State: "unknown"},
			{Scope: "vc:meeting.meetingevent:read", State: "unknown"},
			{Scope: "vc:meeting.message:write", State: "unknown"},
		},
		Events: []string{
			"vc.bot.meeting_invited_v1",
			"vc.bot.meeting_activity_v1",
			"vc.bot.meeting_ended_v1",
		},
		PublicationState: "unknown",
		EventStates:      make(map[string]string, 3),
	}
	if includeAvailability {
		report.Availability = "unknown"
	}
	for _, event := range report.Events {
		report.EventStates[event] = "unknown"
	}
	auth := larkcore.WithTenantAccessToken(token)
	r := getFeishuApplication(ctx, client, cfg.AppID, auth)
	if r == nil || value(r.Data.App.AppId) != cfg.AppID || !feishuApplicationResponseOK(r.ApiResp) {
		return report, nil
	}
	app := r.Data.App
	if app.Scopes != nil {
		for i := range report.Scopes {
			permission := &report.Scopes[i]
			permission.State = meetingTenantScopeState(app.Scopes, permission.Scope)
			if permission.State == "missing" {
				permission.AuthorizationURL = meetingAuthorizationURL(tenant, cfg.AppID, permission.Scope)
			}
		}
	}
	report.checkPublishedMeetingVersion(ctx, client, app.OnlineVersionId, auth)
	if includeAvailability {
		report.Availability = readMeetingAvailability(ctx, tenant, token, app.Owner, httpClient)
	}
	return report, nil
}

func (report *MeetingPermissionReport) checkPublishedMeetingVersion(ctx context.Context, client *lark.Client, onlineVersionID *string, auth larkcore.RequestOptionFunc) {
	if onlineVersionID == nil {
		return
	}
	versionID := *onlineVersionID
	if versionID == "" {
		report.PublicationState = "unpublished"
		return
	}
	version := readPublishedFeishuVersion(ctx, client, report.AppID, versionID, auth)
	if version == nil {
		return
	}
	report.PublishedVersionID = versionID
	report.PublicationState = "verified"
	for _, event := range report.Events {
		report.EventStates[event] = feishuVersionEventState(version, event)
	}
}

func readPublishedFeishuVersion(ctx context.Context, client *lark.Client, appID, versionID string, auth ...larkcore.RequestOptionFunc) *application.ApplicationAppVersion {
	if !feishuPublishedVersionID.MatchString(versionID) {
		return nil
	}
	versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req := application.NewGetApplicationAppVersionReqBuilder().
		AppId(appID).VersionId(versionID).Lang("zh_cn").UserIdType("open_id").Build()
	versionResp, err := client.Application.V6.ApplicationAppVersion.Get(versionCtx, req, auth...)
	if err != nil || versionResp == nil || !feishuApplicationResponseOK(versionResp.ApiResp) ||
		versionResp.Data == nil || versionResp.Data.AppVersion == nil {
		return nil
	}
	version := versionResp.Data.AppVersion
	if value(version.AppId) != appID || value(version.VersionId) != versionID {
		return nil
	}
	return version
}

func feishuApplicationResponseOK(resp *larkcore.ApiResp) bool {
	// The SDK's Success method treats an absent wire code as zero.
	var status struct {
		Code *int `json:"code"`
	}
	return resp != nil && resp.StatusCode == http.StatusOK &&
		json.Unmarshal(resp.RawBody, &status) == nil && status.Code != nil && *status.Code == 0
}

func feishuVersionEventState(version *application.ApplicationAppVersion, event string) string {
	// Preserve the distinction between an absent list and an explicit empty list.
	var detailedEvents []string
	legacyDisplayNames := len(version.Events) > 0 && len(version.Events) == len(version.EventInfos)
	if version.EventInfos != nil {
		detailedEvents = make([]string, len(version.EventInfos))
		for i, event := range version.EventInfos {
			if event == nil {
				legacyDisplayNames = false
				continue
			}
			detailedEvents[i] = value(event.EventType)
			if legacyDisplayNames && (version.Events[i] == "" || version.Events[i] != value(event.EventName)) {
				legacyDisplayNames = false
			}
		}
	}
	detailed := feishuEventListState(detailedEvents, event)
	if legacyDisplayNames {
		// Some published versions mirror the localized names in events.
		return detailed
	}
	legacy := feishuEventListState(version.Events, event)
	switch {
	case legacy == "unknown":
		return detailed
	case detailed == "unknown" || legacy == detailed:
		return legacy
	default:
		return "unknown"
	}
}

func feishuEventListState(events []string, name string) string {
	if events == nil {
		return "unknown"
	}
	state := "missing"
	for _, event := range events {
		switch event {
		case name:
			return "subscribed"
		case "":
			state = "unknown"
		}
	}
	return state
}

func meetingTenantScopeState(scopes []*application.AppScope, name string) string {
	state := "missing"
	for _, scope := range scopes {
		if scope == nil || value(scope.Scope) == "" {
			state = "unknown"
			continue
		}
		if *scope.Scope != name {
			continue
		}
		if len(scope.TokenTypes) == 0 {
			state = "unknown"
		}
		for _, tokenType := range scope.TokenTypes {
			switch tokenType {
			case "tenant":
				return "granted"
			case "user":
			default:
				state = "unknown"
			}
		}
	}
	return state
}
