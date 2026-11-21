package bridge

import (
	"errors"
	"strings"
	"testing"
)

func TestAccountControlPermissionsAndReplies(t *testing.T) {
	for _, tc := range []struct {
		name, user, chat, command, tenant, status, parent string
		want                                              []string
		readsStatus                                       int
	}{
		{name: "owner", user: "owner", chat: "p2p", command: "/account", status: "飞书WS已就绪", readsStatus: 1,
			want: []string{"App ID：fake-app", "租户：feishu", "飞书WS已就绪", "t3-bridge config web"}},
		{name: "admin", user: "user", chat: "p2p", command: "/account", tenant: "lark",
			want: []string{"App ID：fake-app", "租户：lark", "连接：未知", "t3-bridge config gui"}},
		{name: "group", user: "owner", chat: "group", command: "/account", status: "private-status",
			want: []string{"已授权私聊"}},
		{name: "group-change", user: "user", chat: "group", command: "/account change",
			want: []string{"已授权私聊"}},
		{name: "member", user: "other", chat: "p2p", command: "/account", status: "private-status",
			want: []string{"已授权私聊"}},
		{name: "member-change", user: "other", chat: "p2p", command: "/account change",
			want: []string{"已授权私聊"}},
		{name: "change", user: "user", chat: "p2p", command: "/account change", status: "private-status",
			want: []string{"config gui -config CONFIG_PATH", "config web -config CONFIG_PATH", "扫码绑定飞书应用", "私有凭据文件", "appSecretFile", "校验并查看差异", "service stop -instance INSTANCE", "service start -instance INSTANCE"}},
		{name: "extra-argument", user: "user", chat: "p2p", command: "/account change SYNTHETIC_SECRET",
			want: []string{"用法：/account 或 /account change"}},
		{name: "unknown-reference", user: "owner", chat: "p2p", command: "/account", status: "private-status", parent: "unknown",
			want: []string{"引用任务不明或不属于当前用户/目标"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, x, lark, _ := testEngine(t)
			e.Config.access = &accessRuntime{owner: "owner"}
			e.Config.Admins = []string{"user"}
			e.Config.Tenant = tc.tenant
			reads := 0
			if tc.status != "" {
				e.ConnectionStatus = func() string { reads++; return tc.status }
			}
			m := message(tc.name, tc.command)
			m.UserID, m.ChatType, m.ParentID = tc.user, tc.chat, tc.parent
			accept(t, e, m)
			tick(t, e)
			st := e.Store.snapshot()
			cmd := st.Commands[key(m, m.ID)]
			if len(lark.replies) != 1 || cmd == nil || cmd.Kind != "/account" || cmd.Reply != lark.replies[0] || cmd.Card != nil || cmd.Issue != "" {
				t.Fatalf("unexpected account receipt: %+v, replies: %q", cmd, lark.replies)
			}
			for _, want := range tc.want {
				if !strings.Contains(cmd.Reply, want) {
					t.Errorf("reply missing %q: %s", want, cmd.Reply)
				}
			}
			for _, hidden := range []string{e.Config.AppSecretFile, "private-status", "SYNTHETIC_SECRET"} {
				if strings.Contains(cmd.Reply, hidden) {
					t.Errorf("reply exposes %q", hidden)
				}
			}
			if tc.chat == "group" || tc.user == "other" || tc.parent != "" {
				if strings.Contains(cmd.Reply, e.Config.AppID) || strings.Contains(cmd.Reply, "config gui") {
					t.Errorf("rejected command disclosed account details: %s", cmd.Reply)
				}
			}
			if reads != tc.readsStatus || len(st.Tasks) != 0 || x.preflights != 0 || x.opens != 0 || x.sends != 0 {
				t.Fatal("command queried unexpected status or dispatched work")
			}
		})
	}
}

func TestAccountControlRetriesOriginalReceipt(t *testing.T) {
	e, _, lark, _ := testEngine(t)
	e.Config.Admins = []string{"user"}
	status := "飞书WS已就绪"
	e.ConnectionStatus = func() string { return status }
	m := message("account-status", "/account")
	m.ChatType = "p2p"
	id := key(m, m.ID)
	lark.replyErr = errors.New("synthetic unconfirmed reply")
	accept(t, e, m)
	tick(t, e)
	first := e.Store.snapshot().Commands[id]
	if first == nil || first.Issue == "" || first.ReplyAt == "" {
		t.Fatalf("missing pending receipt: %+v", first)
	}
	status = "飞书WS连接中"
	lark.replyErr = nil
	accept(t, e, m)
	tick(t, e)
	accept(t, e, m)
	tick(t, e)
	st := e.Store.snapshot()
	settled := st.Commands[id]
	if settled == nil || settled.Issue != "" || settled.Reply != first.Reply || settled.ReplyAt != first.ReplyAt || len(st.Commands) != 1 || len(st.Inbox) != 0 {
		t.Fatalf("retry did not settle the original receipt: first=%+v, settled=%+v", first, settled)
	}
	if len(lark.replies) != 2 || lark.replies[0] != lark.replies[1] {
		t.Fatalf("unexpected duplicate or changed reply: %q", lark.replies)
	}
}

func TestAccountControlRetryRechecksAdmin(t *testing.T) {
	e, _, lark, _ := testEngine(t)
	e.Config.Admins = []string{"user"}
	m := message("account-revoked", "/account")
	m.ChatType = "p2p"
	lark.replyErr = errors.New("synthetic uncertain delivery")
	accept(t, e, m)
	tick(t, e)
	if len(lark.replies) != 1 {
		t.Fatal("expected one delivery attempt while authorized")
	}
	e.Config.Admins = nil
	lark.replyErr = nil
	tick(t, e)
	state := e.Store.snapshot()
	receipt := state.Commands[key(m, m.ID)]
	if len(lark.replies) != 1 || len(state.Inbox) != 0 || receipt == nil || receipt.Issue != "" || receipt.Reply != "账号查看权限已撤销。" {
		t.Fatal("authorization was not applied to the pending reply")
	}
}

func TestAccountControlRetriesDenial(t *testing.T) {
	e, _, lark, _ := testEngine(t)
	m := message("account-denied", "/account")
	m.ChatType = "p2p"
	lark.replyErr = errors.New("synthetic uncertain delivery")
	accept(t, e, m)
	tick(t, e)
	lark.replyErr = nil
	tick(t, e)
	st := e.Store.snapshot()
	if len(lark.replies) != 2 || lark.replies[0] != lark.replies[1] || len(st.Inbox) != 0 {
		t.Fatalf("denial did not retry the original reply: %q", lark.replies)
	}
}

func TestAccountControlPreservesConfiguredRouteNames(t *testing.T) {
	for _, kind := range []string{"id", "host", "alias"} {
		t.Run(kind, func(t *testing.T) {
			e, x, lark, _ := testEngine(t)
			route, target := "nas", e.Config.Routes["nas"]
			switch kind {
			case "id":
				delete(e.Config.Routes, route)
				route = "account"
			case "host":
				target.HostName = "account"
			case "alias":
				target.Aliases = []string{"account"}
			}
			e.Config.Routes[route] = target
			if err := e.Config.Validate(); err != nil {
				t.Fatalf("existing route no longer validates: %v", err)
			}
			m := message("route", "/account change")
			m.ChatType = "p2p"
			accept(t, e, m)
			tick(t, e)
			if task := onlyTask(t, e); task.RouteID != route || x.sends != 1 || len(lark.replies) != 0 {
				t.Fatalf("configured route lost precedence: route=%q, sends=%d, replies=%q", task.RouteID, x.sends, lark.replies)
			}
		})
	}
}
