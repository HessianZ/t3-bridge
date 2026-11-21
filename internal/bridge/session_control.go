package bridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

type SessionCommand struct {
	ReviewTaskID       string       `json:"reviewTaskId,omitempty"`
	ReceiverStopID     string       `json:"receiverStopId,omitempty"`
	ReconnectAttempted bool         `json:"reconnectAttempted,omitempty"`
	WorkspacePath      string       `json:"workspacePath,omitempty"`
	ProjectAttempted   bool         `json:"projectAttempted,omitempty"`
	RegistrationID     string       `json:"registrationId,omitempty"`
	BaseTargetID       string       `json:"baseTargetId,omitempty"`
	Source             Message      `json:"source"`
	Kind               string       `json:"kind"`
	RouteID            string       `json:"routeId,omitempty"`
	Target             Target       `json:"target"`
	Name               string       `json:"name,omitempty"`
	CreateAt           string       `json:"createAt,omitempty"`
	ChatID             string       `json:"chatId,omitempty"`
	WelcomeAt          string       `json:"welcomeAt,omitempty"`
	Welcomed           bool         `json:"welcomed,omitempty"`
	Reply              string       `json:"reply,omitempty"`
	ReplyCard          bool         `json:"replyCard,omitempty"`
	RouteSelectionText bool         `json:"routeSelectionText,omitempty"`
	AccountAdminReply  bool         `json:"accountAdminReply,omitempty"`
	DocAdminReply      bool         `json:"docAdminReply,omitempty"`
	ChatAdminReply     bool         `json:"chatAdminReply,omitempty"`
	Card               *CommandCard `json:"card,omitempty"`
	ReplyAt            string       `json:"replyAt,omitempty"`
	Issue              string       `json:"issue,omitempty"`

	GroupMessageGrant        *groupMessageGrant `json:"groupMessageGrant,omitempty"`
	GroupMessageGrantUntil   time.Time          `json:"groupMessageGrantUntil,omitzero"`
	GroupMessageScopePending bool               `json:"groupMessageScopePending,omitempty"`
}
type sessionMessenger interface {
	ReplyControl(context.Context, Message, string, bool) error
	CreatePrivateChat(context.Context, Message, string, string) (string, error)
	WelcomeChat(context.Context, string, string, string) error
}

var sessionCommands = []string{"/doctor", "/reconnect", "/timeout", "/input", "/review", "/cd", "/ws", "/new", "/reset", "/status", "/help", "/resume", "/result", "/stop", "/cancel", "/invite", "/remove", "/config", "/ps", "/exit"}

// A slash word is a command; a path with a directory component remains ordinary text.
func slashWord(c Config, text string) string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return ""
	}
	word := words[0]
	if word == "/" || !strings.HasPrefix(word, "/") || strings.ContainsAny(word[1:], "/\\.") {
		return ""
	}
	if ids := c.namedRoutes(strings.TrimPrefix(word, "/"), false); len(ids) == 1 {
		return ""
	}
	// /account and /doc stay out of sessionCommands so existing route names remain valid.
	if word == "/meeting" || word == "/account" || word == "/doc" {
		return word
	}
	if !slices.Contains(sessionCommands, word) {
		if _, err := os.Stat(word); err == nil {
			return ""
		}
	}
	return word
}
func (e *Engine) sessionControl(ctx context.Context, m Message) (bool, error) {
	word := slashWord(e.Config, routeText(m))
	if word == "/config" {
		words := strings.Fields(routeText(m))
		switch {
		case len(words) > 1 && words[1] == "groups":
			word = groupAccessCardCommand
		case len(words) > 1 && words[1] == "route":
			word = routeSelectionCommand
		case isAccessPolicyList(words):
			word = accessPolicyListCommand
		}
	}
	if word == "/cd" {
		words := strings.Fields(routeText(m))
		if len(words) > 1 && (words[1] == "list" || words[1] == "use") {
			word = workspaceProjectsCommand
		}
	}
	privateSettings := word == "/config" && m.ChatType == "p2p" && strings.TrimSpace(routeText(m)) == word
	if m.CommandAction == "" && (word == "" || ((word == "/stop" || word == "/cancel") && strings.TrimSpace(routeText(m)) == word) || ((word == "/invite" || word == "/remove" || word == "/config") && !managementCommand(m) && !privateSettings)) {
		return false, nil
	}
	st := e.Store.snapshot()
	id := key(m, m.ID)
	cmd := st.Commands[id]
	var button CommandButton
	var card *SessionCommand
	if m.CommandAction != "" {
		card, button = commandCardAction(e.Config, &st, m)
		if card == nil || userScope(card.Source) != userScope(m) || (cmd == nil && card.Card.Used && button.changesState()) {
			return true, e.drop(m)
		}
		word = button.Command
		if cmd == nil && (taskListCommand(word) || word == groupAccessCardCommand) && !e.privateAdminAllowed(m) {
			return true, e.drop(m)
		}
	}
	if cmd != nil && cmd.ReconnectAttempted && cmd.Reply == "" {
		cmd.Reply = "本次重连已尝试，结果待 /doctor 核对；未重复重连或重放任务。"
	}
	if cmd != nil && cmd.Kind == "/cd" && cmd.ProjectAttempted && cmd.Reply == "" {
		args := strings.TrimSpace(strings.TrimPrefix(routeText(m), "/cd"))
		if err := e.workspaceControl(ctx, m, id, cmd, args); err != nil {
			return true, err
		}
	}
	if cmd != nil && cmd.Kind == routeSelectionCommand && !e.routeSelectionReplyAllowed(&st, cmd) {
		return true, e.drop(m)
	}
	if cmd != nil && cmd.Kind == workspaceProjectsCommand &&
		(!e.workspaceProjectsAllowed(&st, m, cmd.RouteID) || cmd.BaseTargetID != targetIdentity(e.Config.Routes[cmd.RouteID])) {
		return true, e.drop(m)
	}
	if cmd != nil && privateSettingsControl(cmd.Kind) && !e.privateAdminAllowed(m) {
		return true, e.Store.update(func(st *State) error {
			st.Commands[id].GroupMessageGrant = nil
			st.Commands[id].GroupMessageScopePending = false
			removeInbox(st, m)
			return nil
		})
	}
	if cmd != nil && (cmd.AccountAdminReply || cmd.DocAdminReply || cmd.ChatAdminReply) && !e.privateAdminAllowed(m) {
		cmd.Reply = "账号查看权限已撤销。"
		switch {
		case cmd.ChatAdminReply:
			cmd.Reply = "聊天任务管理权限已撤销。"
		case cmd.DocAdminReply:
			cmd.Reply = "评论任务管理权限已撤销。"
		}
		cmd.AccountAdminReply, cmd.DocAdminReply, cmd.ChatAdminReply = false, false, false
		cmd.Issue = ""
		return true, e.Store.update(func(st *State) error {
			saveCommandState(st, id, cmd)
			removeInbox(st, m)
			return nil
		})
	}
	if cmd == nil {
		cmd = &SessionCommand{Source: m, Kind: word, RouteID: selectedRoute(e.Config, &st, m)}
		var identityArgs []string
		identityCommand := false
		if card == nil && word == "/config" {
			words := strings.Fields(routeText(m))
			if len(words) > 1 && words[1] == "identity" {
				identityCommand, identityArgs = true, words[2:]
			}
		}
		returnFromTaskList := card != nil && word == "/status" && (card.Card.TaskList != nil || taskListCommand(card.Kind))
		if card != nil && !returnFromTaskList {
			cmd.RouteID = card.RouteID
		}
		cmd.Target = effectiveTarget(e.Config, &st, m, cmd.RouteID)
		var current *Task
		var otherResumeProject bool
		if card == nil || cmd.RouteID != "" || returnFromTaskList {
			current = currentTask(e.Config, &st, m, cmd.RouteID)
		}
		if card == nil && word != groupAccessCardCommand && word != accessPolicyListCommand && word != routeSelectionCommand && word != workspaceProjectsCommand && (m.ParentID != "" || m.ControlTask != "" || m.SessionTask != "") {
			reference := boundTask(&st, m)
			if identityCommand {
				cmd.Reply = "请直接发送 /config identity 查看当前私聊的工具身份。"
			} else if reference == nil || !scopedTask(e.Config, m, reference, reference.RouteID) {
				cmd.Reply = "引用任务不明或不属于当前用户/目标；请直接发送命令或回复自己的任务结果。"
			} else if reference.Source.Meeting != nil && word != "/result" && word != "/meeting" {
				cmd.Reply = "请用 /meeting 管理会议任务，或引用该任务发送 /result。"
			} else {
				current = reference
				cmd.RouteID = reference.RouteID
				cmd.Target = reference.Target
				if word == "/resume" {
					otherResumeProject = !sameResumeProject(effectiveTarget(e.Config, &st, m, reference.RouteID), reference.Target)
				}
				if reference.Source.Meeting == nil {
					rememberWorkspace(e.Config, &st, m, reference.RouteID, reference.Target)
				}
			}
		}
		if current != nil && cmd.RouteID == "" {
			cmd.RouteID = current.RouteID
			cmd.Target = current.Target
		}
		cmd.BaseTargetID = targetIdentity(e.Config.Routes[cmd.RouteID])
		reset := false
		resetClick := card != nil && word == "/new"
		var resume *resumeSelection
		var toolIdentitySelection string
		var routeSelection string
		var groupAccessChange *groupAccessCardButton
		resumeClick := card != nil && word == "/resume" && button.TaskID != ""
		displayClick := card != nil && word == displaySettingsCommand && button.Settings != nil
		settingsSubmit := card != nil && word == runSettingsCardCommand
		timeoutSubmit := card != nil && word == timeoutSettingsCommand
		taskListStop := taskListCommand(word) && button.TaskID != ""
		if cmd.Reply == "" {
			switch word {
			case "/doctor", doctorEchoCommand:
				if handled, err := e.doctorCommand(ctx, m, cmd, current, card, button); handled || err != nil {
					return true, err
				}
			case "/meeting":
				if queued, err := e.meetingControl(ctx, &st, id, cmd); queued || err != nil {
					return true, err
				}
			case "/account":
				e.accountControl(m, cmd)
			case groupAccessCardCommand:
				groupAccessChange = e.prepareGroupAccessCard(ctx, &st, m, cmd, card, button)
			case accessPolicyListCommand:
				e.prepareAccessPolicyList(&st, m, cmd)
			case routeSelectionCommand:
				routeSelection = e.prepareRouteSelection(&st, m, cmd, card, button)
			case "/doc":
				e.docControl(&st, m, cmd)
			case chatTasksCommand, docTasksCommand:
				if !taskListStop {
					e.managedTasksControl(&st, m, cmd, word, button.TaskListArgs)
				}
			case "/reconnect", "/timeout", "/input", "/review", "/invite", "/remove", "/config":
				if identityCommand {
					toolIdentitySelection = e.prepareToolIdentitySelection(&st, m, cmd, identityArgs)
					break
				}
				if card != nil && word == "/timeout" {
					if err := e.timeout(m, id, cmd, current, []string{word}); err != nil {
						return true, err
					}
					break
				}
				if err := e.management(ctx, m, id, cmd, current); err != nil {
					return true, err
				}
			case displaySettingsCommand:
				cmd.Reply = "展示设置卡已失效。请重新发送 /config display。"
			case runSettingsCardCommand:
				cmd.Reply = "运行设置卡已失效。请重新发送 /config run。"
			case toolIdentityCommand:
				cmd.Reply = "工具身份选项已失效。请重新发送 /config identity。"
			case timeoutSettingsCommand:
				cmd.Reply = "超时设置卡已失效。请重新发送 /timeout。"
			case groupMessageCheckCommand, groupMessageAuthorizeCommand:
				e.groupMessageControl(ctx, m, cmd, word)
			case workspaceProjectsCommand:
				if err := e.workspaceProjectControl(ctx, m, id, cmd, card, button); err != nil {
					return true, err
				}
			case "/cd", "/ws":
				args := strings.TrimSpace(strings.TrimPrefix(routeText(m), word))
				if card != nil {
					if button.WorkspacePage != 0 {
						if e.workspaceCardScopeMatches(&st, m, card) {
							e.workspaceList(&st, m, cmd, button.WorkspacePage)
						} else {
							cmd.Reply = staleWorkspaceCard
						}
						break
					}
					if button.RemoveWorkspace {
						if err := e.removeWorkspaceAlias(m, id, cmd, button.Name); err != nil {
							return true, err
						}
						break
					}
					args = "list"
					if button.Workspace != nil {
						args = "use " + button.Name
					}
				}
				if err := e.workspaceControl(ctx, m, id, cmd, args); err != nil {
					return true, err
				}
			case "/help":
				cmd.Reply = fmt.Sprintf("/doctor 低敏诊断 · /input 原卡补充 · /review 独立只读复核\n/timeout 超时设置 · /timeout deadline <秒数|off> 绝对期限 · /timeout idle <分钟|off|default> 空闲超时\n/reconnect 本Bridge重连（admin）\n/cd [绝对路径] 工作项目 · /ws list|save|use|remove 命名工作区\n/new、/reset 新上下文 · /new chat [群名] 建私有群\n/status 当前会话 · /result [轮次令牌] [页码] 取脱敏全文 · /resume [page 页码] 查看历史 · /resume [--stop] <短令牌> 切换\n/stop、/cancel 停止当前Bridge任务 · /routeId 内容 选择受控目标\n群管理（owner/admin）：/invite group、/remove group、/invite all group、/config、/config group on|off、/config mention on|off\n动态授权（owner/admin）：/invite|/remove user|admin <真实@或open_id>，/invite|/remove route <routeId> <真实@或open_id>\n/config concurrency 0..%d · /config progress on|off · /config reply [card|text|markdown] · /config idle-timeout <分钟|off>\n群默认需@；使用权不等于生产写入授权。", MaxRunConcurrency)
				cmd.Reply += "\n/ps 查看接收实例 · /exit <实例ID> 关闭当前接收端（管理员私聊）"
				cmd.Reply += "\n/reconnect --wait 等待在途工作 · --cancel 取消等待 · /config display 点选展示设置 · /config tools 工具活动 · /config process 过程显示（stream 独立卡，card 本卡正文）"
				cmd.Reply += "\n/doctor --echo [routeId] 模型回声（管理员发起，群聊结果私信）"
				cmd.Reply += "\n/cd --stop <绝对路径> · /ws use --stop <名称> 停止当前轮次并切换"
				if e.routeSelectionAllowed(&st, m) {
					cmd.Reply += "\n/config route 浏览已授权的项目和模型，选择私聊默认路由"
				}
				if e.privateAdminAllowed(m) {
					cmd.Reply += "\n/status tasks [聊天ID[:话题ID]] [页码] 查找轮次 · /stop task:<令牌> 停止指定聊天轮次"
					cmd.Reply += "\n/config run 编辑运行设置 · /config access 查看群授权 · /config mention check 检查群消息权限"
					cmd.Reply += "\n/config groups 在私聊查看并管理本应用所在群"
					cmd.Reply += "\n/config access user|admin · /config access route <路由ID> 查看权限名单"
					cmd.Reply += "\n/config identity 查看和点选本私聊已配置的工具身份路由"
					cmd.Reply += "\n/cd list 浏览并切换当前路由的已登记项目"
				}
				if slashWord(e.Config, "/account") == "/account" {
					cmd.Reply += "\n/account 查看接收账号 · /account change 主机换绑步骤（所有者或管理员私聊）"
				}
				if slashWord(e.Config, "/doc") == "/doc" {
					cmd.Reply += "\n/doc 查看当前账号的云文档评论入口和使用条件"
					if e.Config.CommentRoute != "" && e.privateAdminAllowed(m) {
						cmd.Reply += "\n/doc tasks [页码] 查找评论轮次并查看停止、空闲超时用法"
					}
				}
				if e.Config.MeetingEnabled || len(st.Meetings) > 0 {
					cmd.Reply += "\n/meeting status|join|leave · 会议控制（管理员私聊）"
				}
				if e.Config.MeetingEnabled && e.Config.MeetingRoute != "" {
					cmd.Reply += "\n/meeting transcript|ask|notes|stop · 会议字幕及问答"
				}
				e.prepareCommandCard(cmd, nil, e.newSessionButtons(&st, cmd))
			case "/result":
				cmd.Reply, cmd.ReplyCard = e.resultPage(&st, m, current)
			case "/ps", "/exit":
				e.receiverCommand(m, cmd)
			case "/status":
				words := strings.Fields(routeText(m))
				if card == nil && len(words) > 1 && words[1] == "tasks" {
					e.chatTasksControl(&st, m, cmd, words[2:])
					break
				}
				cmd.Reply = e.sessionStatus(&st, m, current, cmd.RouteID)
				buttons := e.newSessionButtons(&st, cmd)
				if _, ok := e.timeoutCardTask(&st, m, cmd); ok {
					buttons = append(buttons, CommandButton{Label: "超时设置", Command: "/timeout"})
				}
				if st.AccountID == e.Config.AppID && e.privateAdminAllowed(m) {
					cmd.ChatAdminReply = true
					cmd.Reply = display(appCapacityStatus(&st)+"\n"+cmd.Reply, 1800)
					buttons = append(buttons, CommandButton{Label: "聊天任务", Command: chatTasksCommand})
					if e.Config.CommentRoute != "" {
						buttons = append(buttons, CommandButton{Label: "评论任务", Command: docTasksCommand})
					}
					if !e.Config.TargetAllowed(m, cmd.Target, cmd.RouteID) {
						cmd.RouteID, cmd.Target, cmd.BaseTargetID = "", Target{}, ""
					}
				}
				e.prepareCommandCard(cmd, current, buttons)
			case "/stop":
				if card != nil {
					if err := e.stopFromCommandCard(m, id, cmd); err != nil {
						return true, err
					}
					break
				}
				words := strings.Fields(routeText(m))
				switch {
				case len(words) > 1 && strings.HasPrefix(words[1], "comment:"):
					if err := e.docTaskControl(m, id, cmd, words[1:], false); err != nil {
						return true, err
					}
				case len(words) > 1 && strings.HasPrefix(words[1], "task:"):
					if err := e.stopChatTask(m, id, cmd, words[1:]); err != nil {
						return true, err
					}
				default:
					cmd.Reply = "当前不支持该指令，请用 /help 查看；未交给模型执行。"
				}
			case "/cancel":
				cmd.Reply = "当前不支持该指令，请用 /help 查看；未交给模型执行。"
			case "/new", "/reset":
				if resetClick {
					break
				}
				words := strings.Fields(routeText(m))
				if len(words) > 1 {
					if word != "/new" || words[1] != "chat" {
						cmd.Reply = "用法：/new、/reset 或 /new chat [群名]"
						break
					}
					if !e.Config.TargetAllowed(m, cmd.Target, cmd.RouteID) || cmd.RouteID == "" {
						cmd.Reply = "没有可继承的已授权目标，未建群。"
						break
					}
					cmd.Kind = "new_chat"
					cmd.Name = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(routeText(m), "/new")), "chat"))
					if cmd.Name == "" {
						cmd.Name = "T3 Bridge · " + cmd.RouteID
					}
					if len([]rune(cmd.Name)) > 60 {
						cmd.Reply = "群名最多60字符，未建群。"
					}
					break
				}
				if cmd.RouteID == "" || !e.Config.TargetAllowed(m, cmd.Target, cmd.RouteID) {
					cmd.Reply = "请先用 /routeId 内容 选择已授权目标。"
					break
				}
				reset = true
			case "/resume":
				if !resumeClick {
					resume = e.prepareResume(&st, m, cmd, card, button, otherResumeProject)
				}
			default:
				cmd.Reply = "当前不支持该指令，请用 /help 查看；未交给模型执行。"
			}
		}
		var groupSettingsChanged bool
		var resetIdle string
		published, err := e.Store.updatePublished(func(st *State) error {
			if st.Commands == nil {
				st.Commands = map[string]*SessionCommand{}
			}
			if resumeClick {
				e.resumeFromCommandCard(st, m, cmd)
			}
			if resume != nil && !e.applyResume(st, m, cmd, *resume) {
				cmd.Reply = "会话或原轮次已变化。请重新发送 /resume 查看。"
			}
			if displayClick {
				e.applyDisplaySettingsCard(st, m, cmd)
			}
			if settingsSubmit {
				groupSettingsChanged = e.applyRunSettingsCard(st, m, cmd)
			}
			if groupAccessChange != nil {
				groupSettingsChanged = e.applyGroupAccessCard(st, m, cmd, groupAccessChange)
			}
			if toolIdentitySelection != "" || (card != nil && word == toolIdentityCommand) {
				e.applyToolIdentitySelection(st, m, cmd, toolIdentitySelection)
			}
			if routeSelection != "" || (card != nil && word == routeSelectionCommand && button.RouteSelectionID != "") {
				e.applyRouteSelection(st, m, cmd, routeSelection)
			}
			if timeoutSubmit {
				resetIdle = e.applyTimeoutSettings(st, m, cmd)
			}
			if taskListStop {
				e.stopManagedTaskFromCard(st, m, cmd)
			}
			if resetClick {
				current = e.resetFromCommandCard(st, m, cmd)
				reset = current != nil
			}
			if reset {
				if st.Current == nil {
					st.Current = map[string]string{}
				}
				if st.Selected == nil {
					st.Selected = map[string]string{}
				}
				scope := sessionKey(m, cmd.RouteID, cmd.Target)
				st.Current[scope] = ""
				delete(st.LegacySessions, scope)
				st.Selected[userScope(m)] = cmd.RouteID
				cmd.Reply = "已归档当前映射，下一条消息将开始新上下文；旧任务和历史仍保留。"
				if current != nil && !terminal(current.Phase) {
					st.Tasks[current.ID].StopRequested = true
					cmd.Reply += "\n原Bridge任务已请求停止；结果待核对，不代表回滚。"
				}
			}
			saveCommandState(st, id, cmd)
			return nil
		})
		if published && resetIdle != "" {
			delete(e.idleRuns, resetIdle)
		}
		if published && groupSettingsChanged {
			groups, _ := e.Store.groupAccess()
			e.Config.access.publishGroups(groups)
		}
		if err != nil {
			return true, err
		}
	}
	if cmd.Kind == "new_chat" && cmd.Reply == "" {
		if err := e.newChat(ctx, m, id, cmd); err != nil {
			return true, err
		}
		cmd = e.Store.snapshot().Commands[id]
		if cmd.Reply == "" {
			return true, nil
		}
	}
	if err := e.replySessionCommand(ctx, m, id, cmd); err != nil {
		return true, err
	}
	if cmd.ReceiverStopID != "" && cmd.Issue == "" {
		return true, e.stopReceiverAfterReply(ctx, m, id, cmd)
	}
	return true, nil
}

func (e *Engine) resetFromCommandCard(st *State, m Message, cmd *SessionCommand) *Task {
	cmd.Reply = "卡片中的会话或轮次已变化。请重新发送 /status。"
	origin, button := commandCardAction(e.Config, st, m)
	if origin == nil || origin.Card.Used || origin.Source.TopicID != m.TopicID || origin.Source.ChatType != m.ChatType || button.Command != "/new" {
		return nil
	}
	task := e.newSessionCardTask(st, m, origin)
	if task == nil || task.ID != button.TaskID || task.RequestID != button.RequestID {
		return nil
	}
	cmd.Target = origin.Target
	if !terminal(task.Phase) {
		cancelQueued(e.Config, st, m, task)
	}
	origin.Card.Used = true
	return task
}

func privateSettingsControl(kind string) bool {
	return kind == displaySettingsCommand || kind == runSettingsCardCommand || kind == groupMessageCheckCommand || kind == groupMessageAuthorizeCommand || kind == toolIdentityCommand || kind == groupAccessCardCommand || kind == accessPolicyListCommand
}

func (e *Engine) replySessionCommand(ctx context.Context, m Message, id string, cmd *SessionCommand) error {
	reply, ok := e.Lark.(interface {
		ReplyControl(context.Context, Message, string, bool) error
	})
	if !ok {
		return fmt.Errorf("session reply unavailable")
	}
	cardReply, cardAvailable := e.Lark.(commandCardMessenger)
	first := cmd.ReplyAt == ""
	if cmd.GroupMessageGrant != nil && !time.Now().Before(cmd.GroupMessageGrant.ExpiresAt) {
		cmd.GroupMessageGrant, cmd.Card = nil, nil
		cmd.Reply = "群消息补授权链接已过期。请重新发送 " + groupMessageAuthorizeCommand + "。"
		if err := e.saveCommand(id, cmd); err != nil {
			return err
		}
	}
	if first {
		if cmd.GroupMessageScopePending {
			e.finishGroupMessageScopeCheck(ctx, cmd)
		}
		if !cardAvailable || (e.Store.runSettings().replyMode(m) == "text" && !privateSettingsControl(cmd.Kind)) {
			cmd.useCommandText()
			if cmd.GroupMessageGrant != nil {
				cmd.GroupMessageGrant, cmd.Reply = nil, groupMessageGrantFallback
			}
		}
		cmd.ReplyAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := e.saveCommand(id, cmd); err != nil {
			return err
		}
	}
	if expired(cmd.ReplyAt, commandReplyWindow) {
		cmd.Issue = "回复超出幂等安全窗口，未重复发送；用/status核对。"
		if err := e.saveCommand(id, cmd); err != nil {
			return err
		}
		if err := e.drop(m); err != nil {
			return err
		}
		e.commandFailed(ctx, CommandFailureReplyExpired)
		return nil
	}
	var err error
	fallback := false
	if cardAvailable && cmd.Card != nil && !cmd.RouteSelectionText {
		var replyID string
		replyID, err = cardReply.ReplyCommandCard(ctx, m, commandCardJSON(cmd), true)
		if errors.Is(err, errCommandCardRejected) {
			if first {
				e.commandFailed(ctx, CommandFailureReply)
			}
			fallback = true
			cmd.useCommandText()
			if cmd.GroupMessageGrant != nil {
				cmd.GroupMessageGrant, cmd.Reply = nil, groupMessageGrantFallback
			}
			if err := e.saveCommand(id, cmd); err != nil {
				return err
			}
			_, err = cardReply.ReplyCommandCard(ctx, m, cmd.Reply, false)
		} else if err == nil {
			// Publish the returned message ID only when the command receipt commits.
			card := *cmd.Card
			card.ReplyID = replyID
			cmd.Card = &card
		}
	} else if cardAvailable && m.CommandAction != "" {
		_, err = cardReply.ReplyCommandCard(ctx, m, cmd.Reply, false)
	} else if m.CommandAction != "" {
		return fmt.Errorf("card action reply unavailable")
	} else {
		err = reply.ReplyControl(ctx, m, cmd.Reply, cmd.ReplyCard)
	}
	if err != nil {
		if fallback {
			e.commandFailed(ctx, CommandFailureReplyFallback)
		} else if first {
			e.commandFailed(ctx, CommandFailureReply)
		}
		cmd.Issue = "回复发送未确认，保留原UUID待重试"
		if cmd.Kind == "/result" {
			cmd.Issue = "全文页发送未确认，尚未交付；保留原UUID待重试"
		}
		return e.saveCommand(id, cmd)
	}
	cmd.Issue = ""
	if cmd.GroupMessageGrant != nil {
		cmd.GroupMessageGrantUntil = cmd.GroupMessageGrant.ExpiresAt
	}
	cmd.GroupMessageGrant = nil
	return e.Store.update(func(st *State) error { copy := *cmd; st.Commands[id] = &copy; removeInbox(st, m); return nil })
}

func (cmd *SessionCommand) useCommandText() {
	if cmd.Kind == routeSelectionCommand && cmd.Card != nil {
		// Keep the displayed choices for ACL checks if text delivery needs a retry.
		cmd.RouteSelectionText = true
		return
	}
	cmd.Card = nil
}

func expired(at string, window time.Duration) bool {
	t, err := time.Parse(time.RFC3339Nano, at)
	return err != nil || time.Since(t) > window
}
func (e *Engine) saveCommand(id string, cmd *SessionCommand) error {
	return e.Store.update(func(st *State) error {
		if st.Commands == nil {
			st.Commands = map[string]*SessionCommand{}
		}
		copy := *cmd
		st.Commands[id] = &copy
		return nil
	})
}
func (e *Engine) sessionStatus(st *State, m Message, t *Task, route string) string {
	target, ok := e.Config.Routes[route]
	if !ok || !e.Config.TargetAllowed(m, target, route) {
		return "当前没有已授权目标；请用 /routeId 内容 选择目标。" + controlStatus(st, m)
	}
	target = effectiveTarget(e.Config, st, m, route)
	if t != nil {
		target = t.Target
	}
	project := target.ProjectName
	if project == "" {
		project = target.ProjectID
	}
	if target.ProjectPath != "" {
		project += " · " + target.ProjectPath
	}
	status, link := "新上下文（尚未执行）", "无"
	verified := "待预检"
	if t != nil {
		status = taskControlPhase(t)
		link = "尚未创建"
		if t.ThreadID != "" {
			verified = "最近核对配置"
			link = "链接不可用"
		}
		if reference := taskThreadText(t); reference != "" {
			link = reference
		}
	}
	queued, unresolved := sessionQueueCounts(e.Config, st, m, t)
	text := fmt.Sprintf("状态：%s · 排队：%d", status, queued)
	if unresolved != 0 {
		text += fmt.Sprintf(" · 待分流：%d", unresolved)
	}
	if t != nil {
		switch {
		case t.NotificationIssue != "":
			text += "\n回复待核对：" + display(t.NotificationIssue, 160)
		case terminal(t.Phase) && (!t.TextReply || t.CardID == "") && t.NotifyHash != notificationHash(t):
			text += "\n终态回复：待送达"
		}
	}
	text += fmt.Sprintf("\n目标：%s · 执行器（%s）：%s / %s / %s\n会话：%s\n工作项目：%s\n权限：%s / %s；生产写入仍需明确授权和维护guard。", route, verified, target.Driver, target.ProviderID, target.Model, link, project, target.RuntimeMode, target.InteractionMode)
	switch {
	case target.ToolContext == nil:
		text += "\n飞书工具：当前目标未绑定。"
	case target.ToolContext.Identity == "user-default":
		text += "\n飞书工具：专用应用与用户路由，仅处理绑定用户私聊，派发前核验本机两种身份。"
	case target.ToolContext.UserBinding != nil:
		text += "\n飞书工具：专用用户路由，仅处理绑定用户私聊，每次派发前核验本机身份。"
	case target.ToolContext.BotBinding != nil:
		text += "\n飞书工具：专用 bot 路由，每次派发前核对本机工具身份。"
	case target.ToolContext.Verified:
		text += "\n飞书工具：当前目标已声明核验机器人身份（bot）。"
	default:
		text += "\n飞书工具：已配置机器人身份，待核验。"
	}
	if t != nil {
		text += fmt.Sprintf("\n显式超时：%d秒（0关闭）；截止：%s", t.TimeoutSeconds, t.DeadlineAt)
		text += fmt.Sprintf("\n本轮空闲超时：%d分钟（0关闭）", t.IdleTimeoutMinutes)
		if t.ThreadMeta != nil && t.ThreadMeta.Issue != "" {
			text += "\n列表元数据Issue：" + t.ThreadMeta.Issue
		}
		if t.ResultIssue != "" {
			text += "\n全文Issue：" + t.ResultIssue
		}
		if t.Report != nil {
			text += "\n本轮Agent报告：" + t.Report.Stage + " · " + display(t.Report.Summary, 300) + "（非探针恢复证明）"
		} else if t.ReportIssue != "" {
			text += "\n报告Issue：" + t.ReportIssue
		}
	}
	text += controlStatus(st, m)
	return display(text, 1800)
}

func appCapacityStatus(st *State) string {
	capacity := ReceiverRunCapacity{Limit: st.Settings.MaxConcurrent}
	for _, task := range st.Tasks {
		capacity.add(task)
	}
	limit := "不限"
	if capacity.Limit > 0 {
		limit = fmt.Sprint(capacity.Limit)
	}
	return fmt.Sprintf("本应用并发：占用 %d · 上限 %s · 等额度 %d", capacity.Occupied, limit, capacity.Waiting)
}

func sessionQueueCounts(c Config, st *State, m Message, current *Task) (queued, unresolved int) {
	for _, q := range st.Inbox {
		if q.ID == m.ID || q.Cancelled || userScope(q) != userScope(m) || !c.Allowed(q) ||
			q.Comment != nil || q.Meeting != nil || q.MeetingInvite != nil || q.InputKind == "automatic" ||
			q.CommandAction != "" || slashWord(c, routeText(q)) != "" || isStop(c, routeText(q), current) {
			continue
		}
		// A topic root is not a reply to a task. Unbound input has not yet been routed.
		if q.ParentID == "" && q.ControlTask == "" && q.SessionTask == "" && q.EventID == "" {
			unresolved++
		} else if bound := boundTask(st, q); current != nil && bound != nil && bound.ID == current.ID {
			queued++
		}
	}
	return
}
func (e *Engine) newChat(ctx context.Context, m Message, id string, cmd *SessionCommand) error {
	api, ok := e.Lark.(sessionMessenger)
	if !ok {
		return fmt.Errorf("chat API unavailable")
	}
	if !e.Config.TargetAllowed(m, e.Config.Routes[cmd.RouteID], cmd.RouteID) || !taskTargetAllowed(e.Config, &Task{RouteID: cmd.RouteID, Target: cmd.Target, BaseTargetID: cmd.BaseTargetID}) {
		cmd.Reply = "目标或权限已变化，未继续建群。"
		return e.saveCommand(id, cmd)
	}
	if cmd.ChatID == "" {
		first := cmd.CreateAt == ""
		if first {
			cmd.CreateAt = time.Now().UTC().Format(time.RFC3339Nano)
			if err := e.saveCommand(id, cmd); err != nil {
				return err
			}
		}
		if expired(cmd.CreateAt, 9*time.Hour) {
			cmd.Reply = "建群结果未知，已超过幂等安全窗口，未盲目重建；请人工核对。"
			return e.saveCommand(id, cmd)
		}
		chat, err := api.CreatePrivateChat(ctx, m, cmd.Name, stable("new-chat", id))
		if err != nil {
			if first {
				e.commandFailed(ctx, CommandFailureChatCreate)
			}
			if errors.Is(err, errChatRejected) {
				cmd.Reply = "建群请求被拒绝，未扩大权限；请核对应用现有建群权限。"
				return e.saveCommand(id, cmd)
			}
			return nil
		} // Same UUID only; retain inbox on an uncertain result.
		cmd.ChatID = chat
		published, err := e.Store.updatePublished(func(st *State) error {
			copy := *cmd
			st.Commands[id] = &copy
			if st.Selected == nil {
				st.Selected = map[string]string{}
			}
			if st.Current == nil {
				st.Current = map[string]string{}
			}
			fresh := Message{UserID: m.UserID, ChatID: chat, ChatType: "group"}
			st.Selected[userScope(fresh)] = cmd.RouteID
			rememberWorkspace(e.Config, st, fresh, cmd.RouteID, cmd.Target)
			st.Current[sessionKey(fresh, cmd.RouteID, cmd.Target)] = ""
			st.GroupAccess.setAllowed(chat, true)
			return nil
		})
		if published && e.Config.access != nil {
			groups, _ := e.Store.groupAccess()
			e.Config.access.publishGroups(groups)
		}
		if err != nil {
			return err
		}
	}
	if !cmd.Welcomed {
		first := cmd.WelcomeAt == ""
		if first {
			cmd.WelcomeAt = time.Now().UTC().Format(time.RFC3339Nano)
			if err := e.saveCommand(id, cmd); err != nil {
				return err
			}
		}
		if expired(cmd.WelcomeAt, 55*time.Minute) {
			cmd.Issue = "欢迎发送结果待核对，未超窗重发。"
		} else {
			if err := api.WelcomeChat(ctx, cmd.ChatID, "已建立私有会话入口，目标："+cmd.RouteID+"。@我开始对话；使用权不增加生产写入授权。", stable("welcome", id)); err != nil {
				if first {
					e.commandFailed(ctx, CommandFailureChatWelcome)
				}
				return nil
			}
			cmd.Welcomed = true
		}
	}
	cmd.Reply = "已创建私有群「" + display(cmd.Name, 60) + "」，仅邀请你与机器人；目标：" + cmd.RouteID + "。群内@我可继续。"
	return e.saveCommand(id, cmd)
}

func shortTaskID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
