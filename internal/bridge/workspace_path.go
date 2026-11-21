package bridge

import (
	"path"
	"strings"
	"unicode"
)

// IsAbsoluteWorkspacePath reports whether raw is an accepted absolute T3 workspace path.
func IsAbsoluteWorkspacePath(raw string) bool {
	return normalizeWorkspacePath(raw) != ""
}

// Normalize syntax on the target host, which can differ from the bridge host.
// The configured project ID and T3's project readback still establish identity.
func normalizeWorkspacePath(raw string) string {
	if raw == "" || len(raw) > 4096 || strings.ContainsFunc(raw, unicode.IsControl) {
		return ""
	}
	if strings.HasPrefix(raw, "/") {
		return path.Clean(raw)
	}
	windows := strings.ReplaceAll(raw, `\`, "/")
	if strings.HasPrefix(raw, `\\`) {
		server, rest, hasShare := strings.Cut(windows[2:], "/")
		share, tail, _ := strings.Cut(rest, "/")
		if !hasShare || server == "" || server == "." || server == ".." ||
			share == "" || share == "." || share == ".." || strings.ContainsAny(windows, `:<>"|?*`) {
			return ""
		}
		volume := `\\` + server + `\` + share
		tail = path.Clean("/" + tail)
		if tail == "/" {
			return volume
		}
		return volume + strings.ReplaceAll(tail, "/", `\`)
	}
	if len(windows) < 3 || windows[1:3] != ":/" || strings.ContainsAny(windows[3:], `:<>"|?*`) {
		return ""
	}
	drive := windows[0]
	if drive >= 'a' && drive <= 'z' {
		drive -= 'a' - 'A'
	}
	if drive < 'A' || drive > 'Z' {
		return ""
	}
	return string(drive) + ":" + strings.ReplaceAll(path.Clean(windows[2:]), "/", `\`)
}

func sameWorkspacePath(first, second string) bool {
	clean := normalizeWorkspacePath(first)
	return clean != "" && clean == normalizeWorkspacePath(second)
}

func workspaceDirectoryName(root string) string {
	if strings.HasPrefix(root, "/") {
		return path.Base(root)
	}
	return path.Base(strings.ReplaceAll(root, `\`, "/"))
}
