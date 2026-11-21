package bridge

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

func currentReply(t *Task) ReplyRound {
	r := ReplyRound{RequestID: t.RequestID, InputID: t.DispatchSource.ID, RunID: t.RunID, ResultRunID: t.ResultRunID, Source: t.DispatchSource, Attachments: append([]Attachment{}, t.Attachments...), CardID: t.CardID, Phase: t.Phase, Summary: t.Summary, Result: t.Result, Issue: t.ResultIssue}
	r.Report, r.ReportIssue = t.Report, t.ReportIssue
	if r.InputID == "" {
		r.InputID = t.Source.ID
	}
	// Legacy versions stored the final body in Summary; no new card/UUID is created to expose it.
	if r.Result == "" && r.Issue == "" && t.ReplySource == nil && (t.Phase == "completed" || t.Phase == "failed") {
		old := *t
		finishResult(&old, t.Summary, "")
		r.Result, r.Issue = old.Result, old.ResultIssue
	}
	return r
}
func (e *Engine) resultPage(st *State, m Message, current *Task) (string, bool) {
	words := strings.Fields(routeText(m))
	if len(words) > 3 {
		return "用法：/result [轮次短令牌] [页码]", false
	}
	var chosen *ReplyRound
	if len(words) == 1 && current != nil && scopedTask(e.Config, m, current, current.RouteID) {
		r := replyForMessage(current, m)
		chosen = &r
	} else if len(words) >= 2 {
		for _, t := range st.Tasks {
			if !scopedTask(e.Config, m, t, t.RouteID) {
				continue
			}
			rounds := append([]ReplyRound{currentReply(t)}, t.Replies...)
			for _, r := range rounds {
				if r.RequestID != "" && (words[1] == shortTaskID(r.RequestID) || words[1] == r.RequestID) {
					if chosen != nil {
						return "轮次令牌不唯一，请引用对应卡片或使用完整requestId。", false
					}
					copy := r
					chosen = &copy
				}
			}
		}
	}
	if chosen == nil {
		return "当前用户/聊天/话题和受控目标范围内没有对应轮次；可回复自己的结果卡发送 /result。", false
	}
	if chosen.Issue != "" {
		return "本轮全文未交付：" + display(chosen.Issue, 500), false
	}
	if chosen.Result == "" {
		return "本轮尚未取得可见最终正文；未宣称全文已交付。", false
	}
	page := 1
	if len(words) == 3 {
		var err error
		page, err = strconv.Atoi(words[2])
		if err != nil {
			return "页码必须为正整数。", false
		}
	}
	pages := splitResultPages(sensitive.ReplaceAllString(chosen.Result, "[已脱敏]"))
	token := shortTaskID(chosen.RequestID)
	if page < 1 || page > len(pages) {
		return fmt.Sprintf("全文共%d页；用 /result %s <页码>。", len(pages), token), false
	}
	body := pages[page-1]
	prefix := strings.Join(pages[:page-1], "")
	opening, _ := openResultFence(prefix)
	_, closing := openResultFence(prefix + body)
	if opening != "" {
		body = opening + "\n" + body
	}
	if closing != "" {
		body = strings.TrimSuffix(body, "\n") + "\n" + closing
	}
	elements := []any{
		map[string]any{"tag": "div", "text": map[string]string{"tag": "plain_text", "content": fmt.Sprintf("全文 %d/%d · 轮次 %s", page, len(pages), token)}},
		map[string]string{"tag": "markdown", "content": body},
	}
	if len(pages) > 1 {
		elements = append(elements, map[string]any{"tag": "div", "text": map[string]string{"tag": "plain_text", "content": "取其它页：/result " + token + " <页码>"}})
	}
	return card2JSON(elements), true
}

func splitResultPages(full string) []string {
	const size = 1400
	var pages []string
	for rest := []rune(full); len(rest) > 0; {
		end := min(size, len(rest))
		if end < len(rest) && rest[end-1] != '\n' {
			window := string(rest[:end])
			paragraph := strings.LastIndex(window, "\n\n")
			if paragraph >= 0 && len([]rune(window[:paragraph+2])) >= size/2 {
				end = len([]rune(window[:paragraph+2]))
			} else if line := strings.LastIndexByte(window, '\n'); line >= 0 {
				end = len([]rune(window[:line+1]))
			}
		}
		pages = append(pages, string(rest[:end]))
		rest = rest[end:]
	}
	return pages
}

// A continued page gets its own fence so the card renders the code independently.
func openResultFence(text string) (opening, marker string) {
	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if trimmed == "" || len(line)-len(trimmed) > 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
			continue
		}
		tail := strings.TrimLeft(trimmed, trimmed[:1])
		fence := trimmed[:len(trimmed)-len(tail)]
		if len(fence) < 3 {
			continue
		}
		if marker != "" {
			if strings.HasPrefix(fence, marker) && strings.TrimSpace(tail) == "" {
				opening, marker = "", ""
			}
		} else if fence[0] == '~' || !strings.ContainsRune(tail, '`') {
			opening, marker = strings.TrimSuffix(line, "\r"), fence
		}
	}
	return opening, marker
}
func controlStatus(st *State, m Message) string {
	lines := []string{}
	pending := map[string]bool{}
	for _, q := range st.Inbox {
		if userScope(q) != userScope(m) || q.ID == m.ID || len(lines) >= 4 {
			continue
		}
		word := strings.Fields(routeText(q))
		if len(word) == 0 || !strings.HasPrefix(word[0], "/") {
			continue
		}
		cmd := st.Commands[key(q, q.ID)]
		if cmd == nil && !slices.Contains([]string{"/invite", "/remove", "/config"}, word[0]) {
			continue
		}
		line := display(word[0], 40) + "：处理中/回执待送"
		if cmd != nil {
			pending[key(q, q.ID)] = true
			if cmd.Kind == "new_chat" {
				line = "建群：创建/欢迎/确认待核对"
			}
			if cmd.Issue != "" {
				line += " · " + display(cmd.Issue, 100)
			}
		}
		lines = append(lines, line)
	}
	for id, cmd := range st.Commands {
		if userScope(cmd.Source) == userScope(m) && (cmd.Issue != "" || (cmd.ProjectAttempted && cmd.RegistrationID == "")) && !pending[id] && len(lines) < 4 {
			issue := cmd.Issue
			if cmd.ProjectAttempted && cmd.RegistrationID == "" {
				issue = "项目登记结果待只读核对；不重复创建。 " + issue
			}
			lines = append(lines, display(cmd.Kind, 30)+" · "+display(issue, 120))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n待处理/Issue：\n" + strings.Join(lines, "\n")
}

func replyForMessage(t *Task, m Message) ReplyRound {
	r := currentReply(t)
	if m.ParentID != "" && m.ParentID != r.CardID && m.ParentID != r.InputID && !slices.Contains(r.Source.MergedIDs, m.ParentID) {
		for i := len(t.Replies) - 1; i >= 0; i-- {
			past := t.Replies[i]
			if past.CardID == m.ParentID || past.InputID == m.ParentID || slices.Contains(past.Source.MergedIDs, m.ParentID) {
				return past
			}
		}
	}
	return r
}
