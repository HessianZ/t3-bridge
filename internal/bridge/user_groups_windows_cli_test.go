package bridge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstalledWindowsUserGroupsCLI(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	firstStandard := filepath.Join(root, "first standalone", "lark-cli.exe")
	secondStandard := filepath.Join(root, "second standalone", "lark-cli.exe")
	firstPath := filepath.Join(root, "first PATH")
	secondPath := filepath.Join(root, "second PATH")
	firstDirect := filepath.Join(firstPath, "lark-cli.exe")
	secondDirect := filepath.Join(secondPath, "lark-cli.exe")

	globalPrefix := filepath.Join(root, "custom npm prefix")
	localModules := filepath.Join(root, "project", "node_modules")
	localBin := filepath.Join(localModules, ".bin")
	appData := filepath.Join(root, "roaming")
	packageExe := filepath.Join("@larksuite", "cli", "bin", "lark-cli.exe")
	globalExe := filepath.Join(globalPrefix, "node_modules", packageExe)
	localExe := filepath.Join(localModules, packageExe)
	appDataExe := filepath.Join(appData, "npm", "node_modules", packageExe)

	scriptPrefix := filepath.Join(root, "npm without native binary")
	cmdShim := filepath.Join(scriptPrefix, "lark-cli.cmd")
	jsEntry := filepath.Join(scriptPrefix, "node_modules", "@larksuite", "cli", "bin", "lark-cli.js")
	brokenPrefix := filepath.Join(root, "npm with EXE directories")
	brokenExe := filepath.Join(brokenPrefix, "node_modules", packageExe)
	directoryExe := filepath.Join(brokenPrefix, "lark-cli.exe")

	for _, dir := range []string{localBin, brokenExe, directoryExe} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{
		firstStandard, secondStandard, firstDirect, secondDirect,
		globalExe, localExe, appDataExe, cmdShim, jsEntry,
		filepath.Join(firstPath, "node_modules", packageExe),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0700); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name          string
		standardPaths []string
		pathDirs      []string
		appData       string
		want          string
	}{
		{
			name:          "standalone order before PATH and APPDATA",
			standardPaths: []string{filepath.Join(root, "missing", "lark-cli.exe"), firstStandard, secondStandard},
			pathDirs:      []string{firstPath},
			appData:       appData,
			want:          firstStandard,
		},
		{
			name:     "direct EXE wins within the first PATH directory",
			pathDirs: []string{firstPath, secondPath},
			want:     firstDirect,
		},
		{
			name:     "global npm EXE without a shim wins over a later PATH directory",
			pathDirs: []string{globalPrefix, secondPath},
			appData:  appData,
			want:     globalExe,
		},
		{
			name:     "local npm EXE beside .bin without a shim",
			pathDirs: []string{localBin},
			want:     localExe,
		},
		{
			name:    "APPDATA npm EXE without PATH or a shim",
			appData: appData,
			want:    appDataExe,
		},
		{
			name:     "incomplete PATH installation falls back to APPDATA",
			pathDirs: []string{scriptPrefix, brokenPrefix},
			appData:  appData,
			want:     appDataExe,
		},
		{
			name:     "npm shim and JS without a native EXE are rejected",
			pathDirs: []string{scriptPrefix},
		},
		{
			name:     "PATH EXE directories are rejected",
			pathDirs: []string{brokenPrefix},
		},
		{
			name:          "standalone EXE directory is rejected",
			standardPaths: []string{directoryExe},
		},
		{
			name:          "standalone scripts are rejected",
			standardPaths: []string{cmdShim, jsEntry},
		},
		{
			name:          "relative standalone EXE is rejected",
			standardPaths: []string{filepath.Join("first standalone", "lark-cli.exe")},
		},
		{
			name:     "relative PATH directories are rejected",
			pathDirs: []string{"first PATH", "custom npm prefix", filepath.Join("project", "node_modules", ".bin")},
		},
		{
			name:    "relative APPDATA is rejected",
			appData: "roaming",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := installedWindowsUserGroupsCLI(tc.standardPaths, tc.pathDirs, tc.appData); got != tc.want {
				t.Fatalf("installedWindowsUserGroupsCLI() = %q, want %q", got, tc.want)
			}
		})
	}
}
