package bridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	application "github.com/larksuite/oapi-sdk-go/v3/service/application/v6"
)

// CheckMeetingAvailability checks permissions and whether the app can read its owner's active meetings.
// Admission to a meeting requires its own verification.
func CheckMeetingAvailability(ctx context.Context, cfg Config) (MeetingPermissionReport, error) {
	return checkMeetingAvailability(ctx, cfg, privateHTTP(10*time.Second))
}

func checkMeetingAvailability(ctx context.Context, cfg Config, httpClient larkcore.HttpClient) (MeetingPermissionReport, error) {
	return probeMeetingPermissions(ctx, cfg, httpClient, true)
}

func readMeetingAvailability(ctx context.Context, tenant, token string, owner *application.ApplicationOwner, httpClient larkcore.HttpClient) string {
	if owner == nil || value(owner.OwnerId) == "" {
		return "owner-unavailable"
	}
	ownerID := *owner.OwnerId
	if !openIDPattern.MatchString(ownerID) {
		return "unknown"
	}

	domain := lark.FeishuBaseUrl
	if tenant == "lark" {
		domain = lark.LarkBaseUrl
	}
	query := url.Values{"user_id": {ownerID}}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, domain+"/open-apis/vc/v1/bots/user_active_meeting?"+query.Encode(), nil)
	if err != nil {
		return "unknown"
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := httpClient.Do(req)
	if err != nil {
		return "unknown"
	}
	defer resp.Body.Close()
	status := resp.StatusCode
	if status != http.StatusOK && (status < http.StatusBadRequest || status >= http.StatusInternalServerError) {
		return "unknown"
	}
	var result struct {
		Code *int `json:"code"`
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil || json.Unmarshal(body, &result) != nil || result.Code == nil {
		return "unknown"
	}
	switch *result.Code {
	case 0:
		if status == http.StatusOK {
			return "readable"
		}
	case 20017:
		return "not-in-beta"
	case 99991672:
		return "missing-scope"
	}
	return "unknown"
}
