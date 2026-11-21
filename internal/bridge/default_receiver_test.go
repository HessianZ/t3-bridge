package bridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A fixed synthetic response checks the HTTP contract, not model accuracy.
func TestDefaultReceiverContractDoesNotJudgeAnswerability(t *testing.T) {
	c := testConfig(t)
	c.DefaultRoute = "nas"
	key := filepath.Join(t.TempDir(), "hy4.secret")
	os.WriteFile(key, []byte("synthetic-key"), 0600)
	m := message("ordinary", "用一句话概括刚才的内容，保留限制条件。")
	h := Hy4{Config: Hy4Config{BaseURL: "https://copilot.tencent.com/v2", Model: "hy4flash", KeyFile: key, Verified: true}, Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body struct {
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Messages) != 2 {
			t.Fatal("missing bounded classifier input")
		}
		system := body.Messages[0].Content
		for _, contract := range []string{"只决定接收目标", "不判断能否回答", "任何普通消息", "不代表新建线程", "其他机器实际操作", "多个实际执行目标", "生产写入授权", "不得输出continue"} {
			if !strings.Contains(system, contract) {
				t.Fatal("default receiver contract missing", contract)
			}
		}
		if strings.Contains(system, m.Text) {
			t.Fatal("request text became a system instruction")
		}
		var input struct {
			Text, DefaultRoute string
			Routes             []string
		}
		if err := json.Unmarshal([]byte(body.Messages[1].Content), &input); err != nil {
			t.Fatal(err)
		}
		if input.Text != m.Text || input.DefaultRoute != "nas" || !reflect.DeepEqual(input.Routes, []string{"mac", "nas"}) {
			t.Fatal("original/default/visible routes lost")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(sseDelta(`{"routeId":"nas","action":"new_task"}`, "stop") + "data: [DONE]\n\n")), Header: http.Header{}}, nil
	})}}
	d, err := Route(context.Background(), c, m, nil, h)
	if err != nil || d != (Decision{"nas", "new_task"}) {
		t.Fatal(d, err)
	}
}

// Replay the observed model rejection unchanged. The contract patch must not
// silently convert uncertainty or retry that failed input; only a new accepted
// decision for a new user input can continue the original scoped session.
func TestDefaultReceiverObservedFailureKeepsSixRoundsAndFence(t *testing.T) {
	e, x, _, _ := testEngine(t)
	e.Config.DefaultRoute = "nas"
	for i := 0; i < 6; i++ {
		id := string(rune('a' + i))
		accept(t, e, message(id, "/nas 原会话第"+id+"轮"))
		tick(t, e)
		x.observation.Status = "completed"
		x.observation.Summary = "第" + id + "轮可见结论"
		tick(t, e)
	}
	original := onlyTask(t, e)
	if len(original.Replies) != 5 || x.sends != 6 {
		t.Fatal("six-round fixture missing")
	}
	key := filepath.Join(t.TempDir(), "hy4.secret")
	os.WriteFile(key, []byte("synthetic-key"), 0600)
	calls := 0
	e.Classifier = Hy4{Config: Hy4Config{BaseURL: "https://copilot.tencent.com/v2", Model: "hy4flash", KeyFile: key, Verified: true}, Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		var body struct{ Messages []struct{ Content string } }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		var input struct {
			DefaultRoute string
			Text         string
		}
		json.Unmarshal([]byte(body.Messages[1].Content), &input)
		if input.DefaultRoute != "nas" {
			t.Fatal("implicit current lost the real default target")
		}
		decision := `{"routeId":"","action":"clarify"}` // Actual pre-fix model shape, deliberately retained.
		if calls == 2 {
			decision = `{"routeId":"nas","action":"new_task"}`
		}
		if calls > 2 {
			t.Fatal("failed input reclassified/retried")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(sseDelta(decision, "stop") + "data: [DONE]\n\n")), Header: http.Header{}}, nil
	})}}
	failedInput := message("failed", "只回复重连通过，不要调用工具。")
	accept(t, e, failedInput)
	tick(t, e)
	st := e.Store.snapshot()
	failed := st.Tasks[stable("chat", "topic", failedInput.ID)]
	if failed == nil || failed.Phase != "clarify" || failed.ThreadID != "" || failed.RunID != "" || x.sends != 6 || !reflect.DeepEqual(original, st.Tasks[original.ID]) {
		t.Fatal("observed failure changed or replayed original execution")
	}
	e.Store.Close()
	s, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e.Store = s
	tick(t, e)
	if calls != 1 || !reflect.DeepEqual(failed, e.Store.task(failed.ID)) || !reflect.DeepEqual(original, e.Store.task(original.ID)) {
		t.Fatal("restart retried clarification or changed six-round identity")
	}
	fresh := message("fresh", "请把已有结论缩成一句话，不使用任何工具。")
	accept(t, e, fresh)
	tick(t, e)
	after := e.Store.task(original.ID)
	if calls != 2 || x.opens != 1 || x.sends != 7 || after.ThreadID != original.ThreadID || after.Source.ID != original.Source.ID || len(after.Replies) != 6 || !strings.HasPrefix(x.request, fresh.Text+"\n\n") || !reflect.DeepEqual(failed, e.Store.task(failed.ID)) {
		t.Fatal("new accepted input did not preserve the scoped original/failure identities")
	}
	want := append(append([]ReplyRound{}, original.Replies...), currentReply(original))
	for i, r := range after.Replies {
		if r.RequestID != want[i].RequestID || r.Result != want[i].Result || r.Source.Text != want[i].Source.Text {
			t.Fatal("new round overwrote retained history")
		}
	}
}
