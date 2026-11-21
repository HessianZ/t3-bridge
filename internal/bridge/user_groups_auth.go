package bridge

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

type UserGroupsChallenge struct {
	Ticket    string    `json:"ticket"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type userGroupsAuthTicket struct {
	Ticket     string    `json:"ticket"`
	AppID      string    `json:"appId"`
	Tenant     string    `json:"tenant"`
	Purpose    string    `json:"purpose"`
	URL        string    `json:"url"`
	ExpiresAt  time.Time `json:"expiresAt"`
	DeviceCode string    `json:"deviceCode,omitempty"` // Cleared once the CLI exchanges the code.
}

const userGroupsAuthTicketLimit = 64 << 10

var errUserGroupsAuthTicket = errors.New("授权票据无效或已过期，请重新发起授权")

func userGroupsAuthScope(purpose string) string {
	switch purpose {
	case "list":
		return userGroupsListScope
	case "invite":
		return userGroupsListScope + " " + userGroupsInviteScope
	default:
		return ""
	}
}

func (g *UserGroups) Start(ctx context.Context, purpose string) (UserGroupsChallenge, error) {
	scope := userGroupsAuthScope(purpose)
	if scope == "" {
		return UserGroupsChallenge{}, errors.New("未知的群聊授权用途")
	}
	lock, err := g.lock()
	if err != nil {
		return UserGroupsChallenge{}, err
	}
	defer lock.Close()
	path := g.userGroupsAuthPath()
	if pending, err := g.readUserGroupsAuthTicket(path); err != nil {
		return UserGroupsChallenge{}, err
	} else if pending.Ticket != "" {
		if pending.Purpose == purpose && userGroupsVerificationURL(pending.URL, pending.DeviceCode, pending.Tenant) {
			return UserGroupsChallenge{Ticket: pending.Ticket, URL: pending.URL, ExpiresAt: pending.ExpiresAt}, nil
		}
		return UserGroupsChallenge{}, errors.New("该应用已有待确认授权，请先完成、取消或等待过期")
	}
	if err := g.prepare(ctx); err != nil {
		return UserGroupsChallenge{}, err
	}
	issuedAt := time.Now()
	data, loginErr := g.command(ctx, "", "auth", "login", "--scope", scope, "--no-wait", "--json")
	// v1.0.96 keys this CLI-only scope cache by the device code. The bridge
	// verifies the requested scopes itself and keeps just its private ticket.
	if err := g.clearUserGroupsLoginCache(); err != nil {
		return UserGroupsChallenge{}, err
	}
	if loginErr != nil {
		return UserGroupsChallenge{}, errors.New("无法发起群聊授权，请检查本机 lark-cli 和网络")
	}
	tenant := g.account.Tenant
	verifyURL, code, lifetime, err := parseUserGroupsDeviceLogin(data, tenant)
	if err != nil {
		return UserGroupsChallenge{}, err
	}
	pending := userGroupsAuthTicket{
		Ticket: rand.Text(), AppID: g.account.AppID, Tenant: tenant,
		Purpose: purpose, URL: verifyURL, ExpiresAt: issuedAt.Add(lifetime).UTC(), DeviceCode: code,
	}
	if !time.Now().Before(pending.ExpiresAt) {
		return UserGroupsChallenge{}, errUserGroupsAuthTicket
	}
	if err := writeUserGroupsAuthTicket(path, pending, true); err != nil {
		return UserGroupsChallenge{}, err
	}
	return UserGroupsChallenge{Ticket: pending.Ticket, URL: verifyURL, ExpiresAt: pending.ExpiresAt}, nil
}

func (g *UserGroups) Finish(ctx context.Context, ticket string) (UserGroupsStatus, error) {
	lock, err := g.lock()
	if err != nil {
		return UserGroupsStatus{}, err
	}
	defer lock.Close()
	path := g.userGroupsAuthPath()
	pending, err := g.readUserGroupsAuthTicket(path)
	if err != nil {
		return UserGroupsStatus{}, err
	}
	if ticket == "" || ticket != pending.Ticket {
		return UserGroupsStatus{}, errUserGroupsAuthTicket
	}
	if pending.DeviceCode != "" {
		if err := g.prepare(ctx); err != nil {
			return UserGroupsStatus{}, err
		}
		pollCtx, cancel := context.WithDeadline(ctx, pending.ExpiresAt)
		_, err := g.command(pollCtx, "", "auth", "login", "--device-code", pending.DeviceCode, "--json")
		cancel()
		if err != nil {
			if !time.Now().Before(pending.ExpiresAt) {
				return UserGroupsStatus{}, errors.Join(errUserGroupsAuthTicket, os.Remove(path))
			}
			return UserGroupsStatus{}, errors.New("授权尚未核实，请在浏览器完成授权后重试")
		}
		pending.DeviceCode = ""
		if err := writeUserGroupsAuthTicket(path, pending, false); err != nil {
			return UserGroupsStatus{}, err
		}
	}
	status, err := g.status(ctx)
	if err != nil {
		return UserGroupsStatus{}, err
	}
	if err := os.Remove(path); err != nil {
		return UserGroupsStatus{}, err
	}
	if !status.ListAuthorized || (pending.Purpose == "invite" && !status.InviteAuthorized) {
		return UserGroupsStatus{}, errors.New("应用所有者未授予所需的群聊权限")
	}
	return status, nil
}

func (g *UserGroups) Cancel(_ context.Context, ticket string) error {
	lock, err := g.lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	path := g.userGroupsAuthPath()
	pending, err := g.readUserGroupsAuthTicket(path)
	if err != nil {
		return err
	}
	if ticket == "" || ticket != pending.Ticket {
		return errUserGroupsAuthTicket
	}
	return os.Remove(path)
}

func (g *UserGroups) userGroupsAuthPath() string {
	return filepath.Join(g.dir, "user-groups-auth.json")
}

func (g *UserGroups) readUserGroupsAuthTicket(path string) (userGroupsAuthTicket, error) {
	file, err := localfile.OpenPrivate(path)
	if errors.Is(err, os.ErrNotExist) {
		return userGroupsAuthTicket{}, nil
	}
	if err != nil {
		return userGroupsAuthTicket{}, err
	}
	data, err := io.ReadAll(io.LimitReader(file, userGroupsAuthTicketLimit+1))
	file.Close()
	if err != nil {
		return userGroupsAuthTicket{}, err
	}
	var pending userGroupsAuthTicket
	if len(data) > userGroupsAuthTicketLimit || json.Unmarshal(data, &pending) != nil ||
		pending.Ticket == "" || pending.AppID != g.account.AppID || pending.Tenant != g.account.Tenant ||
		userGroupsAuthScope(pending.Purpose) == "" || pending.ExpiresAt.IsZero() {
		return userGroupsAuthTicket{}, errUserGroupsAuthTicket
	}
	if !time.Now().Before(pending.ExpiresAt) {
		return userGroupsAuthTicket{}, os.Remove(path)
	}
	return pending, nil
}

func writeUserGroupsAuthTicket(path string, pending userGroupsAuthTicket, createOnly bool) error {
	data, err := json.Marshal(pending)
	if err != nil || len(data) > userGroupsAuthTicketLimit {
		return errors.New("无法保存群聊授权票据")
	}
	published, err := localfile.WritePrivateAtomic(path, data, createOnly)
	if err != nil && createOnly && published {
		return errors.Join(err, os.Remove(path))
	}
	return err
}

func parseUserGroupsDeviceLogin(data []byte, tenant string) (string, string, time.Duration, error) {
	var fields map[string]json.RawMessage
	var verifyURL, code string
	var seconds int64
	invalid := errors.New("lark-cli 返回的群聊授权信息无效")
	if json.Unmarshal(data, &fields) != nil ||
		json.Unmarshal(fields["verification_url"], &verifyURL) != nil ||
		json.Unmarshal(fields["device_code"], &code) != nil ||
		json.Unmarshal(fields["expires_in"], &seconds) != nil ||
		code == "" || strings.ContainsAny(code, "\x00 \t\r\n") || seconds <= 0 || seconds > (1<<63-1)/int64(time.Second) {
		return "", "", 0, invalid
	}
	if !userGroupsVerificationURL(verifyURL, code, tenant) {
		return "", "", 0, invalid
	}
	return verifyURL, code, time.Duration(seconds) * time.Second, nil
}

func userGroupsVerificationURL(verifyURL, code, tenant string) bool {
	u, err := url.Parse(verifyURL)
	host := "accounts.feishu.cn"
	if tenant == "lark" {
		host = "accounts.larksuite.com"
	}
	decoded, decodeErr := url.QueryUnescape(verifyURL)
	return err == nil && decodeErr == nil && u.Scheme == "https" && u.User == nil &&
		strings.EqualFold(u.Host, host) &&
		(code == "" || (!strings.Contains(verifyURL, code) && !strings.Contains(decoded, code)))
}

func (g *UserGroups) clearUserGroupsLoginCache() error {
	root, err := os.OpenRoot(g.dir)
	if err == nil {
		defer root.Close()
		err = root.RemoveAll(filepath.Join("cache", "auth_login_scopes"))
	}
	if err != nil {
		return errors.New("无法清理 lark-cli 群聊授权缓存")
	}
	return nil
}
