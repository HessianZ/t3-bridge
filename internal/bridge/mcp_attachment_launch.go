package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

func (t *T3) attachmentLaunchThreadID(requestID string) string {
	client, ok := t.Calls.(*MCPClient)
	if !ok || client.auth == nil || requestID == "" {
		return ""
	}
	payload, signature, ok := strings.Cut(client.auth.token, ".")
	if !ok || signature == "" {
		return ""
	}
	if _, err := base64.RawURLEncoding.DecodeString(signature); err != nil {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return ""
	}
	var claims struct {
		SessionID string `json:"sid"`
		Kind      string `json:"kind"`
		Subject   string `json:"sub"`
		Method    string `json:"method"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.SessionID == "" || claims.Kind != "session" ||
		claims.Subject != "mcp-client" || claims.Method != "bearer-access-token" {
		return ""
	}
	// v0.1.10 derives both thread and first message IDs from the direct MCP client namespace.
	// The server authenticates the bearer. This calculation supplies only a lookup hint.
	pair, _ := json.Marshal([2]string{"client:" + claims.SessionID, requestID})
	// Match JSON.stringify, including characters encoding/json escapes for use in HTML.
	wire := strings.NewReplacer(
		"\\\\", "\\\\", "\\u003c", "<", "\\u003e", ">", "\\u0026", "&", "\\u2028", "\u2028", "\\u2029", "\u2029",
	).Replace(string(pair))
	return fmt.Sprintf("mcp:launch:%x", sha256.Sum256([]byte(wire)))
}

const attachmentLaunchTool = "t3_thread_launch"

func (t *T3) launchWithAttachments(ctx context.Context, target Target, task *Task) (Execution, error) {
	if target.EnvironmentID == "" || target.EnvironmentID != t.Config.EnvironmentID {
		return Execution{}, errors.New("remote attachments unsupported by public API")
	}
	// Engine preflights the exact target before preparing uploads and fences this call.
	type option struct {
		ID    string `json:"id"`
		Value any    `json:"value"`
	}
	options := make([]option, 0, len(target.Options))
	for _, id := range slices.Sorted(maps.Keys(target.Options)) {
		options = append(options, option{ID: id, Value: target.Options[id]})
	}
	args := map[string]any{
		"environmentId": target.EnvironmentID, "projectId": target.ProjectID, "title": task.ThreadMeta.Title,
		"modelSelection": map[string]any{"provider": target.Driver, "instanceId": target.ProviderID, "model": target.Model, "options": options},
		"runtimeMode":    target.RuntimeMode, "interactionMode": target.InteractionMode,
		"workspaceStrategy": map[string]string{"type": "root"}, "clientRequestId": task.RequestID,
		"message": task.RequestMessage, "attachments": task.Attachments,
	}
	var reply struct {
		ThreadID       string            `json:"threadId"`
		RunID          string            `json:"runId"`
		Link           string            `json:"link"`
		ProjectID      string            `json:"projectId"`
		ModelSelection mcpModelSelection `json:"modelSelection"`
	}
	if err := t.Calls.Call(ctx, attachmentLaunchTool, args, &reply); err != nil {
		return Execution{}, err
	}
	if strings.TrimSpace(reply.ThreadID) == "" || strings.TrimSpace(reply.RunID) == "" ||
		reply.ProjectID != target.ProjectID || !reply.ModelSelection.matches(target) {
		invalidateAttachmentLaunchHint(task)
		return Execution{}, errors.New("invalid T3 attachment launch receipt")
	}
	if err := t.Verify(ctx, target, reply.ThreadID); err != nil {
		if errors.Is(err, errT3ThreadIdentity) || errors.Is(err, errT3ThreadConfiguration) ||
			(task.AttachmentLaunch != nil && task.AttachmentLaunch.ExpectedThreadID != reply.ThreadID) {
			invalidateAttachmentLaunchHint(task)
		}
		return Execution{}, err
	}
	return Execution{ThreadID: reply.ThreadID, RunID: reply.RunID, Link: reply.Link}, nil
}

// Keep the pending receipt so the engine can persist it for manual reconciliation.
func invalidateAttachmentLaunchHint(task *Task) {
	if task.AttachmentLaunch != nil {
		task.AttachmentLaunch = &attachmentLaunchReceipt{}
	}
}

var errAttachmentLaunchUnconfirmed = errors.New("T3 attachment launch has no matching read-only evidence")

func (t *T3) recoverAttachmentLaunch(ctx context.Context, target Target, task *Task) (Execution, error) {
	if target.EnvironmentID == "" || target.EnvironmentID != t.Config.EnvironmentID ||
		task.AttachmentLaunch == nil || task.AttachmentLaunch.ExpectedThreadID == "" || task.RequestMessage == "" {
		return Execution{}, errAttachmentLaunchUnconfirmed
	}
	id := task.AttachmentLaunch.ExpectedThreadID
	position := -1
	for range 100 {
		var p threadPage
		args := map[string]any{
			"environmentId": target.EnvironmentID, "threadId": id, "afterPosition": position,
			"view": "messages", "limit": 100, "runLimit": 50, "maxCharsPerItem": 20000,
		}
		if err := t.Calls.Call(ctx, "t3_thread_read", args, &p); err != nil {
			return Execution{}, err
		}
		if !verifyAttachmentLaunchPage(target, id, p) {
			return Execution{}, errAttachmentLaunchUnconfirmed
		}
		for _, item := range p.Items {
			if item.Position <= position {
				return Execution{}, errAttachmentLaunchUnconfirmed
			}
			position = item.Position
			if item.Type != "user_message" {
				continue
			}
			if !isAttachmentLaunchMessage(id, item) || !slices.ContainsFunc(p.RecentRuns, func(run threadRun) bool {
				return run.RunID == item.RunID && run.Ordinal == 1 && run.ProviderID == target.ProviderID &&
					run.Model == target.Model && (run.ExecutedModel == nil || *run.ExecutedModel == target.Model)
			}) {
				return Execution{}, errAttachmentLaunchUnconfirmed
			}
			if err := t.verifyAttachmentLaunchMessage(ctx, target, id, task.RequestMessage, item); err != nil {
				return Execution{}, err
			}
			if err := t.verifyConfiguration(ctx, target, id); err != nil {
				return Execution{}, err
			}
			return Execution{ThreadID: id, RunID: item.RunID, Link: p.Thread.Link}, nil
		}
		if !p.HasMore || p.NextPosition == nil || *p.NextPosition != position || len(p.Items) == 0 {
			return Execution{}, errAttachmentLaunchUnconfirmed
		}
	}
	return Execution{}, errAttachmentLaunchUnconfirmed
}

func verifyAttachmentLaunchPage(target Target, id string, p threadPage) bool {
	return verifyPage(target, id, p) == nil && p.Thread.CreatedBy == "agent" &&
		p.Thread.CreationSource == "mcp" && string(p.Thread.ParentThreadID) == "null" &&
		string(p.Thread.RelationshipToParent) == "null" &&
		(len(p.Thread.DelegatedFrom) == 0 || string(p.Thread.DelegatedFrom) == "null")
}

func isAttachmentLaunchMessage(id string, item threadItem) bool {
	return item.Type == "user_message" && item.MessageID == id && item.ItemID != "" && strings.TrimSpace(item.RunID) != "" &&
		item.Visibility == "local" && item.SourceThreadID == id && item.CreatedBy == "agent" && item.CreationSource == "mcp"
}

func (t *T3) verifyAttachmentLaunchMessage(ctx context.Context, target Target, id, request string, item threadItem) error {
	text, trimmed := item.Text, strings.TrimSpace(request)
	offset := 0
	for reads := 0; item.TextTruncated; reads++ {
		if reads >= 12 || item.NextTextOffset == nil || *item.NextTextOffset <= offset ||
			(!strings.HasPrefix(request, text) && !strings.HasPrefix(trimmed, text)) {
			return errAttachmentLaunchUnconfirmed
		}
		offset = *item.NextTextOffset
		var tail threadPage
		args := map[string]any{
			"environmentId": target.EnvironmentID, "threadId": id, "itemId": item.ItemID,
			"view": "messages", "textOffset": offset, "maxCharsPerItem": 20000,
		}
		if err := t.Calls.Call(ctx, "t3_thread_read", args, &tail); err != nil {
			return err
		}
		if !verifyAttachmentLaunchPage(target, id, tail) || len(tail.Items) != 1 {
			return errAttachmentLaunchUnconfirmed
		}
		part := tail.Items[0]
		if !isAttachmentLaunchMessage(id, part) || part.ItemID != item.ItemID ||
			part.RunID != item.RunID || part.Position != item.Position || part.Text == "" {
			return errAttachmentLaunchUnconfirmed
		}
		text += part.Text
		item = part
	}
	if text == request || text == trimmed {
		return nil
	}
	return errAttachmentLaunchUnconfirmed
}
