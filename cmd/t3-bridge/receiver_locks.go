package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func lockProfileRuntime(path string) (*os.File, error) {
	prefix := profileLockPrefix(path)
	if prefix == "" {
		return nil, errors.New("invalid named profile path")
	}
	lock, err := localfile.Lock(prefix+".receiver.lock", false)
	if errors.Is(err, localfile.ErrLocked) {
		return nil, errors.New("this profile is already running; stop it before archiving or starting another receiver")
	}
	return lock, err
}

func profileLockPrefix(path string) string {
	dir := filepath.Dir(path)
	name, root := filepath.Base(dir), filepath.Dir(dir)
	if filepath.Base(path) != "config.json" || filepath.Base(root) != "profiles" || !serviceInstanceName.MatchString(name) {
		return ""
	}
	// Holding an open file inside a directory can prevent its rename on Windows.
	return filepath.Join(root, "."+name)
}

// Every receiver under the same user configuration directory shares these locks.
// Holding all locks before starting sockets avoids partial startup on a duplicate app.
func acquireReceiverLocks(registry profileRegistry, accounts []bridge.Config) (func(), error) {
	dir := filepath.Join(registry.root, "receiver-locks")
	if err := localfile.MkdirAllPrivate(dir); err != nil {
		return nil, fmt.Errorf("create the local receiver lock directory: %w", err)
	}
	locks := make([]*os.File, 0, len(accounts))
	release := func() {
		for _, lock := range locks {
			_ = lock.Close()
		}
	}
	for _, account := range accounts {
		lock, err := localfile.Lock(receiverAppLockPath(registry, account.Tenant, account.AppID), false)
		if err != nil {
			release()
			if errors.Is(err, localfile.ErrLocked) {
				return nil, fmt.Errorf("Feishu app %s already has a receiver under this user", account.AppID)
			}
			return nil, fmt.Errorf("lock Feishu app %s: %w", account.AppID, err)
		}
		locks = append(locks, lock)
	}
	return release, nil
}

func receiverAppLockPath(registry profileRegistry, tenant, appID string) string {
	if tenant == "" {
		tenant = "feishu"
	}
	name := configRevision([]byte(tenant+"\x00"+appID)) + ".lock"
	return filepath.Join(registry.root, "receiver-locks", name)
}
