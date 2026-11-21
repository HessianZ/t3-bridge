package bridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

func TestListFeishuGroups(t *testing.T) {
	const firstPage = `{"code":0,"data":{"items":[{"chat_id":"oc_one","name":"研发群"}],"has_more":true,"page_token":"next"}}`
	for _, test := range []struct {
		name     string
		pages    []string
		wantJSON string
		wantErr  string
	}{
		{
			name: "preserve names and deduplicate across pages",
			pages: []string{firstPage,
				`{"code":0,"data":{"items":[{"chat_id":"oc_two"},{"chat_id":"oc_one","name":"duplicate"}]}}`},
			wantJSON: `[{"id":"oc_one","name":"研发群"},{"id":"oc_two","name":""}]`,
		},
		{
			name:     "empty list is an array",
			pages:    []string{`{"code":0,"data":{"items":[]}}`},
			wantJSON: `[]`,
		},
		{
			name:    "reject a repeated token without returning partial results",
			pages:   []string{firstPage, firstPage},
			wantErr: "joined groups pagination outside bounds",
		},
		{
			name:    "reject an item with no ID",
			pages:   []string{firstPage, `{"code":0,"data":{"items":[{"name":"invalid"}]}}`},
			wantErr: "invalid joined group",
		},
		{
			name:    "sanitize a later API failure",
			pages:   []string{firstPage, `{"code":999,"msg":"private-list-error"}`},
			wantErr: "cannot list joined groups",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			page, tokenRequests := 0, 0
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				res := httptest.NewRecorder()
				res.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/open-apis/auth/v3/tenant_access_token/internal":
					tokenRequests++
					io.WriteString(res, `{"code":0,"tenant_access_token":"synthetic-token","expire":7200}`)
				case "/open-apis/im/v1/chats":
					if page >= len(test.pages) {
						t.Fatalf("unexpected page %d", page)
					}
					query := r.URL.Query()
					if r.Method != http.MethodGet || query.Get("page_size") != "100" ||
						query.Get("user_id_type") != "open_id" || query.Get("page_token") != []string{"", "next"}[page] ||
						r.Header.Get("Authorization") != "Bearer synthetic-token" {
						t.Fatalf("unexpected list request: %s %s", r.Method, r.URL)
					}
					io.WriteString(res, test.pages[page])
					page++
				default:
					t.Fatalf("unexpected API request: %s %s", r.Method, r.URL.Path)
				}
				return res.Result(), nil
			})
			client := newFeishuProbeClient("synthetic-app", "synthetic-secret", "feishu", &http.Client{Transport: transport})
			token, err := feishuAppTenantToken(t.Context(), client, "synthetic-app", "synthetic-secret")
			if err != nil {
				t.Fatal(err)
			}
			groups, err := listFeishuGroups(context.Background(), client, larkcore.WithTenantAccessToken(token))
			if tokenRequests != 1 {
				t.Fatalf("read %d tokens, expected one", tokenRequests)
			}
			if page != len(test.pages) {
				t.Fatalf("read %d pages, expected %d", page, len(test.pages))
			}
			if test.wantErr != "" {
				if err == nil || err.Error() != test.wantErr || groups != nil {
					t.Fatalf("got groups %v and error %v, expected %q", groups, err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(groups)
			if err != nil || string(got) != test.wantJSON {
				t.Fatalf("got %s (%v), expected %s", got, err, test.wantJSON)
			}
		})
	}
}
