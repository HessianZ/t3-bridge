package bridge

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

var localT3EnvironmentID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// DiscoverLocalT3 reads the standard runtime file and its sibling identity.
// The regular MCP connection still verifies the server's identity on connect.
func DiscoverLocalT3() (string, string, error) {
	base := os.Getenv("T3CODE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "", errors.New("无法定位本机 T3 目录，请手填运行文件路径")
		}
		base = filepath.Join(home, ".t3")
	}
	if !filepath.IsAbs(base) {
		return "", "", errors.New("T3CODE_HOME 必须是绝对路径，请手填运行文件路径")
	}
	dir := filepath.Join(base, "userdata")
	runtimeFile := filepath.Join(dir, "server-runtime.json")
	runtime, err := localfile.OpenRegular(runtimeFile)
	if err != nil {
		return "", "", errors.New("无法读取本机 T3 运行文件，请启动 T3 或手填路径")
	}
	defer runtime.Close()
	if _, err := readMCPRuntimePort(runtime); err != nil {
		return "", "", errors.New("本机 T3 运行文件没有有效端口")
	}
	identity, err := localfile.OpenRegular(filepath.Join(dir, "environment-id"))
	if err != nil {
		return "", "", errors.New("无法读取本机 T3 环境 ID，请启动 T3 或手填环境 ID")
	}
	defer identity.Close()
	const maxIdentityBytes = 256
	raw, err := io.ReadAll(io.LimitReader(identity, maxIdentityBytes+1))
	environmentID := strings.TrimSpace(string(raw))
	if err != nil || len(raw) > maxIdentityBytes || !localT3EnvironmentID.MatchString(environmentID) || environmentID == "REPLACE_ENVIRONMENT_ID" {
		return "", "", errors.New("本机 T3 环境 ID 格式不正确")
	}
	return runtimeFile, environmentID, nil
}
