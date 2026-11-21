package bridge

import (
	"cmp"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCheckMeetingAvailability(t *testing.T) {
	const appID = "cli_meetingavailability"
	const secret = "synthetic-app-secret"
	const token = "synthetic-tenant-token"
	const ownerID = "ou_actual-owner"
	const creatorID = "ou_other-creator"
	const aclID = "ou_config-user"
	const versionID = "oav_published"
	const invited = "vc.bot.meeting_invited_v1"
	const authPath = "/open-apis/auth/v3/tenant_access_token/internal"
	const appPath = "/open-apis/application/v6/applications/" + appID
	const versionPath = appPath + "/app_versions/" + versionID
	const readPath = "/open-apis/vc/v1/bots/user_active_meeting"
	const appData = `"data":{"app":{"app_id":"` + appID + `","online_version_id":"` + versionID + `","creator_id":"` + creatorID + `"`
	const withOwner = `,"owner":{"owner_id":"` + ownerID + `"}`
	const appBody = `{"code":0,` + appData + withOwner + `}}}`
	const readBody = `{"code":0,"msg":{"debug":"synthetic-debug"},"data":{"meetings":[{"meeting_id":"synthetic-meeting-id","meeting_title":"synthetic-title"}]}}`
	const blockedCode = `{"code":20017}`
	const missingScopeCode = `{"code":99991672}`
	secretFile := filepath.Join(t.TempDir(), "app-secret")
	if err := os.WriteFile(secretFile, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, app, body, want string
		status                int
		readErr               error
		defaultProbe          bool
		untrustedApp          bool
		feishu                bool
	}{
		{name: "default does not read", defaultProbe: true},
		{name: "readable with private meeting data", body: readBody, want: "readable"},
		{name: "default tenant readable without data", body: `{"code":0}`, want: "readable", feishu: true},
		{name: "beta code on HTTP 200", body: blockedCode, want: "not-in-beta"},
		{name: "beta code on HTTP 403", body: blockedCode, status: http.StatusForbidden, want: "not-in-beta"},
		{name: "scope code on HTTP 200", body: missingScopeCode, want: "missing-scope"},
		{name: "scope code on HTTP 400", body: missingScopeCode, status: http.StatusBadRequest, want: "missing-scope"},
		{name: "success code on HTTP 403", body: readBody, status: http.StatusForbidden, want: "unknown"},
		{name: "success code on HTTP 201", body: readBody, status: http.StatusCreated, want: "unknown"},
		{name: "beta code on HTTP 500", body: blockedCode, status: http.StatusInternalServerError, want: "unknown"},
		{name: "scope code on HTTP 302", body: missingScopeCode, status: http.StatusFound, want: "unknown"},
		{name: "unknown code", body: `{"code":12345}`, want: "unknown"},
		{name: "missing code", body: `{"data":{"code":0}}`, want: "unknown"},
		{name: "null code", body: `{"code":null}`, want: "unknown"},
		{name: "string code", body: `{"code":"0"}`, want: "unknown"},
		{name: "malformed JSON", body: `{"code":0}x`, want: "unknown"},
		{name: "transport error", readErr: io.ErrUnexpectedEOF, want: "unknown"},
		{name: "no owner with available creator and ACL", app: `{"code":0,` + appData + `}}}`, want: "owner-unavailable"},
		{name: "null owner ID", app: `{"code":0,` + appData + `,"owner":{"owner_id":null}}}}`, want: "owner-unavailable"},
		{name: "unsafe owner ID", app: strings.Replace(appBody, ownerID, "ou_unsafe&extra=1", 1), want: "unknown"},
		{name: "wrong owner ID type", app: strings.Replace(appBody, ownerID, "on_union-id", 1), want: "unknown"},
		{name: "wrong app with valid owner", app: strings.Replace(appBody, appID, "cli_other", 1), untrustedApp: true, want: "unknown"},
		{name: "missing app code with valid owner", app: `{` + appData + withOwner + `}}}`, untrustedApp: true, want: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{AppID: appID, AppSecretFile: secretFile, Tenant: "lark", Users: []string{aclID}}
			wantHost := "open.larksuite.com"
			if tc.feishu {
				cfg.Tenant, wantHost = "", "open.feishu.cn"
			}
			var requests []string
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				requests = append(requests, r.URL.Path)
				if r.URL.Scheme != "https" || r.URL.Host != wantHost {
					t.Fatal("unexpected application host")
				}
				res := httptest.NewRecorder()
				res.Header().Set("Content-Type", "application/json")
				if r.URL.Path == authPath && r.Method == http.MethodPost {
					io.WriteString(res, `{"code":0,"tenant_access_token":"`+token+`","expire":7200}`)
					return res.Result(), nil
				}
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+token {
					t.Fatal("unexpected authenticated request")
				}
				switch r.URL.Path {
				case appPath:
					io.WriteString(res, cmp.Or(tc.app, appBody))
				case versionPath:
					io.WriteString(res, `{"code":0,"data":{"app_version":{"app_id":"`+appID+`","version_id":"`+versionID+`","events":["`+invited+`"]}}}`)
				case readPath:
					if r.URL.RawQuery != "user_id="+ownerID || r.Body != nil {
						t.Fatal("unexpected active meeting request parameters")
					}
					if tc.readErr != nil {
						return nil, tc.readErr
					}
					res.WriteHeader(cmp.Or(tc.status, http.StatusOK))
					io.WriteString(res, tc.body)
				default:
					t.Fatal("unexpected API path")
				}
				return res.Result(), nil
			})
			check := checkMeetingAvailability
			if tc.defaultProbe {
				check = checkMeetingPermissions
			}
			report, err := check(context.Background(), cfg, &http.Client{Transport: transport})
			wantRequests := []string{authPath, appPath}
			wantPublication, wantInvited := "unknown", "unknown"
			if !tc.untrustedApp {
				wantRequests = append(wantRequests, versionPath)
				wantPublication, wantInvited = "verified", "subscribed"
			}
			if tc.body != "" || tc.readErr != nil {
				wantRequests = append(wantRequests, readPath)
			}
			if err != nil || !slices.Equal(requests, wantRequests) || report.Availability != tc.want {
				t.Fatalf("requests = %v, want %v, availability = %q, want %q, err = %v", requests, wantRequests, report.Availability, tc.want, err)
			}
			if report.PublicationState != wantPublication || report.EventStates[invited] != wantInvited {
				t.Fatalf("publication = %q, invited = %q, want %q and %q", report.PublicationState, report.EventStates[invited], wantPublication, wantInvited)
			}
			encoded, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			output := string(encoded)
			for _, private := range []string{ownerID, creatorID, aclID, token, secret, "synthetic-debug", "synthetic-meeting-id", "synthetic-title"} {
				if strings.Contains(output, private) {
					t.Fatal("report contains private probe data")
				}
			}
			if strings.Contains(output, `"availability":`) == tc.defaultProbe {
				t.Fatal("unexpected availability field presence")
			}
		})
	}
}
