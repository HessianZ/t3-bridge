package bridge

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func card2Elements(t *testing.T, raw string) []any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	if body["schema"] != "2.0" || body["header"] != nil || body["elements"] != nil {
		t.Fatal("not a minimal Card 2.0 body", body)
	}
	config, ok := body["config"].(map[string]any)
	if !ok || config["update_multi"] != true || config["width_mode"] != "default" || config["wide_screen_mode"] != nil {
		t.Fatal("invalid Card 2.0 config", config)
	}
	content, ok := body["body"].(map[string]any)
	if !ok {
		t.Fatal("missing Card 2.0 body")
	}
	elements, ok := content["elements"].([]any)
	if !ok {
		t.Fatal("missing Card 2.0 elements")
	}
	return elements
}

func assertThreadButton(t *testing.T, raw, url string) {
	t.Helper()
	count := 0
	for _, rawElement := range card2Elements(t, raw) {
		element := rawElement.(map[string]any)
		label, _ := element["text"].(map[string]any)
		if label["content"] != "打开 T3 会话" {
			continue
		}
		count++
		want := map[string]any{
			"tag": "button", "type": "default",
			"text":      map[string]any{"tag": "plain_text", "content": "打开 T3 会话"},
			"behaviors": []any{map[string]any{"type": "open_url", "default_url": url}},
		}
		if url == "" || !reflect.DeepEqual(element, want) {
			t.Fatalf("unexpected thread button: %#v", element)
		}
	}
	if url != "" && count != 1 {
		t.Fatalf("got %d thread buttons, want 1", count)
	}
}

func TestCard2ThreadLinkUsesOnlyValidatedTaskMetadata(t *testing.T) {
	const hosted = "https://t3.example/threads/env%2Eone/thread%21one"
	const legacy = "t3-thread://v2/env.one/thread!one"
	for _, tc := range []struct{ name, link, url, text string }{
		{"hosted", hosted, hosted, ""},
		{"legacy", "[Previous title](" + legacy + ")", "", legacy},
		{"http", "http://t3.example/threads/env%2Eone/thread%21one", "", ""},
		{"wrong thread", "https://t3.example/threads/env%2Eone/other", "", ""},
		{"agent text only", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := &Task{Phase: "completed", Target: Target{EnvironmentID: "env.one"}, ThreadID: "thread!one", Link: tc.link, Summary: "Agent text: " + hosted}
			body := card(task)
			assertThreadButton(t, body, tc.url)
			if got := strings.Contains(body, "会话："); got != (tc.text != "") || (got && !strings.Contains(body, "会话："+tc.text)) {
				t.Fatalf("unexpected session metadata: %s", body)
			}
		})
	}
}

func TestCard2MarkdownAndIndependentMetadata(t *testing.T) {
	for _, tc := range []struct{ name, summary string }{
		{"markdown", "## 可用工具\n**只读能力**与`mcp`\n- 列出工具\n\n```go\nfmt.Println(\"synthetic\")\n```\n\n| 工具 | 状态 |\n| --- | --- |\n| mcp | 可用 |"},
		{"unclosed_code", "## 未闭合代码块\n```text\n**still code**"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			summary := tc.summary
			task := &Task{ID: "task", RouteID: "nas", Target: Target{Driver: "opencode", Model: "synthetic-model", EnvironmentID: "test"}, Phase: "completed", Summary: summary, ThreadID: "thread", Link: "t3-thread://v2/test/thread"}
			elements := card2Elements(t, card(task))
			if len(elements) != 3 {
				t.Fatal("metadata/answer/session must be separate", elements)
			}
			metadata := elements[0].(map[string]any)
			text := metadata["text"].(map[string]any)
			if metadata["tag"] != "div" || text["tag"] != "plain_text" || text["content"] != "目标：nas · 执行器：opencode / synthetic-model\n状态：completed" {
				t.Fatal("metadata changed or entered markdown", metadata)
			}
			answer := elements[1].(map[string]any)
			if answer["tag"] != "markdown" || answer["content"] != summary {
				t.Fatal("Markdown syntax escaped or mixed with metadata", answer)
			}
			session := elements[2].(map[string]any)
			sessionText := session["text"].(map[string]any)
			if session["tag"] != "div" || sessionText["tag"] != "plain_text" || sessionText["content"] != "会话："+task.Link {
				t.Fatal("unclosed answer can affect session metadata", session)
			}
		})
	}
}

func TestCard2DisplayRedactionAndLength(t *testing.T) {
	task := &Task{Phase: "completed", Summary: "**可见**\napi_key=synthetic-key\ntoken:synthetic-token\npassword=synthetic-password\nBearer synthetic-auth\nhttps://example.com/path?token=synthetic-url"}
	elements := card2Elements(t, card(task))
	answer := elements[1].(map[string]any)["content"].(string)
	if !strings.HasPrefix(answer, "**可见**\n") || strings.Count(answer, "[已脱敏]") != 5 || strings.Contains(answer, "synthetic-") {
		t.Fatal("display redaction changed", answer)
	}
	if elements[0].(map[string]any)["text"].(map[string]any)["content"] != "目标：待确认 · 执行器： / \n状态：completed" {
		t.Fatal("missing unknown target metadata")
	}
	task.Summary = "## 标题\n" + strings.Repeat("界", 1900)
	answer = card2Elements(t, card(task))[1].(map[string]any)["content"].(string)
	if len([]rune(answer)) != 1800 || !strings.HasSuffix(answer, "…") || !strings.HasPrefix(answer, "## 标题\n") {
		t.Fatal("existing 1800-rune bound changed")
	}
}

func TestCard2StopBehaviorAndTerminalStates(t *testing.T) {
	for _, tc := range []struct {
		phase   string
		buttons int
	}{
		{"ready", 1}, {"waiting_resource", 1}, {"tracking", 1},
		{"completed", 0}, {"failed", 0}, {"stopped", 0}, {"blocked", 0}, {"clarify", 0},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			phase := tc.phase
			task := &Task{ID: "original-task-id", Phase: phase, Summary: "合成正文"}
			elements := card2Elements(t, card(task))
			buttons := 0
			for _, raw := range elements {
				e := raw.(map[string]any)
				if e["tag"] == "action" || e["actions"] != nil {
					t.Fatal("Card 1.0 action module leaked")
				}
				if e["tag"] != "button" {
					continue
				}
				buttons++
				if e["value"] != nil || e["url"] != nil {
					t.Fatal("Card 1.0 button fields leaked")
				}
				behaviors := e["behaviors"].([]any)
				if len(behaviors) != 1 {
					t.Fatal("unexpected button behavior")
				}
				behavior := behaviors[0].(map[string]any)
				value := behavior["value"].(map[string]any)
				if behavior["type"] != "callback" || len(value) != 2 || value["action"] != "stop" || value["taskId"] != task.ID {
					t.Fatal("stop callback lost original identity", behavior)
				}
			}
			if buttons != tc.buttons {
				t.Fatal("terminal/active stop changed", phase, buttons)
			}
		})
	}
}
