package bridge

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

var ErrMessageReceiptNotFound = errors.New("message receipt not found")

type ReplyDeliveryState string

const (
	ReplyDeliveryPending       ReplyDeliveryState = "pending"
	ReplyDeliveryConfirmed     ReplyDeliveryState = "confirmed"
	ReplyDeliveryUnconfirmed   ReplyDeliveryState = "unconfirmed"
	ReplyDeliveryNotApplicable ReplyDeliveryState = "not_applicable"
	ReplyDeliveryUnknown       ReplyDeliveryState = "unknown"
)

// MessageReceipt is a local snapshot of the exact round associated with an input.
type MessageReceipt struct {
	AppID          string             `json:"appId"`
	MessageID      string             `json:"messageId"`
	InputID        string             `json:"inputId,omitempty"`
	TaskID         string             `json:"taskId,omitempty"`
	RouteID        string             `json:"routeId,omitempty"`
	EnvironmentID  string             `json:"environmentId,omitempty"`
	ProjectID      string             `json:"projectId,omitempty"`
	Phase          string             `json:"phase"`
	RequestID      string             `json:"requestId,omitempty"`
	ThreadID       string             `json:"threadId,omitempty"`
	ThreadURL      string             `json:"threadUrl,omitempty"`
	RunID          string             `json:"runId,omitempty"`
	ReplyID        string             `json:"replyId,omitempty"`
	DeliveryState  ReplyDeliveryState `json:"deliveryState"`
	Historical     bool               `json:"historical"`
	NeedsReconcile bool               `json:"needsReconcile,omitempty"`
}

// ReadMessageReceipt reads the atomically published snapshot without taking the
// receiver's writer lock or making a request to Feishu or T3.
func ReadMessageReceipt(dir, appID, messageID string) (MessageReceipt, error) {
	if !validReceiptMessageID(messageID) {
		return MessageReceipt{}, errors.New("an incoming Feishu message ID (om_…) is required")
	}
	file, err := localfile.OpenPrivate(filepath.Join(dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return MessageReceipt{}, ErrMessageReceiptNotFound
	}
	if err != nil {
		return MessageReceipt{}, errors.New("cannot open private state snapshot")
	}
	defer file.Close()
	var state State
	decoder := json.NewDecoder(file)
	if decoder.Decode(&state) != nil || decoder.Decode(new(json.RawMessage)) != io.EOF || state.Version != 1 || state.Tasks == nil {
		return MessageReceipt{}, errors.New("cannot read receipt state snapshot")
	}
	if state.AccountID == "" || state.AccountID != appID {
		return MessageReceipt{}, errors.New("state snapshot is not bound to the selected app")
	}
	return state.messageReceipt(appID, messageID)
}

func validReceiptMessageID(id string) bool {
	return strings.HasPrefix(id, "om_") && len(id) > 3 && len(id) <= 128 &&
		!strings.ContainsFunc(id, func(r rune) bool {
			return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '_' || r == '-')
		})
}

func (s *State) messageReceipt(appID, messageID string) (MessageReceipt, error) {
	var found MessageReceipt
	match := func(source Message, input string) bool {
		return source.Comment == nil && (input == messageID || source.ID == messageID || slices.Contains(source.MergedIDs, messageID))
	}
	record := func(task *Task, round ReplyRound, history bool) error {
		if !match(round.Source, round.InputID) {
			return nil
		}
		if found.TaskID != "" || task.ID == "" {
			return errors.New("message matched ambiguous receipt metadata")
		}
		found = MessageReceipt{
			AppID: appID, MessageID: messageID, InputID: round.InputID,
			TaskID: task.ID, RouteID: task.RouteID, Phase: round.Phase, RequestID: round.RequestID,
			EnvironmentID: task.Target.EnvironmentID, ProjectID: task.Target.ProjectID,
			ThreadID: task.ThreadID, ThreadURL: taskThreadURL(task), RunID: round.RunID, ReplyID: round.CardID,
			Historical: history, NeedsReconcile: !history && task.NeedsReconcile,
		}
		if history {
			switch round.DeliveryState {
			case ReplyDeliveryPending, ReplyDeliveryConfirmed, ReplyDeliveryUnconfirmed, ReplyDeliveryNotApplicable:
				found.DeliveryState = round.DeliveryState
			default:
				found.DeliveryState = ReplyDeliveryUnknown
			}
		} else {
			found.DeliveryState = finalReplyDelivery(task)
		}
		return nil
	}
	for _, task := range s.Tasks {
		if task == nil || (task.ReceiverAppID != "" && task.ReceiverAppID != appID) {
			return MessageReceipt{}, errors.New("receipt state contains an invalid or foreign task")
		}
		for _, round := range task.Replies {
			if err := record(task, round, true); err != nil {
				return MessageReceipt{}, err
			}
		}
		source := task.DispatchSource
		if source.ID == "" && len(task.Replies) == 0 {
			source = task.Source // old state before dispatch-source tracking
		}
		current := ReplyRound{InputID: source.ID, Source: source, RequestID: task.RequestID, RunID: task.RunID, Phase: task.Phase, CardID: task.CardID}
		if err := record(task, current, false); err != nil {
			return MessageReceipt{}, err
		}
	}
	if found.TaskID != "" {
		return found, nil
	}
	for _, source := range s.Inbox {
		if match(source, source.ID) {
			phase := "queued"
			if source.Cancelled {
				phase = "cancelled"
			}
			return MessageReceipt{AppID: appID, MessageID: messageID, InputID: source.ID, Phase: phase, DeliveryState: ReplyDeliveryPending}, nil
		}
	}
	return MessageReceipt{}, ErrMessageReceiptNotFound
}

func finalReplyDelivery(task *Task) ReplyDeliveryState {
	if task.Source.MeetingSummary != nil || (task.Source.Meeting != nil && task.Source.Meeting.Room) {
		return ReplyDeliveryNotApplicable
	}
	if !terminal(task.Phase) {
		return ReplyDeliveryPending
	}
	// Private meeting operations can stamp NotifyHash without sending a reply.
	if task.CardID != "" && task.FinalReplyHash != "" && usesChatReplyReceipt(task) && task.FinalReplyHash == finalReplyContentHash(task) {
		return ReplyDeliveryConfirmed
	}
	if task.NotificationIssue != "" {
		return ReplyDeliveryUnconfirmed
	}
	if groupDoctorEcho(task) && task.CardID == "" && task.NotifyHash == notificationHash(task) {
		return ReplyDeliveryNotApplicable
	}
	if !usesChatReplyReceipt(task) {
		return ReplyDeliveryUnknown
	}
	if task.CardID == "" {
		if task.CardAttemptAt != "" {
			return ReplyDeliveryUnconfirmed
		}
		return ReplyDeliveryPending
	}
	if task.NotifyHash == notificationHash(task) {
		// Old receivers could stamp NotifyHash on UUID recovery before patching.
		return ReplyDeliveryUnknown
	}
	return ReplyDeliveryPending
}

func finalReplyContentHash(task *Task) string {
	// Changing form controls leaves the already-confirmed terminal answer intact.
	copy := *task
	copy.InputOpen, copy.FormEcho = false, ""
	return notificationHash(&copy)
}
