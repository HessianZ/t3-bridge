package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/larksuite/oapi-sdk-go/v3/scene/registration"
)

var (
	bindingOpenID       = regexp.MustCompile(`^ou_[A-Za-z0-9_-]{1,100}$`)
	bindingReplaceAppID = regexp.MustCompile(`^cli_[A-Za-z0-9_-]{1,124}$`)
)

func validReplacementAppID(id string) bool {
	return bindingReplaceAppID.MatchString(id)
}

func replacementBindingAccount(config bridge.Config, id string) (bridge.Config, int, error) {
	for i, account := range config.Accounts() {
		if validReplacementAppID(id) && account.AppID == id {
			if _, ok := account.Routes[account.DefaultRoute]; !ok {
				return bridge.Config{}, 0, errors.New("请先为待替换 App 设置默认 T3 路由")
			}
			return account, i, nil
		}
	}
	return bridge.Config{}, 0, errors.New("草稿中未找到待替换 App")
}

func validateReplacementBinding(raw []byte, id string) error {
	config, err := bridge.ParseConfig(raw)
	if err != nil {
		return err
	}
	_, _, err = replacementBindingAccount(config, id)
	return err
}

func replacementStateDir(dir, appID string) string {
	return filepath.Join(dir, "state", appID+"-"+strings.ToLower(rand.Text()))
}

func bindReplacement(raw []byte, replaceAppID string, result *registration.RegisterAppResult, secret, state string) ([]byte, error) {
	config, err := bridge.ParseConfig(raw)
	if err != nil {
		return raw, err
	}
	user := result.UserInfo
	if user == nil {
		return raw, errors.New("扫码结果缺少可核对的租户或用户 Open ID")
	}
	return replaceAccountDraft(raw, config, replaceAppID, bridge.Config{
		AppID: result.ClientID, AppSecretFile: secret, Tenant: user.TenantBrand,
		StateDir: state, Users: []string{user.OpenID},
	})
}

func replaceAccountDraft(raw []byte, config bridge.Config, replaceAppID string, next bridge.Config) ([]byte, error) {
	old, index, err := replacementBindingAccount(config, replaceAppID)
	if err != nil {
		return raw, err
	}
	for _, account := range config.Accounts() {
		if account.AppID == next.AppID {
			return raw, errors.New("请选择草稿中尚未配置的 App。同一 App 可在「应用凭据」中更新")
		}
	}
	if !validReplacementAppID(next.AppID) || !filepath.IsAbs(next.AppSecretFile) {
		return raw, errors.New("请填写新应用 ID 和私有密钥文件的绝对路径")
	}
	if len(next.Users) != 1 || !bindingOpenID.MatchString(next.Users[0]) || (next.Tenant != "feishu" && next.Tenant != "lark") {
		return raw, errors.New("请确认新应用的租户和初始用户 Open ID")
	}
	if filepath.Clean(old.StateDir) == filepath.Clean(next.StateDir) {
		return raw, errors.New("替换 App 需要独立的状态目录")
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		return raw, err
	}
	var accounts []json.RawMessage
	additional := document["additionalAccounts"]
	if len(additional) != 0 {
		if err := json.Unmarshal(additional, &accounts); err != nil {
			return raw, err
		}
	}
	source := raw
	if index > 0 {
		source = accounts[index-1]
	}
	draft, err := bindingAccountDraft(source, old, next, old.DefaultRoute)
	if err != nil {
		return raw, err
	}
	// A dedicated executor may still hold the old App's tool credentials even
	// after its local tool binding is removed from the candidate.
	if tool := old.Routes[old.DefaultRoute].ToolContext; tool != nil && (tool.Verified || tool.BotBinding != nil || tool.UserBinding != nil) {
		route := draft.Routes[old.DefaultRoute]
		route.ProviderID, route.Model = "REPLACE_PROVIDER_INSTANCE_ID", "REPLACE_MODEL_ID"
		route.Options = map[string]any{}
		draft.Routes[old.DefaultRoute] = route
	}
	replacement, err := json.Marshal(draft)
	if err != nil {
		return raw, err
	}
	if index == 0 {
		document = nil
		if err := json.Unmarshal(replacement, &document); err != nil {
			return raw, err
		}
		if len(additional) != 0 {
			document["additionalAccounts"] = additional
		}
	} else {
		accounts[index-1] = replacement
		document["additionalAccounts"], err = json.Marshal(accounts)
		if err != nil {
			return raw, err
		}
	}
	candidate, err := json.Marshal(document)
	if err != nil {
		return raw, err
	}
	candidate, err = bridge.EditConfigJSON(candidate, nil)
	if err != nil {
		return raw, err
	}
	return candidate, nil
}
