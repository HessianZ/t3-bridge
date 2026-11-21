package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

const maxAppCredentialBytes = 64 << 10
const appCredentialLockPrefix = ".t3-bridge-credential-"

var errAppCredentialConflict = errors.New("配置或凭据已变化，请刷新后重试")

// The revisions protect a reviewed update. Only metadata is serialized.
type appCredential struct {
	AppID    string `json:"appId"`
	Tenant   string `json:"tenant"`
	Path     string `json:"path"`
	Status   string `json:"status"`
	Editable bool   `json:"editable"`

	configPath, configVersion, resolvedPath, fileVersion string
}

func listAppCredentials(configPath string) ([]appCredential, error) {
	configPath, err := filepath.Abs(configPath)
	if err != nil {
		return nil, errors.New("无法定位所选配置")
	}
	raw, err := bridge.ReadRegularConfigJSON(configPath)
	if err != nil {
		return nil, errors.New("凭据维护需要已保存的 JSON 配置")
	}
	cfg, err := bridge.ParseConfig(raw)
	if err != nil {
		return nil, err
	}
	version := configRevision(raw)
	accounts := cfg.Accounts()
	protected := configCredentialProtectedPaths(configPath, accounts)
	refs := make([]string, len(accounts))
	for i, account := range accounts {
		refs[i] = credentialPath(account.AppSecretFile)
	}

	items := make([]appCredential, 0, len(accounts))
	for i, account := range accounts {
		item := appCredential{
			AppID: account.AppID, Tenant: account.Tenant, Path: account.AppSecretFile,
			configPath: configPath, configVersion: version, resolvedPath: refs[i],
		}
		if item.Tenant == "" {
			item.Tenant = "feishu"
		}
		switch {
		case refs[i] == "":
			item.Status = "unavailable"
		case credentialPathShared(refs, protected, i):
			item.Status = "shared"
		default:
			item.inspect()
		}
		items = append(items, item)
	}
	return items, nil
}

func configCredentialProtectedPaths(configPath string, accounts []bridge.Config) []string {
	var paths []string
	if configPath != "" {
		paths = append(paths, configPath, configPath+".backup.json", configLockPath(configPath))
	}
	for _, account := range accounts {
		paths = append(paths, account.MCP.TokenFile, account.MCP.RuntimeFile, account.Classifier.KeyFile,
			filepath.Join(account.StateDir, "state.json"), account.AgentReportsFile)
	}
	for i, path := range paths {
		paths[i] = credentialPath(path)
	}
	return paths
}

func selectAppCredential(items []appCredential, appID string) (appCredential, error) {
	for _, item := range items {
		if item.AppID == appID {
			return item, nil
		}
	}
	return appCredential{}, errors.New("所选配置没有该 App")
}

func (item *appCredential) inspect() {
	info, err := os.Lstat(item.Path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		item.Status = "missing"
		item.Editable = privateCredentialParent(item.resolvedPath)
	case err != nil:
		item.Status = "unavailable"
	case info.Mode()&os.ModeSymlink != 0:
		item.Status = "linked"
		if raw, err := readAppCredential(item.resolvedPath); err == nil {
			item.fileVersion = configRevision(raw)
		}
	default:
		item.Status = "unavailable"
		raw, err := readAppCredential(item.resolvedPath)
		if err != nil {
			return
		}
		item.fileVersion = configRevision(raw)
		item.Editable = privateCredentialParent(item.resolvedPath)
		if _, ok := appCredentialText(string(raw)); ok {
			item.Status = "ready"
		}
	}
}

func (item appCredential) value() (string, error) {
	if item.Status != "ready" && item.Status != "linked" {
		return "", errors.New("所选 App 的凭据不可读取")
	}
	raw, err := readAppCredential(item.resolvedPath)
	if err != nil || configRevision(raw) != item.fileVersion {
		return "", errors.New("所选 App 的凭据不可读取或已变化")
	}
	value, ok := appCredentialText(string(raw))
	if !ok {
		return "", errors.New("所选 App 的凭据格式无效")
	}
	return value, nil
}

func (item appCredential) update(action, value string) error {
	if !item.Editable {
		return errors.New("此 App 的凭据路径无法维护，请核对私有目录及文件引用")
	}
	switch action {
	case "set":
		var ok bool
		value, ok = appCredentialText(value)
		if !ok {
			return errors.New("凭据须为非空单行文本，且小于 64 KiB")
		}
	case "remove":
		if value != "" {
			return errors.New("删除凭据无需输入值")
		}
	default:
		return errors.New("未知的凭据操作")
	}

	configLock, err := lockConfigFile(item.configPath)
	if err != nil {
		return errors.New("无法锁定所选配置")
	}
	defer configLock.Close()
	if err := item.checkCurrent(); err != nil {
		return err
	}
	if action == "remove" && item.Status == "missing" {
		return errors.New("所选 App 没有可删除的凭据文件")
	}
	dir := filepath.Dir(item.resolvedPath)
	if err := localfile.MkdirAllPrivate(dir); err != nil {
		return errors.New("凭据文件需要私有目录")
	}
	lockName := appCredentialLockPrefix + configRevision([]byte(strings.ToLower(filepath.Base(item.resolvedPath)))) + ".lock"
	fileLock, err := localfile.Lock(filepath.Join(dir, lockName), true)
	if err != nil {
		return errors.New("无法锁定凭据文件")
	}
	defer fileLock.Close()
	if err := item.checkCurrent(); err != nil {
		return err
	}
	if action == "remove" {
		if err := os.Remove(item.resolvedPath); err != nil {
			return errors.New("无法删除私有凭据文件")
		}
		return nil
	}
	published, err := localfile.WritePrivateAtomic(item.resolvedPath, []byte(value+"\n"), item.Status == "missing")
	switch {
	case err == nil:
		return nil
	case published:
		return errors.New("凭据已写入，但磁盘同步结果未确认，请刷新核对")
	case errors.Is(err, os.ErrExist):
		return errAppCredentialConflict
	default:
		return errors.New("无法写入私有凭据文件")
	}
}

func appCredentialText(value string) (string, bool) {
	value = strings.TrimSpace(value)
	return value, value != "" && len(value) < maxAppCredentialBytes &&
		utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func (item appCredential) checkCurrent() error {
	items, err := listAppCredentials(item.configPath)
	if err != nil {
		return errAppCredentialConflict
	}
	current, err := selectAppCredential(items, item.AppID)
	if err != nil || current != item {
		return errAppCredentialConflict
	}
	return nil
}

func readAppCredential(path string) ([]byte, error) {
	file, err := localfile.OpenPrivate(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxAppCredentialBytes+1))
	if err != nil || len(raw) > maxAppCredentialBytes {
		return nil, errors.New("凭据文件不可读取或超过 64 KiB")
	}
	return raw, nil
}

func privateCredentialParent(path string) bool {
	err := localfile.CheckPrivateDir(filepath.Dir(path))
	return err == nil || errors.Is(err, os.ErrNotExist)
}

// Resolve existing ancestors so comparisons also work for a new file.
func credentialPath(path string) string {
	if !filepath.IsAbs(path) {
		return ""
	}
	path = filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved
	}
	if parent := filepath.Dir(path); parent != path && errors.Is(err, os.ErrNotExist) {
		if resolvedParent := credentialPath(parent); resolvedParent != "" {
			return filepath.Join(resolvedParent, filepath.Base(path))
		}
	}
	return ""
}

func credentialPathShared(refs, protected []string, index int) bool {
	path := refs[index]
	if strings.HasPrefix(strings.ToLower(filepath.Base(path)), appCredentialLockPrefix) {
		return true
	}
	info, err := os.Stat(path)
	sameFile := func(other string) bool {
		if other == "" {
			return false
		}
		if path == other {
			return true
		}
		if err != nil {
			return false
		}
		stat, statErr := os.Stat(other)
		return statErr == nil && os.SameFile(info, stat)
	}
	for i, other := range refs {
		if i != index && sameFile(other) {
			return true
		}
	}
	for _, other := range protected {
		if sameFile(other) {
			return true
		}
	}
	return false
}
