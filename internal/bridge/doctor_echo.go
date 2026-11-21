package bridge

import (
	"context"
	"slices"
	"strings"
	"time"
)

const (
	doctorEchoCommand   = "/doctor --echo"
	doctorEchoKind      = "doctor_echo"
	doctorEchoPrompt    = "Bridge model echo check. Do not use tools or inspect files or history. Reply exactly: OK"
	doctorEchoTimeout   = time.Minute
	doctorEchoCooldown  = 30 * time.Second
	maxDoctorEchoRoutes = 6
)

// A successful registration delivers one reply through the normal task outbox.
func (e *Engine) doctorCommand(ctx context.Context, m Message, cmd *SessionCommand, current *Task, origin *SessionCommand, button CommandButton) (bool, error) {
	fromCard := button.Command == doctorEchoCommand
	route := cmd.RouteID
	if fromCard {
		route = button.Name
	} else {
		words := strings.Fields(routeText(m))
		if len(words) < 2 || words[1] != "--echo" {
			e.prepareDoctorReport(m, cmd, current)
			return false, nil
		}
		if len(words) > 3 {
			cmd.Reply = "用法：/doctor --echo [routeId]"
			return false, nil
		}
		if len(words) == 3 {
			route = words[2]
		}
	}
	group := !fromCard && m.ChatType == "group"
	if (group && !e.Config.doctorGroupEchoAllowed(m)) || (!group && !e.privateAdminAllowed(m)) {
		cmd.Reply = "模型回声仅支持所有者或管理员在已授权的私聊、群聊或话题中发起。"
		return false, nil
	}
	target := e.Config.Routes[route]
	if fromCard && (origin.Kind != "/doctor" || button.DoctorTargetID != targetIdentity(target) || !e.doctorEchoAllowed(m, route, target)) {
		cmd.Reply = "诊断路由或授权已变化，请重新发送 /doctor。"
		return false, nil
	}
	if !fromCard && !e.doctorEchoAllowed(m, route, target) {
		cmd.Reply = "请使用 /doctor --echo <routeId> 选择已授权的 plan / approval-required 诊断路由。"
		return false, nil
	}
	for _, task := range e.Store.snapshot().Tasks {
		if task.Source.InputKind != doctorEchoKind {
			continue
		}
		if !terminal(task.Phase) || task.NeedsReconcile {
			cmd.Reply = "本账号已有待收口的模型回声。"
			return false, nil
		}
		deadline, _ := time.Parse(time.RFC3339Nano, task.DeadlineAt)
		if task.Source.UserID == m.UserID && e.now().Before(deadline.Add(doctorEchoCooldown-doctorEchoTimeout)) {
			cmd.Reply = "同一用户每 30 秒可发起一次模型回声。"
			return false, nil
		}
	}
	source := Message{ID: m.ID, ChatID: m.ChatID, ChatType: m.ChatType, TopicID: m.TopicID, UserID: m.UserID, Text: doctorEchoCommand, InputKind: doctorEchoKind}
	decision := Decision{RouteID: route, Action: "new_task"}
	if group {
		id := key(m, m.ID)
		cmd.Source, cmd.Reply = source, "已登记模型回声。结果将由当前应用私信发给你。"
		cmd.RouteID, cmd.Target, cmd.BaseTargetID = "", Target{}, ""
		if err := e.Store.update(func(st *State) error {
			e.createTask(st, source, decision, "等待模型回声")
			saveCommandState(st, id, cmd)
			return nil
		}); err != nil {
			return true, err
		}
		return true, e.replySessionCommand(ctx, m, id, cmd)
	}
	if !fromCard {
		return true, e.newTask(source, decision, "等待模型回声")
	}
	return true, e.Store.update(func(state *State) error {
		receipt := state.Commands[key(origin.Source, origin.Source.ID)]
		if receipt == nil || receipt.Card == nil || receipt.Card.Used {
			removeInbox(state, m)
			return nil
		}
		task := e.createTask(state, source, decision, "等待模型回声")
		// The event identifies the task, while the original card is a real reply anchor.
		task.ReplySource.ID = receipt.Card.ReplyID
		receipt.Card.Used = true
		return nil
	})
}

func (c Config) doctorGroupEchoAllowed(m Message) bool {
	if m.ChatType != "group" || !openIDPattern.MatchString(m.UserID) || m.Comment != nil || m.Meeting != nil ||
		!c.isAdmin(m.UserID) || c.userDenied(m.UserID) || !c.Allowed(m) {
		return false
	}
	return c.access == nil || c.groupAllowed(m.ChatID)
}

func groupDoctorEcho(t *Task) bool {
	return t.Source.InputKind == doctorEchoKind && t.Source.ChatType == "group"
}

func (e *Engine) groupDoctorTaskAllowed(t *Task) bool {
	return t.ReceiverAppID == e.Config.AppID && e.Config.doctorGroupEchoAllowed(t.Source) && taskTargetAllowed(e.Config, t) &&
		e.doctorEchoAllowed(t.Source, t.RouteID, t.Target)
}

func (e *Engine) doctorEchoAllowed(m Message, id string, t Target) bool {
	return t.RuntimeMode == "approval-required" && t.InteractionMode == "plan" && e.Config.TargetAllowed(m, t, id)
}

func (e *Engine) prepareDoctorReport(m Message, cmd *SessionCommand, current *Task) {
	cmd.Reply = e.doctor(m, current)
	if !e.privateAdminAllowed(m) {
		return
	}
	var routes []string
	for id, target := range e.Config.Routes {
		if e.doctorEchoAllowed(m, id, target) {
			routes = append(routes, id)
		}
	}
	if len(routes) == 0 {
		cmd.Reply += "\n模型回声需要已授权的 plan / approval-required 路由。"
		return
	}
	slices.Sort(routes)
	routes = routes[:min(len(routes), maxDoctorEchoRoutes)]
	cmd.Reply = display(cmd.Reply, 1200) + "\n点选路由可发起独立模型回声，也可发送："
	cmd.RouteID, cmd.Target, cmd.BaseTargetID = "", Target{}, ""
	cmd.Card = &CommandCard{AppID: e.Config.AppID}
	for _, id := range routes {
		label := display(id, 1800)
		cmd.Reply += "\n" + doctorEchoCommand + " " + label
		cmd.Card.Buttons = append(cmd.Card.Buttons, CommandButton{
			Label:          "模型回声 · " + label,
			Command:        doctorEchoCommand,
			Name:           id,
			DoctorTargetID: targetIdentity(e.Config.Routes[id]),
		})
	}
}
