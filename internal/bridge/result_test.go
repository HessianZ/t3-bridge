package bridge

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	channel "github.com/larksuite/channel-sdk-go"
)

type resultHTTPClient func(*http.Request) (*http.Response, error)

func (f resultHTTPClient) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestResultMarkdownReplyAndRetry(t *testing.T) {
	e, x, _, _ := testEngine(t)
	accept(t, e, message("job", "/nas 合成长回答"))
	tick(t, e)
	intro := "## 结果\n\n**粗体** 和 `inline`\n\n| 项 | 状态 |\n| --- | --- |\n| 样本 | 通过 |\n\n"
	full := intro + "```bash\n" + strings.Repeat("printf 'synthetic line'\n", 100) + "echo secret=synthetic-private\n```\n\n末尾标记"
	x.observation = Observation{Status: "completed", Summary: full}
	tick(t, e)
	task := onlyTask(t, e)

	type reply struct {
		MsgType       string `json:"msg_type"`
		Content       string `json:"content"`
		UUID          string `json:"uuid"`
		ReplyInThread bool   `json:"reply_in_thread"`
	}
	replies := map[string][]reply{}
	fail := false
	client := resultHTTPClient(func(r *http.Request) (*http.Response, error) {
		res := httptest.NewRecorder()
		res.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			fmt.Fprint(res, `{"code":0,"tenant_access_token":"synthetic-token","expire":7200}`)
		case "/open-apis/im/v1/messages/page-one/reply", "/open-apis/im/v1/messages/page-two/reply", "/open-apis/im/v1/messages/bad-page/reply":
			var body reply
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				return nil, err
			}
			replies[r.URL.Path] = append(replies[r.URL.Path], body)
			if fail {
				fmt.Fprint(res, `{"code":999,"msg":"synthetic error"}`)
			} else {
				fmt.Fprint(res, `{"code":0,"data":{"message_id":"synthetic-result-card"}}`)
			}
		default:
			t.Errorf("unexpected API request %s", r.URL.Path)
			res.WriteHeader(http.StatusNotFound)
		}
		return res.Result(), nil
	})
	ch, err := channel.New("fake-app", "synthetic-secret", channel.WithHTTPClient(client), channel.WithEnableTokenCache(false))
	if err != nil {
		t.Fatal(err)
	}
	e.Lark = &Lark{Channel: ch}
	request := "/result " + shortTaskID(task.RequestID)
	getReplies := func(id string) []reply { return replies["/open-apis/im/v1/messages/"+id+"/reply"] }
	markdown := func(r reply, page int) string {
		t.Helper()
		if r.MsgType != "interactive" || !r.ReplyInThread || r.UUID == "" {
			t.Fatalf("expected an interactive thread reply, got %+v", r)
		}
		elements := card2Elements(t, r.Content)
		header := elements[0].(map[string]any)["text"].(map[string]any)
		body := elements[1].(map[string]any)
		footer := elements[2].(map[string]any)["text"].(map[string]any)
		if header["tag"] != "plain_text" || !strings.Contains(header["content"].(string), fmt.Sprintf("全文 %d/2", page)) || body["tag"] != "markdown" || footer["tag"] != "plain_text" || !strings.Contains(footer["content"].(string), request) {
			t.Fatalf("unexpected result card: %v", elements)
		}
		return body["content"].(string)
	}

	one := message("page-one", "/result")
	one.ParentID = task.CardID
	accept(t, e, one)
	tick(t, e)
	first := markdown(getReplies(one.ID)[0], 1)
	if !strings.HasPrefix(first, intro+"```bash\n") || !strings.HasSuffix(first, "\n```") {
		t.Fatal("first page lost Markdown or its closing fence")
	}

	two := message("page-two", request+" 2")
	fail = true
	accept(t, e, two)
	tick(t, e)
	cmd := e.Store.snapshot().Commands[key(two, two.ID)]
	if cmd == nil || !cmd.ReplyCard || !strings.Contains(cmd.Issue, "尚未交付") {
		t.Fatal("failed result page falsely acknowledged")
	}
	fail = false
	tick(t, e)
	attempts := getReplies(two.ID)
	if len(attempts) != 2 || attempts[0] != attempts[1] || len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("retry changed the body, reply type, or UUID")
	}
	second := markdown(attempts[1], 2)
	if !strings.HasPrefix(second, "```bash\n") || !strings.HasSuffix(second, "```\n\n末尾标记") || strings.Contains(second, "synthetic-private") {
		t.Fatal("continued code, redaction or result ending lost")
	}
	if strings.TrimSuffix(first, "```")+strings.TrimPrefix(second, "```bash\n") != task.Result {
		t.Fatal("pagination lost part of the stored result")
	}

	bad := message("bad-page", request+" 99")
	accept(t, e, bad)
	tick(t, e)
	if r := getReplies(bad.ID)[0]; r.MsgType != "text" || !strings.Contains(r.Content, "全文共2页") || x.sends != 1 {
		t.Fatal("page error should use a plain-text receipt without dispatching a run")
	}
}
