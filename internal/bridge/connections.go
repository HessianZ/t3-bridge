package bridge

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
)

type ConnectionExit struct {
	Generation uint64
	Err        error
}

// Owns only this Bridge's two clients. It never restarts T3 or edits execution state.
type Connections struct {
	config     Config
	store      *Store
	mcp        *MCPClient
	Lark       *Lark
	Exits      chan ConnectionExit
	generation atomic.Uint64
	ready      atomic.Bool
	verified   atomic.Bool
	newLark    func(Config, *Store) (*Lark, error)
	newMCP     func(context.Context, MCPConfig) (*MCPClient, error)
	stop       context.CancelFunc
	stopped    <-chan struct{}

	// OnFeishuReconnect observes SDK reconnections for the current generation.
	// Set it before the first call to Reconnect.
	OnFeishuReconnect func()
}

func NewConnections(c Config, s *Store) *Connections {
	return &Connections{
		config: c, store: s, Exits: make(chan ConnectionExit, 4),
		newLark: NewLark, newMCP: ConnectVerifiedMCP,
	}
}
func (c *Connections) Generation() uint64 { return c.generation.Load() }
func (c *Connections) Status() string {
	if !c.verified.Load() {
		return "MCP环境尚未核验"
	}
	if c.ready.Load() {
		return "MCP最近建连环境核验通过；飞书WS已就绪（非实时MCP探活）"
	}
	return "MCP最近建连环境核验通过；飞书WS连接中/已断开"
}
func ConnectVerifiedMCP(ctx context.Context, c MCPConfig) (*MCPClient, error) {
	return connectVerifiedMCP(ctx, c, http.DefaultTransport)
}

func connectVerifiedMCP(ctx context.Context, c MCPConfig, transport http.RoundTripper) (*MCPClient, error) {
	client, err := connectMCP(ctx, c, transport)
	if err != nil {
		return nil, err
	}
	var local struct {
		EnvironmentID string `json:"environmentId"`
	}
	if err := client.Call(ctx, "t3_environment_read", map[string]any{}, &local); err != nil || local.EnvironmentID != c.EnvironmentID {
		client.Session.Close()
		return nil, errors.New("receiver MCP identity does not match its dedicated environment")
	}
	return client, nil
}
func (c *Connections) Reconnect(ctx context.Context, e *Engine) (string, error) {
	mcp, err := c.newMCP(ctx, c.config.MCP)
	if err != nil {
		return "", err
	}
	candidate, err := c.newLark(c.config, c.store)
	if err != nil {
		mcp.Session.Close()
		return "", err
	}
	if c.Lark != nil {
		candidate.Config.access = c.Lark.Config.access
	}
	candidate.RefreshOwner(ctx)
	generation := c.trackReceiverLifecycle(candidate)
	// Wait for the old SDK receiver before starting another for the same App.
	if err := c.stopReceiver(ctx); err != nil {
		candidate.Channel.Stop(ctx)
		mcp.Session.Close()
		return "", errors.New("previous Feishu receiver did not stop")
	}
	if c.mcp != nil {
		c.mcp.Session.Close()
	}
	c.mcp, c.Lark = mcp, candidate
	c.verified.Store(true)
	e.Config = candidate.Config
	e.Lark = candidate
	mcpConfig := c.config.MCP
	mcpConfig.Endpoint, mcpConfig.RuntimeFile = mcp.endpoint, ""
	e.T3 = &T3{Calls: mcp, Config: mcpConfig}
	receiverCtx, stop := context.WithCancel(ctx)
	stopped := make(chan struct{})
	c.stop, c.stopped = stop, stopped
	go func() {
		err := candidate.Channel.Start(receiverCtx)
		close(stopped)
		select {
		case c.Exits <- ConnectionExit{generation, err}:
		case <-ctx.Done():
		}
	}()
	return "已重连 T3 MCP 并核对环境。飞书连接正在建立，可用 /doctor 查看。原任务及执行回执保留。", nil
}

func (c *Connections) trackReceiverLifecycle(candidate *Lark) uint64 {
	generation := c.generation.Add(1)
	c.ready.Store(false)
	candidate.Channel.OnReady(func() {
		if c.generation.Load() == generation {
			c.ready.Store(true)
		}
	})
	candidate.Channel.OnReconnected(func() {
		if c.generation.Load() != generation {
			return
		}
		c.ready.Store(true)
		if c.OnFeishuReconnect != nil {
			c.OnFeishuReconnect()
		}
	})
	candidate.Channel.OnDisconnected(func() {
		if c.generation.Load() == generation {
			c.ready.Store(false)
		}
	})
	return generation
}

func (c *Connections) Close(ctx context.Context) {
	c.generation.Add(1)
	c.ready.Store(false)
	c.verified.Store(false)
	_ = c.stopReceiver(ctx)
	if c.mcp != nil {
		c.mcp.Session.Close()
	}
}

func (c *Connections) stopReceiver(ctx context.Context) error {
	if c.stop == nil {
		return nil
	}
	c.stop()
	if err := c.Lark.Channel.Stop(ctx); err != nil {
		return err
	}
	select {
	case <-c.stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
