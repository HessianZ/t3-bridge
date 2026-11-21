package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const legacyBridgeName = "lark-channel-bridge"

// legacyBridgeExecutable accepts a startup option or discovers a host PATH entry.
func legacyBridgeExecutable(explicit string) (string, error) {
	windows := runtime.GOOS == "windows"
	if explicit != "" {
		if _, err := legacyCredentialArgs(explicit, windows); err != nil {
			return "", err
		}
		return explicit, nil
	}
	names := []string{legacyBridgeName}
	if windows {
		names = []string{legacyBridgeName + ".exe", legacyBridgeName + ".cmd"}
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if !filepath.IsAbs(dir) {
			continue
		}
		for _, name := range names {
			path := filepath.Join(dir, name)
			if _, err := legacyCredentialArgs(path, windows); err == nil {
				return path, nil
			}
		}
	}
	return "", nil
}

func legacyCredentialCommand(ctx context.Context, provider string) (*exec.Cmd, error) {
	args, err := legacyCredentialArgs(provider, runtime.GOOS == "windows")
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, args[0], args[1:]...), nil
}

func legacyCredentialArgs(provider string, windows bool) ([]string, error) {
	if !filepath.IsAbs(provider) {
		return nil, errors.New("旧桥必须指定绝对路径")
	}
	if !legacyRegularFile(provider) {
		return nil, errors.New("旧桥路径不存在或不是普通文件")
	}
	if !windows {
		if _, err := exec.LookPath(provider); err != nil {
			return nil, errors.New("旧桥文件不可执行")
		}
		return []string{provider, "secrets", "get"}, nil
	}

	dir := filepath.Dir(provider)
	entry := provider
	switch strings.ToLower(filepath.Ext(provider)) {
	case ".exe":
		return []string{provider, "secrets", "get"}, nil
	case ".mjs":
	default:
		switch strings.ToLower(filepath.Base(provider)) {
		case legacyBridgeName, legacyBridgeName + ".cmd", legacyBridgeName + ".ps1":
			// npm puts global shims in the prefix and local shims in node_modules/.bin.
			modules := filepath.Join(dir, "node_modules")
			if strings.EqualFold(filepath.Base(dir), ".bin") {
				modules = filepath.Dir(dir)
			}
			entry = filepath.Join(modules, legacyBridgeName, "bin", legacyBridgeName+".mjs")
			if !legacyRegularFile(entry) {
				return nil, errors.New("旧桥 npm shim 旁缺少包入口，请指定已安装的 .mjs 入口")
			}
		default:
			return nil, errors.New("Windows 旧桥需要 .exe、.mjs 入口或标准 npm shim")
		}
	}
	for _, nodeDir := range append([]string{dir}, filepath.SplitList(os.Getenv("PATH"))...) {
		if filepath.IsAbs(nodeDir) {
			node := filepath.Join(nodeDir, "node.exe")
			if legacyRegularFile(node) {
				return []string{node, entry, "secrets", "get"}, nil
			}
		}
	}
	return nil, errors.New("旧桥需要 node.exe，已检查入口所在目录和宿主 PATH")
}

func legacyRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
