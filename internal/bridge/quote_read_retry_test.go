package bridge

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestInboundQuoteReadRetry(t *testing.T) {
	const (
		cardID         = "card2-message"
		forwardID      = "forward"
		nestedID       = "nested"
		upstreamDetail = "synthetic-upstream-detail"
		forwardBody    = `{"code":0,"data":{"items":[
			{"message_id":"forward","chat_id":"chat","thread_id":"topic","msg_type":"merge_forward"},
			{"message_id":"nested","upper_message_id":"forward","msg_type":"merge_forward"}
		]}}`
		nestedBody = `{"code":0,"data":{"items":[
			{"message_id":"nested","upper_message_id":"forward","msg_type":"merge_forward"},
			{"message_id":"child","upper_message_id":"nested","msg_type":"text","body":{"content":"{\"text\":\"合成转发正文\"}"}}
		]}}`
		outsideScope = `{"code":0,"data":{"items":[
			{"message_id":"card2-message","chat_id":"outside-chat","thread_id":"topic","msg_type":"text","body":{"content":"{\"text\":\"synthetic-upstream-detail\"}"}}
		]}}`
	)
	type read struct {
		id         string
		status     int
		body       string
		retryAfter bool
	}
	card := read{id: cardID, status: http.StatusOK, body: string(quoteFixture(t, "interactive-card2-get.json"))}
	forward := read{id: forwardID, status: http.StatusOK, body: forwardBody}
	nested := read{id: nestedID, status: http.StatusOK, body: nestedBody}
	htmlGateway := "<html>" + upstreamDetail + "</html>"
	for _, tc := range []struct {
		name        string
		reads       []read
		wantQuote   string
		withForward bool
	}{
		{
			name:      "HTML gateway recovery",
			reads:     []read{{id: cardID, status: http.StatusBadGateway, body: htmlGateway}, card},
			wantQuote: "目标：nas",
		},
		{
			name:      "nested gateway recovery after successful root",
			reads:     []read{forward, {id: nestedID, status: http.StatusBadGateway, body: htmlGateway}, nested},
			wantQuote: "合成转发正文",
		},
		{
			name:  "root and nested read share retry",
			reads: []read{{id: forwardID, status: http.StatusGatewayTimeout}, forward, {id: nestedID, status: http.StatusBadGateway, body: htmlGateway}},
		},
		{
			name:        "forward and quoted roots share retry",
			reads:       []read{forward, {id: nestedID, status: http.StatusBadGateway, body: htmlGateway}, nested, {id: cardID, status: http.StatusServiceUnavailable, body: htmlGateway}},
			withForward: true,
		},
		{
			name:  "Retry-After header even when empty",
			reads: []read{{id: cardID, status: http.StatusServiceUnavailable, body: htmlGateway, retryAfter: true}},
		},
		{
			name:  "rate limit",
			reads: []read{{id: cardID, status: http.StatusTooManyRequests, body: htmlGateway}},
		},
		{
			name:  "permission denied",
			reads: []read{{id: cardID, status: http.StatusForbidden, body: `{"code":999,"msg":"` + upstreamDetail + `"}`}},
		},
		{
			name:  "malformed success response",
			reads: []read{{id: cardID, status: http.StatusOK, body: `{"msg":"` + upstreamDetail + `"`}},
		},
		{
			name:  "scope mismatch",
			reads: []read{{id: cardID, status: http.StatusOK, body: outsideScope}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _, _, x, f := setupAccess(t)
			e.Config.DefaultRoute = "nas"
			e.Classifier = &fakeClassifier{d: Decision{RouteID: "nas", Action: "new_task"}}
			var mu sync.Mutex
			var reads int
			var previousRead time.Time
			const prefix = "/open-apis/im/v1/messages/"
			f.extra = func(w http.ResponseWriter, r *http.Request) bool {
				id, ok := strings.CutPrefix(r.URL.Path, prefix)
				if !ok || strings.HasSuffix(id, "/reply") {
					return false
				}
				mu.Lock()
				defer mu.Unlock()
				reads++
				if reads > len(tc.reads) {
					t.Errorf("unexpected attempt %d for message %q", reads, id)
					w.WriteHeader(http.StatusNotFound)
					return true
				}
				step := tc.reads[reads-1]
				if r.Method != http.MethodGet || id != step.id || r.URL.Query().Get("card_msg_content_type") != "user_card_content" {
					t.Errorf("attempt %d: %s %s, want GET %s%s with user_card_content", reads, r.Method, r.URL, prefix, step.id)
				}
				if reads > 1 && tc.reads[reads-2].id == step.id && time.Since(previousRead) < 250*time.Millisecond {
					t.Error("retried before the 250ms backoff")
				}
				previousRead = time.Now()
				if step.retryAfter {
					w.Header().Set("Retry-After", "")
				}
				if step.body == htmlGateway {
					w.Header().Set("Content-Type", "text/html")
				}
				w.WriteHeader(step.status)
				io.WriteString(w, step.body)
				return true
			}

			m := message("request", "/nas 请总结资料")
			m.ParentID = tc.reads[0].id
			if tc.withForward {
				m.ID, m.InputKind, m.ParentID = forwardID, "merge_forward", cardID
			}
			accept(t, e, m)
			tick(t, e)

			mu.Lock()
			gotReads := reads
			mu.Unlock()
			if gotReads != len(tc.reads) {
				t.Fatalf("message GETs = %d, want %d", gotReads, len(tc.reads))
			}
			task := onlyTask(t, e)
			if tc.wantQuote != "" {
				if x.sends != 1 || !strings.Contains(x.request, tc.wantQuote) || strings.Contains(x.request, upstreamDetail) {
					t.Fatalf("sends = %d, request = %q", x.sends, x.request)
				}
				return
			}
			if x.opens != 0 || x.sends != 0 || task.RunID != "" || task.Phase != "clarify" {
				t.Fatalf("failed read opened %d threads, sent %d times, and left task %q with run %q", x.opens, x.sends, task.Phase, task.RunID)
			}
			hint := "重新引用"
			if tc.withForward {
				hint = "重新转发"
			}
			if task.CardID == "" || !strings.Contains(task.Summary, hint) || strings.Contains(task.Summary, upstreamDetail) {
				t.Fatalf("missing or unsafe failure reply: %q", task.Summary)
			}
		})
	}
}
