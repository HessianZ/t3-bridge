package bridge

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

type t3OAuthCredential struct {
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"expiresAt,omitzero"`
	Resource    string    `json:"resource,omitempty"`
	Scope       string    `json:"scope,omitempty"`
}

// AuthorizeT3 performs an interactive OAuth authorization code grant. T3 owns the
// browser approval and the allowed runtime modes. The caller handles persistence.
func AuthorizeT3(ctx context.Context, config MCPConfig, showURL func(string) error) ([]byte, error) {
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	return authorizeT3(ctx, config, showURL, transport)
}

func authorizeT3(ctx context.Context, config MCPConfig, showURL func(string) error, transport http.RoundTripper) ([]byte, error) {
	if err := config.validateConnection(); err != nil {
		return nil, err
	}
	resource, err := config.connectionEndpoint()
	if err != nil {
		return nil, err
	}
	endpoint, err := url.Parse(resource)
	if err != nil {
		return nil, errors.New("invalid T3 MCP endpoint")
	}
	origin := endpoint.Scheme + "://" + endpoint.Host
	httpClient := privateHTTP(15 * time.Second)
	httpClient.Transport = transport
	var metadata struct {
		Issuer     string   `json:"issuer"`
		Authorize  string   `json:"authorization_endpoint"`
		Register   string   `json:"registration_endpoint"`
		Token      string   `json:"token_endpoint"`
		Challenges []string `json:"code_challenge_methods_supported"`
		IssInReply bool     `json:"authorization_response_iss_parameter_supported"`
	}
	if err := t3OAuthRequest(ctx, httpClient, origin+"/.well-known/oauth-authorization-server", "", nil, &metadata); err != nil {
		return nil, fmt.Errorf("discover T3 OAuth support: %w", err)
	}
	if !slices.Contains(metadata.Challenges, "S256") {
		return nil, errors.New("T3 OAuth requires S256 PKCE support")
	}
	if metadata.Issuer != "" && metadata.Issuer != origin {
		return nil, errors.New("T3 OAuth issuer does not match the configured origin")
	}
	for _, raw := range []string{metadata.Authorize, metadata.Register, metadata.Token} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != endpoint.Scheme || mcpAuthority(u) != endpoint.Host || u.User != nil ||
			u.Path == "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") {
			return nil, errors.New("T3 OAuth endpoints must use the configured origin")
		}
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("open OAuth callback: %w", err)
	}
	defer listener.Close()
	redirect := "http://" + listener.Addr().String() + "/callback"
	registration, err := json.Marshal(map[string]any{
		"client_name":                "T3 Bridge",
		"redirect_uris":              []string{redirect},
		"grant_types":                []string{"authorization_code"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	if err != nil {
		return nil, err
	}
	var client struct {
		ID string `json:"client_id"`
	}
	if err := t3OAuthRequest(ctx, httpClient, metadata.Register, "application/json", registration, &client); err != nil {
		return nil, fmt.Errorf("register T3 OAuth client: %w", err)
	}
	if client.ID == "" {
		return nil, errors.New("T3 OAuth registration did not return a client ID")
	}

	verifier, state := rand.Text()+rand.Text(), rand.Text()
	challenge := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {client.ID},
		"redirect_uri":          {redirect},
		"state":                 {state},
		"code_challenge_method": {"S256"},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"resource":              {resource},
	}
	code, err := t3OAuthCallback(ctx, listener, state, origin, metadata.IssInReply, metadata.Authorize+"?"+query.Encode(), showURL)
	if err != nil {
		return nil, err
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {client.ID},
		"redirect_uri":  {redirect},
		"code":          {code},
		"code_verifier": {verifier},
		"resource":      {resource},
	}
	var token struct {
		t3OAuthCredential
		ExpiresIn int64 `json:"expires_in,omitempty"`
	}
	if err := t3OAuthRequest(ctx, httpClient, metadata.Token, "application/x-www-form-urlencoded", []byte(form.Encode()), &token); err != nil {
		return nil, fmt.Errorf("exchange T3 OAuth code: %w", err)
	}
	if token.AccessToken == "" || strings.ContainsAny(token.AccessToken, " \t\r\n") ||
		token.ExpiresIn < 0 || token.ExpiresIn > int64((1<<63-1)/time.Second) {
		return nil, errors.New("T3 returned an invalid OAuth credential")
	}
	if token.ExpiresIn > 0 {
		token.ExpiresAt = time.Now().UTC().Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	token.Resource = ""
	if endpoint.Scheme == "https" {
		token.Resource = resource
	}
	return json.Marshal(token)
}

func t3OAuthRequest(ctx context.Context, client *http.Client, endpoint, contentType string, body []byte, output any) error {
	method := http.MethodGet
	if contentType != "" {
		method = http.MethodPost
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid OAuth endpoint")
	}
	request.Header.Set("Accept", "application/json")
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("T3 OAuth endpoint is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("T3 returned HTTP %d", response.StatusCode)
	}
	const limit = 64 << 10
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || len(raw) > limit || json.Unmarshal(raw, output) != nil {
		return errors.New("invalid T3 OAuth response")
	}
	return nil
}

func t3OAuthCallback(ctx context.Context, listener net.Listener, state, issuer string, requireIssuer bool, authURL string, showURL func(string) error) (string, error) {
	code := make(chan string, 1)
	var accepted sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		query := r.URL.Query()
		gotIssuer := query.Get("iss")
		if r.Host != listener.Addr().String() || query.Get("state") != state ||
			((requireIssuer || gotIssuer != "") && gotIssuer != issuer) ||
			(query.Get("code") == "" && query.Get("error") == "") {
			http.Error(w, "Invalid T3 authorization response.", http.StatusBadRequest)
			return
		}
		accepted.Do(func() {
			result := query.Get("code")
			if query.Get("error") != "" {
				result = ""
			}
			fmt.Fprintln(w, "Return to T3 Bridge to see the authorization result.")
			code <- result
		})
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, MaxHeaderBytes: 8192}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if server.Shutdown(shutdown) != nil {
			_ = server.Close()
		}
	}()
	failed := make(chan error, 1)
	go func() { failed <- server.Serve(listener) }()
	if err := showURL(authURL); err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-failed:
		return "", errors.New("T3 OAuth callback is unavailable")
	case result := <-code:
		if result == "" {
			return "", errors.New("T3 authorization was declined")
		}
		return result, nil
	}
}
