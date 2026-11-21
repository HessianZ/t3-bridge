package bridge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

func installedUserGroupsCLI(ctx context.Context, standardPaths []string, systemPath string) string {
	if runtime.GOOS == "windows" {
		return installedWindowsUserGroupsCLI(standardPaths, filepath.SplitList(os.Getenv("PATH")), os.Getenv("APPDATA"))
	}
	if path := findUserGroupsCLI(standardPaths, false); path != "" {
		return path
	}
	path, err := exec.LookPath("lark-cli")
	if err != nil || !filepath.IsAbs(path) {
		path = ""
	}
	home, _ := os.UserHomeDir()
	asdfDir := os.Getenv("ASDF_DATA_DIR")
	if asdfDir == "" && filepath.IsAbs(home) {
		asdfDir = filepath.Join(home, ".asdf")
	}
	asdfShim := filepath.Join(asdfDir, "shims", "lark-cli")
	if path != "" && path != asdfShim {
		return findUserGroupsCLI([]string{path}, false)
	}
	if !filepath.IsAbs(asdfDir) {
		return ""
	}
	if _, err := os.Stat(asdfShim); err != nil {
		return ""
	}
	var asdfPaths []string
	for _, dir := range filepath.SplitList(systemPath) {
		asdfPaths = append(asdfPaths, filepath.Join(dir, "asdf"))
	}
	asdf := findUserGroupsCLI(asdfPaths, false)
	if asdf == "" {
		return ""
	}
	// Resolve the selected installation without invoking the shim or passing
	// OAuth data or app credentials to the version manager.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, asdf, "which", "lark-cli")
	cmd.Dir = home
	cmd.Env = userGroupsCLIEnv(os.Environ(), systemPath, "")
	var stdout userGroupsOutput
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil || stdout.truncated {
		return ""
	}
	path = strings.TrimSpace(stdout.String())
	if !filepath.IsAbs(path) {
		return ""
	}
	return findUserGroupsCLI([]string{path}, false)
}

func installedWindowsUserGroupsCLI(standardPaths, pathDirs []string, appData string) string {
	candidates := slices.Clone(standardPaths)
	if filepath.IsAbs(appData) {
		pathDirs = append(slices.Clone(pathDirs), filepath.Join(appData, "npm"))
	}
	for _, dir := range pathDirs {
		if !filepath.IsAbs(dir) {
			continue
		}
		dir = filepath.Clean(dir)
		modules := filepath.Join(dir, "node_modules")
		if strings.EqualFold(filepath.Base(dir), ".bin") && strings.EqualFold(filepath.Base(filepath.Dir(dir)), "node_modules") {
			modules = filepath.Dir(dir)
		}
		// The npm launcher can download a missing executable. Only run an
		// installed binary against the private CLI configuration.
		candidates = append(candidates, filepath.Join(dir, "lark-cli.exe"),
			filepath.Join(modules, "@larksuite", "cli", "bin", "lark-cli.exe"))
	}
	return findUserGroupsCLI(candidates, true)
}
