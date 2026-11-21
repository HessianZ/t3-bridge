package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestConfigFeishuGroupsKeepsAccountAndReturnsOnlyOptions(t *testing.T) {
	want := []bridge.FeishuGroup{{ID: "oc_synthetic", Name: "项目讨论"}}
	for _, tc := range []struct {
		name   string
		groups []bridge.FeishuGroup
		err    error
		code   int
	}{
		{"groups", want, nil, http.StatusOK},
		{"empty", nil, nil, http.StatusOK},
		{"failure", nil, errors.New("synthetic-private-diagnostics"), http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			handler := configFeishuGroups(func(ctx context.Context, app, secret, tenant string) ([]bridge.FeishuGroup, error) {
				called = true
				if _, timed := ctx.Deadline(); app != "cli_second" || secret != "/private/fixture.secret" || tenant != "lark" || !timed {
					t.Fatal("request changed the chosen account or omitted its timeout")
				}
				return tc.groups, tc.err
			})
			form := url.Values{"appId": {" cli_second "}, "appSecretFile": {" /private/fixture.secret "}, "tenant": {" lark "}}
			req := httptest.NewRequest(http.MethodPost, "http://"+webTestHost+"/feishu/groups?appId=cli_first", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if !called || w.Code != tc.code || strings.Contains(w.Body.String(), "fixture.secret") || strings.Contains(w.Body.String(), "synthetic-private-diagnostics") {
				t.Fatalf("unexpected status, request or diagnostics: %d %s", w.Code, w.Body)
			}
			if tc.err == nil {
				var got []bridge.FeishuGroup
				if w.Header().Get("Content-Type") != "application/json" || json.Unmarshal(w.Body.Bytes(), &got) != nil || got == nil || !slices.Equal(got, tc.groups) {
					t.Fatalf("expected a group array: %s", w.Body)
				}
			}
		})
	}
}
