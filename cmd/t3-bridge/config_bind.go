package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
	"github.com/larksuite/oapi-sdk-go/v3/scene/registration"
	qrcode "github.com/skip2/go-qrcode"
)

type qrRegistration struct {
	mu           sync.Mutex
	ctx          context.Context
	register     func(context.Context, *registration.Options) (*registration.RegisterAppResult, error)
	configDir    string
	cancel       context.CancelFunc
	id           string
	replaceAppID string
	view         qrBindingView
	credentials  *registration.RegisterAppResult
}

type qrBindingView struct {
	State    string `json:"state"`
	URL      string `json:"url,omitempty"`
	Image    string `json:"image,omitempty"`
	AppID    string `json:"appId,omitempty"`
	ExpireIn int    `json:"expireIn,omitempty"`
}

func (q *qrRegistration) routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /bind/start", func(w http.ResponseWriter, r *http.Request) {
		values, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			http.Error(w, "扫码请求参数无效", http.StatusBadRequest)
			return
		}
		var replaceAppID string
		if replace, found := values["replaceAppId"]; found {
			if len(replace) != 1 || !validReplacementAppID(replace[0]) {
				http.Error(w, "请选择一个已配置的待替换 App", http.StatusBadRequest)
				return
			}
			replaceAppID = replace[0]
		}
		id := q.startFor(values.Get("comments") == "true", replaceAppID)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
	})
	mux.HandleFunc("GET /bind/status", func(w http.ResponseWriter, r *http.Request) {
		view, ok := q.status(r.URL.Query().Get("id"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(view)
	})
	mux.HandleFunc("POST /bind/cancel", func(w http.ResponseWriter, r *http.Request) {
		q.mu.Lock()
		defer q.mu.Unlock()
		if q.id != "" && q.id == r.URL.Query().Get("id") {
			q.cancel()
			q.id, q.replaceAppID, q.credentials = "", "", nil
			q.view = qrBindingView{}
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func (q *qrRegistration) status(id string) (qrBindingView, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if id == "" || id != q.id {
		return qrBindingView{}, false
	}
	return q.view, true
}

func (q *qrRegistration) start(comments bool) string {
	return q.startFor(comments, "")
}

func (q *qrRegistration) startFor(comments bool, replaceAppID string) string {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.cancel != nil {
		q.cancel()
	}
	ctx, cancel := context.WithTimeout(q.ctx, 10*time.Minute)
	id := rand.Text()
	q.id, q.cancel, q.credentials, q.replaceAppID = id, cancel, nil, replaceAppID
	q.view = qrBindingView{State: "starting"}
	opts := &registration.Options{Source: "t3-bridge"}
	if comments {
		opts.Addons = &registration.AppAddons{
			Events: registration.AppAddonsEvents{Items: registration.AppAddonsEventItems{
				Tenant: []string{"drive.notice.comment_add_v1"},
			}},
			Scopes: registration.AppAddonsScopes{Tenant: []string{
				"docs:document.comment:read", "docs:document.comment:create", "wiki:node:read", "docx:document:readonly",
			}},
		}
	}
	opts.OnQRCode = func(info *registration.QRCodeInfo) {
		url, err := url.Parse(info.URL)
		if err != nil || url.Scheme != "https" || url.Host == "" {
			cancel()
			return
		}
		png, err := qrcode.Encode(info.URL, qrcode.Medium, 288)
		if err != nil {
			cancel()
			return
		}
		q.mu.Lock()
		defer q.mu.Unlock()
		if q.id == id {
			q.view = qrBindingView{State: "pending", URL: info.URL, ExpireIn: info.ExpireIn,
				Image: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)}
		}
	}
	go func() {
		result, err := q.register(ctx, opts)
		q.mu.Lock()
		if q.id == id {
			q.view = qrBindingView{State: "error"}
			if err == nil && result != nil && appIDPattern.MatchString(result.ClientID) && result.ClientSecret != "" {
				q.credentials = result
				q.view = qrBindingView{State: "scanned", AppID: result.ClientID}
			}
		}
		q.mu.Unlock()
		<-ctx.Done()
		q.mu.Lock()
		defer q.mu.Unlock()
		if q.id == id && q.credentials != nil {
			q.credentials = nil
			q.view = qrBindingView{State: "error"}
		}
	}()
	return id
}

var appIDPattern = regexp.MustCompile(`^cli_[A-Za-z0-9]{1,124}$`)

func (q *qrRegistration) apply(raw []byte, id string) ([]byte, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.id != id || q.credentials == nil {
		return raw, errors.New("请先完成扫码")
	}
	dir := q.configDir
	if dir == "" {
		root, err := os.UserConfigDir()
		if err != nil {
			return raw, errors.New("无法定位用户配置目录")
		}
		dir = filepath.Join(root, "t3-bridge")
	}
	result := q.credentials
	secret := filepath.Join(dir, "credentials", result.ClientID+".secret")
	state := filepath.Join(dir, "state", result.ClientID)
	var candidate []byte
	var err error
	if q.replaceAppID == "" {
		candidate, err = bindConfig(raw, result, secret, state)
	} else {
		candidate, err = bindReplacement(raw, q.replaceAppID, result, secret, replacementStateDir(dir, result.ClientID))
	}
	if err != nil {
		return raw, err
	}
	if err := writeAppSecret(secret, result.ClientSecret); err != nil {
		return raw, err
	}
	q.credentials = nil
	q.view = qrBindingView{State: "saved", AppID: result.ClientID}
	q.cancel()
	return candidate, nil
}

func bindConfig(raw []byte, result *registration.RegisterAppResult, secret, state string) ([]byte, error) {
	c, err := bridge.ParseConfig(raw)
	if err != nil {
		return raw, err
	}
	tenant, user, brand := "feishu", "", ""
	if result.UserInfo != nil {
		brand, user = result.UserInfo.TenantBrand, result.UserInfo.OpenID
		if brand == "lark" {
			tenant = "lark"
		}
	}
	appID := result.ClientID
	type change struct {
		Path  []string `json:"path"`
		Value any      `json:"value"`
	}
	var edits []change
	for i, account := range c.Accounts() {
		if account.AppID != appID {
			continue
		}
		accountTenant := account.Tenant
		if accountTenant == "" {
			accountTenant = "feishu"
		}
		if (brand == "feishu" || brand == "lark") && brand != accountTenant {
			return raw, errors.New("扫码租户与已配置账号不一致，请核对所选飞书或 Lark 应用")
		}
		path := []string{"appSecretFile"}
		if i > 0 {
			path = []string{"additionalAccounts", strconv.Itoa(i - 1), "appSecretFile"}
		}
		edits = []change{{path, secret}}
		break
	}
	if len(edits) == 0 {
		if user == "" {
			return raw, errors.New("飞书未返回扫码用户的 Open ID，无法生成账号授权")
		}
		if c.AppID == "REPLACE_APP_ID" {
			edits = []change{
				{[]string{"appId"}, appID}, {[]string{"appSecretFile"}, secret}, {[]string{"tenant"}, tenant},
				{[]string{"receiverAuthorized"}, false}, {[]string{"users"}, []string{user}},
			}
			if c.StateDir == "/var/lib/t3-bridge" || c.StateDir == filepath.Join(filepath.Dir(state), "default") {
				edits = append(edits, change{[]string{"stateDir"}, state})
			}
			if slices.Equal(c.Chats, []string{"REPLACE_CHAT_ID"}) {
				edits = append(edits, change{[]string{"chats"}, []string{}})
			}
			for id, route := range c.Routes {
				if slices.Equal(route.Users, []string{"REPLACE_OPEN_ID"}) {
					edits = append(edits, change{[]string{"routes", id, "users"}, []string{user}})
				}
			}
		} else {
			return addAccountDraft(raw, c, bridge.Config{
				StateDir: state, AppID: appID, AppSecretFile: secret, Tenant: tenant, Users: []string{user},
			}, c.DefaultRoute)
		}
	}
	changes, err := json.Marshal(edits)
	if err != nil {
		return raw, err
	}
	next, err := bridge.EditConfigJSON(raw, changes)
	if err != nil {
		return raw, err
	}
	return next, nil
}

func addAccountDraft(raw []byte, c, account bridge.Config, routeID string) ([]byte, error) {
	var source struct {
		Accounts []json.RawMessage `json:"additionalAccounts"`
	}
	if err := json.Unmarshal(raw, &source); err != nil {
		return raw, err
	}
	draft, err := bindingAccountDraft(raw, c, account, routeID)
	if err != nil {
		return raw, err
	}
	entry, err := json.Marshal(draft)
	if err != nil {
		return raw, err
	}
	edit, err := json.Marshal([]struct {
		Path  []string          `json:"path"`
		Value []json.RawMessage `json:"value"`
	}{{[]string{"additionalAccounts"}, append(source.Accounts, entry)}})
	if err != nil {
		return raw, err
	}
	next, err := bridge.EditConfigJSON(raw, edit)
	if err != nil {
		return raw, err
	}
	return next, nil
}

func bindingAccountDraft(raw []byte, source, account bridge.Config, routeID string) (bridge.Config, error) {
	route, ok := source.Routes[routeID]
	if !ok {
		return bridge.Config{}, errors.New("请选择已配置的 T3 路由作为新账号的目标")
	}
	var fields struct {
		Routes map[string]struct {
			Options json.RawMessage `json:"options"`
		} `json:"routes"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return bridge.Config{}, err
	}
	options := json.NewDecoder(bytes.NewReader(fields.Routes[routeID].Options))
	options.UseNumber()
	if err := options.Decode(&route.Options); err != nil {
		return bridge.Config{}, err
	}
	route.Users = account.Users
	route.Checks, route.ToolContext = nil, nil
	route.RuntimeMode = "approval-required"
	account.ReceiverMachine = strings.ToLower(account.AppID)
	account.MCP, account.DefaultRoute = source.MCP, routeID
	account.Routes = map[string]bridge.Target{routeID: route}
	return account, nil
}

func writeAppSecret(path, secret string) error {
	if err := localfile.MkdirAllPrivate(filepath.Dir(path)); err != nil {
		return errors.New("无法创建本机凭据目录")
	}
	_, err := localfile.WritePrivateAtomic(path, []byte(secret+"\n"), true)
	if errors.Is(err, os.ErrExist) {
		if existing, err := bridge.Secret(path); err == nil && existing == strings.TrimSpace(secret) {
			return nil
		}
		return errors.New("该应用已有凭据文件，请核对后再更新应用凭据配置")
	}
	if err != nil {
		return errors.New("无法保存本机凭据")
	}
	return nil
}
