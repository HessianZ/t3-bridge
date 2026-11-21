package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

var legacyAppLockUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func checkPreflightReceivers(report *bridge.ReceiverPreflight, now time.Time) {
	profiles, profileErr := localProfiles()
	legacyHomes, legacyErr := legacyConfigRoots()
	for i := range report.Accounts {
		account := &report.Accounts[i]
		current := checkReceiverAppLock(profiles, profileErr, *account)
		legacy := checkLegacyReceiverAppLock(legacyHomes, legacyErr, account.AppID, now)
		account.Checks = append([]bridge.ReceiverPreflightCheck{current, legacy}, account.Checks...)
		if current.Status != "ok" || legacy.Status != "ok" {
			report.MarkAccountFailed(account.Enabled)
		}
	}
}

func checkReceiverAppLock(profiles profileRegistry, profileErr error, account bridge.ReceiverPreflightAccount) bridge.ReceiverPreflightCheck {
	check := bridge.ReceiverPreflightCheck{Check: "receiver-lock", Status: "unknown", Detail: "无法核对本机 t3-bridge 接收锁"}
	if profileErr != nil {
		return check
	}
	lock, err := localfile.LockExisting(receiverAppLockPath(profiles, account.Tenant, account.AppID), false)
	switch {
	case errors.Is(err, localfile.ErrLocked):
		check.Status, check.Detail = "failed", "同租户同 App 的 t3-bridge 接收锁正被占用"
	case err == nil:
		_ = lock.Close()
		check.Status, check.Detail = "ok", "当前用户的 t3-bridge 接收锁可用"
	case errors.Is(err, os.ErrNotExist):
		check.Status, check.Detail = "ok", "当前用户目录中没有该 App 的 t3-bridge 接收锁"
	}
	return check
}

func checkLegacyReceiverAppLock(homes []string, homeErr error, appID string, now time.Time) bridge.ReceiverPreflightCheck {
	// The legacy bridge uses proper-lockfile with an app-level lock and a 30 s stale threshold.
	const staleAfter = 30 * time.Second
	check := bridge.ReceiverPreflightCheck{Check: "legacy-receiver-lock", Status: "ok", Detail: "已检查的旧桥目录中没有该 App 的接收锁"}
	if homeErr != nil {
		check.Status, check.Detail = "unknown", "无法确认旧桥配置目录"
	}
	name := legacyAppLockUnsafe.ReplaceAllString(appID, "_") + ".lock"
	for _, home := range homes {
		target := filepath.Join(home, "registry", "locks", "app", name)
		info, err := os.Lstat(target + ".lock")
		switch {
		case errors.Is(err, os.ErrNotExist):
			continue
		case err != nil || !info.IsDir():
			check.Status, check.Detail = "unknown", "无法核对旧桥接收锁"
		case now.Sub(info.ModTime()) > staleAfter:
			check.Status, check.Detail = "unknown", "旧桥接收锁已失去心跳，请核对原实例"
		default:
			check.Status, check.Detail = "failed", "发现仍有心跳的旧桥接收锁，请先停止同 App 的旧实例"
			check.LockHolder = readLegacyReceiverLockHolder(target, appID)
			return check
		}
	}
	return check
}

// The heartbeat determines whether the lock blocks startup. Its sidecar
// provides only a profile and PID hint for the operator.
func readLegacyReceiverLockHolder(target, appID string) *bridge.ReceiverLockHolder {
	const maxBytes = 4 << 10
	file, err := localfile.OpenPrivate(target + ".meta.json")
	if err != nil {
		return nil
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || len(data) > maxBytes {
		return nil
	}
	var meta struct {
		Kind   string `json:"kind"`
		Target string `json:"target"`
		AppID  string `json:"appId"`
		bridge.ReceiverLockHolder
	}
	if json.Unmarshal(data, &meta) != nil || meta.Kind != "app" ||
		filepath.Clean(meta.Target) != target || meta.AppID != appID || meta.PID <= 0 {
		return nil
	}
	profile := meta.Profile
	if profile == "" || len(profile) > 128 || profile == "." || profile == ".." ||
		strings.ContainsAny(profile, `/\:*?"<>|`) ||
		strings.ContainsFunc(profile, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) {
		return nil
	}
	return &meta.ReceiverLockHolder
}
