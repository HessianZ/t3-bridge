package bridge

import (
	"context"
	"errors"
	"time"

	im "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const (
	processInitialText    = "处理过程\n已开始跟踪本轮"
	processCreateWindow   = 55 * time.Minute // The Reply UUID deduplication window is one hour.
	processPatchWindow    = 30 * 24 * time.Hour
	processStreamInterval = 4 * time.Second
	processPreviewLimit   = 1600
)

type processMessage struct {
	Mode          toolActivityMode `json:"mode"`
	ReplyTo       string           `json:"replyTo"`
	InThread      bool             `json:"inThread,omitempty"`
	Stage         string           `json:"stage,omitempty"`
	StreamPreview string           `json:"streamPreview,omitempty"`
	MessageID     string           `json:"messageId,omitempty"`
	CreateAt      time.Time        `json:"createAt,omitzero"`
	LastAttempt   time.Time        `json:"lastAttempt,omitzero"`
	SentHash      string           `json:"sentHash,omitempty"`
	TriedFinal    bool             `json:"triedFinal,omitempty"`
	Closed        bool             `json:"closed,omitempty"`
	FinalText     string           `json:"finalText,omitempty"`
	Issue         string           `json:"issue,omitempty"`
}

func processHasPreview(mode toolActivityMode) bool {
	return mode == processStream || mode == processStreamBrief || mode == processStreamDetailed
}

func processToolMode(mode toolActivityMode) toolActivityMode {
	switch mode {
	case processStreamBrief:
		return toolActivityBrief
	case processStreamDetailed:
		return toolActivityDetailed
	default:
		return mode
	}
}

func newProcessMessage(m Message, mode toolActivityMode) *processMessage {
	if !processEnabled(mode) || m.Comment != nil || m.InputKind == "automatic" || (mode == processStream && m.Meeting != nil) {
		return nil
	}
	if m.Meeting != nil {
		mode = processToolMode(mode)
	}
	anchor := m.ID
	if m.InputKind == "form" {
		// The form event has a synthetic input ID. Its verified card is the Feishu reply anchor.
		anchor = m.ControlCard
	}
	if anchor == "" {
		return nil
	}
	return &processMessage{Mode: mode, ReplyTo: anchor, InThread: m.TopicID != ""}
}

func (t *Task) setProcessMode(m Message, mode toolActivityMode) {
	t.CardPreview = ""
	t.CardPreviewEnabled = false
	if mode == processCard {
		t.Process = nil
		t.CardPreviewEnabled = !t.TextReply && m.Comment == nil && m.Meeting == nil &&
			m.MeetingSummary == nil && m.InputKind != "automatic" && m.InputKind != doctorEchoKind
		return
	}
	t.Process = newProcessMessage(m, mode)
}

func (t *Task) cardPreviewText() string {
	if !t.CardPreviewEnabled || t.TextReply || t.StopRequested || t.NeedsReconcile || terminal(t.Phase) {
		return ""
	}
	return t.CardPreview
}

func processStage(o Observation) string {
	switch {
	case o.PendingApprovals > 0:
		return "等待用户处理 T3 审批或问题"
	case len(o.Tasks) > 0:
		return "等待异步工作收口"
	}
	switch o.Status {
	case "preparing", "queued", "starting":
		return "T3 正在排队或启动"
	case "running":
		return "T3 正在执行"
	case "waiting":
		return "T3 正在等待"
	default:
		return "正在核对运行与收口状态"
	}
}

func processText(t *Task) string {
	stage := t.Process.Stage
	switch t.Phase {
	case "completed":
		stage = "本轮执行已结束，结论见本轮回复"
	case "failed", "blocked":
		stage = "本轮已结束，状态见本轮回复"
	case "stopped":
		stage = "本轮已停止继续派发，状态见本轮回复"
		if t.NeedsReconcile {
			stage = "已停止继续派发，T3 停止结果待核对"
		}
	}
	if stage == "" {
		return processInitialText
	}
	text := "处理过程\n" + stage
	if activity := toolActivityText(t.RunActivity, processToolMode(t.Process.Mode), terminal(t.Phase)); activity != "" {
		text += "\n" + activity
	}
	if processHasPreview(t.Process.Mode) && !terminal(t.Phase) && t.Process.StreamPreview != "" {
		text += "\n\n公开正文预览\n" + t.Process.StreamPreview
	}
	return text
}

type processMessenger interface {
	upsertProcess(context.Context, processMessage, string, string) (string, error)
}

func (e *Engine) notify(ctx context.Context, t *Task) error {
	if err := e.notifyWorkingReaction(ctx, t); err != nil {
		return err
	}
	if err := e.notifyCommentWorkingReaction(ctx, t); err != nil {
		return err
	}
	api, ok := e.Lark.(processMessenger)
	if !ok || t.ReceiverAppID != e.Config.AppID {
		return e.notifyResult(ctx, t)
	}
	if p := t.Process; p != nil && !p.Closed && t.RunID != "" && scopedTask(e.Config, replySource(t), t, t.RouteID) {
		save := func(updated processMessage) error {
			t.Process = &updated
			return e.commit(t)
		}
		if err := e.deliverProcess(ctx, api, *p, t.RequestID, t.CardID, processText(t), terminal(t.Phase), save); err != nil {
			return err
		}
	}
	if err := e.notifyResult(ctx, t); err != nil {
		return err
	}
	for i, round := range t.Replies {
		p := round.Process
		if p == nil || p.Closed || p.FinalText == "" || (p.MessageID != "" && p.MessageID == t.CardID) || !scopedTask(e.Config, round.Source, t, t.RouteID) {
			continue
		}
		save := func(updated processMessage) error {
			return e.Store.update(func(st *State) error {
				st.Tasks[t.ID].Replies[i].Process = &updated
				return nil
			})
		}
		if err := e.deliverProcess(ctx, api, *p, round.RequestID, round.CardID, p.FinalText, true, save); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) deliverProcess(ctx context.Context, api processMessenger, p processMessage, requestID, cardID, text string, final bool, save func(processMessage) error) error {
	if requestID == "" || (p.MessageID != "" && p.MessageID == cardID) {
		return nil
	}
	now := e.now().UTC()
	interval := runActivityInterval
	if processHasPreview(p.Mode) && p.MessageID != "" {
		interval = processStreamInterval
	}
	due := !now.Before(p.LastAttempt.Add(interval))
	uuid := stable(requestID, "process")
	if p.MessageID == "" {
		if !p.CreateAt.IsZero() && (now.Before(p.CreateAt) || !now.Before(p.CreateAt.Add(processCreateWindow))) {
			p.Closed, p.FinalText = true, ""
			p.Issue = "创建结果未确认，已停止重试；请在原聊天中核对"
			return save(p)
		}
		if !due {
			return nil
		}
		if p.CreateAt.IsZero() {
			p.CreateAt = now
		}
		p.LastAttempt = now
		if err := save(p); err != nil {
			return err
		}
		// Retries use the same body as well as the same UUID, even after a phase change.
		id, err := sendProcess(ctx, api, p, uuid, processInitialText)
		if err != nil || id == "" || id == cardID || id == p.ReplyTo {
			return nil
		}
		p.MessageID, p.SentHash = id, stable(processInitialText)
		if err := save(p); err != nil || !final {
			return err
		}
	}
	hash := stable(text)
	if hash == p.SentHash || (!due && (!final || p.TriedFinal)) {
		return nil
	}
	if !now.Before(p.CreateAt.Add(processPatchWindow)) {
		p.Closed, p.FinalText = true, ""
		p.Issue = "过程消息已超过编辑窗口"
		return save(p)
	}
	p.LastAttempt, p.TriedFinal = now, final
	if err := save(p); err != nil {
		return err
	}
	if _, err := sendProcess(ctx, api, p, uuid, text); err != nil {
		return nil
	}
	p.SentHash = hash
	if final {
		p.Closed, p.FinalText = true, ""
	}
	return save(p)
}

func sendProcess(ctx context.Context, api processMessenger, p processMessage, uuid, text string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return api.upsertProcess(ctx, p, uuid, text)
}

func (l *Lark) upsertProcess(ctx context.Context, p processMessage, uuid, text string) (string, error) {
	var element any = map[string]string{"tag": "markdown", "content": text}
	if processHasPreview(p.Mode) {
		element = map[string]any{"tag": "div", "text": map[string]string{"tag": "plain_text", "content": text}}
	}
	content := card2JSON([]any{element})
	messages := l.Channel.RawClient().Im.V1.Message
	if p.MessageID != "" {
		r, err := messages.Patch(ctx, im.NewPatchMessageReqBuilder().MessageId(p.MessageID).Body(im.NewPatchMessageReqBodyBuilder().Content(content).Build()).Build())
		if err != nil || r == nil || !r.Success() {
			return "", errors.New("process message update unconfirmed")
		}
		return p.MessageID, nil
	}
	r, err := messages.Reply(ctx, im.NewReplyMessageReqBuilder().MessageId(p.ReplyTo).Body(im.NewReplyMessageReqBodyBuilder().MsgType("interactive").Content(content).Uuid(uuid).ReplyInThread(p.InThread).Build()).Build())
	if err != nil || r == nil || !r.Success() || r.Data == nil || value(r.Data.MessageId) == "" {
		return "", errors.New("process message creation unconfirmed")
	}
	return value(r.Data.MessageId), nil
}
