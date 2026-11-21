package bridge

import (
	"errors"
	"strings"
	"testing"
)

func TestDocControlStatusAndScope(t *testing.T) {
	for _, tc := range []struct {
		name, chat, text, user, parent string
		commentRoute                   string
		policy                         AccessPolicy
		tool                           *ToolContext
		want                           string
	}{
		{name: "disabled", chat: "p2p", text: "/doc bind OLD_WORKSPACE", user: "user", want: "设置「云文档评论路由 ID」（commentRoute）"},
		{name: "enabled", chat: "p2p", text: "/doc", user: "user", commentRoute: "mac", want: "在支持的文档评论中 @ 机器人"},
		{name: "enabled in a group", chat: "group", text: "/doc", user: "user", commentRoute: "mac", want: "在支持的文档评论中 @ 机器人"},
		{name: "route access revoked", chat: "p2p", text: "/doc", user: "user", commentRoute: "mac",
			policy: AccessPolicy{Routes: map[string]map[string]bool{"mac": {"user": false}}}, want: "账号权限及评论路由授权"},
		{name: "group access only", chat: "group", text: "/doc", user: "guest", commentRoute: "mac",
			policy: AccessPolicy{Routes: map[string]map[string]bool{"mac": {"guest": true}}}, want: "账号权限及评论路由授权"},
		{name: "private tool binding", chat: "p2p", text: "/doc", user: "user", commentRoute: "mac",
			tool: &ToolContext{UserBinding: &ToolUserBinding{UserOpenID: "user"}}, want: "账号权限及评论路由授权"},
		{name: "unknown reference", chat: "p2p", text: "/doc", user: "user", parent: "unknown", commentRoute: "mac",
			want: "引用任务不明或不属于当前用户/目标"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, x, lark, _ := testEngine(t)
			e.Config.access = &accessRuntime{policy: tc.policy}
			e.Config.DefaultRoute = "nas"
			e.Config.CommentRoute = tc.commentRoute
			target := e.Config.Routes["mac"]
			target.ProjectID, target.ProjectName, target.ProjectPath, target.ToolContext = "PRIVATE_PROJECT_ID", "PRIVATE_PROJECT_NAME", "/private/comment-project", tc.tool
			e.Config.Routes["mac"] = target
			lark.onResolve = func() { t.Error("information command resolved message content") }
			m := message("doc-status", tc.text)
			m.UserID, m.ChatType, m.ParentID = tc.user, tc.chat, tc.parent
			accept(t, e, m)
			tick(t, e)
			st := e.Store.snapshot()
			cmd := st.Commands[key(m, m.ID)]
			if cmd == nil || cmd.Kind != "/doc" || cmd.Issue != "" || len(lark.replies) != 1 || cmd.Reply != lark.replies[0] || !strings.Contains(cmd.Reply, tc.want) {
				t.Fatalf("expected a /doc receipt and reply containing %q: %+v, %q", tc.want, cmd, lark.replies)
			}
			if userScope(cmd.Source) != userScope(m) || len(st.Inbox) != 0 || len(st.Tasks) != 0 || x.preflights != 0 || x.opens != 0 || x.sends != 0 || lark.notifications != 0 {
				t.Fatal("command changed source scope or dispatched work")
			}
			for _, hidden := range []string{"mac", target.EnvironmentID, target.ProjectID, target.ProjectName, target.ProjectPath, target.Model, "OLD_WORKSPACE"} {
				if strings.Contains(cmd.Reply, hidden) {
					t.Errorf("reply exposed %q: %s", hidden, cmd.Reply)
				}
			}
		})
	}
}

func TestDocControlRetriesOriginalReceipt(t *testing.T) {
	e, _, lark, _ := testEngine(t)
	e.Config.CommentRoute = "mac"
	m := message("doc-retry", "/doc")
	id := key(m, m.ID)
	lark.replyErr = errors.New("synthetic unconfirmed reply")
	accept(t, e, m)
	tick(t, e)
	first := e.Store.snapshot().Commands[id]
	if first == nil || first.Issue == "" || first.ReplyAt == "" {
		t.Fatalf("missing pending receipt: %+v", first)
	}
	e.Config.CommentRoute = ""
	lark.replyErr = nil
	accept(t, e, m)
	tick(t, e)
	accept(t, e, m)
	tick(t, e)
	st := e.Store.snapshot()
	settled := st.Commands[id]
	if settled == nil || settled.Issue != "" || settled.Reply != first.Reply || settled.ReplyAt != first.ReplyAt || len(st.Commands) != 1 || len(st.Inbox) != 0 || len(lark.replies) != 2 || lark.replies[0] != lark.replies[1] {
		t.Fatalf("retry did not settle the original receipt: first=%+v, settled=%+v, replies=%q", first, settled, lark.replies)
	}
}

func TestDocControlRouteNamesAndHelp(t *testing.T) {
	for _, kind := range []string{"available", "id", "host", "alias"} {
		t.Run(kind, func(t *testing.T) {
			e, x, lark, _ := testEngine(t)
			route, target := "nas", e.Config.Routes["nas"]
			switch kind {
			case "id":
				delete(e.Config.Routes, route)
				route = "doc"
			case "host":
				target.HostName = "doc"
			case "alias":
				target.Aliases = []string{"doc"}
			}
			e.Config.Routes[route] = target
			if err := e.Config.Validate(); err != nil {
				t.Fatalf("route no longer validates: %v", err)
			}
			accept(t, e, message("help", "/help"))
			tick(t, e)
			if len(lark.replies) != 1 || strings.Contains(lark.replies[0], "/doc 查看") != (kind == "available") {
				t.Fatalf("incorrect /doc discovery: %q", lark.replies)
			}
			if kind != "available" {
				accept(t, e, message("route", "/doc 继续处理"))
				tick(t, e)
				if task := onlyTask(t, e); task.RouteID != route || x.sends != 1 || len(lark.replies) != 1 {
					t.Fatalf("configured route lost precedence: route=%q, sends=%d, replies=%q", task.RouteID, x.sends, lark.replies)
				}
			}
		})
	}
}

func TestDocControlKeepsDocumentCommentOnCommentRoute(t *testing.T) {
	e, x, lark, _ := testEngine(t)
	e.Config.CommentRoute = "mac"
	m := Message{ID: "doc-comment", UserID: "user", ChatID: "document:fake", Text: "/doc", Comment: &CommentSource{FileType: "docx", FileToken: "fake", CommentID: "thread"}}
	accept(t, e, m)
	tick(t, e)
	if task := onlyTask(t, e); task.RouteID != "mac" || x.sends != 1 || len(lark.replies) != 0 || len(e.Store.snapshot().Commands) != 0 {
		t.Fatalf("comment changed command scope: route=%q, sends=%d, replies=%q", task.RouteID, x.sends, lark.replies)
	}
}
