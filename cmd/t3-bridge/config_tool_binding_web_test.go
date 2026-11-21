package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestConfigWebToolBindingFields(t *testing.T) {
	source := tuiTestSource(t)
	config, err := bridge.ParseConfig(source.JSON)
	if err != nil {
		t.Fatal(err)
	}
	account := config
	account.AppID, account.Tenant, account.StateDir = "cli_binding", "lark", t.TempDir()
	target := config.Routes[config.DefaultRoute]
	target.ProviderID, target.Users = "dedicated-provider", []string{"ou_binding"}
	account.Users = target.Users
	target.ToolContext = &bridge.ToolContext{
		EnvironmentID: target.EnvironmentID, CLIPath: filepath.Join(account.StateDir, "lark-cli"),
		Identity: "user", Version: "1.0.96", Verified: true,
		UserBinding: &bridge.ToolUserBinding{
			AppID: account.AppID, Tenant: account.Tenant, UserOpenID: target.Users[0],
			Profile: "Personal.Work", ConfigDir: account.StateDir,
		},
	}
	account.DefaultRoute = "Work.Tools"
	account.Routes = map[string]bridge.Target{account.DefaultRoute: target}
	config.AdditionalAccounts = []bridge.Config{account}
	source.JSON, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	page := httptest.NewRecorder()
	configWebHandler(webTestHost, []webSource{source}).ServeHTTP(page, httptest.NewRequest(http.MethodGet, "http://"+webTestHost+"/", nil))
	assertWebPage(t, page, http.StatusOK, `data-route="Work.Tools"`, `data-tool-binding-field="appId" value="cli_binding"`,
		`<option value="user" selected>`, `<option value="lark" selected>`, `data-tool-binding-field="profile" value="Personal.Work"`,
		`data-tool-binding-field="userOpenId" value="ou_binding"`, `"version": "1.0.96"`, `"verified": true`)
	_, clonedRoute, ok := strings.Cut(page.Body.String(), `<template id="route-template">`)
	clonedRoute, _, end := strings.Cut(clonedRoute, "</template>")
	if !ok || !end || !strings.Contains(clonedRoute, `type="button" data-tool-binding-apply`) ||
		!strings.Contains(clonedRoute, `<option value="off">`) || strings.Count(clonedRoute, `data-key="toolContext"`) != 1 {
		t.Fatal("new route template is missing the tool binding controls or advanced JSON edit source")
	}
}
