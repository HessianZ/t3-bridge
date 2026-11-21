package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
)

// Inspect uses public identity/configuration reads, not an unrestricted thread-list scan.
func (t *T3) InspectThread(ctx context.Context, target Target, task *Task) (NativeThread, error) {
	if err := t.preflightTarget(ctx, target); err != nil {
		return NativeThread{}, err
	}
	return t.inspectThread(ctx, target, task, true)
}

func (t *T3) verifyLegacySession(ctx context.Context, target Target, threadID string) error {
	project, err := t.Project(ctx, target, target.ProjectID)
	if err != nil || !validProject(project) || project.ID != target.ProjectID || !sameWorkspacePath(project.Root, target.ProjectPath) {
		return errors.New("cannot verify the legacy thread project path")
	}
	native, err := t.inspectThread(ctx, target, &Task{ThreadID: threadID}, false)
	if err != nil || !native.Quiet {
		return errors.New("cannot confirm a quiet legacy thread with the configured identity")
	}
	return nil
}

func (t *T3) inspectThread(ctx context.Context, target Target, task *Task, requireKnownRun bool) (NativeThread, error) {
	if err := t.Verify(ctx, target, task.ThreadID); err != nil {
		return NativeThread{}, err
	}
	raw, err := t.readThreadMetadata(ctx, target.EnvironmentID, task.ThreadID, 50)
	if err != nil {
		return NativeThread{}, err
	}
	native, err := raw.inspect(target, task.ThreadID)
	if err != nil {
		return NativeThread{}, err
	}
	// Only a recorded Bridge run can establish ownership. Pre-send continuations keep
	// the preceding run in Replies; a later manual/foreign run must not be organized.
	latest := raw.Thread.LatestRun
	known := latest != "" && task.RunID != "" && (latest == task.RunID || latest == task.ResultRunID)
	for _, r := range task.Replies {
		if latest != "" && r.RunID != "" && (latest == r.RunID || latest == r.ResultRunID) {
			known = true
		}
	}
	if requireKnownRun && !known {
		return NativeThread{}, errThreadMetadataUnavailable
	}
	for _, id := range task.ChildThreads {
		child, err := t.readThreadMetadata(ctx, target.EnvironmentID, id, 1)
		if err != nil {
			return NativeThread{}, err
		}
		c := child.Thread
		if c.ID != id || c.Pending == nil || len(c.ActiveRun) == 0 || len(child.Runs) == 0 {
			return NativeThread{}, errThreadMetadataUnavailable
		}
		if string(c.ActiveRun) != "null" || *c.Pending != 0 || len(c.Background) != 0 || c.Waiting || (c.Status != "idle" && !nativeTerminal(c.Status)) || !nativeTerminal(child.Runs[0].Status) {
			native.Quiet = false
		}
	}
	return native, nil
}

type threadMetadataPage struct {
	Thread struct {
		threadIdentity
		Title          *string           `json:"title"`
		Settled        *bool             `json:"settled"`
		Archived       bool              `json:"archived"`
		ActiveRun      json.RawMessage   `json:"activeRunId"`
		LatestRun      string            `json:"latestRunId"`
		Status         string            `json:"status"`
		CreatedBy      string            `json:"createdBy"`
		CreationSource string            `json:"creationSource"`
		Parent         json.RawMessage   `json:"parentThreadId"`
		Pending        *int              `json:"pendingRequestCount"`
		Background     []json.RawMessage `json:"pendingBackgroundTasks"`
		Waiting        bool              `json:"blockedOnThreadWait"`
	} `json:"thread"`
	Runs []threadRun `json:"recentRuns"`
}

func (t *T3) readThreadMetadata(ctx context.Context, environmentID, id string, runLimit int) (threadMetadataPage, error) {
	var page threadMetadataPage
	err := t.Calls.Call(ctx, "t3_thread_read", map[string]any{"environmentId": environmentID, "threadId": id, "limit": 1, "runLimit": runLimit}, &page)
	return page, err
}

var errThreadMetadataUnavailable = errors.New("thread metadata identity/quiet evidence unavailable")

func (raw threadMetadataPage) inspect(target Target, id string) (NativeThread, error) {
	p := raw.Thread
	if p.ID != id || p.Title == nil || p.Settled == nil || p.Pending == nil || len(p.ActiveRun) == 0 || string(p.Parent) != "null" || (p.CreatedBy != "agent" && p.CreatedBy != "user") || p.CreationSource != "mcp" || p.Archived {
		return NativeThread{}, errThreadMetadataUnavailable
	}
	quiet := string(p.ActiveRun) == "null" && *p.Pending == 0 && len(p.Background) == 0 && !p.Waiting && (p.Status == "idle" || nativeTerminal(p.Status))
	found := false
	for _, r := range raw.Runs {
		if r.RunID == p.LatestRun {
			if r.Model != target.Model || r.ProviderID != target.ProviderID || (r.ExecutedModel != nil && *r.ExecutedModel != target.Model) {
				return NativeThread{}, errThreadMetadataUnavailable
			}
			found = true
		}
		if !nativeTerminal(r.Status) {
			quiet = false
		}
	}
	if !found {
		return NativeThread{}, errThreadMetadataUnavailable
	}
	return NativeThread{Title: *p.Title, Settled: *p.Settled, Quiet: quiet}, nil
}
func nativeTerminal(status string) bool {
	return slices.Contains([]string{"completed", "interrupted", "failed", "cancelled", "rolled_back"}, status)
}
func (t *T3) RenameThread(ctx context.Context, target Target, task *Task, title string) error {
	if mcpOperateAccess(t.Calls) == "missing" {
		return ErrMCPOperationNotGranted
	}
	var out struct {
		ThreadID string `json:"threadId"`
		Title    string `json:"title"`
	}
	err := t.Calls.Call(ctx, "t3_thread_update", map[string]any{"action": "rename", "environmentId": target.EnvironmentID, "threadId": task.ThreadID, "title": title, "clientRequestId": stable(task.ID, "title-v1", title)}, &out)
	if err != nil {
		return err
	}
	if out.ThreadID != task.ThreadID || out.Title != title {
		return errors.New("thread rename acknowledgement differs")
	}
	return nil
}
func (t *T3) OrganizeThread(ctx context.Context, target Target, task *Task, action string) error {
	if action != "settle" && action != "unsettle" {
		return errors.New("unsupported thread metadata action")
	}
	if mcpOperateAccess(t.Calls) == "missing" {
		return ErrMCPOperationNotGranted
	}
	return t.Calls.Call(ctx, "t3_thread_organize", map[string]any{"action": action, "environmentId": target.EnvironmentID, "threadId": task.ThreadID}, nil)
}
