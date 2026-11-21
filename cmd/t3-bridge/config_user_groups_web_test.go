package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestConfigUserGroupsWebUsesSelectedAccountAndRequiresConfirmation(t *testing.T) {
	service := &webUserGroupsFixture{}
	calls := 0
	handler := configUserGroupsHandler(func(account bridge.Config) (webUserGroupsService, error) {
		calls++
		if account.AppID != "cli_second" || account.AppSecretFile != "/fixture/second.secret" || account.Tenant != "lark" {
			t.Fatalf("request changed selected account: %+v", account)
		}
		return service, nil
	})
	base := `"appId":"cli_second","appSecretFile":"/fixture/second.secret","tenant":"lark",`
	post := func(origin, data string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "http://"+webTestHost+"/feishu/my-groups", strings.NewReader("{"+base+data+"}"))
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	origin := "http://" + webTestHost
	if w := post("https://other.test", `"action":"status"`); w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin request: %d", w.Code)
	}
	if w := post(origin, `"action":"invite","chatId":"oc_synthetic"`); w.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed invite: %d", w.Code)
	}
	if calls != 0 {
		t.Fatal("blocked requests opened a service")
	}
	w := post(origin, `"action":"start","purpose":"list"`)
	var challenge struct {
		bridge.UserGroupsChallenge
		Image string
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &challenge) != nil ||
		challenge.Ticket != "opaque-ticket" || challenge.URL != fixtureUserGroupsURL ||
		!strings.HasPrefix(challenge.Image, "data:image/png;base64,") || service.purpose != "list" {
		t.Fatalf("invalid authorization challenge: %d %s", w.Code, w.Body)
	}
	w = post(origin, `"action":"list","query":"项目","pageToken":"cursor"`)
	if w.Code != http.StatusOK || service.query != "项目" || service.cursor != "cursor" {
		t.Fatalf("search did not preserve input: %d %s", w.Code, w.Body)
	}
	w = post(origin, `"action":"invite","chatId":"oc_synthetic","confirm":true`)
	if w.Code != http.StatusOK || service.invites != 1 || !strings.Contains(w.Body.String(), "pending") {
		t.Fatalf("confirmed invite did not report pending: %d %s", w.Code, w.Body)
	}
	w = post(origin, `"action":"finish","ticket":"opaque-ticket"`)
	if w.Code != http.StatusUnprocessableEntity || strings.Contains(w.Body.String(), "private fixture") {
		t.Fatalf("finish leaked a diagnostic: %d %s", w.Code, w.Body)
	}
}

const fixtureUserGroupsURL = "https://accounts.larksuite.com/oauth/device?user_code=FIXTURE"

type webUserGroupsFixture struct {
	webUserGroupsService
	purpose, query, cursor string
	invites                int
}

func (f *webUserGroupsFixture) Start(_ context.Context, purpose string) (bridge.UserGroupsChallenge, error) {
	f.purpose = purpose
	return bridge.UserGroupsChallenge{Ticket: "opaque-ticket", URL: fixtureUserGroupsURL}, nil
}

func (f *webUserGroupsFixture) List(_ context.Context, query, cursor string) (bridge.UserGroupsPage, error) {
	f.query, f.cursor = query, cursor
	return bridge.UserGroupsPage{BotKnown: true}, nil
}

func (f *webUserGroupsFixture) Finish(context.Context, string) (bridge.UserGroupsStatus, error) {
	return bridge.UserGroupsStatus{}, errors.New("private fixture diagnostic")
}

func (f *webUserGroupsFixture) Invite(_ context.Context, chatID string) (bridge.UserGroupInvitation, error) {
	if chatID != "oc_synthetic" {
		return bridge.UserGroupInvitation{}, errors.New("wrong target")
	}
	f.invites++
	return bridge.UserGroupInvitation{State: "pending"}, nil
}
