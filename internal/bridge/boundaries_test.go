package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestHy4OfficialBoundaryAndNoFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hy4.secret")
	os.WriteFile(path, []byte("synthetic-key"), 0600)
	config := Hy4Config{BaseURL: "https://tokenhub.tencentmaas.com/v1", Model: "hy4-flash", KeyFile: path, Verified: true}
	requests := 0
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.String() != "https://tokenhub.tencentmaas.com/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer synthetic-key" {
			t.Error("not official direct request")
		}
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		if b["model"] != "hy4-flash" || b["stream"] != false || b["max_tokens"] != float64(512) || b["thinking"].(map[string]any)["type"] != "disabled" {
			t.Error("invalid model parameters")
		}
		if _, ok := b["reasoning_effort"]; ok {
			t.Fatal("CodeBuddy-only parameter sent to TokenHub")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"{\"routeId\":\"nas\",\"action\":\"new_task\"}"}}]}`)), Header: http.Header{}}, nil
	})
	h := Hy4{Config: config, Client: &http.Client{Transport: transport}}
	d, err := h.Classify(context.Background(), message("one", "检查我的服务器"), []string{"nas"}, "")
	if err != nil || d.RouteID != "nas" {
		t.Fatalf("%+v %v", d, err)
	}
	h.Config.Verified = false
	if _, err = h.Classify(context.Background(), message("one", "检查服务器"), []string{"nas"}, ""); err == nil || requests != 1 {
		t.Fatal("unverified account called")
	}
	h.Config = config
	h.Config.Model = "example/free"
	if _, err = h.Classify(context.Background(), message("one", "服务器"), []string{"nas"}, ""); err == nil || requests != 1 {
		t.Fatal("alias fallback")
	}
	h.Config = config
	h.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded })
	if _, err = h.Classify(context.Background(), message("one", "服务器"), []string{"nas"}, ""); err == nil || strings.Contains(err.Error(), "synthetic-key") {
		t.Fatal("timeout leaked or retried")
	}
}
func TestConfigSecretsAndMCPEndpoint(t *testing.T) {
	c := testConfig(t)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"http://example.com/mcp", "https://example.com/mcp?token=x", "https://user@example.com/mcp", "http://127.0.0.1/not-mcp"} {
		if _, err := parseMCPEndpoint(endpoint); err == nil {
			t.Fatal("accepted " + endpoint)
		}
	}
	endpoint, err := parseMCPEndpoint("https://Example.COM:443/mcp")
	if err != nil || endpoint.String() != "https://example.com/mcp" {
		t.Fatalf("invalid HTTPS endpoint: %v, %v", endpoint, err)
	}
	secret := filepath.Join(t.TempDir(), "test.secret")
	os.WriteFile(secret, []byte("synthetic"), 0644)
	if _, err := Secret(secret); err == nil {
		t.Fatal("public secret accepted")
	}
	os.Chmod(secret, 0600)
	if value, err := Secret(secret); err != nil || value != "synthetic" {
		t.Fatal(err)
	}
	bad := c.Routes["nas"]
	bad.Driver = "codex"
	c.Routes["nas"] = bad
	if c.Validate() != nil {
		t.Fatal("explicit generic driver rejected")
	}
	old := testConfig(t).Routes["nas"]
	if taskTargetAllowed(c, &Task{RouteID: "nas", Target: old}) {
		t.Fatal("driver change silently adopted by old target")
	}
}
func TestUploadOnlySameOriginAndImageType(t *testing.T) {
	c := testConfig(t)
	target := c.Routes["nas"]
	token := filepath.Join(t.TempDir(), "mcp.secret")
	os.WriteFile(token, []byte("test-token"), 0600)
	var data bytes.Buffer
	png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	for _, escaped := range []bool{false, true} {
		t.Run(map[bool]string{true: "escape", false: "loopback"}[escaped], func(t *testing.T) {
			posts := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				posts++
				b, _ := io.ReadAll(r.Body)
				if !bytes.Equal(b, data.Bytes()) || r.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("bad upload")
				}
				w.WriteHeader(204)
			}))
			defer s.Close()
			c.MCP.Endpoint = s.URL + "/mcp"
			c.MCP.TokenFile = token
			api := &T3{Config: c.MCP, Calls: scriptedCaller(func(name string, a map[string]any) (any, error) {
				if name != "t3_attachment_prepare_upload" {
					t.Fatal(name)
				}
				upload := a["upload"].(map[string]any)
				if len(upload) != 4 || upload["id"] != nil {
					t.Fatal("prepare input contains invalid attachment id")
				}
				u := "/upload/synthetic"
				if escaped {
					u = "https://example.com/upload"
				}
				return map[string]any{"attachmentId": "uploaded", "relativeUrl": u}, nil
			})}
			a, err := api.Upload(context.Background(), target, Image{Name: "test.png"}, data.Bytes())
			if escaped {
				if err == nil || posts != 0 {
					t.Fatal("escaped local upload")
				}
			} else if err != nil || posts != 1 || a.ID != "uploaded" {
				t.Fatalf("upload=%+v err=%v", a, err)
			}
		})
	}
}
func TestHealthHTTPStatusAndRecoveryFloorRestart(t *testing.T) {
	m, now, _ := loadManifest(t)
	key := filepath.Join(t.TempDir(), "health.secret")
	os.WriteFile(key, []byte(m.Key), 0600)
	source := m.Sources[0]
	for _, status := range []int{200, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-HomeOps-Signature", source.Signature)
				w.WriteHeader(status)
				w.Write(fixture(t, source.File))
			}))
			defer s.Close()
			events, floor, err := FetchHealth(context.Background(), HealthSource{URL: s.URL + "/healthz", Node: source.Node, LocalIP: "127.0.0.1", KeyFile: key}, now, time.Time{})
			if status == 503 {
				if err == nil {
					t.Fatal("503 accepted")
				}
				return
			}
			if err != nil || len(events) != 1 {
				t.Fatal(err)
			}
			e, _, _, _ := testEngine(t)
			e.Store.update(func(st *State) error { st.RecoveryFloors[source.Node] = floor.Format(time.RFC3339Nano); return nil })
			e.Store.Close()
			reopened, err := OpenStore(e.Config.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if reopened.snapshot().RecoveryFloors[source.Node] != floor.Format(time.RFC3339Nano) {
				t.Fatal("floor reset on restart")
			}
		})
	}
}
func TestNotificationUnknownBeyondUUIDWindowDoesNotRecreate(t *testing.T) {
	e, _, l, _ := testEngine(t)
	l.fail = true
	accept(t, e, message("one", "NAS检查磁盘"))
	tick(t, e)
	task := onlyTask(t, e)
	attempts := l.notifications
	e.Store.update(func(st *State) error {
		st.Tasks[task.ID].CardAttemptAt = time.Now().Add(-56 * time.Minute).UTC().Format(time.RFC3339Nano)
		return nil
	})
	tick(t, e)
	task = onlyTask(t, e)
	if l.notifications != attempts || task.NotificationIssue == "" {
		t.Fatal("recreated card outside server dedup window")
	}
}
func TestHTTPRedirectRefused(t *testing.T) {
	called := false
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer dest.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, dest.URL, 302) }))
	defer src.Close()
	_, err := privateHTTP(time.Second).Get(src.URL)
	if err == nil || called {
		t.Fatal("private request followed redirect")
	}
}

func TestDedicatedChannelPolicyPermitsScopedRepliesWithoutMention(t *testing.T) {
	c := testConfig(t)
	c.AppSecretFile = filepath.Join(t.TempDir(), "app.secret")
	os.WriteFile(c.AppSecretFile, []byte("synthetic"), 0600)
	s, err := OpenStore(c.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	l, err := NewLark(c, s)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Channel.Stop(context.Background())
	p := l.Channel.GetPolicy()
	if p.RequireMention == nil || *p.RequireMention || p.DMMode != "open" || len(p.GroupAllowlist) != 0 || p.RespondToMentionAll == nil || *p.RespondToMentionAll {
		t.Fatal("SDK blocks owner/new-group/scoped replies before application ACL")
	}
	m := message("denied", "hello")
	m.ChatType, m.UserID = "p2p", "stranger"
	if l.Config.Allowed(m) {
		t.Fatal("open SDK bypassed DM application ACL")
	}
	m.ChatType, m.ChatID = "group", "unknown-group"
	if l.Config.Allowed(m) {
		t.Fatal("open SDK bypassed group application ACL")
	}
}
