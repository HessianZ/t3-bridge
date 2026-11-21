package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

type UserGroups struct {
	account Config
	dir     string

	cliPath      string
	owner        func(context.Context) (string, error)
	joinedGroups func(context.Context) ([]FeishuGroup, error)
}

type UserGroupsStatus struct {
	ListAuthorized   bool `json:"listAuthorized"`
	InviteAuthorized bool `json:"inviteAuthorized"`
}

type UserGroupsPage struct {
	Groups        []UserGroup `json:"groups"`
	NextPageToken string      `json:"nextPageToken"`
	BotKnown      bool        `json:"botKnown"`
}

type UserGroup struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	BotJoined bool   `json:"botJoined"`
}

type UserGroupInvitation struct {
	State string `json:"state"`
}

var userGroupsAppID = regexp.MustCompile(`^cli_[A-Za-z0-9_-]{1,124}$`)

func NewUserGroups(account Config, root string) (*UserGroups, error) {
	if account.Tenant == "" {
		account.Tenant = "feishu"
	}
	if !userGroupsAppID.MatchString(account.AppID) || !filepath.IsAbs(account.AppSecretFile) ||
		!filepath.IsAbs(root) || (account.Tenant != "feishu" && account.Tenant != "lark") {
		return nil, errors.New("群管理需要有效的应用 ID、租户及本机凭据和配置目录")
	}
	key := sha256.Sum256([]byte(account.Tenant + "\x00" + account.AppID))
	return &UserGroups{
		account: account,
		dir:     filepath.Join(root, "user-groups", fmt.Sprintf("%x", key)),
	}, nil
}

func (g *UserGroups) lock() (io.Closer, error) {
	if err := localfile.MkdirAllPrivate(g.dir); err != nil {
		return nil, err
	}
	return localfile.Lock(filepath.Join(g.dir, "profile.lock"), false)
}

func (g *UserGroups) Status(ctx context.Context) (UserGroupsStatus, error) {
	lock, err := g.lock()
	if err != nil {
		return UserGroupsStatus{}, err
	}
	defer lock.Close()
	return g.status(ctx)
}

func (g *UserGroups) List(ctx context.Context, query, pageToken string) (UserGroupsPage, error) {
	lock, err := g.lock()
	if err != nil {
		return UserGroupsPage{}, err
	}
	defer lock.Close()
	status, err := g.status(ctx)
	if err != nil {
		return UserGroupsPage{}, err
	}
	if !status.ListAuthorized {
		return UserGroupsPage{}, errors.New("请先使用当前应用所有者账号授权读取群列表")
	}
	args := []string{"im", "+chat-list", "--as", "user", "--json", "--sort", "active_time"}
	if query = strings.TrimSpace(query); query != "" {
		args = []string{"im", "+chat-search", "--as", "user", "--json", "--query", query,
			"--search-types", "private,public_joined,external"}
	}
	if pageToken != "" {
		args = append(args, "--page-token", pageToken)
	}
	out, err := g.command(ctx, "", args...)
	if err != nil {
		return UserGroupsPage{}, err
	}
	var response struct {
		OK       bool   `json:"ok"`
		Identity string `json:"identity"`
		Data     *struct {
			Chats []struct {
				ID   string `json:"chat_id"`
				Name string `json:"name"`
			} `json:"chats"`
			HasMore   bool   `json:"has_more"`
			PageToken string `json:"page_token"`
		} `json:"data"`
	}
	if json.Unmarshal(out, &response) != nil || !response.OK || response.Identity != "user" || response.Data == nil {
		return UserGroupsPage{}, errors.New("群管理 CLI 未返回有效的用户群列表")
	}
	page := UserGroupsPage{Groups: make([]UserGroup, 0, len(response.Data.Chats))}
	if response.Data.HasMore {
		if response.Data.PageToken == "" || response.Data.PageToken == pageToken {
			return UserGroupsPage{}, errors.New("群管理 CLI 未返回有效的下一页令牌")
		}
		page.NextPageToken = response.Data.PageToken
	}
	botGroups, err := g.botGroups(ctx)
	page.BotKnown = err == nil
	joined := make(map[string]bool, len(botGroups))
	if page.BotKnown {
		for _, group := range botGroups {
			joined[group.ID] = true
		}
	}
	for _, chat := range response.Data.Chats {
		if groupChatIDPattern.MatchString(chat.ID) {
			page.Groups = append(page.Groups, UserGroup{ID: chat.ID, Name: chat.Name, BotJoined: joined[chat.ID]})
		}
	}
	return page, nil
}

func (g *UserGroups) Invite(ctx context.Context, chatID string) (UserGroupInvitation, error) {
	if !groupChatIDPattern.MatchString(chatID) {
		return UserGroupInvitation{}, errors.New("群 ID 必须为有效的 oc_ 标识")
	}
	lock, err := g.lock()
	if err != nil {
		return UserGroupInvitation{}, err
	}
	defer lock.Close()
	status, err := g.status(ctx)
	if err != nil {
		return UserGroupInvitation{}, err
	}
	if !status.ListAuthorized || !status.InviteAuthorized {
		return UserGroupInvitation{}, errors.New("请先使用当前应用所有者账号授权邀请机器人")
	}
	isJoined := func(groups []FeishuGroup) bool {
		return slices.ContainsFunc(groups, func(group FeishuGroup) bool { return group.ID == chatID })
	}
	if groups, err := g.botGroups(ctx); err == nil && isJoined(groups) {
		return UserGroupInvitation{State: "joined"}, nil
	}
	if err := ctx.Err(); err != nil {
		return UserGroupInvitation{}, err
	}
	data := `{"id_list":["` + g.account.AppID + `"]}` // NewUserGroups validates the App ID.
	out, err := g.command(ctx, "", "im", "chat.members", "create", "--chat-id", chatID,
		"--member-id-type", "app_id", "--succeed-type", "1", "--data", data, "--as", "user", "--json")
	var exit *exec.ExitError
	var commandErr *userGroupsCommandError
	switch {
	case errors.As(err, &exit) && exit.ExitCode() == 10:
		return UserGroupInvitation{State: "confirmation-required"}, nil
	case errors.As(err, &commandErr) && commandErr.rejected:
		return UserGroupInvitation{State: "rejected"}, nil
	case err == nil:
		if state := userGroupsInvitationState(out, g.account.AppID); state == "pending" || state == "rejected" {
			return UserGroupInvitation{State: state}, nil
		}
	}
	// Confirm membership even when Feishu accepts the request. The write may
	// also have reached Feishu before the CLI lost its response.
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	groups, err := g.botGroups(checkCtx)
	if err == nil && isJoined(groups) {
		return UserGroupInvitation{State: "joined"}, nil
	}
	return UserGroupInvitation{State: "unknown"}, nil
}

func userGroupsInvitationState(out []byte, appID string) string {
	var response struct {
		OK       bool   `json:"ok"`
		Identity string `json:"identity"`
		Data     *struct {
			Invalid    []string `json:"invalid_id_list"`
			NotExisted []string `json:"not_existed_id_list"`
			Pending    []string `json:"pending_approval_id_list"`
		} `json:"data"`
	}
	if json.Unmarshal(out, &response) != nil || !response.OK || response.Identity != "user" || response.Data == nil {
		return ""
	}
	data := response.Data
	if slices.Contains(data.Invalid, appID) || slices.Contains(data.NotExisted, appID) {
		return "rejected"
	}
	if slices.Contains(data.Pending, appID) {
		return "pending"
	}
	if len(data.Invalid)+len(data.NotExisted)+len(data.Pending) > 0 {
		return ""
	}
	return "joined"
}

func (g *UserGroups) lookupOwner(ctx context.Context) (string, error) {
	if g.owner != nil {
		return g.owner(ctx)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client, tenantAuth, err := feishuAppReadClient(ctx, g.account.AppID, g.account.AppSecretFile, g.account.Tenant)
	if err != nil {
		return "", err
	}
	owner := lookupFeishuAppOwner(ctx, client, g.account.AppID, tenantAuth)
	if owner == "" {
		return "", errors.New("无法核实当前应用所有者")
	}
	return owner, nil
}

func (g *UserGroups) botGroups(ctx context.Context) ([]FeishuGroup, error) {
	if g.joinedGroups != nil {
		return g.joinedGroups(ctx)
	}
	return ListFeishuGroups(ctx, g.account.AppID, g.account.AppSecretFile, g.account.Tenant)
}
