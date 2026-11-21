package bridge

import (
	"fmt"
	"strconv"
	"strings"
)

const accessPolicyListCommand = "/config access"

func isAccessPolicyList(words []string) bool {
	return len(words) >= 3 && words[0] == "/config" && words[1] == "access"
}

func (e *Engine) prepareAccessPolicyList(st *State, m Message, cmd *SessionCommand) {
	cmd.Kind, cmd.RouteID, cmd.Target, cmd.BaseTargetID = accessPolicyListCommand, "", Target{}, ""
	cmd.Card, cmd.ReplyCard = nil, false
	if !e.privateAdminAllowed(m) {
		cmd.Reply = "仅当前应用的所有者或管理员可在已授权私聊查看权限清单。"
		return
	}
	if e.Store == nil || e.Config.AppID == "" || st.AccountID != e.Config.AppID {
		cmd.Reply = "接收端未绑定到当前应用，无法查看权限清单。"
		return
	}
	if m.ParentID != "" || m.ControlTask != "" || m.SessionTask != "" || m.CommandAction != "" {
		cmd.Reply = "请直接发送权限清单命令，例如 /config access user。"
		return
	}
	const usage = "请发送 /config access user [页码]、/config access admin [页码] 或 /config access route <路由ID> [页码]。"
	words := strings.Fields(routeText(m))
	if !isAccessPolicyList(words) {
		cmd.Reply = usage
		return
	}
	kind, args := words[2], words[3:]
	policy := st.AccessPolicy
	var route, owner string
	var configured []string
	var overrides map[string]bool
	switch kind {
	case "user":
		owner, configured, overrides = e.Config.ownerID(), e.Config.Users, policy.Users
	case "admin":
		owner, configured, overrides = e.Config.ownerID(), e.Config.Admins, policy.Admins
	case "route":
		if len(args) == 0 || len(args) > 2 {
			cmd.Reply = usage
			return
		}
		route, args = args[0], args[1:]
		target, ok := e.Config.Routes[route]
		if !ok {
			cmd.Reply = "未知受控路由。"
			return
		}
		configured, overrides = target.Users, policy.Routes[route]
	default:
		cmd.Reply = usage
		return
	}
	if len(args) > 1 {
		cmd.Reply = usage
		return
	}
	const invalidPage = "页码无效或超出范围。"
	page := 1
	if len(args) == 1 {
		var err error
		page, err = strconv.Atoi(args[0])
		if err != nil || page < 1 || strings.HasPrefix(args[0], "+") {
			cmd.Reply = invalidPage
			return
		}
	}
	const pageSize = 6
	entries := appendAccessEntries(nil, kind, route, owner, configured, overrides)
	pages := max(1, (len(entries)+pageSize-1)/pageSize)
	if page > pages {
		cmd.Reply = invalidPage
		return
	}
	label := kind
	if route != "" {
		label += " " + route
	}
	command := accessPolicyListCommand + " " + label
	var reply strings.Builder
	fmt.Fprintf(&reply, "权限清单 · %s · 第 %d/%d 页", label, page, pages)
	if kind == "route" {
		reply.WriteString("\n逐路由 ACL 仅表示本层权限。实际使用还需通过其他授权层和目标可用性检查。")
	}
	if len(entries) == 0 {
		reply.WriteString("\n当前没有权限条目。")
	}
	for _, entry := range entries[(page-1)*pageSize : min(page*pageSize, len(entries))] {
		fmt.Fprintf(&reply, "\n%s · 配置 %t · 运行覆盖 %s · 本层生效 %t", entry.UserID, entry.Configured, entry.Override, entry.Effective)
		if kind != "route" {
			status := "未知"
			switch {
			case owner == entry.UserID && owner != "":
				status = "是"
			case owner != "":
				status = "否"
			}
			reply.WriteString(" · 所有者 " + status)
		}
	}
	if page > 1 {
		fmt.Fprintf(&reply, "\n上一页 · %s %d", command, page-1)
	}
	if page < pages {
		fmt.Fprintf(&reply, "\n下一页 · %s %d", command, page+1)
	}
	cmd.Reply = reply.String()
}
