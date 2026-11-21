package bridge

import "context"

type attachmentLaunchReceipt struct {
	ExpectedThreadID string `json:"expectedThreadId,omitempty"`
}

type attachmentLauncher interface {
	attachmentLaunchThreadID(requestID string) string
	launchWithAttachments(context.Context, Target, *Task) (Execution, error)
	recoverAttachmentLaunch(context.Context, Target, *Task) (Execution, error)
}

var _ attachmentLauncher = (*T3)(nil)

func (e *Engine) launchAttachmentDispatch(ctx context.Context, t *Task, launcher attachmentLauncher) error {
	ex, err := launcher.launchWithAttachments(ctx, t.Target, t)
	if err != nil {
		return e.attachmentDispatchUnconfirmed(t)
	}
	return e.confirmAttachmentDispatch(t, ex)
}

func (e *Engine) recoverAttachmentDispatch(ctx context.Context, t *Task) error {
	if launcher, ok := e.T3.(attachmentLauncher); ok && t.AttachmentLaunch.ExpectedThreadID != "" {
		ex, err := launcher.recoverAttachmentLaunch(ctx, t.Target, t)
		if err == nil {
			return e.confirmAttachmentDispatch(t, ex)
		}
	}
	return e.attachmentDispatchUnconfirmed(t)
}

func (e *Engine) confirmAttachmentDispatch(t *Task, ex Execution) error {
	t.ThreadID, t.RunID, t.Link = ex.ThreadID, ex.RunID, ex.Link
	t.ThreadMeta.TitleChecked = true
	t.Phase, t.NeedsReconcile = "tracking", false
	t.Summary = "正在执行"
	if t.StopRequested {
		t.Summary = "已确认原附件运行，等待处理停止请求"
	}
	t.Pending = nil
	return e.commit(t)
}

func (e *Engine) attachmentDispatchUnconfirmed(t *Task) error {
	summary := "附件任务启动结果待核对，保留原请求和附件回执"
	if t.AttachmentLaunch.ExpectedThreadID == "" {
		summary = "附件任务启动结果待人工核对，保留原请求和附件回执"
	}
	if t.StopRequested {
		summary += "；确认原运行后处理停止请求"
	}
	if t.Phase == "needs_reconcile" && t.NeedsReconcile && t.Summary == summary {
		return nil
	}
	t.Phase, t.NeedsReconcile, t.Summary = "needs_reconcile", true, summary
	return e.commit(t)
}
