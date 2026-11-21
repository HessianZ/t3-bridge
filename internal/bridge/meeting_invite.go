package bridge

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"time"

	vc "github.com/larksuite/oapi-sdk-go/v3/service/vc/v1"
)

const meetingInviteWindow = 2 * time.Minute

type pendingMeetingInvite struct {
	Number string `json:"number"`
	ID     string `json:"id"`
	CallID string `json:"callId"`
}

type meetingInviteTransport interface {
	JoinInvitedMeeting(context.Context, string, string) (string, error)
}

func (c Config) meetingInviterAllowed(user string) bool {
	return c.MeetingEnabled && slices.Contains(c.MeetingAutoJoinUsers, user) && c.isAdmin(user) && !c.userDenied(user)
}

func freshMeetingInvite(now, sent time.Time) bool {
	age := now.Sub(sent)
	return age >= -time.Minute && age <= meetingInviteWindow
}

func (l *Lark) receiveMeetingInvite(ctx context.Context, raw *vc.P2BotMeetingInvitedV1) error {
	invite := normalizeMeetingInvite(raw, l.Config.AppID)
	if invite == nil || !l.Config.meetingInviterAllowed(invite.InviterID) {
		return nil
	}
	millis, err := strconv.ParseInt(raw.EventV2Base.Header.CreateTime, 10, 64)
	sent := time.UnixMilli(millis)
	if err != nil || !freshMeetingInvite(time.Now(), sent) {
		return nil
	}
	bot := l.Channel.GetBotIdentity(ctx)
	if bot == nil || bot.OpenID == "" || (invite.BotID != bot.OpenID && invite.BotID != bot.UserID) || invite.InviterID == bot.OpenID || invite.InviterID == bot.UserID {
		return nil
	}
	if err := l.Store.acceptMeetingInvite(*invite, sent); err != nil {
		l.fail()
		return err
	}
	return nil
}

func (s *Store) acceptMeetingInvite(invite meetingInviteEvent, sent time.Time) error {
	m := Message{ID: stable("meeting-invite-call", invite.CallID), ChatID: "meeting-invite", UserID: invite.InviterID,
		MeetingInvite: &pendingMeetingInvite{Number: invite.MeetingNumber, ID: invite.MeetingID, CallID: invite.CallID},
		ReceivedAt:    sent.UTC().Format(time.RFC3339Nano)}
	return s.update(func(st *State) error {
		callKey := key(m, m.ID)
		eventKey := key(m, stable("meeting-invite-event", invite.EventID))
		if st.AccountID != invite.AppID || st.Seen[callKey] || st.Seen[eventKey] {
			return nil
		}
		if len(st.Inbox) >= 256 {
			return errors.New("inbox full")
		}
		st.Seen[callKey], st.Seen[eventKey] = true, true
		st.Inbox = append(st.Inbox, m)
		return nil
	})
}

func (e *Engine) consumeMeetingInvite(ctx context.Context, m Message) error {
	sent, err := time.Parse(time.RFC3339Nano, m.ReceivedAt)
	api, available := e.Lark.(meetingInviteTransport)
	if !available || err != nil || !freshMeetingInvite(e.now(), sent) || !e.Config.meetingInviterAllowed(m.UserID) {
		return e.drop(m)
	}
	invite := m.MeetingInvite
	pending := meetingRecord{ID: invite.ID, Inviter: m.UserID, Phase: meetingJoinPending}
	attempt := false
	if err := e.Store.update(func(st *State) error {
		_, queued := queuedPresent(st, m)
		old := st.Meetings[invite.Number]
		attempt = queued && st.AccountID == e.Config.AppID && (old.Phase == "" || old.Phase == meetingJoinRejected)
		if attempt {
			if st.Meetings == nil {
				st.Meetings = make(map[string]meetingRecord)
			}
			st.Meetings[invite.Number] = pending
		}
		// Keep the pending meeting and remove call_id before attempting the remote write.
		removeInbox(st, m)
		return nil
	}); err != nil || !attempt {
		return err
	}
	if e.Store.snapshot().Meetings[invite.Number] != pending {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	joined, err := api.JoinInvitedMeeting(ctx, invite.Number, invite.CallID)
	result := pending
	var rejected *meetingRejection
	switch {
	case err == nil && joined == invite.ID:
		result.Phase = meetingJoinAccepted
	case errors.As(err, &rejected):
		result.Phase = meetingJoinRejected
	}
	if result == pending {
		return nil
	}
	return e.Store.update(func(st *State) error {
		if st.Meetings[invite.Number] == pending {
			st.Meetings[invite.Number] = result
		}
		return nil
	})
}
