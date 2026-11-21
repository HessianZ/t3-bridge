package bridge

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"
)

type LocalMeetingRequest struct {
	Action   string `json:"action"`
	Number   string `json:"number,omitempty"`
	Revision string `json:"revision,omitempty"`
}

func (r LocalMeetingRequest) Validate() error {
	switch r.Action {
	case "status":
		if r.Number != "" || r.Revision != "" {
			return errors.New("会议状态请求不能指定会议号或回执版本")
		}
		return nil
	case "join", "leave", "confirm-left":
		if !validMeetingNumber(r.Number) {
			return errors.New("会议号必须为 9 位数字")
		}
	default:
		return errors.New("未知会议操作")
	}
	if r.Action == "join" {
		if r.Revision != "" {
			return errors.New("入会请求不能指定回执版本")
		}
	} else if len(r.Revision) != 32 || strings.Trim(r.Revision, "0123456789abcdef") != "" {
		return errors.New("会议回执版本必须为 32 位小写十六进制")
	}
	return nil
}

type LocalMeetingEntry struct {
	Number   string `json:"number"`
	Phase    string `json:"phase"`
	Origin   string `json:"origin"`
	Revision string `json:"revision"`
}

type LocalMeetingView struct {
	Enabled   bool                `json:"enabled"`
	Available bool                `json:"available"`
	Meetings  []LocalMeetingEntry `json:"meetings"`
	Outcome   string              `json:"outcome"`
}

var errLocalMeetingConflict = errors.New("会议回执已变化")

// LocalMeeting runs on the receiver worker and uses its bound App state.
func (e *Engine) LocalMeeting(ctx context.Context, request LocalMeetingRequest) (LocalMeetingView, error) {
	if err := request.Validate(); err != nil {
		return LocalMeetingView{}, err
	}
	if err := ctx.Err(); err != nil {
		return LocalMeetingView{}, err
	}
	s := e.Store
	if s == nil || e.Config.AppID == "" {
		return LocalMeetingView{}, errors.New("接收端没有绑定应用状态")
	}
	api, available := e.Lark.(meetingTransport)
	s.mu.Lock()
	appID, before := s.state.AccountID, s.state.Meetings[request.Number]
	s.mu.Unlock()
	if appID != e.Config.AppID {
		return LocalMeetingView{}, errors.New("接收端状态未绑定到当前应用")
	}
	view := LocalMeetingView{Enabled: e.Config.MeetingEnabled, Available: available, Meetings: []LocalMeetingEntry{}}
	if request.Action != "status" {
		view.Outcome = e.changeLocalMeeting(ctx, api, request, before)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.AccountID != appID {
		return LocalMeetingView{}, errLocalMeetingConflict
	}
	for _, number := range slices.Sorted(maps.Keys(s.state.Meetings)) {
		record := s.state.Meetings[number]
		origin := "chat"
		switch {
		case record.Local:
			origin = "local"
		case record.Inviter != "":
			origin = "invite"
		}
		view.Meetings = append(view.Meetings, LocalMeetingEntry{Number: number, Phase: record.Phase, Origin: origin,
			Revision: localMeetingRevision(appID, number, record)})
	}
	return view, nil
}

func localMeetingRevision(appID, number string, record meetingRecord) string {
	data, _ := json.Marshal(record) // The record contains only strings and a boolean.
	return stable("local-meeting", appID, number, string(data))
}

func (e *Engine) changeLocalMeeting(ctx context.Context, api meetingTransport, request LocalMeetingRequest, before meetingRecord) string {
	number := request.Number
	if request.Action != "join" && (before.Phase == "" || request.Revision != localMeetingRevision(e.Config.AppID, number, before)) {
		return "conflict"
	}
	if request.Action == "confirm-left" {
		if before.Phase != meetingJoinPending && before.Phase != meetingJoinAccepted && before.Phase != meetingLeavePending {
			return "conflict"
		}
		published, _, err := e.saveLocalMeeting(number, before, meetingRecord{})
		return localMeetingSaveOutcome(published, err, "confirmed", "unavailable")
	}

	pending := before
	switch request.Action {
	case "join":
		if !e.Config.MeetingEnabled || api == nil {
			return "unavailable"
		}
		if before.Phase != "" && before.Phase != meetingJoinRejected {
			if before.Phase == meetingJoinPending || before.Phase == meetingLeavePending {
				return "pending"
			}
			return "conflict"
		}
		pending = meetingRecord{Phase: meetingJoinPending, Local: true}
	case "leave":
		if before.Phase != meetingJoinAccepted || !validInvitedMeetingID(before.ID) {
			return "conflict"
		}
		if api == nil {
			return "unavailable"
		}
		pending.Phase = meetingLeavePending
	}
	// A fresh attempt distinguishes pending receipts created for the same meeting number.
	pending.LocalAttempt = rand.Text()
	if published, _, err := e.saveLocalMeeting(number, before, pending); err != nil {
		return localMeetingSaveOutcome(published, err, "", "unavailable")
	}
	if !e.matchesLocalMeeting(number, pending) {
		return "conflict"
	}
	if ctx.Err() != nil {
		return "pending"
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, outcome := pending, "joined"
	var err error
	if request.Action == "join" {
		var id string
		id, err = api.JoinMeeting(ctx, number)
		if err == nil && !validInvitedMeetingID(id) {
			err = errMeetingUnconfirmed
		}
		result.ID, result.Phase = id, meetingJoinAccepted
	} else {
		err = api.LeaveMeeting(ctx, before.ID)
		result, outcome = meetingRecord{}, "left"
	}
	var rejected *meetingRejection
	if errors.As(err, &rejected) {
		result, outcome = before, "rejected"
	} else if err != nil {
		// No reliable response. Preserve the pending receipt and check for an end event.
		if !e.matchesLocalMeeting(number, pending) {
			return "conflict"
		}
		return "pending"
	}
	published, ended, err := e.saveLocalMeeting(number, pending, result)
	if ended {
		outcome = "ended"
	}
	return localMeetingSaveOutcome(published, err, outcome, "pending")
}

func (e *Engine) matchesLocalMeeting(number string, record meetingRecord) bool {
	e.Store.mu.Lock()
	defer e.Store.mu.Unlock()
	return e.Store.state.AccountID == e.Config.AppID && e.Store.state.Meetings[number] == record
}

func (e *Engine) saveLocalMeeting(number string, before, after meetingRecord) (published, ended bool, err error) {
	published, err = e.Store.updatePublished(func(st *State) error {
		if st.AccountID != e.Config.AppID || st.Meetings[number] != before {
			return errLocalMeetingConflict
		}
		ended = e.Store.setMeetingRecord(e.Config, st, number, before, after)
		return nil
	})
	if !published {
		return false, false, err
	}
	if ended {
		e.Store.forgetClosedMeetingCaptions(after.ID)
	} else if before.ID != "" && after.Phase == "" {
		e.Store.forgetClosedMeetingCaptions(before.ID)
	}
	return published, ended, err
}

func localMeetingSaveOutcome(published bool, err error, success, unpublished string) string {
	switch {
	case err == nil:
		return success
	case errors.Is(err, errLocalMeetingConflict):
		return "conflict"
	case published:
		return "pending"
	default:
		return unpublished
	}
}
