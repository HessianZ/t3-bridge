package bridge

import (
	"cmp"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

type DrainReport struct {
	Drained  bool           `json:"drained"`
	Accounts []DrainAccount `json:"accounts"`
}

type DrainAccount struct {
	AppID           string                `json:"appId"`
	Status          string                `json:"status"`
	Blockers        []DrainBlocker        `json:"blockers,omitempty"`
	DeliverySamples []DrainDeliverySample `json:"deliverySamples,omitempty"`
}

type DrainDeliverySample struct {
	MessageID string `json:"messageId"`
	Kind      string `json:"kind"`
}

type DrainBlocker struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

// InspectReceiverDrain reports durable local state for each configured App.
// It holds acquired locks together until the inspection ends and never creates a state file or lock.
func InspectReceiverDrain(config Config) DrainReport {
	report := DrainReport{Drained: true}
	for _, account := range config.Accounts() {
		lock, status := lockDrainState(account.StateDir)
		result := DrainAccount{AppID: account.AppID, Status: status}
		if lock != nil {
			defer lock.Close()
			result = inspectDrainState(account)
		}
		report.Accounts = append(report.Accounts, result)
		report.Drained = report.Drained && result.Status == "drained"
	}
	return report
}

func lockDrainState(dir string) (*os.File, string) {
	if err := localfile.CheckPrivateDir(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "missing"
		}
		return nil, "unavailable"
	}
	lock, err := localfile.LockExisting(stateLockPath(dir), false)
	switch {
	case err == nil:
		return lock, ""
	case errors.Is(err, localfile.ErrLocked):
		return nil, "busy"
	case errors.Is(err, os.ErrNotExist):
		if _, err := os.Lstat(filepath.Join(dir, "state.json")); errors.Is(err, os.ErrNotExist) {
			return nil, "missing"
		}
	}
	return nil, "unavailable"
}

func inspectDrainState(account Config) DrainAccount {
	result := DrainAccount{AppID: account.AppID, Status: "invalid"}
	state, err := readStateSnapshot(account.StateDir)
	switch {
	case err != nil:
		return result
	case state.Version == 0:
		result.Status = "missing"
		return result
	case state.AccountID == "":
		result.Status = "unbound"
		return result
	case state.AccountID != account.AppID:
		result.Status = "foreign"
		return result
	}
	for _, task := range state.Tasks {
		if task == nil {
			return result
		}
		if task.ReceiverAppID != "" && task.ReceiverAppID != account.AppID {
			result.Status = "foreign"
			return result
		}
	}

	active, controls := drainCounts(&state)
	commands, meetings, runs, delivery := 0, 0, 0, 0
	for _, command := range state.Commands {
		if command == nil {
			return result
		}
		if drainCommandUnconfirmed(command) {
			commands++
		}
	}
	for _, meeting := range state.Meetings {
		if meeting.Phase != meetingJoinRejected {
			meetings++
		}
	}
	samples := make(map[DrainDeliverySample]struct{})
	addSample := func(kind, id string, source Message) {
		if source.Comment != nil {
			return
		}
		if !validReceiptMessageID(id) {
			id = source.ID
			if !validReceiptMessageID(id) {
				return
			}
		}
		samples[DrainDeliverySample{MessageID: id, Kind: kind}] = struct{}{}
	}
	for _, task := range state.Tasks {
		if drainRunUnconfirmed(task) {
			runs++
		}
		delivery += taskDeliveryGapCount(task, addSample)
	}
	for sample := range samples {
		result.DeliverySamples = append(result.DeliverySamples, sample)
	}
	slices.SortFunc(result.DeliverySamples, func(a, b DrainDeliverySample) int {
		return cmp.Or(cmp.Compare(a.MessageID, b.MessageID), cmp.Compare(a.Kind, b.Kind))
	})
	const maxDrainDeliverySamples = 8
	if len(result.DeliverySamples) > maxDrainDeliverySamples {
		result.DeliverySamples = result.DeliverySamples[:maxDrainDeliverySamples]
	}
	for _, blocker := range []DrainBlocker{
		{"active", active}, {"inbox", len(state.Inbox)}, {"controls", controls},
		{"meetings", meetings}, {"command-outcome", commands}, {"run-outcome", runs}, {"delivery", delivery},
	} {
		if blocker.Count > 0 {
			result.Blockers = append(result.Blockers, blocker)
		}
	}
	result.Status = "drained"
	if len(result.Blockers) > 0 {
		result.Status = "blocked"
	}
	return result
}

func taskDeliveryGapCount(task *Task, sample func(kind, id string, source Message)) int {
	count := 0
	add := func(kind, id string, source Message) {
		count++
		if sample != nil {
			sample(kind, id, source)
		}
	}
	current := task.DispatchSource
	if current.ID == "" && len(task.Replies) == 0 {
		current = task.Source
	}
	if task.NotificationIssue != "" || (terminal(task.Phase) &&
		(task.NotifyHash == "" || (usesChatReplyReceipt(task) && task.CardID != "" && task.NotifyHash != notificationHash(task)))) {
		add("reply", current.ID, current)
	}
	if task.Process != nil && processDeliveryPending(task.Process,
		terminal(task.Phase) && (task.RunID != "" || !task.Process.CreateAt.IsZero())) {
		add("process", current.ID, current)
	}
	for _, reply := range task.Replies {
		if reply.NotificationIssue != "" || reply.DeliveryState == ReplyDeliveryPending || reply.DeliveryState == ReplyDeliveryUnconfirmed {
			add("reply", reply.InputID, reply.Source)
		}
		if reply.Process != nil && processDeliveryPending(reply.Process, reply.Process.FinalText != "" || !reply.Process.CreateAt.IsZero()) {
			add("process", reply.InputID, reply.Source)
		}
	}
	return count
}

func processDeliveryPending(process *processMessage, needsFinal bool) bool {
	return process.Issue != "" || (!process.Closed &&
		(needsFinal || (process.MessageID == "" && !process.CreateAt.IsZero())))
}
