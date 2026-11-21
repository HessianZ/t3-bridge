package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

var errConfigRevisionConflict = errors.New("config revision conflict: reload the source before saving")

func configRevision(raw []byte) string {
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func saveConfigCandidate(path string, candidate []byte, expectedRevision string) (string, error) {
	if !strings.EqualFold(filepath.Ext(path), ".json") {
		return "", errors.New("config must be JSON (.json)")
	}
	createOnly := expectedRevision == "missing"
	if !createOnly {
		if decoded, err := hex.DecodeString(expectedRevision); err != nil || len(decoded) != sha256.Size {
			return "", errors.New("expected config revision must be a SHA-256 hex digest or missing")
		}
	}
	if _, err := bridge.ParseConfig(candidate); err != nil {
		return "", err
	}
	revision := configRevision(candidate)

	if createOnly {
		if err := ensureConfigParent(filepath.Dir(path)); err != nil {
			return "", err
		}
	}
	lock, err := lockConfigFile(path)
	if err != nil {
		return "", err
	}
	defer lock.Close()

	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if !createOnly {
			return "", errConfigRevisionConflict
		}
	case err != nil:
		return "", err
	case !info.Mode().IsRegular():
		return "", errors.New("config save requires a regular file or a new path")
	case createOnly:
		return "", errConfigRevisionConflict
	default:
		original, err := bridge.ReadConfigJSON(path)
		if err != nil {
			return "", err
		}
		originalRevision := configRevision(original)
		if !strings.EqualFold(originalRevision, expectedRevision) {
			return "", errConfigRevisionConflict
		}
		if originalRevision == revision {
			if private, err := localfile.OpenPrivate(path); err == nil {
				return revision, private.Close()
			}
		}
		if _, err := localfile.WritePrivateAtomic(path+".backup.json", original, false); err != nil {
			return "", fmt.Errorf("back up config: %w", err)
		}
	}

	published, err := localfile.WritePrivateAtomic(path, candidate, createOnly)
	if err != nil {
		if published {
			return revision, fmt.Errorf("configuration published but could not confirm durability: %w", err)
		}
		if createOnly && errors.Is(err, os.ErrExist) {
			return "", errConfigRevisionConflict
		}
		return "", fmt.Errorf("save config: %w", err)
	}
	return revision, nil
}

func lockConfigFile(path string) (*os.File, error) {
	// The lock file stays in place so concurrent writers always lock the same inode.
	return localfile.Lock(configLockPath(path), true)
}

func configLockPath(path string) string {
	lockPath := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".lock")
	if prefix := profileLockPrefix(path); prefix != "" {
		lockPath = prefix + ".config.lock"
	}
	return lockPath
}

func ensureConfigParent(dir string) error {
	info, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return localfile.MkdirAllPrivate(dir)
	}
	if err == nil && !info.IsDir() {
		return errors.New("configuration parent is not a directory")
	}
	return err
}

func hasConfigBackup(path string) bool {
	if path == "" {
		return false
	}
	file, err := localfile.OpenPrivate(path + ".backup.json")
	if err != nil {
		return false
	}
	return file.Close() == nil
}

func readConfigBackup(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("没有可用的配置备份")
	}
	backup, err := bridge.ReadPrivateConfigJSON(path + ".backup.json")
	if err != nil {
		return nil, errors.New("没有可用的私有配置备份")
	}
	return backup, nil
}
