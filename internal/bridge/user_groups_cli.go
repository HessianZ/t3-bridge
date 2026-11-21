package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	userGroupsListScope   = "im:chat:read"
	userGroupsInviteScope = "im:chat.members:write_only"
	userGroupsOutputLimit = 1 << 20
)

type userGroupsCLIStatus struct {
	AppID      string `json:"appId"`
	Brand      string `json:"brand"`
	Identities struct {
		User struct {
			Available   bool   `json:"available"`
			Status      string `json:"status"`
			TokenStatus string `json:"tokenStatus"`
			OpenID      string `json:"openId"`
			Scope       string `json:"scope"`
		} `json:"user"`
	} `json:"identities"`
}

// prepare is called under the profile lock by explicit device authorization.
func (g *UserGroups) prepare(ctx context.Context) error {
	if err := g.prepareCLIProfile(); err != nil {
		return err
	}
	_, err := g.cliStatus(ctx)
	return err
}

func (g *UserGroups) cliStatus(ctx context.Context) (userGroupsCLIStatus, error) {
	var status userGroupsCLIStatus
	out, err := g.command(ctx, "", "auth", "status", "--json")
	if err != nil {
		return status, err
	}
	if json.Unmarshal(out, &status) != nil || status.AppID != g.account.AppID || status.Brand != g.account.Tenant {
		return userGroupsCLIStatus{}, errors.New("无法确认群管理 CLI 的应用和租户身份")
	}
	return status, nil
}

func (g *UserGroups) status(ctx context.Context) (UserGroupsStatus, error) {
	_, credential, err := g.readCLIProfile()
	if errors.Is(err, os.ErrNotExist) {
		return UserGroupsStatus{}, nil
	}
	if err != nil || credential.ID != g.account.AppSecretFile {
		return UserGroupsStatus{}, err
	}
	status, err := g.cliStatus(ctx)
	if err != nil {
		return UserGroupsStatus{}, err
	}
	user := status.Identities.User
	if !user.Available || (user.TokenStatus != "valid" && user.Status != "ready" && user.Status != "needs_refresh") ||
		!strings.HasPrefix(user.OpenID, "ou_") || len(user.OpenID) == len("ou_") {
		return UserGroupsStatus{}, nil
	}
	owner, err := g.lookupOwner(ctx)
	if err != nil {
		return UserGroupsStatus{}, err
	}
	if owner != user.OpenID {
		return UserGroupsStatus{}, nil
	}
	var result UserGroupsStatus
	for _, scope := range strings.Fields(user.Scope) {
		switch scope {
		case userGroupsListScope:
			result.ListAuthorized = true
		case userGroupsInviteScope:
			result.InviteAuthorized = true
		}
	}
	return result, nil
}

type userGroupsCommandError struct {
	exit     *exec.ExitError
	rejected bool
}

func (e *userGroupsCommandError) Error() string {
	return fmt.Sprintf("群管理 CLI 执行失败（退出码 %d）", e.exit.ExitCode())
}

func (e *userGroupsCommandError) Unwrap() error { return e.exit }

func userGroupsCLIRejected(out []byte) bool {
	var response struct {
		OK    *bool `json:"ok"`
		Error struct {
			Type string `json:"type"`
			Code int    `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(out, &response) != nil || response.OK == nil || *response.OK {
		return false
	}
	switch response.Error.Type {
	case "authorization", "validation", "config":
		return true
	case "api":
		// A platform business error has a Feishu code. HTTP and transport
		// failures may have sent the write and require a membership check.
		return response.Error.Code > 599
	default:
		return false
	}
}

func (g *UserGroups) command(ctx context.Context, input string, args ...string) ([]byte, error) {
	paths, searchPath := userGroupsCLIPaths()
	path := g.cliPath
	if path == "" {
		path = installedUserGroupsCLI(ctx, paths, searchPath)
	}
	if path == "" {
		return nil, errors.New("在本机受信任的安装目录中找不到 lark-cli")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = g.dir
	cmd.Env = userGroupsCLIEnv(os.Environ(), searchPath, g.dir)
	cmd.Stdin = strings.NewReader(input)
	cmd.WaitDelay = time.Second
	var stdout, stderr userGroupsOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 10 {
		return nil, &userGroupsCommandError{exit: exit}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if stdout.truncated || stderr.truncated {
		return nil, errors.New("群管理 CLI 输出超过限制")
	}
	if exit != nil {
		return stdout.Bytes(), &userGroupsCommandError{exit: exit,
			rejected: userGroupsCLIRejected(stderr.Bytes()) || userGroupsCLIRejected(stdout.Bytes())}
	}
	if err != nil {
		return nil, errors.New("无法运行群管理 CLI")
	}
	return stdout.Bytes(), nil
}

type userGroupsOutput struct {
	buffer    bytes.Buffer
	truncated bool
}

func (b *userGroupsOutput) Write(data []byte) (int, error) {
	remaining := userGroupsOutputLimit - b.buffer.Len()
	if len(data) > remaining {
		b.truncated = true
	}
	b.buffer.Write(data[:min(len(data), remaining)])
	return len(data), nil
}

func (b *userGroupsOutput) Bytes() []byte { return b.buffer.Bytes() }

func (b *userGroupsOutput) String() string { return b.buffer.String() }

func userGroupsCLIEnv(in []string, searchPath, dir string) []string {
	env := make([]string, 0, len(in)+4)
	for _, entry := range in {
		name, _, _ := strings.Cut(entry, "=")
		name = strings.ToUpper(name)
		if name == "PATH" || strings.HasPrefix(name, "LARKSUITE_") || strings.HasPrefix(name, "LARK_CLI_") ||
			name == "LARK_CHANNEL" || strings.HasPrefix(name, "LARK_CHANNEL_") ||
			strings.HasPrefix(name, "OPENCLAW_") || strings.HasPrefix(name, "HERMES_") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "PATH="+searchPath, "LARKSUITE_CLI_CONFIG_DIR="+dir,
		"LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1", "LARKSUITE_CLI_NO_SKILLS_NOTIFIER=1")
}

func userGroupsCLIPaths() ([]string, string) {
	if runtime.GOOS == "windows" {
		return []string{`C:\Program Files\lark-cli\lark-cli.exe`, `C:\Program Files\lark-cli.exe`}, `C:\Windows\System32;C:\Windows`
	}
	const searchPath = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
	var paths []string
	for _, dir := range filepath.SplitList(searchPath) {
		paths = append(paths, filepath.Join(dir, "lark-cli"))
	}
	return paths, searchPath
}

func findUserGroupsCLI(paths []string, windows bool) string {
	for _, candidate := range paths {
		if !filepath.IsAbs(candidate) {
			continue
		}
		path, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			continue
		}
		if windows && !strings.EqualFold(filepath.Ext(path), ".exe") {
			continue
		}
		// Run the installed native program instead of an npm launcher that can
		// download another executable.
		if strings.HasSuffix(filepath.ToSlash(path), "/@larksuite/cli/scripts/run.js") {
			path, err = filepath.EvalSymlinks(filepath.Join(filepath.Dir(path), "..", "bin", "lark-cli"))
			if err != nil {
				continue
			}
		}
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() &&
			(windows || info.Mode().Perm()&0111 != 0 && info.Mode().Perm()&0022 == 0) {
			return path
		}
	}
	return ""
}
