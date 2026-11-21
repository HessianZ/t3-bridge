package bridge

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCheckMeetingPermissions(t *testing.T) {
	const appID = "cli_meetingpermissions"
	const secret = "synthetic-app-secret"
	const token = "synthetic-tenant-token"
	const appPath = "/open-apis/application/v6/applications/" + appID
	const scopes = `[{"scope":"vc:meeting.bot.join:write","token_types":["user","tenant"]},{"scope":"vc:meeting.meetingevent:read","token_types":["user"]},{"scope":"vc:meeting.message:write","token_types":["Tenant"]}]`
	const appData = `"data":{"app":{"app_id":"` + appID + `","scopes":` + scopes + `}}`
	const success = `{"code":0,` + appData + `}`
	unknown := [3]string{"unknown", "unknown", "unknown"}
	secretFile := filepath.Join(t.TempDir(), "app-secret")
	if err := os.WriteFile(secretFile, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body string
		status     int
		want       [3]string
	}{
		{"exact tenant, user, and unrecognized identity", success, http.StatusOK, [3]string{"granted", "missing", "unknown"}},
		{"explicit empty scopes", `{"code":0,"data":{"app":{"app_id":"` + appID + `","scopes":[]}}}`, http.StatusOK, [3]string{"missing", "missing", "missing"}},
		{"missing scopes", `{"code":0,"data":{"app":{"app_id":"` + appID + `"}}}`, http.StatusOK, unknown},
		{"missing app ID", `{"code":0,"data":{"app":{"scopes":[]}}}`, http.StatusOK, unknown},
		{"wrong app", `{"code":0,"data":{"app":{"app_id":"cli_other","scopes":` + scopes + `}}}`, http.StatusOK, unknown},
		{"missing code", `{` + appData + `}`, http.StatusOK, unknown},
		{"null code", `{"code":null,` + appData + `}`, http.StatusOK, unknown},
		{"failed code", `{"code":999,` + appData + `}`, http.StatusOK, unknown},
		{"non-200", success, http.StatusForbidden, unknown},
		{"malformed JSON", `{"code":0,`, http.StatusOK, unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{AppID: appID, AppSecretFile: secretFile, Tenant: "lark", MeetingEnabled: true}
			wantTenant, wantHost := "lark", "open.larksuite.com"
			if tc.name == "explicit empty scopes" {
				cfg.Tenant, cfg.MeetingEnabled = "", false
				wantTenant, wantHost = "feishu", "open.feishu.cn"
			}
			authCalls, appCalls := 0, 0
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != wantHost {
					t.Fatalf("unexpected tenant host %q", r.URL.Host)
				}
				res := httptest.NewRecorder()
				res.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/open-apis/auth/v3/tenant_access_token/internal":
					authCalls++
					var body struct {
						AppID     string `json:"app_id"`
						AppSecret string `json:"app_secret"`
					}
					if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil || body.AppID != appID || body.AppSecret != secret {
						t.Fatal("unexpected application token request")
					}
					io.WriteString(res, `{"code":0,"tenant_access_token":"`+token+`","expire":7200}`)
				case appPath:
					appCalls++
					query := r.URL.Query()
					if authCalls != 1 || r.Method != http.MethodGet || query.Get("lang") != "zh_cn" ||
						query.Get("user_id_type") != "open_id" || r.Header.Get("Authorization") != "Bearer "+token {
						t.Fatal("unexpected application scope request")
					}
					res.WriteHeader(tc.status)
					io.WriteString(res, tc.body)
				default:
					t.Fatalf("unexpected API request: %s %s", r.Method, r.URL.Path)
				}
				return res.Result(), nil
			})
			report, err := checkMeetingPermissions(context.Background(), cfg, &http.Client{Transport: transport})
			if err != nil || authCalls != 1 || appCalls != 1 {
				t.Fatalf("auth calls=%d, app calls=%d, err=%v", authCalls, appCalls, err)
			}
			got, err := json.Marshal(report)
			var links [3]string
			for i, scope := range [3]string{"vc:meeting.bot.join:write", "vc:meeting.meetingevent:read", "vc:meeting.message:write"} {
				if tc.want[i] == "missing" {
					links[i] = fmt.Sprintf(`,"authorizationUrl":%q`, "https://"+wantHost+"/app/"+appID+"/auth?q="+url.QueryEscape(scope))
				}
			}
			want := fmt.Sprintf(`{"appId":%q,"tenant":%q,"enabled":%t,"scopes":[`+
				`{"scope":"vc:meeting.bot.join:write","state":%q%s},`+
				`{"scope":"vc:meeting.meetingevent:read","state":%q%s},`+
				`{"scope":"vc:meeting.message:write","state":%q%s}],`+
				`"events":["vc.bot.meeting_invited_v1","vc.bot.meeting_activity_v1","vc.bot.meeting_ended_v1"],`+
				`"publicationState":"unknown","eventStates":{"vc.bot.meeting_activity_v1":"unknown","vc.bot.meeting_ended_v1":"unknown","vc.bot.meeting_invited_v1":"unknown"}}`,
				appID, wantTenant, cfg.MeetingEnabled, tc.want[0], links[0], tc.want[1], links[1], tc.want[2], links[2])
			if err != nil || string(got) != want {
				t.Fatalf("report = %s (%v), want %s", got, err, want)
			}
		})
	}
}

func TestCheckMeetingPermissionsPublishedVersion(t *testing.T) {
	const appID = "cli_meetingversion"
	const token = "synthetic-tenant-token"
	const versionID = "oav_Online-1"
	const authPath = "/open-apis/auth/v3/tenant_access_token/internal"
	const appPath = "/open-apis/application/v6/applications/" + appID
	const versionPath = appPath + "/app_versions/" + versionID
	const invited = "vc.bot.meeting_invited_v1"
	const activity = "vc.bot.meeting_activity_v1"
	const ended = "vc.bot.meeting_ended_v1"
	const withScopes = `,"scopes":[{"scope":"vc:meeting.bot.join:write","token_types":["tenant"]}]`
	const online = `,"online_version_id":"` + versionID + `"`
	const draft = `,"unaudit_version_id":"oav_draft"`
	appBody := func(fields string) string {
		return `{"code":0,"data":{"app":{"app_id":"` + appID + `"` + fields + `}}}`
	}
	versionBody := func(fields string) string {
		return `{"code":0,"data":{"app_version":{"app_id":"` + appID + `","version_id":"` + versionID + `"` + fields + `}}}`
	}
	publishedApp := appBody(withScopes + online + draft)
	legacyEvents := versionBody(`,"events":["` + invited + `","VC.BOT.MEETING_ACTIVITY_V1","` + ended + ` "]`)
	unknown := [3]string{"unknown", "unknown", "unknown"}
	missing := [3]string{"missing", "missing", "missing"}
	secretFile := filepath.Join(t.TempDir(), "app-secret")
	if err := os.WriteFile(secretFile, []byte("synthetic-app-secret"), 0600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, app, version, publication string
		appStatus, versionStatus        int
		versionErr                      error
		wantScope                       string
		wantEvents                      [3]string
	}{
		{name: "missing online ID with draft", app: appBody(withScopes + draft)},
		{name: "null online ID with draft", app: appBody(withScopes + draft + `,"online_version_id":null`)},
		{name: "explicit unpublished", app: appBody(withScopes + draft + `,"online_version_id":""`), publication: "unpublished"},
		{name: "unsafe online ID", app: appBody(withScopes + `,"online_version_id":"../oav_other"`)},
		{name: "untrimmed online ID", app: appBody(withScopes + `,"online_version_id":" oav_online "`)},
		{name: "oversized online ID", app: appBody(withScopes + `,"online_version_id":"` + strings.Repeat("a", 129) + `"`)},
		{name: "mismatched application identity", app: strings.Replace(publishedApp, appID, "cli_other", 1), wantScope: "unknown"},
		{name: "missing application code", app: strings.Replace(publishedApp, `"code":0,`, "", 1), wantScope: "unknown"},
		{name: "application HTTP failure", appStatus: http.StatusForbidden, wantScope: "unknown"},
		{name: "version transport failure", versionErr: io.ErrUnexpectedEOF},
		{name: "version HTTP failure", version: legacyEvents, versionStatus: http.StatusForbidden},
		{name: "version HTTP success other than 200", version: legacyEvents, versionStatus: http.StatusAccepted},
		{name: "missing version code", version: strings.Replace(legacyEvents, `"code":0,`, "", 1)},
		{name: "null version code", version: strings.Replace(legacyEvents, `"code":0`, `"code":null`, 1)},
		{name: "failed version code", version: strings.Replace(legacyEvents, `"code":0`, `"code":999`, 1)},
		{name: "missing version data", version: `{"code":0,"data":{}}`},
		{name: "wrong version app ID", version: strings.Replace(legacyEvents, appID, "cli_other", 1)},
		{name: "wrong version ID", version: strings.Replace(legacyEvents, versionID, "oav_draft", 1)},
		{name: "missing unique version ID", version: strings.Replace(legacyEvents, `"version_id"`, `"version"`, 1)},
		{name: "absent and null event lists", version: versionBody(`,"events":null`), publication: "verified"},
		{name: "explicit empty legacy list", version: versionBody(`,"events":[]`), publication: "verified", wantEvents: missing},
		{name: "explicit empty detailed list", version: versionBody(`,"event_infos":[]`), publication: "verified", wantEvents: missing},
		{name: "legacy list matches exact identifiers", version: legacyEvents, publication: "verified",
			wantEvents: [3]string{"subscribed", "missing", "missing"}},
		{name: "detailed list ignores names and verifies without scopes", app: appBody(online + draft),
			version:     versionBody(`,"event_infos":[{"event_type":"` + activity + `","event_name":"` + ended + `"}]`),
			publication: "verified", wantScope: "unknown", wantEvents: [3]string{"missing", "subscribed", "missing"}},
		{name: "conflicting lists affect only conflicting event",
			version:     versionBody(`,"events":["` + invited + `","` + activity + `"],"event_infos":[{"event_type":"` + invited + `"}]`),
			publication: "verified", wantEvents: [3]string{"subscribed", "unknown", "missing"}},
		{name: "legacy display names are not event identifiers",
			version:     versionBody(`,"events":["会议邀请","会议活动"],"event_infos":[{"event_type":"` + invited + `","event_name":"会议邀请"},{"event_type":"` + activity + `","event_name":"会议活动"}]`),
			publication: "verified", wantEvents: [3]string{"subscribed", "subscribed", "missing"}},
		{name: "partial detailed list retains positive evidence",
			version:     versionBody(`,"event_infos":[null,{"event_name":"` + ended + `"},{"event_type":"` + activity + `"}]`),
			publication: "verified", wantEvents: [3]string{"unknown", "subscribed", "unknown"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests []string
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				requests = append(requests, r.URL.Path)
				if r.URL.Host != "open.larksuite.com" {
					t.Fatalf("unexpected tenant host %q", r.URL.Host)
				}
				res := httptest.NewRecorder()
				res.Header().Set("Content-Type", "application/json")
				if r.URL.Path == authPath && r.Method == http.MethodPost {
					io.WriteString(res, `{"code":0,"tenant_access_token":"`+token+`","expire":7200}`)
					return res.Result(), nil
				}
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+token {
					t.Fatalf("unexpected authenticated request: %s %s", r.Method, r.URL.Path)
				}
				switch r.URL.Path {
				case appPath:
					if tc.appStatus != 0 {
						res.WriteHeader(tc.appStatus)
					}
					io.WriteString(res, cmp.Or(tc.app, publishedApp))
				case versionPath:
					if tc.versionErr != nil {
						return nil, tc.versionErr
					}
					if tc.versionStatus != 0 {
						res.WriteHeader(tc.versionStatus)
					}
					io.WriteString(res, tc.version)
				default:
					t.Fatalf("unexpected API request: %s %s", r.Method, r.URL.Path)
				}
				return res.Result(), nil
			})
			cfg := Config{AppID: appID, AppSecretFile: secretFile, Tenant: "lark", MeetingEnabled: true}
			report, err := checkMeetingPermissions(context.Background(), cfg, &http.Client{Transport: transport})
			wantRequests := []string{authPath, appPath}
			if tc.version != "" || tc.versionErr != nil {
				wantRequests = append(wantRequests, versionPath)
			}
			if err != nil || !slices.Equal(requests, wantRequests) {
				t.Fatalf("requests = %v, want %v, err = %v", requests, wantRequests, err)
			}
			wantPublication := cmp.Or(tc.publication, "unknown")
			wantID := ""
			if wantPublication == "verified" {
				wantID = versionID
			}
			if report.PublicationState != wantPublication || report.PublishedVersionID != wantID {
				t.Fatalf("publication = (%q, %q), want (%q, %q)", report.PublicationState, report.PublishedVersionID, wantPublication, wantID)
			}
			if wantScope := cmp.Or(tc.wantScope, "granted"); report.Scopes[0].State != wantScope {
				t.Fatalf("join scope = %q, want %q", report.Scopes[0].State, wantScope)
			}
			orderedEvents := []string{invited, activity, ended}
			if !slices.Equal(report.Events, orderedEvents) || len(report.EventStates) != len(orderedEvents) {
				t.Fatalf("unexpected event report: %+v", report)
			}
			wantEvents := tc.wantEvents
			if wantEvents == ([3]string{}) {
				wantEvents = unknown
			}
			for i, event := range orderedEvents {
				if got := report.EventStates[event]; got != wantEvents[i] {
					t.Errorf("%s = %q, want %q", event, got, wantEvents[i])
				}
			}
		})
	}
}
