package bridge

import (
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

const maxResultBytes = 128 << 10

// Past turns stay inside the existing Task, keyed by their original request identity.
type ReplyRound struct {
	Report            *AgentReport       `json:"report,omitempty"`
	ReportIssue       string             `json:"reportIssue,omitempty"`
	InputID           string             `json:"inputId,omitempty"`
	RequestID         string             `json:"requestId"`
	RunID             string             `json:"runId,omitempty"`
	ResultRunID       string             `json:"resultRunId,omitempty"`
	Source            Message            `json:"source"`
	Attachments       []Attachment       `json:"attachments,omitempty"`
	CardID            string             `json:"cardId,omitempty"`
	TextReply         bool               `json:"textReply,omitempty"`
	MarkdownReply     bool               `json:"markdownReply,omitempty"`
	Phase             string             `json:"phase"`
	Summary           string             `json:"summary"`
	Result            string             `json:"result,omitempty"`
	Issue             string             `json:"issue,omitempty"`
	NotificationIssue string             `json:"notificationIssue,omitempty"`
	DeliveryState     ReplyDeliveryState `json:"deliveryState,omitempty"`

	Process *processMessage `json:"process,omitempty"`
}

func replySource(t *Task) Message {
	if t.ReplySource != nil {
		return *t.ReplySource
	}
	return t.Source
}

func usesChatReplyReceipt(t *Task) bool {
	source := replySource(t)
	return source.Comment == nil && source.Meeting == nil && t.Source.Meeting == nil && t.Source.MeetingSummary == nil
}

func cardUUID(t *Task) string {
	if t.CardRequestID == "" {
		return stable(t.ID, "card")
	}
	return stable(t.ID, t.CardRequestID, "card")
}
func cardStopRequest(t *Task) any {
	if t.CardRequestID == "" {
		return nil
	}
	return t.CardRequestID
}
func knownReplyReference(t *Task, id string) bool {
	if id == "" {
		return false
	}
	if id == t.CardID {
		return true
	}
	for _, r := range t.Replies {
		if r.CardID == id {
			return true
		}
	}
	return false
}
func currentStopReference(t *Task, m Message) bool {
	if m.ControlCard != "" {
		return !t.TextReply && m.ControlCard == t.CardID
	}
	if m.ParentID == "" {
		return true
	}
	return m.ParentID == t.CardID || m.ParentID == replySource(t).ID || slices.Contains(t.DispatchSource.MergedIDs, m.ParentID)
}
func archiveReply(t *Task) {
	if t.RequestID == "" {
		return
	}
	for _, r := range t.Replies {
		if r.RequestID == t.RequestID {
			return
		}
	}
	r := currentReply(t)
	if t.Process != nil && t.RunID != "" {
		p := *t.Process
		if !p.Closed {
			p.FinalText = processText(t)
		}
		r.Process = &p
	}
	r.TextReply, r.MarkdownReply = t.TextReply, t.MarkdownReply
	r.Summary = display(t.Summary, 1800)
	r.DeliveryState = finalReplyDelivery(t)
	if r.Source.Comment != nil {
		r.NotificationIssue = t.NotificationIssue
	}
	t.Replies = append(t.Replies, r)
}
func beginReply(t *Task, m Message, ordinary bool, settings RunSettings) {
	archiveReply(t)
	if ordinary || t.TextReply {
		t.CardID = ""
		t.CardAttemptAt = ""
	}
	if !ordinary {
		for i := len(t.Replies) - 1; i >= 0; i-- {
			r := t.Replies[i]
			if r.CardID != "" && (r.CardID == m.ParentID || r.InputID == m.ParentID || slices.Contains(r.Source.MergedIDs, m.ParentID)) {
				if r.TextReply {
					t.CardID = ""
					t.CardAttemptAt = ""
					break
				}
				t.CardID = r.CardID
				break
			}
		}
	}
	t.setReplyMode(m, settings)
	t.TextReplyBody = ""
	t.ReplySource = &m
	t.CardRequestID = stable(t.ID, m.ID)
	t.InputOpen = false
	t.FormEcho = ""
	if m.InputKind == "form" {
		t.FormEcho = display(m.Text, 1000)
	}
	t.NotifyHash = ""
	t.FinalReplyHash = ""
	t.NotificationIssue = ""
	t.Result = ""
	t.ResultIssue = ""
	t.RunActivity = nil
	t.setProcessMode(m, settings.ProcessMode)
	t.Report = nil
	t.ReportIssue = ""
}

func (t *Task) setReplyMode(m Message, settings RunSettings) {
	eligible := t.CardID == "" && t.CardAttemptAt == "" && t.Source.InputKind != "automatic"
	mode := settings.replyMode(m)
	t.MarkdownReply = eligible && mode == "markdown"
	t.TextReply = t.MarkdownReply || (eligible && mode == "text")
}

func finishResult(t *Task, full, issue string) {
	t.Result = ""
	t.ResultIssue = issue
	if issue != "" {
		return
	}
	full = sensitive.ReplaceAllString(full, "[已脱敏]")
	if len(full) > maxResultBytes {
		t.ResultIssue = "回答超过128KiB全文限额，未保存/交付完整内容"
		return
	}
	if strings.TrimSpace(full) == "" {
		t.ResultIssue = "本轮未取得可见最终正文，全文尚未交付"
		return
	}
	t.Result = full
}
func messageResources(m Message) []Image { return append(append([]Image{}, m.Images...), m.Files...) }
func attachmentName(name, kind string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." || name == "/" {
		name = kind
	}
	runes := []rune(name)
	if len(runes) > 120 {
		name = string(runes[:120])
	}
	return name
}
