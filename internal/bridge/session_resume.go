package bridge

import (
	"fmt"
	"strconv"
	"strings"
)

const sessionHistoryPageSize = 8

const staleResumeCard = "卡片中的会话轮次、目标、工作区或权限已变化。请重新发送 /resume。"

type resumeRound struct {
	TaskID    string `json:"taskId"`
	RequestID string `json:"requestId"`
}

type resumeSelection struct {
	Round resumeRound
	Stop  *resumeRound
}

func (e *Engine) prepareResume(st *State, m Message, cmd, origin *SessionCommand, button CommandButton, otherProject bool) *resumeSelection {
	words := strings.Fields(routeText(m))
	page := 1
	token := ""
	stop := false
	switch {
	case origin != nil:
		if button.ResumePage != 0 {
			if !e.resumeCardMatches(st, m, origin) {
				cmd.Reply = staleResumeCard
				return nil
			}
			page = button.ResumePage
		}
	case len(words) == 1:
	case len(words) == 3 && words[1] == "page":
		var err error
		page, err = strconv.Atoi(words[2])
		if err != nil || page < 1 {
			cmd.Reply = "用法：/resume page <正整数页码>"
			return nil
		}
	case len(words) == 3 && (words[1] == "use" || words[1] == "--stop"):
		token, stop = words[2], words[1] == "--stop"
	case len(words) == 2 && words[1] != "page" && words[1] != "use" && words[1] != "--stop":
		token = words[1]
	default:
		cmd.Reply = "用法：/resume、/resume page <页码>、/resume <短令牌> 或 /resume --stop <短令牌>"
		return nil
	}

	target := effectiveTarget(e.Config, st, m, cmd.RouteID)
	if token == "" {
		e.prepareResumePage(st, m, cmd, page, otherProject || !sameResumeProject(target, cmd.Target))
		return nil
	}
	cmd.Target = target
	var chosen *Task
	for _, task := range st.Tasks {
		if task == nil || shortTaskID(task.ID) != token || !sessionHistoryCandidate(e.Config, st, m, cmd.RouteID, target, task) {
			continue
		}
		if chosen != nil {
			chosen = nil
			break
		}
		chosen = task
	}
	if chosen == nil {
		cmd.Reply = "短令牌无效或不属于当前用户、聊天/话题、目标及权限范围。"
		return nil
	}
	selection := &resumeSelection{Round: resumeRound{TaskID: chosen.ID, RequestID: chosen.RequestID}}
	if stop {
		if otherProject || cmd.RouteID != selectedRoute(e.Config, st, m) {
			cmd.Reply = "请在当前工作区直接发送 /resume --stop <短令牌>。"
			return nil
		}
		selection.Stop = resumeStopRound(currentTask(e.Config, st, m, cmd.RouteID), chosen)
	}
	return selection
}

func (e *Engine) prepareResumePage(st *State, m Message, cmd *SessionCommand, page int, otherProject bool) {
	history := sessionHistory(e.Config, st, m, cmd.RouteID)
	pages := max(1, (len(history)+sessionHistoryPageSize-1)/sessionHistoryPageSize)
	if page < 1 || page > pages {
		cmd.Reply = fmt.Sprintf("会话历史共 %d 页。用 /resume 或 /resume page <页码> 查看。", pages)
		return
	}
	private := m.ChatType == "p2p" && m.Comment == nil
	canSelect := private && !otherProject && selectedRoute(e.Config, st, m) == cmd.RouteID
	current := currentTask(e.Config, st, m, cmd.RouteID)
	cmd.Reply = fmt.Sprintf("当前范围可恢复的会话 · %d/%d 页", page, pages)
	if len(history) == 0 {
		cmd.Reply += "\n暂无可恢复会话。"
	}
	var choices []CommandButton
	start := (page - 1) * sessionHistoryPageSize
	for i := start; i < min(len(history), start+sessionHistoryPageSize); i++ {
		task := history[i]
		token := shortTaskID(task.ID)
		line := fmt.Sprintf("\n%d · %s · %s", i+1, token, display(task.Phase, 50))
		if current != nil && task.ID == current.ID {
			line += "（当前）"
		}
		cmd.Reply += line + " · " + display(strings.Join(strings.Fields(task.Source.Text), " "), 60)
		if canSelect && resumeCardCandidate(e.Config, st, task) {
			label := "切换到"
			stop := resumeStopRound(current, task)
			if stop != nil {
				label = "停止本轮并切换到"
			}
			choices = append(choices, CommandButton{Label: fmt.Sprintf("%s %d · %s", label, i+1, token), Command: "/resume",
				TaskID: task.ID, RequestID: task.RequestID, ResumeStop: stop})
		}
	}
	if len(history) != 0 {
		cmd.Reply += "\n发送 /resume <短令牌> · 停止并切换用 /resume --stop <短令牌>。"
		if pages > 1 {
			cmd.Reply += "\n翻页用 /resume page <页码>。"
		}
	}
	if canSelect && page > 1 {
		choices = append(choices, CommandButton{Label: "上一页", Command: "/resume", ResumePage: page - 1})
	}
	if canSelect && page < pages {
		choices = append(choices, CommandButton{Label: "下一页", Command: "/resume", ResumePage: page + 1})
	}
	if private {
		e.prepareCommandCard(cmd, nil, choices)
	}
}

func resumeStopRound(current, chosen *Task) *resumeRound {
	if current == nil || current.ID == chosen.ID || (current.ThreadID != "" && current.ThreadID == chosen.ThreadID) || terminal(current.Phase) || current.StopRequested {
		return nil
	}
	return &resumeRound{TaskID: current.ID, RequestID: current.RequestID}
}

func (e *Engine) resumeCardMatches(st *State, m Message, origin *SessionCommand) bool {
	return origin.Kind == "/resume" && m.ChatType == "p2p" && origin.Source.ChatType == "p2p" && origin.Source.Comment == nil &&
		origin.Source.TopicID == m.TopicID && selectedRoute(e.Config, st, m) == origin.RouteID &&
		sameResumeProject(effectiveTarget(e.Config, st, m, origin.RouteID), origin.Target)
}

func (e *Engine) applyResume(st *State, m Message, cmd *SessionCommand, selection resumeSelection) bool {
	target := effectiveTarget(e.Config, st, m, cmd.RouteID)
	if m.CommandAction == "" && selection.Stop == nil && !sameResumeProject(target, cmd.Target) {
		// An explicit reply can select an owned session in the referenced project.
		reference := boundTask(st, m)
		if scopedTask(e.Config, m, reference, cmd.RouteID) && reference.Source.Meeting == nil {
			target = reference.Target
		}
	}
	task := st.Tasks[selection.Round.TaskID]
	if task == nil || task.ID != selection.Round.TaskID || task.RequestID != selection.Round.RequestID ||
		!sameResumeProject(target, cmd.Target) || !sessionHistoryCandidate(e.Config, st, m, cmd.RouteID, target, task) {
		return false
	}
	var stopped *Task
	if selection.Stop != nil {
		current := currentTask(e.Config, st, m, cmd.RouteID)
		if selectedRoute(e.Config, st, m) != cmd.RouteID || current == nil || current.ID != selection.Stop.TaskID || current.RequestID == "" || current.RequestID != selection.Stop.RequestID ||
			targetIdentity(target) != targetIdentity(current.Target) || (current.ReceiverAppID != "" && current.ReceiverAppID != e.Config.AppID) ||
			(target.ProjectPath != "" && current.Target.ProjectPath != "" && target.ProjectPath != current.Target.ProjectPath) ||
			current.ID == task.ID || (current.ThreadID != "" && current.ThreadID == task.ThreadID) {
			return false
		}
		if !terminal(current.Phase) && !current.StopRequested {
			cancelQueued(e.Config, st, m, current)
			current.StopRequested = true
			stopped = current
		}
	}
	if m.CommandAction == "" {
		setCurrent(e.Config, st, m, task)
	} else {
		if st.Current == nil {
			st.Current = make(map[string]string)
		}
		st.Current[sessionKey(m, cmd.RouteID, target)] = task.ID
	}
	cmd.Reply = "已切换到会话 " + shortTaskID(task.ID) + "；未重发任何任务。"
	if stopped != nil {
		cmd.Reply += "\n已请求停止原轮次 " + shortTaskID(stopped.ID) + "。远端状态请在原结果卡查看。"
	}
	return true
}
