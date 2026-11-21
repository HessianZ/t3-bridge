package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	channel "github.com/larksuite/channel-sdk-go"
	channeltypes "github.com/larksuite/channel-sdk-go/types"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestChatTaskControlReplyAnchorsLoopback(t *testing.T) {
	e, executor, _, _ := testEngine(t)
	e.Config.DefaultRoute = "mac"
	e.Config.Admins = []string{"user"}
	e.Config.Topics = nil
	e.Config.Chats = append(e.Config.Chats, "oc_group")
	loop := newLoopbackLark(t)
	lark, err := loop.channel(e.Config, e.Store)
	if err != nil {
		t.Fatal(err)
	}
	e.Lark = lark
	task := func(id, topic string) *Task {
		source := Message{ID: "om_" + id, UserID: "other", ChatID: "oc_group", ChatType: "group", TopicID: topic}
		return &Task{
			ID: id, RequestID: "request-" + id, ReceiverAppID: e.Config.AppID, RouteID: "mac", Target: e.Config.Routes["mac"],
			Source: source, DispatchSource: source, Phase: "tracking", ThreadID: "thread-" + id, RunID: "run", TextReply: true,
			WorkingReaction: &workingReactionReceipt{MessageID: source.ID},
		}
	}
	selected, sibling := task("select", "omt_selected"), task("other", "omt_other")
	if err := e.Store.update(func(st *State) error {
		st.AccountID = e.Config.AppID
		st.Tasks[selected.ID], st.Tasks[sibling.ID] = selected, sibling
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	list := Message{ID: "om_admin_list", UserID: "user", ChatID: "chat", ChatType: "p2p", Text: "/status tasks oc_group:omt_selected"}
	accept(t, e, list)
	tick(t, e)
	receipt := e.Store.snapshot().Commands[key(list, list.ID)]
	token := "task:" + chatTaskToken(selected)
	if receipt == nil || !strings.Contains(receipt.Reply, token) || strings.Contains(receipt.Reply, chatTaskToken(sibling)) {
		t.Fatalf("topic query did not identify the intended round: %+v", receipt)
	}
	stop := list
	stop.ID, stop.Text = "om_admin_stop", "/stop "+token
	accept(t, e, stop)
	tick(t, e)
	if stopped := e.Store.task(selected.ID); stopped.Phase != "stopped" || stopped.CardID != "card-"+selected.Source.ID ||
		executor.stops != 1 || executor.sends != 0 || e.Store.task(sibling.ID).Phase != "tracking" {
		t.Fatalf("wrong original run or reply: %+v", stopped)
	}
	loop.mu.Lock()
	defer loop.mu.Unlock()
	if len(loop.replies) != 3 || loop.replies[list.ID] != 1 || loop.replies[stop.ID] != 1 || loop.replies[selected.Source.ID] != 1 {
		t.Fatalf("wrong Feishu reply anchors: %+v", loop.replies)
	}
}

func TestGenericBridgeLoopbackTransports(t *testing.T) {
	t.Run("runtime_file", func(t *testing.T) { testGenericBridgeTransports(t, false) })
	t.Run("https_endpoint", func(t *testing.T) { testGenericBridgeTransports(t, true) })
}

func testGenericBridgeTransports(t *testing.T, useTLS bool) {
	t.Helper()
	cfg, err := LoadConfig("../../config/generic.example.json")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg.StateDir = filepath.Join(dir, "state")
	cfg.MCP.TokenFile = filepath.Join(dir, "oauth.json")
	target := cfg.Routes[cfg.DefaultRoute]
	t3 := &loopbackT3{target: target}
	t3Server := t3.serve(t, useTLS)
	var resource string
	if useTLS {
		cfg.MCP.Endpoint = t3Server.URL + "/mcp"
		resource = cfg.MCP.Endpoint
	} else {
		endpoint, err := url.Parse(t3Server.URL)
		if err != nil {
			t.Fatal(err)
		}
		cfg.MCP.Endpoint = ""
		cfg.MCP.RuntimeFile = filepath.Join(dir, "server-runtime.json")
		if err := os.WriteFile(cfg.MCP.RuntimeFile, []byte(fmt.Sprintf(`{"port":%s}`, endpoint.Port())), 0600); err != nil {
			t.Fatal(err)
		}
	}
	token := fmt.Sprintf(`{"access_token":"synthetic-token","expiresAt":"2099-01-01T00:00:00Z","resource":%q}`, resource)
	if err := os.WriteFile(cfg.MCP.TokenFile, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	lark := newLoopbackLark(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	start := func() (*Engine, *websocket.Conn, func()) {
		store, err := OpenStore(cfg.StateDir)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.BindAccount(cfg.AppID); err != nil {
			store.Close()
			t.Fatal(err)
		}
		conn := NewConnections(cfg, store)
		conn.newLark = lark.channel
		if useTLS {
			conn.newMCP = func(ctx context.Context, config MCPConfig) (*MCPClient, error) {
				return connectVerifiedMCP(ctx, config, t3Server.Client().Transport)
			}
		}
		var peer *websocket.Conn
		stop := sync.OnceFunc(func() {
			conn.Close(context.Background())
			if conn.stopped != nil {
				select {
				case <-conn.stopped:
				default:
					t.Error("Feishu receiver was not joined before closing the store")
				}
			}
			if peer != nil {
				peer.Close()
			}
			store.Close()
		})
		t.Cleanup(stop)
		e := &Engine{Store: store, Now: func() time.Time { return time.Now().Add(2 * quietWindow) }}
		if _, err := conn.Reconnect(ctx, e); err != nil {
			t.Fatal(err)
		}
		select {
		case peer = <-lark.peers:
		case exit := <-conn.Exits:
			t.Fatalf("Feishu SDK exited: %v", exit.Err)
		case <-ctx.Done():
			t.Fatal("Feishu SDK did not connect")
		}
		return e, peer, stop
	}

	e, peer, stop := start()
	send := func(event, id, user, kind, content string) {
		t.Helper()
		lark.send(t, ctx, peer, event, id, user, cfg.Chats[0], kind, content)
		if err := e.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	send("evt-denied", "denied", "unauthorized-user", "text", `{"text":"这一条不可派发"}`)
	if len(e.Store.snapshot().Tasks) != 0 {
		t.Fatal("unauthorized input reached worker")
	}
	send("evt-first", "first", cfg.Users[0], "text", `{"text":"只回复合成结果。"}`)
	first := onlyTask(t, e)
	if first.Phase != "tracking" || first.RunID != "run-1" || first.CardID != "card-first" {
		t.Fatalf("initial send/card receipt missing: phase=%s run=%q card=%q", first.Phase, first.RunID, first.CardID)
	}

	stop()
	e, peer, _ = start()
	t3.completeFirst()
	send("evt-redelivered", "first", cfg.Users[0], "text", `{"text":"只回复合成结果。"}`)
	resumed := onlyTask(t, e)
	const expectedResult = "合成结果\n\nT3 计划草案\n\n1. 核对配置 [已脱敏]"
	if resumed.Phase != "completed" || resumed.ThreadID != first.ThreadID || resumed.RunID != first.RunID ||
		resumed.RequestID != first.RequestID || resumed.CardID != first.CardID || resumed.Result != expectedResult {
		t.Fatalf("restart did not recover first receipt and answer: phase=%s result=%q", resumed.Phase, resumed.Result)
	}

	send("evt-second", "second", cfg.Users[0], "post", `{"title":"",
		"content":[[{"tag":"text","text":"续问一句。"}],[{"tag":"text","text":"补充第二段。"}]],
		"content_v2":[[{"tag":"md","text":"**续问一句。**"}],[{"tag":"text","text":"补充第二段。"}]]}`)
	second := onlyTask(t, e)
	if second.Phase != "tracking" || second.ThreadID != first.ThreadID || second.RunID != "run-2" ||
		second.RequestID == first.RequestID || len(second.Replies) != 1 || second.Replies[0].Result != expectedResult {
		t.Fatalf("follow-up did not reuse original thread and retain previous round: phase=%s run=%q", second.Phase, second.RunID)
	}
	send("evt-stop", "stop", cfg.Users[0], "text", `{"text":"/stop"}`)
	stopped := onlyTask(t, e)
	if stopped.Phase != "stopped" || !stopped.StopSent || stopped.NeedsReconcile {
		t.Fatalf("stop receipt missing: phase=%s", stopped.Phase)
	}

	t3.mu.Lock()
	defer t3.mu.Unlock()
	if t3.launches != 1 || len(t3.requests) != 2 || t3.stopped != "run-2" ||
		!strings.HasPrefix(t3.requests[0], "只回复合成结果。\n") {
		t.Fatal("unexpected launch, replay, request body or stop target")
	}
	if t3.stopRequestID != stable(second.ID, second.RequestID, "stop") {
		t.Fatalf("unexpected interrupt request ID: %v", t3.stopRequestID)
	}
	if !strings.HasPrefix(t3.requests[1], "**续问一句。**\n补充第二段。\n") {
		t.Fatalf("rich text follow-up lost Markdown or paragraphs: %q", strings.SplitN(t3.requests[1], "\n\n", 2)[0])
	}
	lark.mu.Lock()
	defer lark.mu.Unlock()
	if lark.replies["first"] != 1 || lark.replies["denied"] != 0 || !strings.Contains(lark.patchedResult, "合成结果") ||
		!strings.Contains(lark.patchedResult, "T3 计划草案") || strings.Contains(lark.patchedResult, "synthetic-plan-value") {
		t.Fatal("missing original-card result or unexpected duplicate/unauthorized reply")
	}
}

type loopbackLark struct {
	url           string
	peers         chan *websocket.Conn
	delivered     chan string
	mu            sync.Mutex
	replies       map[string]int
	patchedResult string
}

func newLoopbackLark(t *testing.T, handlers ...func(http.ResponseWriter, *http.Request) bool) *loopbackLark {
	t.Helper()
	f := &loopbackLark{peers: make(chan *websocket.Conn, 1), delivered: make(chan string, 1), replies: map[string]int{}}
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == larkws.GenEndpointUri:
			endpoint := "ws://" + r.Host + "/socket?device_id=synthetic&service_id=1"
			json.NewEncoder(w).Encode(larkws.EndpointResp{Data: &larkws.Endpoint{Url: endpoint}})
		case r.URL.Path == "/socket":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			select {
			case f.peers <- conn:
			default:
				conn.Close()
				t.Error("unexpected additional Feishu connection")
			}
		case strings.Contains(r.URL.Path, "tenant_access_token"):
			io.WriteString(w, `{"code":0,"tenant_access_token":"synthetic-feishu-token","expire":7200}`)
		case r.URL.Path == "/open-apis/bot/v3/info":
			io.WriteString(w, `{"code":0,"bot":{"open_id":"synthetic-bot","app_name":"Loopback","activate_status":1}}`)
		case strings.HasPrefix(r.URL.Path, "/open-apis/application/v6/applications/"):
			io.WriteString(w, `{"code":0,"data":{"app":{"owner":{"owner_id":"synthetic-owner"}}}}`)
		case strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages/"):
			var body struct {
				Content string `json:"content"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			id := strings.TrimPrefix(r.URL.Path, "/open-apis/im/v1/messages/")
			switch {
			case r.Method == http.MethodPost && strings.HasSuffix(id, "/reply"):
				source := strings.TrimSuffix(id, "/reply")
				f.replies[source]++
				fmt.Fprintf(w, `{"code":0,"data":{"message_id":"card-%s"}}`, source)
			case r.Method == http.MethodPatch:
				if id == "card-first" && strings.Contains(body.Content, "合成结果") {
					f.patchedResult = body.Content
				}
				io.WriteString(w, `{"code":0,"data":{}}`)
			default:
				t.Errorf("unexpected Feishu message API: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(404)
			}
		default:
			for _, handle := range handlers {
				if handle(w, r) {
					return
				}
			}
			t.Errorf("unexpected Feishu endpoint: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	f.url = server.URL
	return f
}

func (f *loopbackLark) channel(c Config, s *Store) (*Lark, error) {
	safety := channeltypes.DefaultChannelConfig().Safety
	safety.Batch.MaxMessages = 1
	sdk, err := channel.New(c.AppID, "synthetic-secret", channel.WithDomain(f.url), channel.WithEnableTokenCache(false),
		channel.WithLogLevel(larkcore.LogLevelError), channel.WithSafetyConfig(safety))
	if err != nil {
		return nil, err
	}
	lark := newLark(sdk, c, s)
	// A WebSocket frame acknowledgement can precede the Channel SDK's queued delivery.
	sdk.OnMessage(func(ctx context.Context, msg *channel.NormalizedMessage) error {
		select {
		case f.delivered <- msg.MessageID:
		case <-ctx.Done():
		}
		return nil
	})
	return lark, nil
}

func (f *loopbackLark) send(t *testing.T, ctx context.Context, peer *websocket.Conn, event, id, user, chat, kind, content string) {
	t.Helper()
	at := fmt.Sprint(time.Now().UnixMilli())
	payload := fmt.Sprintf(`{"schema":"2.0","header":{"event_id":%q,"event_type":"im.message.receive_v1","create_time":%q},
		"event":{"sender":{"sender_type":"user","sender_id":{"open_id":%q}},
		"message":{"message_id":%q,"create_time":%q,"chat_id":%q,"chat_type":"p2p","message_type":%q,"content":%q}}}`,
		event, at, user, id, at, chat, kind, content)
	frame := larkws.Frame{Method: int32(larkws.FrameTypeData), Service: 1, Payload: []byte(payload), Headers: []larkws.Header{
		{Key: larkws.HeaderType, Value: string(larkws.MessageTypeEvent)}, {Key: larkws.HeaderMessageID, Value: event},
	}}
	data, err := frame.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	deadline, _ := ctx.Deadline()
	if err := peer.SetWriteDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if err := peer.WriteMessage(websocket.BinaryMessage, data); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-f.delivered:
		if got != id {
			t.Fatalf("delivered message %q, want %q", got, id)
		}
	case <-ctx.Done():
		t.Fatalf("message %q did not reach Bridge handler", id)
	}
}

type loopbackT3 struct {
	target        Target
	mu            sync.Mutex
	launches      int
	requests      []string
	firstDone     bool
	stopped       string
	stopRequestID string
}

func (f *loopbackT3) completeFirst() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.firstDone = true
}

func (f *loopbackT3) serve(t *testing.T, useTLS bool) *httptest.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "synthetic-t3", Version: "test"}, nil)
	for _, name := range []string{"t3_environment_read", "t3_project_read", "orchestrator_capabilities", "t3_thread_launch", "t3_thread_configure",
		"t3_thread_send", "t3_thread_wait", "t3_thread_read", "t3_thread_interrupt"} {
		mcp.AddTool(server, &mcp.Tool{Name: name}, func(_ context.Context, _ *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, map[string]any, error) {
			out, err := f.call(name, args)
			if err != nil {
				t.Error(err)
			}
			return nil, out, err
		})
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	listen := httptest.NewServer
	if useTLS {
		listen = httptest.NewTLSServer
	}
	httpServer := listen(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Error("incorrect MCP endpoint or token reference")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(httpServer.Close)
	return httpServer
}

func (f *loopbackT3) call(name string, args map[string]any) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	target := f.target
	if id := args["environmentId"]; id != nil && id != target.EnvironmentID {
		return nil, fmt.Errorf("%s used a different environment", name)
	}
	if id := args["threadId"]; id != nil && id != "thread" {
		return nil, fmt.Errorf("%s used a different thread", name)
	}
	switch name {
	case "t3_environment_read":
		return map[string]any{"environmentId": target.EnvironmentID}, nil
	case "t3_project_read":
		return map[string]any{"id": target.ProjectID}, nil
	case "orchestrator_capabilities":
		return map[string]any{"runtimeMode": target.RuntimeMode, "providers": []any{map[string]any{
			"providerInstanceId": target.ProviderID, "driverKind": target.Driver, "runtimeModes": []string{target.RuntimeMode},
			"models": []any{map[string]any{"id": target.Model}},
		}}}, nil
	case "t3_thread_launch":
		if args["projectId"] != target.ProjectID || args["message"] != nil {
			return nil, errors.New("launch omitted target or included a message before verification")
		}
		f.launches++
		return map[string]any{"threadId": "thread", "projectId": target.ProjectID}, nil
	case "t3_thread_configure":
		return map[string]any{"threadId": "thread", "runtimeMode": target.RuntimeMode, "interactionMode": target.InteractionMode,
			"modelSelection": map[string]any{"provider": target.Driver, "instanceId": target.ProviderID, "model": target.Model, "options": target.Options}}, nil
	case "t3_thread_send":
		message, ok := args["message"].(string)
		if !ok || message == "" || args["mode"] != "queue" || args["clientRequestId"] == "" {
			return nil, errors.New("invalid queued submission")
		}
		f.requests = append(f.requests, message)
		return map[string]any{"threadId": "thread", "runId": fmt.Sprintf("run-%d", len(f.requests))}, nil
	case "t3_thread_wait":
		if f.firstDone && args["runId"] == "run-1" {
			return map[string]any{"threadId": "thread", "resultRun": map[string]any{"runId": "run-1", "status": "completed"}}, nil
		}
		return map[string]any{"threadId": "thread", "timedOut": true}, nil
	case "t3_thread_read":
		page := pageFor(target)
		thread := page["thread"].(map[string]any)
		thread["activeRunId"] = nil
		thread["parentThreadId"] = nil
		thread["createdBy"] = "agent"
		thread["creationSource"] = "mcp"
		thread["title"] = "合成测试"
		thread["settled"] = false
		thread["status"] = "idle"
		var runs, items []any
		for i, request := range f.requests {
			id := fmt.Sprintf("run-%d", i+1)
			status := "running"
			if i == 0 && f.firstDone {
				status = "completed"
			} else {
				thread["activeRunId"] = id
				thread["status"] = "running"
			}
			thread["latestRunId"] = id
			runs = append(runs, map[string]any{"runId": id, "status": status, "model": target.Model, "providerInstanceId": target.ProviderID})
			items = append(items, map[string]any{"position": 3*i + 1, "runId": id, "createdBy": "user", "type": "user_message", "text": strings.TrimSpace(request)})
			if status == "completed" {
				items = append(items,
					map[string]any{"position": 3*i + 2, "runId": id, "itemId": "plan-" + id, "type": "proposed_plan", "status": "completed",
						"visibility": "local", "sourceThreadId": "thread", "text": "1. 核对配置 token=synthetic-plan-value", "updatedAt": "2026-11-19T01:00:00Z"},
					map[string]any{"position": 3*i + 3, "runId": id, "createdBy": "agent", "type": "assistant_message", "text": "合成结果"},
				)
			}
		}
		page["recentRuns"], page["items"] = runs, items
		return page, nil
	case "t3_thread_interrupt":
		if f.stopped != "" || args["threadId"] != "thread" || args["runId"] != "run-2" {
			return nil, errors.New("stop was retried or targeted the wrong run")
		}
		if _, hasStop := args["stop"]; hasStop {
			return nil, errors.New("unexpected stop field in interrupt")
		}
		f.stopped = "run-2"
		f.stopRequestID, _ = args["clientRequestId"].(string)
		return map[string]any{"threadId": "thread", "runId": "run-2", "status": "interrupt_requested"}, nil
	}
	return nil, fmt.Errorf("unexpected tool %s", name)
}
