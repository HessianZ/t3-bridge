package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sseDelta(content string, finish any) string {
	b, _ := json.Marshal(map[string]any{"model": "hy4-flash", "choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": content}, "finish_reason": finish}}})
	return "data: " + string(b) + "\n\n"
}
func TestCodeBuddyHy4StreamingDecision(t *testing.T) {
	key := filepath.Join(t.TempDir(), "hy4.secret")
	if err := os.WriteFile(key, []byte("synthetic-key"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	h := Hy4{Config: Hy4Config{BaseURL: "https://copilot.tencent.com/v2", Model: "hy4flash", KeyFile: key, Verified: true}, Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://copilot.tencent.com/v2/chat/completions" || r.Header.Get("Authorization") != "Bearer synthetic-key" {
			t.Fatal("incorrect official endpoint/auth")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		messages := body["messages"].([]any)
		system := messages[0].(map[string]any)
		prompt := system["content"].(string)
		if system["role"] != "system" || !strings.Contains(prompt, "未绑定的新消息") || !strings.Contains(prompt, "续聊由程序处理") || !strings.Contains(prompt, "new_task|coordinate|clarify") || !strings.Contains(prompt, "不得输出continue") || strings.Contains(prompt, "new_task|continue") {
			t.Fatal("classifier prompt permits continuation without a bound thread")
		}
		var input map[string]any
		if err := json.Unmarshal([]byte(messages[1].(map[string]any)["content"].(string)), &input); err != nil {
			t.Fatal(err)
		}
		if _, ok := input["defaultRoute"]; ok {
			t.Fatal("unconfigured default changed legacy request")
		}
		if !strings.Contains(prompt, "只有明确授权在单一机器执行才能 new_task") {
			t.Fatal("unconfigured default changed legacy contract")
		}
		if body["model"] != "hy4flash" || body["reasoning_effort"] != "none" || body["stream"] != true || body["max_tokens"] != float64(96) {
			t.Fatalf("invalid CodeBuddy parameters: %v", body)
		}
		if _, ok := body["thinking"]; ok {
			t.Fatal("unsupported thinking parameter sent")
		}
		stream := ": heartbeat\r\n\r\n" + sseDelta(`{"routeId":"nas",`, nil) + sseDelta(`"action":"new_task"}`, "stop") + "data: {\"choices\":[],\"usage\":{\"completion_tokens\":24}}\n\n" + "data: [DONE]\n\n"
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(stream)), Header: http.Header{"Content-Type": []string{"text/event-stream"}}}, nil
	})}}
	d, err := Route(context.Background(), testConfig(t), message("one", "请替我确认NAS现在磁盘是否正常"), nil, h)
	if err != nil || d != (Decision{"nas", "new_task"}) || calls != 1 {
		t.Fatalf("decision=%+v err=%v calls=%d", d, err, calls)
	}
}

type streamFailure struct{}

func (streamFailure) Read([]byte) (int, error) {
	return 0, errors.New("private remote failure synthetic-key")
}

func TestCodeBuddyHy4StreamFailures(t *testing.T) {
	key := filepath.Join(t.TempDir(), "hy4.secret")
	if err := os.WriteFile(key, []byte("synthetic-key"), 0600); err != nil {
		t.Fatal(err)
	}
	decision := `{"routeId":"nas","action":"new_task"}`
	completed := sseDelta(decision, "stop")
	for _, tc := range []struct {
		name, stream string
		status       int
		readErr      bool
	}{
		{"http_error", "private remote failure synthetic-key", 400, false},
		{"remote_error", `data: {"error":{"message":"private remote failure synthetic-key"}}` + "\n\n", 200, false},
		{"malformed_event", "data: {not JSON}\n\n", 200, false},
		{"truncated_json", sseDelta(`{"routeId":`, nil), 200, false},
		{"missing_done", completed, 200, false},
		{"missing_stop", sseDelta(decision, nil) + "data: [DONE]\n\n", 200, false},
		{"empty_finish_missing_stop", sseDelta(decision, "") + "data: [DONE]\n\n", 200, false},
		{"token_limit", sseDelta(decision, "length") + "data: [DONE]\n\n", 200, false},
		{"read_error", completed, 200, true},
		{"line_limit", "data: " + strings.Repeat("x", 16384) + "\n\n", 200, false},
		{"output_limit", sseDelta(strings.Repeat("x", 9000), nil) + sseDelta(strings.Repeat("x", 9000), nil) + "data: [DONE]\n\n", 200, false},
		{"total_limit", strings.Repeat(": heartbeat\n", 6000) + completed + "data: [DONE]\n\n", 200, false},
		{"decision_unknown_field", sseDelta(`{"routeId":"nas","action":"new_task","provider":"other"}`, "stop") + "data: [DONE]\n\n", 200, false},
		{"decision_unknown_route", sseDelta(`{"routeId":"other","action":"new_task"}`, "stop") + "data: [DONE]\n\n", 200, false},
		{"decision_unknown_action", sseDelta(`{"routeId":"nas","action":"execute"}`, "stop") + "data: [DONE]\n\n", 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			h := Hy4{Config: Hy4Config{BaseURL: "https://copilot.tencent.com/v2", Model: "hy4flash", KeyFile: key, Verified: true}, Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				var reader io.Reader = strings.NewReader(tc.stream)
				if tc.readErr {
					reader = io.MultiReader(reader, streamFailure{})
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(reader), Header: http.Header{}}, nil
			})}}
			_, err := Route(context.Background(), testConfig(t), message("one", "核对那台服务器"), nil, h)
			if err == nil || calls != 1 {
				t.Fatal("invalid stream accepted or retried", err, calls)
			}
			if strings.Contains(err.Error(), "private remote") || strings.Contains(err.Error(), "synthetic-key") {
				t.Fatal("remote error/credential leaked")
			}
		})
	}
}

func TestHy4OnlyApprovedOfficialPairs(t *testing.T) {
	for _, tc := range []struct {
		base, model string
		valid       bool
	}{
		{"https://copilot.tencent.com/v2", "hy4flash", true},
		{"https://tokenhub.tencentmaas.com/v1", "hy4-flash", true},
		{"https://copilot.tencent.com/v2", "hy4-flash", false},
		{"https://tokenhub.tencentmaas.com/v1", "hy4flash", false},
		{"https://copilot.tencent.com/v2", "example/free", false},
		{"https://example.com/v2", "hy4flash", false},
		{"https://copilot.tencent.com/v2?token=secret", "hy4flash", false},
	} {
		t.Run(tc.base+"/"+tc.model, func(t *testing.T) {
			c := testConfig(t)
			c.Classifier = Hy4Config{BaseURL: tc.base, Model: tc.model, KeyFile: "/private/hy4.secret"}
			if err := c.Validate(); (err == nil) != tc.valid {
				t.Fatal("official pair validation", err)
			}
		})
	}
}

func TestCodeBuddyHy4EmptyFinishReasonFixture(t *testing.T) {
	stream, err := os.ReadFile("../../testdata/codebuddy-hy4-empty-finish.sse")
	if err != nil {
		t.Fatal(err)
	}
	content, err := readHy4SSE(strings.NewReader(string(stream)))
	if err != nil {
		t.Fatal(err)
	}
	var decision Decision
	if err = decodeStrict([]byte(content), &decision); err != nil || decision != (Decision{"nas", "new_task"}) {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
}
