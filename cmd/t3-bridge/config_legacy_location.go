package main

import (
	"errors"
	"os"
	"path/filepath"
)

// detectLegacyConfig checks the two configuration layouts used by the old bridge.
// The caller opens the selected file only when the user requests an import.
func detectLegacyConfig() (string, error) {
	roots, rootErr := legacyConfigRoots()
	for _, root := range roots {
		path, err := legacyConfigIn(root)
		if !errors.Is(err, os.ErrNotExist) {
			return path, err
		}
	}
	if rootErr != nil {
		return "", rootErr
	}
	return "", os.ErrNotExist
}

func legacyConfigRoots() ([]string, error) {
	if root, set := os.LookupEnv("LARK_CHANNEL_HOME"); set {
		if !filepath.IsAbs(root) {
			return nil, errors.New("LARK_CHANNEL_HOME must be absolute")
		}
		return []string{filepath.Clean(root)}, nil
	}

	var roots []string
	home, _ := os.UserHomeDir()
	if filepath.IsAbs(home) {
		roots = append(roots, filepath.Join(home, ".lark-channel"))
	}
	xdg, set := os.LookupEnv("XDG_CONFIG_HOME")
	if !set {
		if !filepath.IsAbs(home) {
			return roots, os.ErrNotExist
		}
		xdg = filepath.Join(home, ".config")
	}
	if !filepath.IsAbs(xdg) {
		return roots, errors.New("XDG_CONFIG_HOME must be absolute")
	}
	return append(roots, filepath.Join(xdg, "lark-channel-bridge")), nil
}

func legacyConfigIn(root string) (string, error) {
	if !filepath.IsAbs(root) {
		return "", errors.New("LARK_CHANNEL_HOME must be absolute")
	}
	path := filepath.Join(root, "config.json")
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("the old config.json must be a regular file")
	}
	return path, nil
}
