package bridge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	vc "github.com/larksuite/oapi-sdk-go/v3/service/vc/v1"
)

const (
	meetingJoinPending  = "join_pending"
	meetingJoinAccepted = "join_accepted"
	meetingJoinRejected = "join_rejected"
	meetingLeavePending = "leave_pending"
)

type meetingRecord struct {
	Scope        string `json:"scope"`
	ID           string `json:"id,omitempty"`
	Phase        string `json:"phase"`
	TaskID       string `json:"taskId,omitempty"`
	Inviter      string `json:"inviter,omitempty"`
	Actor        string `json:"actor,omitempty"`
	Origin       string `json:"origin,omitempty"`
	Local        bool   `json:"local,omitempty"`
	LocalAttempt string `json:"localAttempt,omitempty"`
	JoinAttempt  string `json:"joinAttempt,omitempty"`
}

type meetingTransport interface {
	JoinMeeting(context.Context, string) (string, error)
	LeaveMeeting(context.Context, string) error
}

func (e *Engine) meetingControl(ctx context.Context, st *State, id string, cmd *SessionCommand) (bool, error) {
	m := cmd.Source
	if m.ChatType != "p2p" || m.Comment != nil || !e.Config.isAdmin(m.UserID) {
		cmd.Reply = "会议操作仅供所有者或管理员在私聊使用。"
		return false, nil
	}
	action := "status"
	var args []string
	if words := strings.Fields(routeText(m)); len(words) > 1 {
		action, args = words[1], words[2:]
	}
	number := strings.Join(args, "")
	question := ""
	if action == "ask" {
		body := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(routeText(m)), "/meeting"))
		question = strings.TrimSpace(strings.TrimPrefix(body, "ask"))
		number = ""
		if len(args) > 0 && validMeetingNumber(args[0]) {
			number = args[0]
			question = strings.TrimSpace(strings.TrimPrefix(question, number))
		}
		if question == "" {
			cmd.Reply = "用法：/meeting ask [会议号] <问题>"
			return false, nil
		}
	}
	scope := userScope(m)
	var owned []string
	for no, record := range st.Meetings {
		if record.ownedBy(scope, m.UserID) {
			owned = append(owned, no)
		}
	}
	if action == "status" && len(args) == 0 {
		cmd.Reply = e.meetingStatus(st.Meetings, owned)
		return false, nil
	}
	if number == "" && slices.Contains([]string{"leave", "transcript", "ask", "notes", "stop"}, action) {
		if len(owned) != 1 {
			cmd.Reply = "请指定一场已跟踪的会议号。用 /meeting status 查看。"
			return false, nil
		}
		number = owned[0]
	}
	if !validMeetingNumber(number) || !slices.Contains([]string{"join", "leave", "confirm-left", "transcript", "ask", "notes", "stop"}, action) {
		cmd.Reply = "会议命令：status、join <9 位会议号>、leave、transcript、ask <问题>、notes、stop、confirm-left <会议号>。请勿在命令中提供会议密码。"
		return false, nil
	}
	record := st.Meetings[number]
	if record.Phase != "" && !record.ownedBy(scope, m.UserID) {
		cmd.Reply = "无法在当前私聊管理这个会议号。"
		return false, nil
	}
	if action == "join" {
		if !e.Config.MeetingEnabled {
			cmd.Reply = "此账号尚未启用会议入会。"
			return false, nil
		}
		if record.Phase != "" && record.Phase != meetingJoinRejected {
			cmd.Reply = "此会议已有回执。用 /meeting status 核对。"
			return false, nil
		}
	} else if record.Phase == "" {
		cmd.Reply = "当前私聊没有此会议的回执。"
		return false, nil
	}
	if action == "leave" && record.Phase == meetingJoinRejected {
		cmd.Reply = "飞书已拒绝自动入会。可用 /meeting join 手动重新提交。"
		return false, nil
	}
	if record.Scope == "" && action != "join" && action != "confirm-left" {
		claimed := record
		claimed.Scope = scope
		matched := false
		if err := e.Store.update(func(st *State) error {
			matched = st.Meetings[number] == record
			if matched {
				st.Meetings[number] = claimed
			}
			return nil
		}); err != nil {
			return false, err
		}
		if !matched {
			cmd.Reply = "会议回执已变化，请重新查看 /meeting status。"
			return false, nil
		}
		record = claimed
	}
	if slices.Contains([]string{"transcript", "ask", "notes", "stop"}, action) {
		return e.meetingContentControl(ctx, st, cmd, action, number, question, record)
	}
	if action == "confirm-left" {
		cmd.Reply = "已记录你确认机器人不在该会议中。"
		_, err := e.saveMeeting(number, record, meetingRecord{}, id, cmd)
		return false, err
	}
	if action == "leave" && record.ID == "" {
		cmd.Reply = "入会结果尚未确认。请在飞书核对参会名单及等候室，必要时请主持人移除机器人，然后用 /meeting confirm-left <会议号> 解除待核对状态。"
		return false, nil
	}
	api, ok := e.Lark.(meetingTransport)
	if !ok {
		cmd.Reply = "此接收端未提供飞书会议接口。"
		return false, nil
	}
	return false, e.changeMeeting(ctx, api, action, number, record, id, cmd)
}

func (r meetingRecord) ownedBy(scope, user string) bool {
	if r.Local {
		return false
	}
	if r.Scope != "" {
		return r.Scope == scope
	}
	return r.Inviter != "" && r.Inviter == user
}

func validMeetingNumber(number string) bool {
	return len(number) == 9 && strings.Trim(number, "0123456789") == ""
}

func (e *Engine) meetingStatus(records map[string]meetingRecord, owned []string) string {
	status := "入会已关闭。"
	if e.Config.MeetingEnabled {
		status = "入会已启用。机器人入会后对参会者可见。"
	}
	if len(owned) == 0 {
		return status + "\n当前私聊没有未关闭的会议回执。"
	}
	status += "\n当前私聊的会议回执："
	slices.Sort(owned)
	for _, number := range owned {
		phase := "入会结果待核对"
		record := records[number]
		switch record.Phase {
		case meetingJoinAccepted:
			phase = "飞书已受理入会"
			if e.Config.MeetingEnabled && e.Config.MeetingRoute != "" {
				phase += fmt.Sprintf(" · 推送缓存 %d 句", len(e.Store.copyMeetingCaptions(record.ID).Captions))
			}
		case meetingLeavePending:
			phase = "离会结果待核对"
		case meetingJoinRejected:
			phase = "飞书已拒绝自动入会"
		}
		if record.Inviter != "" {
			phase += " · 邀请"
		}
		status += "\n" + number + " · " + phase
	}
	return display(status, 1800)
}

func (e *Engine) changeMeeting(ctx context.Context, api meetingTransport, action, number string, record meetingRecord, id string, cmd *SessionCommand) error {
	pending := record
	pending.Phase = meetingLeavePending
	cmd.Reply = "离会结果待核对。请查看会中参会名单及等候室。"
	if action == "join" {
		pending = meetingRecord{Scope: userScope(cmd.Source), Phase: meetingJoinPending, Actor: cmd.Source.UserID, Origin: cmd.Source.ChatID, JoinAttempt: id}
		cmd.Reply = "入会结果待核对。请查看会中参会名单及等候室。"
	}
	// The pending command and its meeting record must survive before the remote write.
	if saved, err := e.saveMeeting(number, record, pending, id, cmd); !saved || err != nil {
		return err
	}
	before := pending
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var err error
	if action == "join" {
		pending.ID, err = api.JoinMeeting(ctx, number)
		if err == nil {
			pending.Phase = meetingJoinAccepted
			cmd.Reply = "飞书已受理入会请求。请在参会名单或等候室核对。"
		}
	} else {
		err = api.LeaveMeeting(ctx, record.ID)
		if err == nil {
			pending = meetingRecord{}
			cmd.Reply = "飞书已确认离会。"
		}
	}
	var rejected *meetingRejection
	if errors.As(err, &rejected) {
		cmd.Reply = rejected.Reason
		pending = record
	} else if err != nil {
		e.commandFailed(ctx, CommandFailureMeeting)
	}
	_, err = e.saveMeeting(number, before, pending, id, cmd)
	return err
}

func (e *Engine) saveMeeting(number string, before, after meetingRecord, id string, cmd *SessionCommand) (bool, error) {
	matched := false
	closedID := ""
	err := e.Store.update(func(st *State) error {
		matched = st.Meetings[number] == before
		if matched {
			if e.Store.setMeetingRecord(e.Config, st, number, before, after) {
				closedID = after.ID
				cmd.Reply = "会议已结束，入会回执已关闭。"
			} else if after.Phase == "" {
				closedID = before.ID
			}
		} else {
			cmd.Reply = "会议回执已更新。请用 /meeting status 核对。"
		}
		saveCommandState(st, id, cmd)
		return nil
	})
	if err == nil && closedID != "" {
		e.Store.forgetClosedMeetingCaptions(closedID)
	}
	return matched, err
}

func (s *Store) setMeetingRecord(c Config, st *State, number string, before, after meetingRecord) bool {
	// The application response supplies the exact ID. The short number only
	// selects which in-flight attempt can retain the earlier event.
	ended := st.AccountID == c.AppID && before.Phase == meetingJoinPending && before.ID == "" && after.Phase == meetingJoinAccepted &&
		slices.Contains(st.EarlyMeetingEnds[number], after.ID)
	if before != after {
		delete(st.EarlyMeetingEnds, number)
	}
	closeID := before.ID
	if ended {
		s.queueMeetingSummary(c, st, number, after)
		closeID, after = after.ID, meetingRecord{}
	}
	if after.Phase == "" {
		delete(st.Meetings, number)
		closeMeetingRoom(st, closeID)
		return ended
	}
	if st.Meetings == nil {
		st.Meetings = make(map[string]meetingRecord)
	}
	st.Meetings[number] = after
	return false
}

func (l *Lark) receiveMeetingEnded(_ context.Context, event *vc.P2BotMeetingEndedV1) error {
	if event.EventV2Base == nil || event.EventV2Base.Header == nil || event.EventV2Base.Header.AppID != l.Config.AppID || event.Event == nil || event.Event.Meeting == nil {
		return nil
	}
	id := value(event.Event.Meeting.Id)
	if id == "" {
		return nil
	}
	if err := l.Store.update(func(st *State) error {
		closeMeetingRoom(st, id)
		if st.AccountID == l.Config.AppID {
			rememberEarlyMeetingEnd(st, value(event.Event.Meeting.MeetingNo), id)
		}
		for number, record := range st.Meetings {
			if record.ID == id {
				l.Store.queueMeetingSummary(l.Config, st, number, record)
				delete(st.Meetings, number)
				delete(st.EarlyMeetingEnds, number)
			}
		}
		return nil
	}); err != nil {
		l.fail()
		return err
	}
	l.Store.forgetClosedMeetingCaptions(id)
	return nil
}

func rememberEarlyMeetingEnd(st *State, number, id string) {
	if !validMeetingNumber(number) || len(id) > 128 || !validInvitedMeetingID(id) {
		return
	}
	record := st.Meetings[number]
	if record.Phase != meetingJoinPending || record.ID != "" || (record.JoinAttempt == "" && record.LocalAttempt == "") {
		return
	}
	ids := st.EarlyMeetingEnds[number]
	if len(ids) >= 8 || slices.Contains(ids, id) {
		return
	}
	if st.EarlyMeetingEnds == nil {
		st.EarlyMeetingEnds = make(map[string][]string)
	}
	st.EarlyMeetingEnds[number] = append(ids, id)
}
