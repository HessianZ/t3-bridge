package bridge

import (
	"context"
	"strings"
	"testing"

	channel "github.com/larksuite/channel-sdk-go"
)

func TestP2aFormUsesOfficialShapeAndOneShotOwnedRound(t *testing.T) {
	e, _, ch, x, _ := setupAccess(t)
	receiveAccess(t, ch, groupMessage("one", "user", "chat", "/nas 讨论", true))
	tick(t, e)
	x.observation.Status = "completed"
	x.observation.Summary = "done"
	tick(t, e)
	roleCommand(t, e, ch, "open", "user", "/input", "")
	task := onlyTask(t, e)
	if !task.InputOpen {
		t.Fatal("form did not open")
	}
	var form map[string]any
	for _, element := range card2Elements(t, card(task)) {
		v := element.(map[string]any)
		if v["tag"] == "form" {
			form = v
		}
	}
	if form == nil {
		t.Fatal("no Card2 form")
	}
	elements := form["elements"].([]any)
	input := elements[0].(map[string]any)
	submit := elements[1].(map[string]any)
	if input["name"] != "reply_text" || input["max_length"] != float64(1000) || submit["form_action_type"] != "submit" || submit["behaviors"] != nil || submit["value"] != nil {
		t.Fatal("mixed Card1/button behaviors in form")
	}
	action := channel.CardActionEvent{EventID: "submit", MessageID: task.CardID, ChatID: "chat", Operator: channel.CardActionOperator{OpenID: "user"}, Action: channel.CardActionPayload{Tag: "button", Name: stable(task.ID, task.RequestID, "form-submit"), FormValue: map[string]any{"reply_text": "## 原样输入\n**仅讨论**"}}}
	for _, mode := range []string{"user", "chat", "round", "hidden", "extra-field", "bounds"} {
		bad := action
		bad.EventID = "bad-" + mode
		switch mode {
		case "user":
			bad.Operator.OpenID = "other"
		case "chat":
			bad.ChatID = "outside"
		case "round":
			bad.Action.Name = "old-round"
		case "hidden":
			bad.Action.Value = map[string]any{"action": "continue", "payload": "restart"}
		case "extra-field":
			bad.Action.FormValue = map[string]any{"reply_text": "a", "hidden": "restart"}
		case "bounds":
			bad.Action.FormValue = map[string]any{"reply_text": strings.Repeat("界", 1001)}
		}
		if err := ch.action(context.Background(), &bad); err != nil {
			t.Fatal(err)
		}
		if len(e.Store.snapshot().Inbox) != 0 || !onlyTask(t, e).InputOpen {
			t.Fatal("bad callback affected form", mode)
		}
	}
	if err := ch.action(context.Background(), &action); err != nil {
		t.Fatal(err)
	}
	action.EventID = "different-duplicate"
	ch.action(context.Background(), &action)
	if len(e.Store.snapshot().Inbox) != 1 || onlyTask(t, e).InputOpen {
		t.Fatal("form submitted more than once")
	}
	tick(t, e)
	current := onlyTask(t, e)
	if x.sends != 2 || current.CardID != task.CardID || current.RequestID == task.RequestID || !strings.HasPrefix(x.request, "## 原样输入\n**仅讨论**") || !strings.Contains(x.request, "仅获只读讨论授权") || !strings.Contains(card(current), "你提交的文本") {
		t.Fatal("form lost visible text/original card/authorization boundary")
	}
	if len(current.Replies) != 1 || current.Replies[0].DeliveryState != ReplyDeliveryConfirmed || current.Replies[0].CardID != task.CardID {
		t.Fatal("handling the form lost the original terminal delivery evidence")
	}
	tick(t, e)
	roleCommand(t, e, ch, "reopen", "user", "/input", "")
	ch.action(context.Background(), &action)
	if len(e.Store.snapshot().Inbox) != 0 || !onlyTask(t, e).InputOpen {
		t.Fatal("old-round form accepted on same physical card")
	}
}
func TestP2aOldCardCannotOpenFormOrChangeCurrentDeadline(t *testing.T) {
	e, x, l, _ := testEngine(t)
	accept(t, e, message("one", "/nas 第一轮"))
	tick(t, e)
	first := onlyTask(t, e)
	x.observation.Status = "completed"
	x.observation.Summary = "first"
	tick(t, e)
	accept(t, e, message("two", "/nas 第二轮"))
	tick(t, e)
	second := onlyTask(t, e)
	for _, command := range []string{"/input", "/timeout 1"} {
		m := message(command, command)
		m.ParentID = first.CardID
		accept(t, e, m)
		tick(t, e)
	}
	current := onlyTask(t, e)
	if current.InputOpen || current.TimeoutSeconds != 0 || current.CardID != second.CardID || x.sends != 2 {
		t.Fatal("old card changed new round interaction")
	}
	if !strings.Contains(l.replies[len(l.replies)-1], "旧轮") {
		t.Fatal("old round rejected without explanation")
	}
}
