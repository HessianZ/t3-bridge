package bridge

import (
	"context"
	"strings"

	channel "github.com/larksuite/channel-sdk-go"
)

func (l *Lark) handleAction(ctx context.Context, a *channel.CardActionEvent) error {
	if a.EventID == "" {
		return nil
	}
	st := l.Store.snapshot()
	card, chat := a.MessageID, a.ChatID
	if card == "" {
		card = a.Context.OpenMessageID
	}
	if chat == "" {
		chat = a.Context.OpenChatID
	}
	if a.Action.Value["action"] == commandCardActionType {
		action, _ := a.Action.Value["id"].(string)
		if a.Action.Tag != "button" || len(action) != 32 || len(a.Action.Value) != 2 ||
			(a.Context.OpenMessageID != "" && a.Context.OpenMessageID != card) || (a.Context.OpenChatID != "" && a.Context.OpenChatID != chat) {
			return nil
		}
		m := Message{ID: "card:" + a.EventID, UserID: a.Operator.OpenID, ChatID: chat, ParentID: card, CommandAction: action}
		origin, button := commandCardAction(l.Config, &st, m)
		if origin == nil || (origin.Card.Used && button.changesState()) {
			return nil
		}
		// Card callbacks identify the reply message. Its durable receipt supplies the topic.
		m.TopicID, m.ChatType = origin.Source.TopicID, origin.Source.ChatType
		if (button.Command == groupMessageCheckCommand || button.Command == groupMessageAuthorizeCommand || button.Command == doctorEchoCommand || taskListCommand(button.Command)) && !l.Config.privateAdminAllowed(m) {
			return nil
		}
		if button.Command == runSettingsCardCommand {
			if origin.Kind != runSettingsCardCommand || origin.Card.RunSettingsForm == nil || !l.Config.privateAdminAllowed(m) {
				return nil
			}
			// An invalid submission still gets a private error reply, without storing the raw input.
			if submission, err := parseRunSettingsForm(a.Action.FormValue); err == nil {
				m.RunSettingsForm = submission
			}
		} else if button.Command == timeoutSettingsCommand {
			if origin.Card.TimeoutSettings == nil {
				return nil
			}
			m.TimeoutSettings = parseTimeoutSettings(a.Action.FormValue)
		} else if len(a.Action.FormValue) != 0 {
			return nil
		}
		return l.acceptAction(m)
	}
	var t *Task
	if a.Action.Value["action"] == "stop" {
		id, _ := a.Action.Value["taskId"].(string)
		t = st.Tasks[id]
	} else {
		for _, candidate := range st.Tasks {
			if candidate.CardID == card && candidate.Source.ChatID == chat && candidate.InputOpen && a.Action.Name == stable(candidate.ID, candidate.RequestID, "form-submit") {
				t = candidate
				break
			}
		}
	}
	if t == nil {
		return nil
	}
	m := Message{ID: "card:" + a.EventID, UserID: a.Operator.OpenID, ChatID: chat, TopicID: t.Source.TopicID, ChatType: t.Source.ChatType, Text: "/stop", ControlTask: t.ID, ControlCard: card}
	if !l.Config.Allowed(m) || m.UserID != t.Source.UserID || card != t.CardID || chat != t.Source.ChatID {
		return nil
	}
	if a.Action.Value["action"] == "stop" {
		if a.Action.Value["requestId"] != cardStopRequest(t) {
			return nil
		}
	} else {
		text, ok := a.Action.FormValue["reply_text"].(string)
		if !ok || a.Action.Tag != "button" || len(a.Action.Value) > 0 || len(a.Action.FormValue) != 1 || strings.TrimSpace(text) == "" || len([]rune(text)) > 1000 || len(text) > 4000 || strings.ContainsRune(text, 0) || t.StopRequested || !scopedTask(l.Config, m, t, t.RouteID) {
			return nil
		}
		m.Text = text
		m.InputKind = "form"
		m.RoutingText = "用户提交补充，仅作讨论，不构成审批或生产授权"
		m.FormRequestID = t.RequestID
	}
	return l.acceptAction(m)
}

func (l *Lark) acceptAction(m Message) error {
	if err := l.Store.Accept(l.Config, m); err != nil {
		l.fail()
		return err
	}
	return nil
}
func inputForm(t *Task) any {
	return map[string]any{"tag": "form", "name": stable(t.ID, t.RequestID, "form"), "elements": []any{
		map[string]any{"tag": "input", "name": "reply_text", "required": true, "input_type": "multiline_text", "max_length": 1000, "label": map[string]string{"tag": "plain_text", "content": "补充讨论（不是T3审批/生产授权）"}},
		map[string]any{"tag": "button", "name": stable(t.ID, t.RequestID, "form-submit"), "form_action_type": "submit", "text": map[string]string{"tag": "plain_text", "content": "提交补充文本"}},
	}}
}
