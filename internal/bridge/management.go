package bridge

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

type AccessPolicy struct {
	Users  map[string]bool            `json:"users,omitempty"`
	Admins map[string]bool            `json:"admins,omitempty"`
	Routes map[string]map[string]bool `json:"routes,omitempty"`
}

func (c Config) ownerID() string {
	if c.access == nil {
		return ""
	}
	c.access.mu.RLock()
	defer c.access.mu.RUnlock()
	return c.access.owner
}
func (c Config) userAllowed(user string) bool {
	if user == "" {
		return false
	}
	if user == c.ownerID() {
		return true
	}
	if c.access != nil {
		c.access.mu.RLock()
		allowed, known := c.access.policy.Users[user]
		c.access.mu.RUnlock()
		if known {
			return allowed
		}
	}
	return slices.Contains(c.Users, user)
}
func (c Config) userDenied(user string) bool {
	if user == c.ownerID() {
		return false
	}
	if c.access == nil {
		return false
	}
	c.access.mu.RLock()
	defer c.access.mu.RUnlock()
	allowed, known := c.access.policy.Users[user]
	return known && !allowed
}
func (e *Engine) publishPolicy() {
	policy := e.Store.snapshot().AccessPolicy
	if e.Config.access == nil {
		e.Config.access = &accessRuntime{}
	}
	e.Config.access.mu.Lock()
	e.Config.access.policy = policy
	e.Config.access.mu.Unlock()
}
func managementCommand(m Message) bool {
	w := strings.Fields(routeText(m))
	if len(w) == 0 {
		return false
	}
	if w[0] == "/doctor" || w[0] == "/reconnect" || w[0] == "/timeout" || w[0] == "/input" || w[0] == "/review" {
		return true
	}
	if len(w) > 1 && (w[0] == "/invite" || w[0] == "/remove") {
		return w[1] == "user" || w[1] == "admin" || w[1] == "route"
	}
	if groupMessageCommand(w) {
		return true
	}
	return len(w) > 1 && w[0] == "/config" && slices.Contains([]string{"concurrency", "progress", "reply", "tools", "process", "idle-timeout", "display", "run", "identity", "groups"}, w[1])
}

var openIDPattern = regexp.MustCompile(`^ou_[A-Za-z0-9_-]{1,100}$`)

func mentionedUser(m Message, token string) string {
	if token == "@all" || token == "@_all" {
		return ""
	}
	id := m.UserMentions[token]
	switch id {
	case "all", "all_members", "@all", "@_all":
		return ""
	}
	if id != "" && len(id) <= 128 {
		return id
	}
	if openIDPattern.MatchString(token) {
		return token
	}
	return ""
}
func (e *Engine) management(ctx context.Context, m Message, id string, cmd *SessionCommand, current *Task) error {
	words := strings.Fields(routeText(m))
	switch words[0] {
	case "/timeout":
		if len(words) > 1 && strings.HasPrefix(words[1], "comment:") {
			return e.docTaskControl(m, id, cmd, words[1:], true)
		}
		return e.timeout(m, id, cmd, current, words)
	case "/input":
		if current != nil && current.TextReply && !current.StopRequested && scopedTask(e.Config, m, current, current.RouteID) {
			cmd.Reply = "纯文本轮次请直接发送补充消息，或引用本轮消息继续。"
			return nil
		}
		if current == nil || current.StopRequested || !scopedTask(e.Config, m, current, current.RouteID) || current.CardID == "" || current.CardRequestID == "" || !currentStopReference(current, m) {
			cmd.Reply = "请先回复自己的可续聊任务卡，再发送 /input；旧轮/停止任务不能打开输入。"
			return nil
		}
		cmd.Reply = "已在当前卡片打开补充输入；提交文本会显示在卡片中，仅作继续讨论，不代替T3审批或生产授权。"
		return e.Store.update(func(st *State) error {
			t := st.Tasks[current.ID]
			t.InputOpen = true
			t.NotifyHash = ""
			t.NotificationIssue = ""
			saveCommandState(st, id, cmd)
			return nil
		})
	case "/reconnect":
		return e.reconnectCommand(ctx, m, id, cmd, words[1:])
	case "/review":
		return e.review(ctx, m, id, cmd, current, words)
	}
	if !e.Config.isAdmin(m.UserID) {
		cmd.Reply = "仅owner/admin可修改授权或运行设置；管理权不等于目标执行权。"
		return nil
	}
	if words[0] == "/config" {
		if groupMessageCommand(words) {
			if len(words) != 3 {
				cmd.Reply = "用法：" + groupMessageCheckCommand + " 或 " + groupMessageAuthorizeCommand
			} else {
				e.groupMessageControl(ctx, m, cmd, strings.Join(words, " "))
			}
			return nil
		}
		if len(words) == 1 || words[1] == "run" {
			e.prepareRunSettingsCard(m, cmd, words)
			return nil
		}
		if words[1] == "display" {
			e.prepareDisplaySettingsCard(m, cmd, words)
			return nil
		}
		return e.configureRunSettings(ctx, id, cmd, words[1:])
	}
	kind := words[1]
	route := ""
	tokens := words[2:]
	if len(tokens) == 0 || (kind == "route" && len(tokens) == 1) {
		cmd.Reply = "请在 /invite 或 /remove 后指定 user、admin 或 route <路由ID>，再填写一个或多个真实 @ 或 Open ID。"
		return nil
	}
	if kind == "route" {
		route, tokens = tokens[0], tokens[1:]
		if _, ok := e.Config.Routes[route]; !ok {
			cmd.Reply = "未知受控route，未改变权限。"
			return nil
		}
	}
	allow := words[0] == "/invite"
	mode := "deny"
	if allow {
		mode = "allow"
	}
	seen := make(map[string]bool, len(tokens))
	requests := make([]LocalAccessPolicyRequest, 0, len(tokens))
	for _, token := range tokens {
		user := mentionedUser(m, token)
		if user == "" {
			cmd.Reply = "请使用消息中的真实用户@或本应用open_id；不按名字/自称推断身份。"
			return nil
		}
		if !seen[user] {
			seen[user] = true
			requests = append(requests, LocalAccessPolicyRequest{Kind: kind, UserID: user, RouteID: route, Mode: mode})
		}
	}
	if len(requests) == 1 {
		cmd.Reply = fmt.Sprintf("已保存%s授权：%s=%t", kind, requests[0].UserID, allow)
	} else {
		cmd.Reply = fmt.Sprintf("已为 %d 人保存 %s 权限 · 允许 %t", len(requests), kind, allow)
	}
	if route != "" {
		cmd.Reply += " · route=" + route
	}
	cmd.Reply += "。下一条消息生效；用户/群/私聊门禁、route使用权与管理权分别判断，不增加生产写入授权。"
	err := e.updateAccessPolicy(ctx, requests, func(st *State) {
		saveCommandState(st, id, cmd)
	})
	if err == errAccessOwnerUnknown || err == errAccessOwnerRemoval {
		cmd.Reply = err.Error()
		return nil
	}
	return err
}
func saveCommandState(st *State, id string, cmd *SessionCommand) {
	if st.Commands == nil {
		st.Commands = map[string]*SessionCommand{}
	}
	copy := *cmd
	st.Commands[id] = &copy
}
func revokePending(c Config, st *State, user, route string) {
	for i, q := range st.Inbox {
		if q.UserID != user || immediate(c, st, q) {
			continue
		}
		if route == "" || q.RouteAtReceive == "" || q.RouteAtReceive == route {
			st.Inbox[i].Cancelled = true
		}
	}
	for _, t := range st.Tasks {
		if t.Source.UserID == user && (route == "" || t.RouteID == route) && slices.Contains([]string{"ready", "waiting_capacity", "waiting_resource", "opening"}, t.Phase) {
			t.Phase = "blocked"
			t.Summary = "授权已撤销，未派发；恢复权限也不会自动重试旧任务。"
			t.NotifyHash = ""
		}
	}
}
func (e *Engine) timeout(m Message, id string, cmd *SessionCommand, current *Task, w []string) error {
	st := e.Store.snapshot()
	if current != nil && m.ParentID != "" && m.CommandAction == "" && !currentStopReference(current, m) {
		cmd.Reply = "这是旧轮引用；请回复最新卡或无引用设置当前轮超时，未改变期限。"
		return nil
	}
	if cmd.RouteID == "" || !e.Config.TargetAllowed(m, cmd.Target, cmd.RouteID) {
		cmd.Reply = "没有已授权目标，未改变超时。"
		return nil
	}
	k := sessionKey(m, cmd.RouteID, cmd.Target)
	idleScope := idleTimeoutScope(m, cmd.RouteID, cmd.Target)
	if len(w) == 2 && w[1] == "default" {
		return e.idleTimeout(id, cmd, current, idleScope, w[1:])
	}
	if len(w) == 1 || (len(w) == 2 && (w[1] == "idle" || w[1] == "deadline")) {
		e.prepareTimeoutSettings(&st, m, cmd, current)
		return nil
	}
	if w[1] == "idle" {
		return e.idleTimeout(id, cmd, current, idleScope, w[2:])
	}
	args := w[1:]
	if args[0] == "deadline" {
		args = args[1:]
	}
	if len(args) != 1 {
		cmd.Reply = "用法：/timeout deadline <秒数|off>，范围1..86400秒。"
		return nil
	}
	n, ok := parseAbsoluteTimeout(args[0])
	if !ok {
		cmd.Reply = "绝对期限为1..86400秒，或off关闭。"
		return nil
	}
	cmd.Reply = fmt.Sprintf("已显式设置本用户当前scope/目标/项目超时=%d秒（0关闭）；在途从现在计时，尚未派发从Send前计时。仅停止Bridge原run，未知结果待核对，不代表回滚。", n)
	return e.Store.update(func(st *State) error {
		var task *Task
		if current != nil {
			task = st.Tasks[current.ID]
		}
		e.setAbsoluteTimeout(st, k, task, n)
		saveCommandState(st, id, cmd)
		return nil
	})
}

func parseAbsoluteTimeout(value string) (int, bool) {
	if value == "off" {
		return 0, true
	}
	n, err := strconv.Atoi(value)
	return n, err == nil && n >= 1 && n <= 86400
}

func (e *Engine) setAbsoluteTimeout(st *State, scope string, task *Task, seconds int) {
	if st.Timeouts == nil {
		st.Timeouts = map[string]int{}
	}
	st.Timeouts[scope] = seconds
	if task == nil || terminal(task.Phase) {
		return
	}
	task.TimeoutSeconds = seconds
	task.DeadlineAt = ""
	if seconds > 0 && task.RequestMessage != "" {
		task.DeadlineAt = e.now().Add(time.Duration(seconds) * time.Second).UTC().Format(time.RFC3339Nano)
	}
}

func parseIdleTimeout(value string) (int, bool) {
	if value == "off" {
		return 0, true
	}
	n, err := strconv.Atoi(value)
	return n, err == nil && n >= 1 && n <= 120
}

func idleTimeoutScope(m Message, route string, target Target) string {
	scope := sessionKey(m, route, target)
	if m.Comment != nil && m.Comment.CommentID != "" {
		return stable("comment-idle", scope, m.Comment.CommentID)
	}
	return scope
}

func (st *State) idleTimeout(scope string) int {
	if n, ok := st.IdleTimeouts[scope]; ok {
		return n
	}
	return st.Settings.IdleTimeoutMinutes
}

func (st *State) setIdleTimeout(scope string, task *Task, minutes int, inherit bool) int {
	if inherit {
		delete(st.IdleTimeouts, scope)
	} else {
		if st.IdleTimeouts == nil {
			st.IdleTimeouts = map[string]int{}
		}
		st.IdleTimeouts[scope] = minutes
	}
	minutes = st.idleTimeout(scope)
	if task != nil && !terminal(task.Phase) && !task.StopRequested {
		task.IdleTimeoutMinutes = minutes
	}
	return minutes
}

func (e *Engine) idleTimeout(id string, cmd *SessionCommand, current *Task, scope string, args []string) error {
	if len(args) != 1 {
		cmd.Reply = "用法：/timeout idle <1..120分钟|off|default>。"
		return nil
	}
	n, ok := parseIdleTimeout(args[0])
	inherit := args[0] == "default"
	if !ok && !inherit {
		cmd.Reply = "空闲超时为1..120分钟，或用 off 关闭、default 跟随默认值。"
		return nil
	}
	err := e.Store.update(func(st *State) error {
		var task *Task
		if current != nil {
			task = st.Tasks[current.ID]
		}
		n = st.setIdleTimeout(scope, task, n, inherit)
		cmd.Reply = fmt.Sprintf("已设置本轮及此范围后续轮次的空闲超时为%d分钟（0关闭）。", n)
		if inherit {
			cmd.Reply += "后续轮次跟随Bridge默认值。"
		}
		saveCommandState(st, id, cmd)
		return nil
	})
	if err == nil && current != nil {
		delete(e.idleRuns, current.ID)
	}
	return err
}
