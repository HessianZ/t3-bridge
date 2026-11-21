package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/localfile"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestT3HTTPSAuthorizationMCPAndSameOriginUpload(t *testing.T) {
	var escaped atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		escaped.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer other.Close()

	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "test"}, nil)
	var uploadURL atomic.Value
	uploadURL.Store("/upload")
	mcp.AddTool(mcpServer, &mcp.Tool{Name: "t3_environment_read"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, map[string]string, error) {
		return nil, map[string]string{"environmentId": "tls-env"}, nil
	})
	mcp.AddTool(mcpServer, &mcp.Tool{Name: "t3_attachment_prepare_upload"}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, map[string]string, error) {
		return nil, map[string]string{"attachmentId": "pending", "relativeUrl": uploadURL.Load().(string)}, nil
	})
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, nil)
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := "https://" + r.Host
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": origin, "authorization_endpoint": origin + "/authorize",
				"registration_endpoint": origin + "/register", "token_endpoint": origin + "/token",
				"code_challenge_methods_supported": []string{"S256"}, "authorization_response_iss_parameter_supported": true,
			})
		case "/register":
			fmt.Fprint(w, `{"client_id":"fixture"}`)
		case "/token":
			if r.ParseForm() != nil || r.Form.Get("code") != "fixture-code" || r.Form.Get("resource") != origin+"/mcp" {
				http.Error(w, "wrong resource", http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `{"access_token":"fixture-token","expires_in":3600}`)
		case "/mcp", "/upload":
			if r.Header.Get("Authorization") != "Bearer fixture-token" {
				http.Error(w, "wrong token", http.StatusUnauthorized)
				return
			}
			calls.Add(1)
			if r.URL.Path == "/upload" {
				if r.URL.Query().Get("redirect") != "" {
					http.Redirect(w, r, other.URL+"/upload", http.StatusTemporaryRedirect)
					return
				}
				if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "image/png" {
					t.Error("wrong upload request")
				}
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			mcpHandler.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := MCPConfig{Endpoint: server.URL + "/mcp", TokenFile: filepath.Join(t.TempDir(), "oauth.json"), EnvironmentID: "tls-env"}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	transport := server.Client().Transport
	data, err := authorizeT3(ctx, cfg, func(address string) error {
		u, err := url.Parse(address)
		if err != nil || u.Scheme != "https" ||
			u.Query().Get("resource") != cfg.Endpoint || u.Query().Get("code_challenge_method") != "S256" {
			return fmt.Errorf("authorization URL did not use the HTTPS resource")
		}
		values := url.Values{"state": {u.Query().Get("state")}, "code": {"fixture-code"}, "iss": {server.URL}}
		resp, err := server.Client().Get(u.Query().Get("redirect_uri") + "?" + values.Encode())
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("callback failed: %d", resp.StatusCode)
		}
		return nil
	}, transport)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localfile.WritePrivateAtomic(cfg.TokenFile, data, true); err != nil {
		t.Fatal(err)
	}

	if client, err := ConnectMCP(ctx, cfg); err == nil || client != nil || calls.Load() != 0 {
		t.Fatal("untrusted TLS endpoint reached authenticated MCP")
	}
	changed := cfg
	changed.Endpoint = "https://elsewhere.invalid/mcp"
	if client, err := connectMCP(ctx, changed, transport); err == nil || client != nil || !strings.Contains(err.Error(), "bound") {
		t.Fatal("credential was accepted for another endpoint")
	}
	client, err := connectMCP(ctx, cfg, transport)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Session.Close()
	var identity struct {
		EnvironmentID string `json:"environmentId"`
	}
	if err := client.Call(ctx, "t3_environment_read", map[string]any{}, &identity); err != nil || identity.EnvironmentID != cfg.EnvironmentID {
		t.Fatalf("HTTPS environment read failed: %v", err)
	}
	rotated, err := json.Marshal(map[string]string{"access_token": "next-session-token", "resource": cfg.Endpoint})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localfile.WritePrivateAtomic(cfg.TokenFile, rotated, false); err != nil {
		t.Fatal(err)
	}

	var payload bytes.Buffer
	if err := png.Encode(&payload, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	executor := &T3{Config: cfg, Calls: client}
	send := func() error {
		_, err := executor.Upload(ctx, Target{EnvironmentID: cfg.EnvironmentID}, Image{Name: "fixture.png"}, payload.Bytes())
		return err
	}
	if err := send(); err != nil {
		t.Fatal(err)
	}
	uploadURL.Store(other.URL + "/upload")
	if err := send(); err == nil {
		t.Fatal("cross-origin upload accepted")
	}
	uploadURL.Store("/upload?redirect=1")
	if err := send(); err == nil || escaped.Load() != 0 {
		t.Fatal("upload followed a redirect")
	}
}

func TestRemoteMCPStillRequiresLocalHostForAttachmentCache(t *testing.T) {
	cfg := testConfig(t)
	cfg.MCP.Endpoint = "https://env.example.invalid/mcp"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	target := cfg.Routes["nas"]
	target.CacheFollowupAttachments = true
	cfg.Routes["nas"] = target
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "cacheFollowupAttachments") {
		t.Fatal("direct HTTPS connection bypassed the attachment cache host check")
	}
}
