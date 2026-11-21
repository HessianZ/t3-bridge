// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package runtime

import (
	"context"
	"encoding/json"

	"github.com/larksuite/channel-sdk-go/types"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkvc "github.com/larksuite/oapi-sdk-go/v3/service/vc/v1"
)

// OnMeetingInvited registers the meeting bot invite event before starting the channel.
func (ch *Client) OnMeetingInvited(handler func(context.Context, *larkvc.P2BotMeetingInvitedV1) error) {
	if ch.wsClient != nil && ch.wsClient.EventHandler() != nil {
		ch.wsClient.EventHandler().OnCustomizedEvent("vc.bot.meeting_invited_v1", func(ctx context.Context, req *larkevent.EventReq) error {
			event, err := decodeMeetingInvited(req)
			if err != nil {
				return err
			}
			return handler(ctx, event)
		})
	}
}

func decodeMeetingInvited(req *larkevent.EventReq) (*larkvc.P2BotMeetingInvitedV1, error) {
	var wire struct {
		*larkevent.EventV2Base
		Event *struct {
			larkvc.P2BotMeetingInvitedV1Data
			Bot     *meetingInviteUser `json:"bot"`
			Inviter *meetingInviteUser `json:"inviter"`
			Meeting *struct {
				larkvc.MeetingAgentEventMeeting
				HostUser *meetingInviteUser `json:"host_user"`
			} `json:"meeting"`
		} `json:"event"`
	}
	if err := json.Unmarshal(req.Body, &wire); err != nil {
		return nil, err
	}
	event := &larkvc.P2BotMeetingInvitedV1{EventV2Base: wire.EventV2Base, EventReq: req}
	if data := wire.Event; data != nil {
		event.Event = &data.P2BotMeetingInvitedV1Data
		event.Event.Bot = (*larkvc.MeetingAgentEventUser)(data.Bot)
		event.Event.Inviter = (*larkvc.MeetingAgentEventUser)(data.Inviter)
		if meeting := data.Meeting; meeting != nil {
			event.Event.Meeting = &meeting.MeetingAgentEventMeeting
			event.Event.Meeting.HostUser = (*larkvc.MeetingAgentEventUser)(meeting.HostUser)
		}
	}
	return event, nil
}

// Meeting pushes can carry an ID object, while the generated VC model expects a string.
type meetingInviteUser larkvc.MeetingAgentEventUser

func (u *meetingInviteUser) UnmarshalJSON(raw []byte) error {
	wire := struct {
		*larkvc.MeetingAgentEventUser
		ID json.RawMessage `json:"id"`
	}{MeetingAgentEventUser: (*larkvc.MeetingAgentEventUser)(u)}
	if err := json.Unmarshal(raw, &wire); err != nil || len(wire.ID) == 0 {
		return err
	}
	if wire.ID[0] == '{' {
		var ids larkim.UserId
		if err := json.Unmarshal(wire.ID, &ids); err != nil {
			return err
		}
		u.Id = ids.OpenId
		return nil
	}
	return json.Unmarshal(wire.ID, &u.Id)
}

// OnMeetingEnded registers the meeting bot event before starting the channel.
func (ch *Client) OnMeetingEnded(handler func(context.Context, *larkvc.P2BotMeetingEndedV1) error) {
	if ch.wsClient != nil && ch.wsClient.EventHandler() != nil {
		ch.wsClient.EventHandler().OnP2BotMeetingEndedV1(handler)
	}
}

// OnMeetingActivity registers the meeting bot activity event before starting the channel.
func (ch *Client) OnMeetingActivity(handler func(context.Context, *larkvc.P2BotMeetingActivityV1) error) {
	if ch.wsClient != nil && ch.wsClient.EventHandler() != nil {
		ch.wsClient.EventHandler().OnP2BotMeetingActivityV1(handler)
	}
}

// OnComment registers a handler for CommentEvent.
func (ch *Client) OnComment(handler func(ctx context.Context, event *types.CommentEvent) error) {
	ch.onCommentHandlers = append(ch.onCommentHandlers, handler)
	if ch.commentHandlerReg || ch.wsClient == nil {
		return
	}
	ch.commentHandlerReg = true
	dispatcher := ch.wsClient.EventHandler()
	if dispatcher != nil {
		dispatcher.OnCustomizedEvent("drive.notice.comment_add_v1", func(ctx context.Context, event *larkevent.EventReq) error {
			return ch.inbound.HandleComment(ctx, event)
		})
	}
}

// OnBotAdded registers a handler for BotAddedEvent.
func (ch *Client) OnBotAdded(handler func(ctx context.Context, event *types.BotAddedEvent) error) {
	ch.onBotAddedHandlers = append(ch.onBotAddedHandlers, handler)
	if ch.botAddedHandlerReg || ch.wsClient == nil {
		return
	}
	ch.botAddedHandlerReg = true
	dispatcher := ch.wsClient.EventHandler()
	if dispatcher != nil {
		dispatcher.OnP2ChatMemberBotAddedV1(func(ctx context.Context, event *larkim.P2ChatMemberBotAddedV1) error {
			return ch.inbound.HandleBotAdded(ctx, event)
		})
	}
}

// OnReaction registers a handler for ReactionEvent.
func (ch *Client) OnReaction(handler func(ctx context.Context, event *types.ReactionEvent) error) {
	ch.onReactionHandlers = append(ch.onReactionHandlers, handler)
	if ch.reactionHandlerReg || ch.wsClient == nil {
		return
	}
	ch.reactionHandlerReg = true
	dispatcher := ch.wsClient.EventHandler()
	if dispatcher != nil {
		dispatcher.OnP2MessageReactionCreatedV1(func(ctx context.Context, event *larkim.P2MessageReactionCreatedV1) error {
			return ch.inbound.HandleReaction(ctx, event)
		})
		dispatcher.OnP2MessageReactionDeletedV1(func(ctx context.Context, event *larkim.P2MessageReactionDeletedV1) error {
			return ch.inbound.HandleReaction(ctx, event)
		})
	}
}

// OnCardAction registers a handler for CardActionEvent events.
func (ch *Client) OnCardAction(handler func(ctx context.Context, event *types.CardActionEvent) error) {
	ch.onCardActionHandlers = append(ch.onCardActionHandlers, handler)
	if ch.wsClient == nil {
		return
	}
	dispatcher := ch.wsClient.EventHandler()
	if dispatcher != nil {
		dispatcher.OnP2CardActionTrigger(func(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
			return ch.inbound.HandleCardAction(ctx, event)
		})
	}
}

// OnReject registers a handler for messages rejected by safety policies.
func (ch *Client) OnReject(handler func(ctx context.Context, event *types.RejectEvent) error) {
	ch.onRejectHandlers = append(ch.onRejectHandlers, handler)
}
