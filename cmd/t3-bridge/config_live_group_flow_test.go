package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	channel "github.com/larksuite/channel-sdk-go"
)

func TestConfigLiveGroupFlowUsesSelectedAppAndPersistsGrant(t *testing.T) {
	const app, chat = "cli_group_target", "oc_selected"
	feishu := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			var request struct {
				AppID string `json:"app_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				http.Error(w, "invalid fixture app", http.StatusBadRequest)
				return
			}
			fmt.Fprintf(w, `{"code":0,"tenant_access_token":%q,"expire":7200}`, request.AppID)
		case "/open-apis/im/v1/chats":
			fmt.Fprint(w, `{"code":0,"data":{"items":[{"chat_id":"oc_selected","name":"Selected QA"},{"chat_id":"oc_sibling","name":"Sibling QA"}],"has_more":false}}`)
		case "/open-apis/application/v6/applications/" + app:
			fmt.Fprint(w, `{"code":0,"data":{"app":{"scopes":[{"scope":"im:message.group_msg","token_types":["tenant"]}]}}}`)
		case "/open-apis/application/v6/applications/cli_group_other":
			fmt.Fprint(w, `{"code":0,"data":{"app":{"scopes":[]}}}`)
		default:
			t.Errorf("unexpected Feishu fixture call %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer feishu.Close()

	root := t.TempDir()
	secretFile := filepath.Join(root, "app.secret")
	if err := os.WriteFile(secretFile, []byte("synthetic-qa"), 0600); err != nil {
		t.Fatal(err)
	}
	registry := localReceiverProcesses(profileRegistry{root: root})
	accounts := []bridge.Config{
		{AppID: "cli_group_other", Tenant: "feishu", AppSecretFile: secretFile, StateDir: filepath.Join(root, "other")},
		{AppID: app, Tenant: "lark", AppSecretFile: secretFile, StateDir: filepath.Join(root, "target")},
	}
	start := func() (string, func()) {
		t.Helper()
		receiver, err := registry.register(accounts)
		if err != nil {
			t.Fatal(err)
		}
		var engines []*bridge.Engine
		for _, account := range accounts {
			store, err := bridge.OpenStore(account.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.BindReceiver(account); err != nil {
				t.Fatal(err)
			}
			lark, err := bridge.NewLark(account, store)
			if err != nil {
				t.Fatal(err)
			}
			lark.Channel, err = channel.New(account.AppID, "synthetic-qa", channel.WithDomain(feishu.URL), channel.WithHTTPClient(feishu.Client()))
			if err != nil {
				t.Fatal(err)
			}
			engines = append(engines, &bridge.Engine{Config: lark.Config, Store: store, Lark: lark})
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			ticks := time.NewTicker(5 * time.Millisecond)
			defer ticks.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticks.C:
					for _, engine := range engines {
						account := newReceiverGroupAccount(engine.Config.Tenant, engine.Config.AppID)
						receiver.serveGroupAccess(ctx, account, receiverGroupPendingPath(receiver.dir, account), engine.LocalGroupAccess)
					}
				}
			}
		}()
		return receiver.process.ID, func() {
			cancel()
			<-done
			for _, engine := range engines {
				engine.Store.Close()
			}
			if err := receiver.close(); err != nil {
				t.Error(err)
			}
		}
	}
	process, stop := start()
	defer func() { stop() }()
	handler := configRuntimeGroupsHandler(registry.list, registry.groupAccess)
	base := "http://" + webTestHost
	scoped := url.Values{"appId": {app}, "tenant": {"lark"}, "chatId": {chat}}
	get := func(values url.Values) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, base+"/runtime/groups?"+values.Encode(), nil))
		return w
	}
	page := get(scoped)
	assertWebPage(t, page, http.StatusOK, app, chat, "Selected QA", "未授权", "机器人已入群")
	if strings.Contains(page.Body.String(), "oc_sibling") || strings.Contains(page.Body.String(), "cli_group_other") {
		t.Fatal("the selected App/group exposed a different candidate")
	}

	read := func(account bridge.Config, allowed bool, scope bridge.GroupMessageScope) {
		t.Helper()
		var output bytes.Buffer
		err := runConfigLiveGroups([]string{"-process", process, "-app-id", account.AppID, "-tenant", account.Tenant, "-chat-id", chat}, &output, registry.groupAccess)
		var view bridge.GroupAccessView
		if err != nil || json.Unmarshal(output.Bytes(), &view) != nil || len(view.Groups) != 1 ||
			view.Groups[0].ID != chat || view.Groups[0].Allowed != allowed || !view.Groups[0].Joined ||
			!view.Groups[0].RequiresMention || view.MessageScope != scope {
			t.Fatalf("unexpected group lookup for %s: %+v, %v", account.AppID, view, err)
		}
	}
	read(accounts[1], false, bridge.GroupMessageScopeGranted)
	form := url.Values{"process": {process}, "tenant": {"lark"}, "appId": {app}, "chatId": {chat}, "access": {"allow"}, "returnToGroup": {"1"}}
	request := httptest.NewRequest(http.MethodPost, base+"/runtime/groups", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", base)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("the authorized group update returned %d", response.Code)
	}
	returnTo, err := url.Parse(response.Header().Get("Location"))
	if err != nil || returnTo.Path != "/runtime/groups" || returnTo.Query().Get("chatId") != chat || returnTo.Query().Get("process") != process {
		t.Fatalf("the group update did not return to the selected group: %v, %v", returnTo, err)
	}
	assertWebPage(t, get(returnTo.Query()), http.StatusOK, "群权限已更新", "已授权 （运行覆盖）")
	read(accounts[1], true, bridge.GroupMessageScopeGranted)
	read(accounts[0], false, bridge.GroupMessageScopeMissing)

	old := process
	stop()
	stop = func() {}
	process, stop = start()
	scoped.Set("process", old)
	assertWebPage(t, get(scoped), http.StatusNotFound, "接收进程")
	read(accounts[1], true, bridge.GroupMessageScopeGranted)
	read(accounts[0], false, bridge.GroupMessageScopeMissing)
}
