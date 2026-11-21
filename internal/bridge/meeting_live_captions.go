package bridge

import (
	"context"
	"slices"

	vc "github.com/larksuite/oapi-sdk-go/v3/service/vc/v1"
)

const meetingCaptionReplayWindow = 128

type meetingCaptionWindow struct {
	result meetingCaptionResult
	events []string
}

func (l *Lark) receiveMeetingActivity(ctx context.Context, raw *vc.P2BotMeetingActivityV1) error {
	event := normalizeMeetingActivity(raw, l.Config.AppID)
	if event == nil {
		return nil
	}
	byMeeting := make(map[string][]meetingCaption)
	for _, item := range event.Items {
		if len(item.Captions) > 0 {
			byMeeting[item.MeetingID] = append(byMeeting[item.MeetingID], item.Captions...)
		}
	}
	for id, rows := range byMeeting {
		l.Store.keepMeetingCaptions(event.AppID, id, event.EventID, rows)
	}
	return l.receiveMeetingRoomCommands(ctx, event, raw.EventV2Base.Header.CreateTime)
}

func (s *Store) keepMeetingCaptions(appID, meetingID, eventID string, rows []meetingCaption) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if meetingID == "" || len(rows) == 0 || s.state.AccountID != appID {
		return
	}
	for _, record := range s.state.Meetings {
		if record.ID != meetingID || record.Phase != meetingJoinAccepted {
			continue
		}
		previous := s.meetingCaptions[meetingID]
		if eventID != "" {
			if slices.Contains(previous.events, eventID) {
				return
			}
			if len(previous.events) == meetingCaptionReplayWindow {
				previous.events = slices.Delete(previous.events, 0, 1)
			}
			previous.events = append(previous.events, eventID)
		}
		merged := mergeMeetingCaptions(slices.Concat(previous.result.Captions, rows))
		merged.Truncated = previous.result.Truncated || merged.Truncated
		previous.result = merged
		if s.meetingCaptions == nil {
			s.meetingCaptions = make(map[string]meetingCaptionWindow)
		}
		s.meetingCaptions[meetingID] = previous
		return
	}
}

func (s *Store) copyMeetingCaptions(meetingID string) meetingCaptionResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := s.meetingCaptions[meetingID].result
	result.Captions = slices.Clone(result.Captions)
	return result
}

func (s *Store) forgetClosedMeetingCaptions(meetingID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.state.Meetings {
		if record.ID == meetingID {
			return
		}
	}
	delete(s.meetingCaptions, meetingID)
}

func mergeMeetingCaptionSources(pages, live meetingCaptionResult, botSpeakerID string) meetingCaptionResult {
	result := mergeMeetingCaptions(slices.Concat(pages.Captions, live.Captions), botSpeakerID)
	result.Incomplete = pages.Incomplete
	result.Truncated = result.Truncated || pages.Truncated || live.Truncated
	return result
}
