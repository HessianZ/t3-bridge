package bridge

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestReviewIncludesStoredDialogueAndOldCardAnchor(t *testing.T) {
	for _, oldCard := range []bool{false, true} {
		t.Run(fmt.Sprint(oldCard), func(t *testing.T) {
			e, x, _, _ := testEngine(t)
			e.Config.DefaultRoute = "nas"
			target := e.Config.Routes["nas"]
			target.Model = "codebuddy-direct/hy4free"
			target.Options = map[string]any{"variant": "high"}
			e.Config.Routes["nas-review"] = target
			e.Config.ReviewRoute = "nas-review"
			accept(t, e, message("set", "/nas 记住口令 HOMEOPS-1118，只用粗体回复"))
			tick(t, e)
			x.observation.Status = "completed"
			x.observation.Summary = "**HOMEOPS-1118**"
			tick(t, e)
			first := onlyTask(t, e)
			accept(t, e, message("recall", "/nas 刚才的口令是什么"))
			tick(t, e)
			x.observation.Summary = "HOMEOPS-1118"
			tick(t, e)
			if oldCard {
				accept(t, e, message("future", "/nas 后来轮次的资料 FUTURE-EXCLUDED"))
				tick(t, e)
				x.observation.Summary = "FUTURE-ANSWER-EXCLUDED"
				tick(t, e)
				accept(t, e, message("stop-original", "/stop"))
				tick(t, e)
			}
			before := onlyTask(t, e)
			m := message("review", "/review 只复核刚才两轮是否一致，不要运行命令")
			if oldCard {
				m.ParentID = before.Replies[1].CardID
			}
			accept(t, e, m)
			tick(t, e)
			st := e.Store.snapshot()
			r := st.Tasks[stable(m.ChatID, m.TopicID, m.ID)]
			if r == nil || !strings.Contains(r.DispatchSource.Quote, first.Source.Text) || !strings.Contains(r.DispatchSource.Quote, "**HOMEOPS-1118**") || !strings.Contains(r.DispatchSource.Quote, "刚才的口令是什么") || !strings.Contains(r.DispatchSource.Quote, shortTaskID(first.RequestID)) {
				t.Fatal("review lost the earlier visible dialogue")
			}
			if strings.Contains(r.DispatchSource.Quote, "FUTURE-") {
				t.Fatal("old-card review included future rounds")
			}
			if r.Target.Model != target.Model || r.Target.Options["variant"] != "high" || r.DispatchSource.InputKind != "review" || x.opens != 2 || !reflect.DeepEqual(before, st.Tasks[before.ID]) || st.Selected[userScope(m)] != "nas" || !strings.HasPrefix(r.RequestMessage, m.Text+"\n\n") || !strings.Contains(r.RequestMessage, "仅获只读讨论授权") {
				t.Fatal("review changed the DS task, executor or authorization")
			}
			sends := x.sends
			accept(t, e, m)
			tick(t, e)
			if x.sends != sends || len(e.Store.snapshot().Tasks) != 2 {
				t.Fatal("duplicate review redispatched")
			}
		})
	}
}

func TestReviewHistoryBoundsRedactionAndScope(t *testing.T) {
	for _, kind := range []string{"bounded", "eight-rounds", "user", "chat", "topic", "route", "project", "provider", "foreign-round"} {
		t.Run(kind, func(t *testing.T) {
			e, _, _, _ := testEngine(t)
			target := e.Config.Routes["nas"]
			reviewTarget := target
			reviewTarget.Model = "codebuddy-direct/hy4free"
			reviewTarget.Options = map[string]any{"variant": "high"}
			e.Config.Routes["nas-review"] = reviewTarget
			e.Config.ReviewRoute = "nas-review"
			m := message("review", "/review 只看已存文字")
			task := &Task{ID: "owned-task", RouteID: "nas", Target: target, Source: message("source", "original"), RequestID: "current-request", DispatchSource: message("current", "current input"), Phase: "completed", Result: "current visible result"}
			for i := 0; i < 12; i++ {
				task.Replies = append(task.Replies, ReplyRound{RequestID: fmt.Sprintf("request-%02d", i), Source: message(fmt.Sprintf("in-%02d", i), fmt.Sprintf("OLD-%02d secret=synthetic-secret\n", i)+strings.Repeat("文", 1100)), Result: fmt.Sprintf("ANSWER-%02d\n", i) + strings.Repeat("答", 4200)})
			}
			if kind == "eight-rounds" {
				for i := range task.Replies {
					task.Replies[i].Source.Text = fmt.Sprintf("OLD-%02d secret=synthetic-secret", i)
					task.Replies[i].Result = fmt.Sprintf("ANSWER-%02d", i)
				}
			}
			task.RequestMessage = "MACHINE-PROMPT-EXCLUDED"
			task.EventContext = "MACHINE-CONTEXT-EXCLUDED"
			task.DispatchSource.Quote = "QUOTE-EXCLUDED"
			task.DispatchSource.History = "HISTORY-EXCLUDED"
			task.Attachments = []Attachment{{ID: "ATTACHMENT-EXCLUDED", Type: "file"}}
			switch kind {
			case "user":
				task.Source.UserID = "other"
			case "chat":
				task.Source.ChatID = "foreign"
			case "topic":
				task.Source.TopicID = "foreign"
			case "route":
				task.RouteID = "foreign"
			case "project":
				task.Target.ProjectID = "foreign"
			case "provider":
				task.Target.ProviderID = "foreign"
			case "foreign-round":
				task.Replies[len(task.Replies)-1].Source.UserID = "other"
				task.Replies[len(task.Replies)-1].Result = "FOREIGN-ROUND-EXCLUDED"
			}
			before := fmt.Sprintf("%#v", task)
			cmd := &SessionCommand{}
			if err := e.review(context.Background(), m, key(m, m.ID), cmd, task, strings.Fields(m.Text)); err != nil {
				t.Fatal(err)
			}
			st := e.Store.snapshot()
			if kind != "bounded" && kind != "eight-rounds" && kind != "foreign-round" {
				if len(st.Tasks) != 0 || cmd.ReviewTaskID != "" {
					t.Fatal("foreign task supplied review context")
				}
				return
			}
			r := st.Tasks[cmd.ReviewTaskID]
			if r == nil {
				t.Fatal("review not created")
			}
			quote := r.DispatchSource.Quote
			if len([]rune(quote)) > 12000 || !strings.Contains(quote, "省略") || !strings.Contains(quote, "current visible result") || !strings.Contains(quote, shortTaskID(task.RequestID)) {
				t.Fatal("missing bounded context or omission notice", len([]rune(quote)))
			}
			if !strings.Contains(quote, "[已脱敏]") || (kind == "eight-rounds" && strings.Count(quote, " / request ") != 8) {
				t.Fatal("redaction or exact eight-round window lost")
			}
			for _, secret := range []string{"synthetic-secret", "OLD-00", "OLD-01", "OLD-02", "OLD-03", "OLD-04", "FOREIGN-ROUND-EXCLUDED", "MACHINE-PROMPT-EXCLUDED", "MACHINE-CONTEXT-EXCLUDED", "QUOTE-EXCLUDED", "HISTORY-EXCLUDED", "ATTACHMENT-EXCLUDED"} {
				if strings.Contains(quote, secret) {
					t.Fatal("review leaked omitted/private content", secret)
				}
			}
			if strings.Count(quote, " / request ") > 8 || fmt.Sprintf("%#v", task) != before {
				t.Fatal("history exceeded round bound or mutated source")
			}
		})
	}
}
