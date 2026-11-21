package bridge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	channel "github.com/larksuite/channel-sdk-go"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type lifecycleChannel struct {
	channel.Channel
	onReady, onDisconnected, onReconnected func()
	started, done                          chan struct{}
	once                                   sync.Once
	stops                                  int
}

func (c *lifecycleChannel) OnReady(f func())        { c.onReady = f }
func (c *lifecycleChannel) OnDisconnected(f func()) { c.onDisconnected = f }
func (c *lifecycleChannel) OnReconnected(f func())  { c.onReconnected = f }
func (c *lifecycleChannel) Start(ctx context.Context) error {
	c.onReady()
	close(c.started)
	select {
	case <-ctx.Done():
	case <-c.done:
	}
	return nil
}
func (c *lifecycleChannel) Stop(context.Context) error {
	c.once.Do(func() { c.stops++; c.Channel.Stop(context.Background()); close(c.done) })
	return nil
}
func TestP2aConnectionsReplaceOnlyOwnClientsAndVerifyBeforeSwap(t *testing.T) {
	e, _, capture, x, fixture := setupAccess(t)
	receiveAccess(t, capture, groupMessage("one", "user", "chat", "/nas task", true))
	tick(t, e)
	original := e.Store.snapshot()
	var environment atomic.Value
	environment.Store(e.Config.MCP.EnvironmentID)
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-reconnect", Version: "test"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "t3_environment_read"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, map[string]string, error) {
		return nil, map[string]string{"environmentId": environment.Load().(string)}, nil
	})
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	defer httpServer.Close()
	token := filepath.Join(t.TempDir(), "mcp.secret")
	os.WriteFile(token, []byte("synthetic-only"), 0600)
	cfg := e.Config
	cfg.MCP.Endpoint = httpServer.URL + "/mcp"
	cfg.MCP.TokenFile = token
	connections := NewConnections(cfg, e.Store)
	defer connections.Close(context.Background())
	channels := []*lifecycleChannel{}
	connections.newLark = func(c Config, s *Store) (*Lark, error) {
		sdk, err := channel.New("fake-app", "synthetic-secret", channel.WithOpenBaseURL(fixture.baseURL), channel.WithEnableTokenCache(false))
		if err != nil {
			return nil, err
		}
		ch := &lifecycleChannel{Channel: sdk, started: make(chan struct{}), done: make(chan struct{})}
		channels = append(channels, ch)
		return newLark(ch, c, s), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < 2; i++ {
		if _, err := connections.Reconnect(ctx, e); err != nil {
			t.Fatal(err)
		}
		select {
		case <-channels[i].started:
		case <-time.After(time.Second):
			t.Fatal("replacement did not start")
		}
		if !strings.Contains(connections.Status(), "WS已就绪") {
			t.Fatal("readiness not tracked")
		}
	}
	if channels[0].stops != 1 || channels[1].stops != 0 || connections.Generation() != 2 {
		t.Fatal("SDK instance reused or two old clients retained")
	}
	select {
	case exit := <-connections.Exits:
		if exit.Generation == connections.Generation() {
			t.Fatal("old receiver exit treated as current")
		}
	case <-time.After(time.Second):
		t.Fatal("old receiver not closed")
	}
	channels[0].onDisconnected()
	if !strings.Contains(connections.Status(), "WS已就绪") {
		t.Fatal("old receiver callback changed new readiness")
	}
	channels[1].onDisconnected()
	if strings.Contains(connections.Status(), "WS已就绪") {
		t.Fatal("disconnection hidden")
	}
	channels[1].onReconnected()
	before := connections.mcp
	environment.Store("wrong-environment")
	if _, err := connections.Reconnect(ctx, e); err == nil {
		t.Fatal("wrong environment accepted")
	}
	if connections.mcp != before || connections.Generation() != 2 || len(channels) != 2 || channels[1].stops != 0 {
		t.Fatal("failed candidate displaced valid clients")
	}
	if !reflect.DeepEqual(original, e.Store.snapshot()) || x.sends != 1 || x.stops != 0 {
		t.Fatal("reconnect modified/replayed original execution state")
	}
}
func TestP2aRemotePreflightRequiresFormalReachableLink(t *testing.T) {
	for _, tt := range []struct {
		name        string
		status      string
		access      string
		runtimeMode string
		wantErr     error
	}{
		{name: "none", status: "none", wantErr: ErrRemoteUnavailable},
		{name: "expired", status: "expired", wantErr: ErrRemoteUnavailable},
		{name: "error", status: "error", wantErr: ErrRemoteUnavailable},
		{name: "unreachable", status: "unreachable", access: "full-access", wantErr: ErrRemoteUnavailable},
		{name: "read-only", status: "reachable", access: "read-only", wantErr: ErrRemoteAccessInsufficient},
		{name: "insufficient supervised access", status: "reachable", access: "approval-required", runtimeMode: "auto-accept-edits", wantErr: ErrRemoteAccessInsufficient},
		{name: "exact supervised access", status: "reachable", access: "approval-required"},
		{name: "auto-accept-edits ceiling", status: "reachable", access: "auto-accept-edits"},
		{name: "auto ceiling", status: "reachable", access: "auto", runtimeMode: "auto-accept-edits"},
		{name: "full-access ceiling", status: "reachable", access: "full-access", runtimeMode: "auto"},
		{name: "missing access", status: "reachable", wantErr: ErrRemoteAccessInsufficient},
		{name: "unknown access", status: "reachable", access: "unknown", wantErr: ErrRemoteAccessInsufficient},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			target := cfg.Routes["mac"]
			if tt.runtimeMode != "" {
				target.RuntimeMode = tt.runtimeMode
			}
			var environmentReads, projectReads, capabilityReads int
			api := &T3{Config: cfg.MCP, Calls: scriptedCaller(func(name string, _ map[string]any) (any, error) {
				switch name {
				case "t3_environment_links":
					if tt.status == "error" {
						return nil, errors.New("unavailable")
					}
					links := []map[string]string{}
					if tt.status != "none" {
						link := map[string]string{"environmentId": target.EnvironmentID, "status": tt.status}
						if tt.access != "" {
							link["access"] = tt.access
						}
						links = append(links, link)
					}
					return map[string]any{"links": links}, nil
				case "t3_environment_read":
					environmentReads++
					return map[string]string{"environmentId": target.EnvironmentID}, nil
				case "t3_project_read":
					projectReads++
					return map[string]string{"id": target.ProjectID}, nil
				case "orchestrator_capabilities":
					capabilityReads++
					return map[string]any{"runtimeMode": "full-access", "providers": []any{map[string]any{"providerInstanceId": target.ProviderID, "driverKind": target.Driver, "runtimeModes": []string{target.RuntimeMode}, "models": []any{map[string]any{"id": target.Model, "options": []any{map[string]any{"id": "effort", "values": []string{"high"}}}}}}}}, nil
				}
				t.Fatal(name)
				return nil, nil
			})}
			err := api.Preflight(context.Background(), target)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Preflight error = %v, want %v", err, tt.wantErr)
			}
			wantReads := 0
			if tt.wantErr == nil {
				wantReads = 1
			}
			if environmentReads != wantReads || projectReads != wantReads || capabilityReads != wantReads {
				t.Errorf("forwarded reads: environment=%d project=%d capabilities=%d, want %d each", environmentReads, projectReads, capabilityReads, wantReads)
			}
		})
	}
}
