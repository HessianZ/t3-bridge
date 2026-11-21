package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ToolCaller interface {
	Call(context.Context, string, any, any) error
}
type MCPClient struct {
	Session       *mcp.ClientSession
	endpoint      string
	auth          *bearerTransport
	operateAccess string
}
type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(r)
}
func ConnectMCP(ctx context.Context, c MCPConfig) (*MCPClient, error) {
	return connectMCP(ctx, c, http.DefaultTransport)
}

func connectMCP(ctx context.Context, c MCPConfig, transport http.RoundTripper) (*MCPClient, error) {
	endpoint, err := c.connectionEndpoint()
	if err != nil {
		return nil, err
	}
	credential, err := c.loadMCPCredential(endpoint)
	if err != nil {
		return nil, err
	}
	client := privateHTTP(35 * time.Second)
	auth := &bearerTransport{credential.AccessToken, transport}
	client.Transport = auth
	session, err := mcp.NewClient(&mcp.Implementation{Name: "t3-bridge", Version: "0.1.0"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: client, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		return nil, errors.New("cannot connect to the configured T3 MCP endpoint")
	}
	return &MCPClient{Session: session, endpoint: endpoint, auth: auth, operateAccess: t3OperateAccess(credential.Scope)}, nil
}

func parseMCPEndpoint(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" ||
		u.ForceQuery || strings.Contains(raw, "#") || u.EscapedPath() != "/mcp" ||
		(u.Scheme != "https" && (u.Scheme != "http" || !isLoopbackMCPHost(u.Hostname()))) {
		return nil, errors.New("MCP endpoint must use HTTPS or loopback HTTP /mcp without credentials, query or fragment")
	}
	u.Host = mcpAuthority(u)
	return u, nil
}

func mcpAuthority(u *url.URL) string {
	host := strings.ToLower(u.Host)
	if port := u.Port(); u.Scheme == "https" && port == "443" || u.Scheme == "http" && port == "80" {
		return strings.TrimSuffix(host, ":"+port)
	}
	return host
}

func isLoopbackMCPHost(host string) bool {
	return slices.Contains([]string{"localhost", "127.0.0.1", "::1"}, strings.ToLower(host))
}

// LocalEnvironment reports whether the selected environment uses a local MCP connection.
func (c MCPConfig) LocalEnvironment(environmentID string) bool {
	if environmentID != c.EnvironmentID {
		return false
	}
	if c.RuntimeFile != "" {
		return true
	}
	endpoint, err := parseMCPEndpoint(c.Endpoint)
	return err == nil && isLoopbackMCPHost(endpoint.Hostname())
}

func (c MCPConfig) connectionEndpoint() (string, error) {
	if c.RuntimeFile == "" {
		endpoint, err := parseMCPEndpoint(c.Endpoint)
		if err != nil {
			return "", err
		}
		return endpoint.String(), nil
	}
	file, err := os.Open(c.RuntimeFile)
	if err != nil {
		return "", errors.New("cannot read T3 runtime file")
	}
	defer file.Close()
	port, err := readMCPRuntimePort(file)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("http://127.0.0.1:%d/mcp", port), nil
}

func readMCPRuntimePort(reader io.Reader) (int, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, MaxConfigBytes+1))
	var runtime struct {
		Port int `json:"port"`
	}
	if err != nil || len(raw) > MaxConfigBytes || json.Unmarshal(raw, &runtime) != nil || runtime.Port < 1 || runtime.Port > 65535 {
		return 0, errors.New("invalid T3 runtime port")
	}
	return runtime.Port, nil
}

func (c MCPConfig) bearer(endpoint string) (string, error) {
	credential, err := c.loadMCPCredential(endpoint)
	return credential.AccessToken, err
}

func (c MCPConfig) loadMCPCredential(endpoint string) (t3OAuthCredential, error) {
	value, err := Secret(c.TokenFile)
	if err != nil || !strings.EqualFold(filepath.Ext(c.TokenFile), ".json") {
		return t3OAuthCredential{AccessToken: value}, err
	}
	var credential t3OAuthCredential
	if json.Unmarshal([]byte(value), &credential) != nil || strings.TrimSpace(credential.AccessToken) == "" {
		return t3OAuthCredential{}, errors.New("invalid T3 OAuth file")
	}
	if !credential.ExpiresAt.IsZero() && !time.Now().Before(credential.ExpiresAt) {
		return t3OAuthCredential{}, errors.New("T3 OAuth credential expired")
	}
	// A separate registration credential can carry full access. Bind it to
	// the saved HTTPS endpoint before connecting and sending it to T3.
	if c.projectSetup && strings.HasPrefix(endpoint, "https://") && credential.Resource == "" {
		return t3OAuthCredential{}, errors.New("T3 project setup credential requires an HTTPS resource binding")
	}
	if credential.Resource != "" && credential.Resource != endpoint {
		return t3OAuthCredential{}, errors.New("T3 OAuth credential is bound to another MCP endpoint")
	}
	return credential, nil
}

func t3OperateAccess(scope string) string {
	scopes := strings.Fields(scope)
	if slices.Contains(scopes, "orchestration:operate") {
		return "granted"
	}
	if len(scopes) > 0 {
		return "missing"
	}
	return "unknown"
}

func mcpOperateAccess(caller ToolCaller) string {
	if client, ok := caller.(*MCPClient); ok {
		return client.operateAccess
	}
	return "unknown"
}
func (c *MCPClient) Call(ctx context.Context, name string, args, out any) error {
	r, err := c.Session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil || r == nil || r.IsError || r.NeedsInput() {
		return fmt.Errorf("T3 %s failed or requires authorization", name)
	}
	var b []byte
	if r.StructuredContent != nil {
		b, err = json.Marshal(r.StructuredContent)
	} else {
		for _, v := range r.Content {
			if t, ok := v.(*mcp.TextContent); ok {
				b = []byte(t.Text)
				break
			}
		}
	}
	if err != nil || len(b) == 0 || len(b) > 4<<20 {
		return errors.New("T3 response outside bounds")
	}
	if out == nil {
		return nil
	}
	if json.Unmarshal(b, out) != nil {
		return errors.New("invalid T3 response")
	}
	return nil
}

type T3 struct {
	Calls  ToolCaller
	Config MCPConfig
	HTTP   *http.Client
}

var ErrRemoteUnavailable = errors.New("remote environment is not formally linked and reachable")
var ErrRemoteAccessInsufficient = errors.New("目标 T3 关联权限不足")
var ErrMCPOperationNotGranted = errors.New("本端 T3 OAuth 未授予派发权限，请重新授权并至少选择监督权限")

var t3AccessModes = []string{"read-only", "approval-required", "auto-accept-edits", "auto", "full-access"}

func (t *T3) linkedEnvironmentAccess(ctx context.Context, environmentID string) (string, error) {
	var linked struct {
		Links []struct {
			EnvironmentID string `json:"environmentId"`
			Status        string `json:"status"`
			Access        string `json:"access"`
		} `json:"links"`
	}
	if err := t.Calls.Call(ctx, "t3_environment_links", map[string]any{}, &linked); err != nil {
		return "", ErrRemoteUnavailable
	}
	for _, link := range linked.Links {
		if link.EnvironmentID == environmentID && link.Status == "reachable" {
			if !slices.Contains(t3AccessModes, link.Access) {
				return "unknown", nil
			}
			return link.Access, nil
		}
	}
	return "", ErrRemoteUnavailable
}

func (t *T3) capabilities(ctx context.Context, environmentID, search string) (T3Capabilities, error) {
	var cap T3Capabilities
	args := map[string]any{"environmentId": environmentID}
	if search != "" {
		args["search"] = search
	}
	err := t.Calls.Call(ctx, "orchestrator_capabilities", args, &cap)
	return cap, err
}

func (t *T3) Preflight(ctx context.Context, target Target) error {
	if mcpOperateAccess(t.Calls) == "missing" {
		return ErrMCPOperationNotGranted
	}
	return t.preflightTarget(ctx, target)
}

func (t *T3) preflightTarget(ctx context.Context, target Target) error {
	requested := slices.Index(t3AccessModes, target.RuntimeMode)
	if t.Config.EnvironmentID != "" && t.Config.EnvironmentID != target.EnvironmentID {
		access, err := t.linkedEnvironmentAccess(ctx, target.EnvironmentID)
		if err != nil {
			return err
		}
		if ceiling := slices.Index(t3AccessModes, access); ceiling <= 0 || ceiling < requested {
			return fmt.Errorf("%w（关联 %s，路由 %s）。请在 T3 核对关联权限或调整路由运行模式", ErrRemoteAccessInsufficient, access, target.RuntimeMode)
		}
	}
	var env struct {
		EnvironmentID string `json:"environmentId"`
	}
	if err := t.Calls.Call(ctx, "t3_environment_read", map[string]any{"environmentId": target.EnvironmentID}, &env); err != nil {
		return err
	}
	if env.EnvironmentID != target.EnvironmentID {
		return errors.New("environment mismatch")
	}
	var project struct {
		ID         string  `json:"id"`
		DeletedAt  *string `json:"deletedAt"`
		ArchivedAt *string `json:"archivedAt"`
	}
	if err := t.Calls.Call(ctx, "t3_project_read", map[string]any{"environmentId": target.EnvironmentID, "projectId": target.ProjectID}, &project); err != nil {
		return err
	}
	if project.ID != target.ProjectID || project.DeletedAt != nil || project.ArchivedAt != nil {
		return errors.New("project unavailable")
	}
	cap, err := t.capabilities(ctx, target.EnvironmentID, target.Model)
	if err != nil {
		return err
	}
	if slices.Index(t3AccessModes, cap.RuntimeMode) < requested {
		return errors.New("OAuth runtime ceiling too narrow")
	}
	for _, p := range cap.Providers {
		if p.ID == target.ProviderID && p.Driver == target.Driver {
			// T3 omits runtimeModes for providers that offer every mode.
			if p.RuntimeModes != nil && !slices.Contains(p.RuntimeModes, target.RuntimeMode) {
				return errors.New("provider permission unavailable")
			}
			for _, m := range p.Models {
				if m.ID == target.Model {
					return m.ValidateOptions(target.Options)
				}
			}
		}
	}
	return errors.New("explicit provider/driver/model unavailable")
}
func (t *T3) Open(ctx context.Context, target Target, id, title string) (Execution, error) {
	var r struct {
		ThreadID  string  `json:"threadId"`
		RunID     *string `json:"runId"`
		Link      string  `json:"link"`
		ProjectID string  `json:"projectId"`
	}
	err := t.Calls.Call(ctx, "t3_thread_launch", map[string]any{"environmentId": target.EnvironmentID, "projectId": target.ProjectID, "title": title, "modelSelection": map[string]any{"provider": target.Driver, "instanceId": target.ProviderID, "model": target.Model, "options": target.Options}, "runtimeMode": target.RuntimeMode, "interactionMode": target.InteractionMode, "workspaceStrategy": map[string]string{"type": "root"}, "clientRequestId": id}, &r)
	if err != nil {
		return Execution{}, err
	}
	if r.ThreadID == "" || r.ProjectID != target.ProjectID || r.RunID != nil {
		return Execution{}, errors.New("unexpected non-idle launch")
	}
	return Execution{ThreadID: r.ThreadID, Link: r.Link}, nil
}

type threadIdentity struct {
	ID              string `json:"threadId"`
	ProjectID       string `json:"projectId"`
	ProviderID      string `json:"providerInstanceId"`
	Model           string `json:"model"`
	RuntimeMode     string `json:"runtimeMode"`
	InteractionMode string `json:"interactionMode"`
}

type threadRun struct {
	RunID         string  `json:"runId"`
	Ordinal       int     `json:"ordinal"`
	Status        string  `json:"status"`
	Model         string  `json:"model"`
	ProviderID    string  `json:"providerInstanceId"`
	ExecutedModel *string `json:"executedModel"`
}

type threadItem struct {
	Position       int    `json:"position"`
	ItemID         string `json:"itemId"`
	SubagentID     string `json:"subagentId"`
	MessageID      string `json:"messageId"`
	RunID          string `json:"runId"`
	CreatedBy      string `json:"createdBy"`
	CreationSource string `json:"creationSource"`
	Type           string `json:"type"`
	Status         string `json:"status"`
	UpdatedAt      string `json:"updatedAt"`
	Channel        string `json:"channel"`
	SourceThreadID string `json:"sourceThreadId"`
	Visibility     string `json:"visibility"`
	Title          string `json:"title"`
	Text           string `json:"text"`
	TextTruncated  bool   `json:"textTruncated"`
	NextTextOffset *int   `json:"nextTextOffset"`
}

type threadPage struct {
	Thread struct {
		threadIdentity
		Link                 string          `json:"link"`
		CreatedBy            string          `json:"createdBy"`
		CreationSource       string          `json:"creationSource"`
		ParentThreadID       json.RawMessage `json:"parentThreadId"`
		RelationshipToParent json.RawMessage `json:"relationshipToParent"`
		DelegatedFrom        json.RawMessage `json:"delegatedFrom"`
		ActiveRunID          string          `json:"activeRunId"`
		PendingRequestCount  int             `json:"pendingRequestCount"`
		Pending              []struct {
			TaskID        string `json:"taskId"`
			ChildThreadID string `json:"childThreadId"`
		} `json:"pendingBackgroundTasks"`
	} `json:"thread"`
	Items        []threadItem `json:"items"`
	RecentRuns   []threadRun  `json:"recentRuns"`
	HasMore      bool         `json:"hasMore"`
	NextPosition *int         `json:"nextPosition"`
}

var (
	errT3ThreadIdentity      = errors.New("actual T3 identity drift")
	errT3ThreadConfiguration = errors.New("actual T3 configuration differs from explicit selection")
)

func verifyPage(target Target, id string, p threadPage) error {
	return verifyThreadIdentity(target, id, p.Thread.threadIdentity)
}
func verifyThreadIdentity(target Target, id string, actual threadIdentity) error {
	if actual.ID != id || actual.ProjectID != target.ProjectID || actual.ProviderID != target.ProviderID || actual.Model != target.Model || actual.RuntimeMode != target.RuntimeMode || actual.InteractionMode != target.InteractionMode {
		return errT3ThreadIdentity
	}
	return nil
}
func (t *T3) Verify(ctx context.Context, target Target, id string) error {
	var p threadPage
	if err := t.Calls.Call(ctx, "t3_thread_read", map[string]any{"environmentId": target.EnvironmentID, "threadId": id, "limit": 1}, &p); err != nil {
		return err
	}
	if err := verifyPage(target, id, p); err != nil {
		return err
	}
	return t.verifyConfiguration(ctx, target, id)
}

type mcpModelSelection struct {
	Provider   *string         `json:"provider"`
	InstanceID string          `json:"instanceId"`
	Model      string          `json:"model"`
	Options    json.RawMessage `json:"options"`
}

func (selection mcpModelSelection) matches(target Target) bool {
	options, err := readModelOptions(selection.Options)
	if err != nil {
		return false
	}
	// Preflight verifies instance -> driver; the server may omit the provider field.
	a, _ := json.Marshal(options)
	b, _ := json.Marshal(target.Options)
	return (selection.Provider == nil || *selection.Provider == target.Driver) && selection.InstanceID == target.ProviderID &&
		selection.Model == target.Model && string(a) == string(b)
}

type threadConfiguration struct {
	ThreadID        string            `json:"threadId"`
	ModelSelection  mcpModelSelection `json:"modelSelection"`
	RuntimeMode     string            `json:"runtimeMode"`
	InteractionMode string            `json:"interactionMode"`
}

func (t *T3) readThreadConfiguration(ctx context.Context, environmentID, id string) (threadConfiguration, error) {
	// T3 treats configure as a read when modelSelection is omitted.
	var actual threadConfiguration
	err := t.Calls.Call(ctx, "t3_thread_configure", map[string]any{"environmentId": environmentID, "threadId": id}, &actual)
	return actual, err
}

func (t *T3) verifyConfiguration(ctx context.Context, target Target, id string) error {
	actual, err := t.readThreadConfiguration(ctx, target.EnvironmentID, id)
	if err != nil {
		return err
	}
	if actual.ThreadID != id || !actual.ModelSelection.matches(target) || actual.RuntimeMode != target.RuntimeMode || actual.InteractionMode != target.InteractionMode {
		return errT3ThreadConfiguration
	}
	return nil
}

// Official configure responses use [{id,value}]; retain compatibility with object options.
func readModelOptions(raw json.RawMessage) (map[string]any, error) {
	options := map[string]any{}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return options, nil
	}
	invalid := errors.New("invalid T3 model options")
	if raw[0] == '{' {
		if json.Unmarshal(raw, &options) != nil {
			return nil, invalid
		}
		return options, nil
	}
	var pairs []struct {
		ID    string          `json:"id"`
		Value json.RawMessage `json:"value"`
	}
	if decodeStrict(raw, &pairs) != nil {
		return nil, invalid
	}
	for _, pair := range pairs {
		if _, exists := options[pair.ID]; exists || pair.ID == "" || len(pair.Value) == 0 {
			return nil, invalid
		}
		var value any
		if json.Unmarshal(pair.Value, &value) != nil {
			return nil, invalid
		}
		options[pair.ID] = value
	}
	return options, nil
}

func (t *T3) Send(ctx context.Context, target Target, task *Task) (Execution, error) {
	if len(task.Attachments) > 0 {
		// The public attachment-send tool can steer an unrelated active run.
		return Execution{}, errors.New("T3 native attachments require a new thread launch")
	}
	var r struct {
		ThreadID string `json:"threadId"`
		RunID    string `json:"runId"`
		Link     string `json:"link"`
	}
	args := map[string]any{"environmentId": target.EnvironmentID, "threadId": task.ThreadID, "message": task.RequestMessage, "mode": "queue", "clientRequestId": task.RequestID}
	if err := t.Calls.Call(ctx, "t3_thread_send", args, &r); err != nil {
		return Execution{}, err
	}
	if r.ThreadID != task.ThreadID || r.RunID == "" {
		return Execution{}, errors.New("invalid T3 send identity")
	}
	return Execution{r.ThreadID, r.RunID, r.Link}, nil
}
func (t *T3) Observe(ctx context.Context, target Target, task *Task) (Observation, error) {
	// Wait is bounded and includes delegated work; the read below determines identity and final content.
	var wait struct {
		ThreadID  string `json:"threadId"`
		RunID     string `json:"runId"`
		Status    string `json:"status"`
		TimedOut  *bool  `json:"timedOut"`
		ResultRun *struct {
			RunID  string `json:"runId"`
			Status string `json:"status"`
		} `json:"resultRun"`
	}
	if task.RunID != "" {
		if err := t.Calls.Call(ctx, "t3_thread_wait", map[string]any{"environmentId": target.EnvironmentID, "threadId": task.ThreadID, "runId": task.RunID, "timeoutMs": 1000, "untilDelegatedWorkSettled": true}, &wait); err != nil {
			return Observation{}, err
		}
	}
	o := Observation{RunID: task.RunID, ChildThreads: append([]string{}, task.ChildThreads...)}
	resultRunID := task.RunID
	originalWaitSettled := false
	if wait.ResultRun != nil {
		if wait.ThreadID != task.ThreadID || wait.ResultRun.RunID == "" {
			return o, errors.New("invalid settled result identity")
		}
		resultRunID = wait.ResultRun.RunID
		o.Status = wait.ResultRun.Status
		o.Unsettled = wait.TimedOut != nil && *wait.TimedOut
		originalWaitSettled = wait.RunID == task.RunID && nativeTerminal(wait.Status) && wait.TimedOut != nil && !*wait.TimedOut
		if resultRunID != task.RunID {
			// The aggregate result can be a separately submitted turn on the same thread.
			resultRunID, o.Status, o.Unsettled = task.RunID, wait.Status, true
			if originalWaitSettled {
				attributed, err := t.attributedResultRun(ctx, target, task)
				if err != nil {
					return o, err
				}
				if attributed != "" {
					resultRunID, o.Unsettled = attributed, false
				}
			}
		}
	} else {
		o.Unsettled = true
	}
	position := -1
	summaryPosition := -1
	progressPosition, requestPosition := -1, -1
	processPreview := task.Process != nil && processHasPreview(task.Process.Mode) && !task.Process.Closed
	stream := task.RunID != "" && (wait.ResultRun == nil || wait.ResultRun.RunID == task.RunID) && (task.CardPreviewEnabled || processPreview)
	liveWait := stream && wait.ThreadID == task.ThreadID && wait.RunID == task.RunID && wait.Status == "running" && wait.TimedOut != nil && *wait.TimedOut
	previewPosition, resultRunVerified := -1, false
	livePreviewPosition, livePreview := -1, ""
	originalRunVerified := false
	var proposedPlan *threadItem
	// T3 trims outer whitespace on input; preserve the original durable request/fence.
	wireRequest := strings.TrimSpace(task.RequestMessage)
	for page := 0; page < 100; page++ {
		var p threadPage
		if err := t.Calls.Call(ctx, "t3_thread_read", map[string]any{"environmentId": target.EnvironmentID, "threadId": task.ThreadID, "afterPosition": position, "limit": 100, "runLimit": 50, "maxCharsPerItem": 20000, "view": "messages"}, &p); err != nil {
			return o, err
		}
		if err := verifyPage(target, task.ThreadID, p); err != nil {
			return o, err
		}
		o.PendingApprovals = p.Thread.PendingRequestCount
		o.Tasks = nil

		for _, bg := range p.Thread.Pending {
			o.Tasks = append(o.Tasks, bg.TaskID)
			if bg.ChildThreadID != "" && !slices.Contains(o.ChildThreads, bg.ChildThreadID) {
				o.ChildThreads = append(o.ChildThreads, bg.ChildThreadID)
			}
		}
		for i := range p.Items {
			item := &p.Items[i]
			isPlan := task.RunID != "" && task.Source.InputKind != doctorEchoKind && completeProposedPlan(*item, task.ThreadID, resultRunID)
			if isPlan && (proposedPlan == nil || item.Position > proposedPlan.Position) {
				proposedPlan = item
			}
			if stream && !item.TextTruncated && item.NextTextOffset == nil {
				text := strings.TrimSpace(item.Text)
				localAnswer := item.Type == "assistant_message" && item.RunID == task.RunID && item.CreatedBy == "agent" &&
					item.CreationSource == "provider" && item.Visibility == "local" && item.SourceThreadID == task.ThreadID && item.Channel == ""
				if item.Position > previewPosition && text != "" && (isPlan || localAnswer && item.Status == "completed") {
					if isPlan {
						text = proposedPlanTitle + "\n" + text
					}
					previewPosition = item.Position
					o.StreamPreview = display(text, processPreviewLimit)
				} else if liveWait && p.Thread.ActiveRunID == task.RunID && localAnswer && item.Status == "running" &&
					item.ItemID != "" && item.MessageID != "" && item.Position > livePreviewPosition {
					livePreviewPosition = item.Position
					livePreview = display(text, processPreviewLimit)
				}
			}
			// An open assistant item is only a preview, including during a terminal read race.
			if item.Type == "assistant_message" && item.Status == "running" {
				continue
			}
			wanted := resultRunID
			if wanted == "" {
				wanted = o.RunID
			}
			visible := item.CreatedBy == "agent" && visibleReplyType(item.Type) && item.RunID == wanted
			if resultRunID != task.RunID && task.RunID != "" {
				visible = visible && completedWakeReply(*item, task.ThreadID)
			}
			// MCP sends are local user_message inputs authored by agent, not assistant replies.
			requestInput := ((item.CreatedBy == "user" && (item.Type == "" || item.Type == "message" || item.Type == "user_message")) ||
				(item.CreatedBy == "agent" && item.Type == "user_message" && item.Visibility == "local" && item.SourceThreadID == task.ThreadID)) &&
				(item.Visibility == "" || item.Visibility == "local") && (item.SourceThreadID == "" || item.SourceThreadID == task.ThreadID) &&
				visibleReplyType(item.Channel) && item.RunID != "" && (task.RunID == "" || item.RunID == task.RunID)
			expand := visible || requestInput
			for reads := 0; expand && len(item.Text) <= maxResultBytes && item.TextTruncated && item.NextTextOffset != nil && reads < 4; reads++ {
				var tail threadPage
				if err := t.Calls.Call(ctx, "t3_thread_read", map[string]any{"environmentId": target.EnvironmentID, "threadId": task.ThreadID, "itemId": item.ItemID, "textOffset": *item.NextTextOffset, "maxCharsPerItem": 20000, "view": "messages"}, &tail); err != nil {
					return o, err
				}
				if len(tail.Items) != 1 || tail.Items[0].ItemID != item.ItemID {
					return o, errors.New("invalid T3 text continuation")
				}
				part := tail.Items[0]
				item.Text += part.Text
				item.TextTruncated = part.TextTruncated
				item.NextTextOffset = part.NextTextOffset
			}
			if expand && (item.TextTruncated || len(item.Text) > maxResultBytes) {
				if visible {
					item.TextTruncated = true
					item.Text = display(item.Text, 1600)
				} else {
					return o, errors.New("T3 request exceeds reconciliation bounds")
				}
			}
			if requestInput && (item.Text == task.RequestMessage || item.Text == wireRequest) {
				o.FoundRequest = true
				o.RunID = item.RunID
				requestPosition = item.Position
				if task.RunID != "" && o.requestPosition == nil {
					pos := item.Position
					o.requestPosition = &pos
				}
			}
		}
		for _, item := range p.Items {
			if item.Type == "assistant_message" && item.Status == "running" {
				continue
			}
			mainRun := task.RunID
			if mainRun == "" {
				mainRun = o.RunID
			}
			owned := mainRun != "" && (item.RunID == mainRun || (item.RunID == "" && requestPosition >= 0 && item.Position > requestPosition && p.Thread.ActiveRunID == mainRun))
			if owned && item.CreatedBy == "agent" && visibleReplyType(item.Type) && visibleReplyType(item.Channel) && (item.Visibility == "" || item.Visibility == "local") && (item.SourceThreadID == "" || item.SourceThreadID == task.ThreadID) && item.Position > progressPosition {
				if text := progressLine(item.Text); text != "" {
					progressPosition = item.Position
					o.Progress = text
				}
			}
			if resultRunID == "" {
				resultRunID = o.RunID
			}
			if item.RunID == resultRunID && item.CreatedBy == "agent" && visibleReplyType(item.Type) && visibleReplyType(item.Channel) &&
				(item.Visibility == "" || item.Visibility == "local") && (item.SourceThreadID == "" || item.SourceThreadID == task.ThreadID) &&
				item.Position > summaryPosition && (task.RunID == "" || resultRunID == task.RunID || completedWakeReply(item, task.ThreadID)) {
				summaryPosition = item.Position
				o.Summary = item.Text
				o.ResultIssue = ""
				if item.TextTruncated {
					o.ResultIssue = "最终正文超过读取/128KiB限额，全文未交付"
				}
			}
		}
		for _, r := range p.RecentRuns {
			if originalWaitSettled && r.RunID == task.RunID {
				originalRunVerified = nativeTerminal(r.Status) && r.Model == target.Model && r.ProviderID == target.ProviderID &&
					(r.ExecutedModel == nil || *r.ExecutedModel == target.Model)
			}
			if r.RunID == resultRunID {
				if r.Model != target.Model || r.ProviderID != target.ProviderID || (r.ExecutedModel != nil && *r.ExecutedModel != target.Model) {
					return o, errors.New("executed provider/model drift")
				}
				o.Status = r.Status
				resultRunVerified = true
			}
		}
		if !p.HasMore {
			if !resultRunVerified {
				o.StreamPreview = ""
				o.Unsettled = true
			} else if livePreviewPosition > previewPosition && o.Status == "running" && p.Thread.ActiveRunID == task.RunID {
				o.StreamPreview = livePreview
			}
			if task.RunID == "" && o.FoundRequest {
				o.Unsettled = true
			}
			if task.RunID != "" && resultRunID != task.RunID && (!o.FoundRequest || !originalRunVerified || !resultRunVerified) {
				o.Summary, o.ResultIssue, o.Unsettled = "", "", true
			}
			o.OriginalRunSettled = originalRunVerified && resultRunVerified && !o.Unsettled
			childFailed := false
			for _, childID := range o.ChildThreads {
				var child threadPage
				if err := t.Calls.Call(ctx, "t3_thread_read", map[string]any{"environmentId": target.EnvironmentID, "threadId": childID, "limit": 1, "runLimit": 1}, &child); err != nil {
					return o, err
				}
				if child.Thread.ID != childID || len(child.RecentRuns) == 0 {
					return o, errors.New("child identity or result unavailable")
				}
				status := child.RecentRuns[0].Status
				if len(child.Thread.Pending) > 0 || child.Thread.PendingRequestCount > 0 || child.Thread.ActiveRunID != "" || !nativeTerminal(status) {
					o.Tasks = append(o.Tasks, "child:"+childID)
				} else if status != "completed" {
					childFailed = true
				}
			}
			if proposedPlan != nil && resultRunVerified && o.Status == "completed" && !o.Unsettled &&
				o.PendingApprovals == 0 && len(o.Tasks) == 0 {
				t.includeProposedPlan(ctx, target, task.ThreadID, *proposedPlan, &o)
			}
			if !o.Unsettled && resultRunID != task.RunID && nativeTerminal(o.Status) && o.PendingApprovals == 0 && len(o.Tasks) == 0 {
				o.ResultRunID = resultRunID
			}
			if childFailed && !o.Unsettled {
				o.Status = "failed"
				o.Summary = strings.TrimSpace("异步子任务未成功结束，请核对会话结论。\n" + o.Summary)
			}
			return o, nil
		}
		if p.NextPosition == nil || *p.NextPosition <= position {
			return o, errors.New("invalid T3 pagination")
		}
		position = *p.NextPosition
	}
	return o, errors.New("T3 history exceeds bounded reconciliation scan")
}

func completedWakeReply(item threadItem, threadID string) bool {
	return item.Type == "assistant_message" && item.Status == "completed" && item.CreationSource == "provider" &&
		item.Visibility == "local" && item.SourceThreadID == threadID && item.Channel == ""
}

const proposedPlanTitle = "T3 计划草案"

func (t *T3) includeProposedPlan(ctx context.Context, target Target, threadID string, item threadItem, observation *Observation) {
	plan, ok := t.readProposedPlan(ctx, target, threadID, item)
	if !ok {
		plan = "正文未完整读取，请在 T3 会话中查看。"
	}
	answer := strings.TrimSpace(observation.Summary)
	if answer == strings.TrimSpace(plan) {
		answer = ""
	}
	if answer != "" {
		answer += "\n\n"
	}
	heading := answer + proposedPlanTitle + "\n\n"
	if len(heading)+len(plan) > maxResultBytes {
		plan = "正文超过全文限额，请在 T3 会话中查看。"
	}
	if len(heading)+len(plan) <= maxResultBytes {
		observation.Summary = heading + plan
	}
}

func (t *T3) Stop(ctx context.Context, target Target, task *Task) error {
	stop, err := t.prepareStop(ctx, target, task, "", nil)
	if err != nil {
		return err
	}
	return stop()
}

func (t *T3) prepareIdleStop(ctx context.Context, task *Task, marker string, requestPosition *int) (func() error, error) {
	return t.prepareStop(ctx, task.Target, task, marker, requestPosition)
}

var errStopBackgroundPending = errors.New("T3 background work still requires reconciliation")

func (t *T3) prepareStop(ctx context.Context, target Target, task *Task, idleMarker string, requestPosition *int) (func() error, error) {
	if task.RunID == "" {
		return nil, errors.New("owned run identity unknown")
	}
	if err := t.Verify(ctx, target, task.ThreadID); err != nil {
		return nil, err
	}
	var p threadPage
	if err := t.Calls.Call(ctx, "t3_thread_read", map[string]any{"environmentId": target.EnvironmentID, "threadId": task.ThreadID, "limit": 1, "runLimit": 50}, &p); err != nil {
		return nil, err
	}
	found := false
	for _, r := range p.RecentRuns {
		if r.RunID == task.RunID {
			found = true
			if idleMarker != "" && r.Status != "running" {
				return nil, errors.New("idle stop requires the original run to be running")
			}
			if r.Status == "queued" {
				return func() error {
					return t.Calls.Call(ctx, "t3_queue", map[string]any{"environmentId": target.EnvironmentID, "threadId": task.ThreadID, "action": "cancel", "queuedRunId": task.RunID}, nil)
				}, nil
			}
		}
	}
	if !found {
		return nil, errors.New("owned run not found")
	}
	if idleMarker != "" {
		probe, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		sample, err := t.readRunActivity(probe, task, true, requestPosition)
		if err != nil || sample.IdleMarker != idleMarker {
			return nil, errors.New("idle stop activity changed or became unavailable")
		}
	}
	// T3 ignores runId on the thread-wide stop branch. Plain interrupt remains
	// bound to this run even if the active run changes after these reads.
	return func() error {
		reason := "用户从原飞书任务停止"
		if idleMarker != "" {
			reason = "原飞书任务运行空闲超时"
		}
		var receipt struct {
			ThreadID string `json:"threadId"`
			RunID    string `json:"runId"`
			Status   string `json:"status"`
		}
		args := map[string]any{"environmentId": target.EnvironmentID, "threadId": task.ThreadID, "runId": task.RunID, "reason": reason, "clientRequestId": stable(task.ID, task.RequestID, "stop")}
		if err := t.Calls.Call(ctx, "t3_thread_interrupt", args, &receipt); err != nil {
			return err
		}
		if receipt.ThreadID != task.ThreadID || receipt.RunID != task.RunID || (receipt.Status != "interrupt_requested" && !nativeTerminal(receipt.Status)) {
			return errors.New("T3 interrupt acknowledgement does not confirm the owned run")
		}
		if len(p.Thread.Pending) != 0 || len(task.TaskIDs) != 0 {
			return errStopBackgroundPending
		}
		return nil
	}, nil
}
func (t *T3) Upload(ctx context.Context, target Target, im Image, data []byte) (Attachment, error) {
	if target.EnvironmentID != t.Config.EnvironmentID {
		return Attachment{}, errors.New("remote attachments unsupported by public API")
	}
	a, err := attachmentMetadata(im, data, false)
	if err != nil {
		return Attachment{}, err
	}
	base, err := parseMCPEndpoint(t.Config.Endpoint)
	if err != nil {
		return a, err
	}
	connected, usesMCP := t.Calls.(*MCPClient)
	if usesMCP && (connected.auth == nil || connected.endpoint != base.String()) {
		return a, errors.New("attachment upload endpoint does not match the active MCP session")
	}
	var r struct {
		ID          string `json:"attachmentId"`
		RelativeURL string `json:"relativeUrl"`
		UploadURL   string `json:"uploadUrl"`
	}
	if err := t.Calls.Call(ctx, "t3_attachment_prepare_upload", map[string]any{"upload": map[string]any{"type": a.Type, "name": a.Name, "mimeType": a.MIME, "sizeBytes": a.Size}}, &r); err != nil {
		return a, err
	}
	a.ID = r.ID // Preserve the pending identity even when the subsequent HTTP upload fails.
	raw := r.UploadURL
	if raw == "" {
		raw = r.RelativeURL
	}
	u, err := url.Parse(raw)
	if err != nil {
		return a, errors.New("invalid upload URL")
	}
	u = base.ResolveReference(u)
	if u.Scheme != base.Scheme || mcpAuthority(u) != base.Host || u.User != nil {
		return a, errors.New("upload URL escaped the configured T3 origin")
	}
	req, err := http.NewRequestWithContext(ctx, "POST", u.String(), bytes.NewReader(data))
	if err != nil {
		return a, errors.New("invalid upload request")
	}
	req.Header.Set("Content-Type", a.MIME)
	client := privateHTTP(20 * time.Second)
	if t.HTTP != nil {
		custom := *t.HTTP
		custom.CheckRedirect = client.CheckRedirect
		client = &custom
	}
	if usesMCP {
		// Prepare, upload and launch use the same credentials even if the file is rotated.
		client.Transport = connected.auth
	} else {
		token, err := t.Config.bearer(base.String())
		if err != nil {
			return a, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return a, errors.New("upload failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || r.ID == "" {
		return a, errors.New("upload rejected")
	}
	return a, nil
}

// The public messages view excludes activity; additionally ignore explicit non-answer item types.
func visibleReplyType(kind string) bool {
	kind = strings.ToLower(kind)
	if kind == "user_message" || kind == "proposed_plan" {
		return false
	}
	for _, hidden := range []string{"reason", "thinking", "analysis", "tool", "command"} {
		if strings.Contains(kind, hidden) {
			return false
		}
	}
	return true
}

func progressLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if sensitive.MatchString(line) || strings.ContainsAny(line, "`/\\={}[]") || strings.Contains(line, "--") {
			return ""
		}
		return display(line, 120)
	}
	return ""
}

// Only the caller that has not attempted Send may discard these pending uploads.
func (t *T3) Discard(ctx context.Context, target Target, id string) error {
	if target.EnvironmentID != t.Config.EnvironmentID {
		return errors.New("remote attachment discard unsupported")
	}
	return t.Calls.Call(ctx, "t3_attachment_discard", map[string]any{"attachmentId": id}, nil)
}
