package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
)

func TestConfigAuthorizeT3(t *testing.T) {
	var mu sync.Mutex
	var registered, challenge string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := "http://" + r.Host
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                                         origin,
				"authorization_endpoint":                         origin + "/authorize",
				"registration_endpoint":                          origin + "/register",
				"token_endpoint":                                 origin + "/token",
				"code_challenge_methods_supported":               []string{"S256"},
				"authorization_response_iss_parameter_supported": true,
			})
		case "/register":
			var registration struct {
				Redirect []string `json:"redirect_uris"`
				Auth     string   `json:"token_endpoint_auth_method"`
			}
			if err := json.NewDecoder(r.Body).Decode(&registration); err != nil || len(registration.Redirect) != 1 ||
				registration.Auth != "none" || !strings.HasPrefix(registration.Redirect[0], "http://127.0.0.1:") {
				http.Error(w, "invalid registration", http.StatusBadRequest)
				return
			}
			mu.Lock()
			registered = registration.Redirect[0]
			mu.Unlock()
			fmt.Fprint(w, `{"client_id":"fixture"}`)
		case "/token":
			_ = r.ParseForm()
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			mu.Lock()
			defer mu.Unlock()
			if r.Form.Get("code") != "fixture-code" || r.Form.Get("redirect_uri") != registered ||
				r.Form.Get("client_id") != "fixture" || r.Form.Get("resource") != origin+"/mcp" ||
				base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
				http.Error(w, "invalid token request", http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `{"access_token":"fixture-token","expires_in":3600,"scope":"orchestration:read"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "credentials", "t3.json")
	config := bridge.MCPConfig{Endpoint: server.URL + "/mcp", TokenFile: path}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	showURL := func(address string) error {
		authorization, err := url.Parse(address)
		if err != nil {
			return err
		}
		query := authorization.Query()
		mu.Lock()
		challenge = query.Get("code_challenge")
		mu.Unlock()
		redirect := query.Get("redirect_uri")
		if authorization.Path != "/authorize" || query.Get("code_challenge_method") != "S256" {
			return fmt.Errorf("incomplete authorization request")
		}
		for _, result := range []struct {
			state, issuer string
			want          int
		}{
			{"wrong-state", server.URL, http.StatusBadRequest},
			{query.Get("state"), "http://127.0.0.1:1", http.StatusBadRequest},
			{query.Get("state"), server.URL, http.StatusOK},
		} {
			callback := redirect + "?" + url.Values{"state": {result.state}, "iss": {result.issuer}, "code": {"fixture-code"}}.Encode()
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, callback, nil)
			if err != nil {
				return err
			}
			response, err := server.Client().Do(request)
			if err != nil {
				return err
			}
			response.Body.Close()
			if response.StatusCode != result.want {
				return fmt.Errorf("callback status %d, want %d", response.StatusCode, result.want)
			}
		}
		return nil
	}
	if err := authorizeConfiguredT3(ctx, config, false, showURL); err != nil {
		t.Fatal(err)
	}
	raw, err := bridge.Secret(path)
	if err != nil {
		t.Fatal(err)
	}
	var credential struct {
		AccessToken string    `json:"access_token"`
		ExpiresAt   time.Time `json:"expiresAt"`
		Scope       string    `json:"scope"`
	}
	if json.Unmarshal([]byte(raw), &credential) != nil || credential.AccessToken != "fixture-token" ||
		credential.ExpiresAt.IsZero() || credential.Scope != "orchestration:read" {
		t.Fatal("expected the private OAuth credential to contain the token, expiry, and granted scope")
	}
	if err := authorizeConfiguredT3(ctx, config, false, showURL); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("authorization should require explicit replacement, got %v", err)
	}
	if err := authorizeConfiguredT3(ctx, config, true, func(address string) error {
		// Simulate a different writer while the user is completing approval.
		if err := os.WriteFile(path, []byte("{\"access_token\":\"other-token\"}"), 0600); err != nil {
			return err
		}
		return showURL(address)
	}); err == nil || !strings.Contains(err.Error(), "changed during authorization") {
		t.Fatalf("authorization overwrote a changed credential, got %v", err)
	}

	t.Run("terminal authorization", func(t *testing.T) {
		source := tuiTestSource(t)
		connection := config
		connection.EnvironmentID = "fixture-environment"
		connection.TokenFile = filepath.Join(t.TempDir(), "credentials", "tui-token.json")
		edits, err := json.Marshal([]configTUIEdit{{Path: []string{"mcp"}, Value: connection}})
		if err != nil {
			t.Fatal(err)
		}
		source.JSON, err = bridge.EditConfigJSON(source.JSON, edits)
		if err != nil {
			t.Fatal(err)
		}
		m := newConfigTUI(source)
		tuiKeys(m, tea.KeyF6)
		if view := m.View(); !strings.Contains(view, "Sign-in code") || !strings.Contains(view, "Sharing & access") {
			t.Fatal("TUI authorization review should explain where to get the T3 code")
		}
		_, waitLink := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if waitLink == nil {
			t.Fatalf("TUI authorization did not start: %s", m.status)
		}
		defer m.authorization.cancel()
		message := waitLink().(configTUIAuthorizationResult)
		_, waitSave := m.Update(message)
		view := m.View()
		if waitSave == nil || message.address == "" || !strings.Contains(view, "打开 T3 授权页") || !strings.Contains(view, "5 分钟") {
			t.Fatalf("TUI authorization did not show the link: %v", message.err)
		}
		if err := showURL(message.address); err != nil {
			t.Fatal(err)
		}
		m.Update(waitSave())
		if m.screen != tuiBrowse || m.status != "凭据已保存，可查询项目和模型" || m.draft != string(source.JSON) {
			t.Fatalf("unexpected TUI result: %s", m.status)
		}
		if _, err := bridge.Secret(connection.TokenFile); err != nil {
			t.Fatal(err)
		}
	})

	const host = "127.0.0.1:33221"
	const origin = "http://" + host
	for _, terminal := range []string{"saved", "canceled", "expired"} {
		t.Run("workspace "+terminal, func(t *testing.T) {
			connection := config
			connection.TokenFile = filepath.Join(t.TempDir(), "credentials", "workspace-token.json")
			payload, err := json.Marshal(map[string]any{"connection": connection})
			if err != nil {
				t.Fatal(err)
			}
			workspaceCtx, endWorkspace := context.WithCancel(ctx)
			defer endWorkspace()
			if terminal == "expired" {
				expired, cancel := context.WithDeadline(workspaceCtx, time.Unix(0, 0))
				defer cancel()
				workspaceCtx = expired
			}
			workspace := configWebHandler(host, nil, &qrRegistration{ctx: workspaceCtx})
			request := func(method, route, sender, body string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(method, origin+route, strings.NewReader(body))
				req.Header.Set("Origin", sender)
				res := httptest.NewRecorder()
				workspace.ServeHTTP(res, req)
				return res
			}
			const route = "/t3/authorize/start"
			if res := request(http.MethodPost, route, "http://127.0.0.1:33222", string(payload)); res.Code != http.StatusForbidden {
				t.Fatalf("cross-origin authorization request returned %d", res.Code)
			}
			res := request(http.MethodPost, route, origin, string(payload))
			var started map[string]string
			if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &started) != nil || started["id"] == "" {
				t.Fatalf("workspace authorization failed to start, status %d", res.Code)
			}
			query := "?id=" + started["id"]
			acted := false
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					t.Fatalf("workspace authorization did not report %s", terminal)
				case <-tick.C:
				}
				res := request(http.MethodGet, "/t3/authorize/status"+query, "", "")
				var view t3AuthorizationView
				if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &view) != nil {
					t.Fatalf("workspace authorization status HTTP %d", res.Code)
				}
				switch view.State {
				case "starting":
					continue
				case "pending":
					if view.URL == "" || terminal == "expired" {
						t.Fatal("unexpected pending authorization")
					}
					if !acted {
						if terminal == "saved" {
							if err := showURL(view.URL); err != nil {
								t.Fatal(err)
							}
						} else if res := request(http.MethodPost, "/t3/authorize/cancel"+query, origin, ""); res.Code != http.StatusNoContent {
							t.Fatalf("workspace cancel returned %d", res.Code)
						}
						acted = true
					}
				default:
					if view.State != terminal || view.URL != "" || (terminal != "expired" && !acted) {
						t.Fatalf("unexpected workspace authorization state %q: %s", view.State, view.Problem)
					}
					if terminal == "saved" {
						if _, err := bridge.Secret(connection.TokenFile); err != nil {
							t.Fatal(err)
						}
					} else if _, err := os.Stat(connection.TokenFile); !os.IsNotExist(err) {
						t.Fatal("unfinished authorization must not create a credential")
					}
					return
				}
			}
		})
	}
}
