package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func managedProjectWorkspace(configPath string, account bridge.Config, routeID string) (string, error) {
	registry, err := localProfiles()
	if err != nil {
		return "", errors.New("无法确定当前用户的工作区存储目录")
	}
	return registry.managedProjectWorkspace(configPath, account, routeID)
}

func (r profileRegistry) managedProjectWorkspace(configPath string, account bridge.Config, routeID string) (string, error) {
	if account.AppID == "" || strings.HasPrefix(account.AppID, "REPLACE_") {
		return "", errors.New("请先绑定飞书应用或填写实际 App ID，再为该账号准备专用工作区")
	}
	environment := account.Routes[routeID].EnvironmentID
	if environment == "" || environment != strings.TrimSpace(environment) || strings.HasPrefix(environment, "REPLACE_") ||
		strings.ContainsFunc(environment, unicode.IsControl) || !account.MCP.LocalEnvironment(environment) {
		return "", errors.New("专用工作区要求本机 MCP 连接及同一 T3 环境。其他目标请登记目标机已有目录")
	}
	if !filepath.IsAbs(r.root) {
		return "", errors.New("无法确定当前用户的绝对工作区存储目录")
	}
	canonical, err := filepath.Abs(configPath)
	if err == nil {
		canonical, err = canonicalProjectConfigPath(canonical)
	}
	if err != nil {
		return "", errors.New("无法读取所选配置的本机路径")
	}
	if runtime.GOOS == "windows" {
		canonical = strings.ToLower(canonical)
	}
	tenant := account.Tenant
	if tenant == "" {
		tenant = "feishu"
	}
	identity := fmt.Sprintf("%q %q %q %q", canonical, tenant, account.AppID, routeID)
	name := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return '-'
	}, routeID)
	nameRunes := []rune(name)
	name = strings.Trim(string(nameRunes[:min(24, len(nameRunes))]), "-")
	if name == "" {
		name = "workspace"
	}
	return filepath.Join(r.root, "workspaces", name+"-"+configRevision([]byte(identity))[:20]), nil
}

// Resolve the existing prefix so previewing a new file yields the same workspace after saving it.
func canonicalProjectConfigPath(path string) (string, error) {
	canonical, err := filepath.EvalSymlinks(path)
	if !errors.Is(err, os.ErrNotExist) {
		return canonical, err
	}
	parent := filepath.Dir(path)
	if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) || parent == path {
		return "", err
	}
	canonicalParent, err := canonicalProjectConfigPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(canonicalParent, filepath.Base(path)), nil
}

func prepareManagedProjectWorkspace(directory string) (*os.File, error) {
	parent := filepath.Dir(directory)
	if err := localfile.MkdirAllPrivate(parent); err != nil {
		return nil, fmt.Errorf("无法准备私有工作区父目录 %s (%w)", parent, err)
	}
	lock, err := localfile.Lock(directory+".lock", false)
	if errors.Is(err, localfile.ErrLocked) {
		return nil, errors.New("另一项操作正在登记该专用工作区")
	}
	if err != nil {
		return nil, fmt.Errorf("无法锁定专用工作区 %s (%w)", directory, err)
	}
	if err := localfile.MkdirAllPrivate(directory); err != nil {
		lock.Close()
		return nil, fmt.Errorf("无法准备私有工作区 %s (%w)", directory, err)
	}
	return lock, nil
}
