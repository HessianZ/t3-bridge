package bridge

import (
	"fmt"
	"strings"
)

const accountWorkbench = "配置工作台（接收端主机）：t3-bridge config gui -config CONFIG_PATH 或 t3-bridge config web -config CONFIG_PATH"

func (e *Engine) privateAdminAllowed(m Message) bool {
	return e.Config.privateAdminAllowed(m)
}

func (c Config) privateAdminAllowed(m Message) bool {
	return m.ChatType == "p2p" && m.Comment == nil && m.Meeting == nil && c.isAdmin(m.UserID) && c.Allowed(m)
}

func (e *Engine) accountControl(m Message, cmd *SessionCommand) {
	if !e.privateAdminAllowed(m) {
		cmd.Reply = "只有当前应用的所有者或管理员可以在已授权私聊查看接收账号或获取换绑步骤。"
		return
	}
	words := strings.Fields(routeText(m))
	switch {
	case len(words) == 1:
		tenant := e.Config.Tenant
		if tenant == "" {
			tenant = "feishu"
		}
		connection := "未知（暂无接收进程状态）"
		if e.ConnectionStatus != nil {
			if status := e.ConnectionStatus(); status != "" {
				connection = status
			}
		}
		cmd.Reply = fmt.Sprintf("当前接收账号\nApp ID：%s\n租户：%s\n连接：%s\n%s\n换绑步骤：/account change", e.Config.AppID, tenant, connection, accountWorkbench)
		cmd.AccountAdminReply = true
	case len(words) == 2 && words[1] == "change":
		cmd.Reply = "请在接收端主机操作。App Secret 请仅保存在主机私有凭据文件中，勿发到飞书。\n当前接收 App：" + e.Config.AppID + "\n" + accountWorkbench + `
1. 将 CONFIG_PATH 换成该实例实际使用的配置路径。受管实例可用 t3-bridge service list 确认实例名 INSTANCE，并核对其服务配置路径。
2. 在「扫码绑定飞书应用」选择「替换已有账号」，或在「手动替换账号」输入新 App 的密钥，也可引用已有私有凭据文件（appSecretFile）。核对替换后的默认 T3 路由、初始用户和独立状态目录。轮换同一 App 的密钥可使用「应用凭据」。
3. 点击「检查应用 API」及「校验并查看差异」。暂停输入，确认在途任务和通知已排空。在「运行状态」停止对应受管实例，或执行 t3-bridge service stop -instance INSTANCE。共用实例会停止其所有账号。
4. 保存到该实例的配置文件后，对新 App 运行接收预检并确认启用。在工作台启动实例，或执行 t3-bridge service start -instance INSTANCE。核对服务状态和新账号的飞书连接，在新账号私聊发送 /account。`
		cmd.AccountAdminReply = true
	default:
		cmd.Reply = "用法：/account 或 /account change。请勿在飞书发送 App Secret。"
	}
}
