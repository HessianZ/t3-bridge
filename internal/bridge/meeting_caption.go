package bridge

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	meetingCaptionMaxPages     = 8
	meetingCaptionMaxSentences = 30
	meetingCaptionMaxTextBytes = 6000
)

type meetingCaptionReader interface {
	ReadMeetingCaptionPage(ctx context.Context, meetingID, cursor string) ([]meetingCaption, string, error)
}

type meetingCaptionResult struct {
	Captions   []meetingCaption
	Incomplete bool // The reader did not reach the end of pagination.
	Truncated  bool // The output limits dropped or shortened usable captions.
}

// readMeetingCaptions takes a verified long meeting ID. Results from successful
// pages are available even when a later page fails.
func readMeetingCaptions(ctx context.Context, reader meetingCaptionReader, meetingID string, botSpeakerIDs ...string) (meetingCaptionResult, error) {
	var captions []meetingCaption
	seenCursors := make(map[string]bool)
	cursor := ""
	var readErr error
	for range meetingCaptionMaxPages {
		if readErr = ctx.Err(); readErr != nil {
			break
		}
		seenCursors[cursor] = true
		var page []meetingCaption
		page, cursor, readErr = reader.ReadMeetingCaptionPage(ctx, meetingID, cursor)
		captions = append(captions, page...)
		if readErr != nil || cursor == "" || seenCursors[cursor] {
			break
		}
	}
	result := mergeMeetingCaptions(captions, botSpeakerIDs...)
	result.Incomplete = cursor != "" || readErr != nil
	return result, readErr
}

// Merge older rows first so a newer fragment can supply its revised text while
// retaining identifiers and times that are absent in that fragment.
func mergeMeetingCaptions(rows []meetingCaption, botSpeakerIDs ...string) meetingCaptionResult {
	var captions []meetingCaption
	positions := make(map[meetingCaption]int)
	for _, caption := range rows {
		key := caption
		if caption.SentenceID != "" {
			key = meetingCaption{SentenceID: caption.SentenceID}
		}
		if index, exists := positions[key]; exists {
			previous := captions[index]
			if strings.TrimSpace(caption.Text) == "" {
				caption.Text = previous.Text
			}
			caption.SpeakerID = cmp.Or(caption.SpeakerID, previous.SpeakerID)
			caption.StartTime = cmp.Or(caption.StartTime, previous.StartTime)
			caption.EndTime = cmp.Or(caption.EndTime, previous.EndTime)
			captions[index] = caption
			continue
		}
		positions[key] = len(captions)
		captions = append(captions, caption)
	}
	captions = slices.DeleteFunc(captions, func(caption meetingCaption) bool {
		return strings.TrimSpace(caption.Text) == "" ||
			(caption.SpeakerID != "" && slices.Contains(botSpeakerIDs, caption.SpeakerID))
	})
	slices.SortStableFunc(captions, func(a, b meetingCaption) int {
		// Keep encounter order among equal sort keys, including missing times.
		startA, _ := strconv.ParseInt(cmp.Or(a.StartTime, a.EndTime), 10, 64)
		startB, _ := strconv.ParseInt(cmp.Or(b.StartTime, b.EndTime), 10, 64)
		return cmp.Compare(startA, startB)
	})
	retained, truncated := limitMeetingCaptions(captions)
	return meetingCaptionResult{Captions: retained, Truncated: truncated}
}

func limitMeetingCaptions(captions []meetingCaption) ([]meetingCaption, bool) {
	start := len(captions)
	remaining := meetingCaptionMaxTextBytes
	for start > 0 && len(captions)-start < meetingCaptionMaxSentences {
		caption := &captions[start-1]
		if len(caption.Text) > remaining {
			if start == len(captions) {
				// Preserve a usable prefix when the newest sentence alone exceeds the limit.
				for remaining > 0 && !utf8.RuneStart(caption.Text[remaining]) {
					remaining--
				}
				caption.Text = strings.Clone(caption.Text[:remaining])
				start--
			}
			return slices.Clone(captions[start:]), true
		}
		remaining -= len(caption.Text)
		start--
	}
	return slices.Clone(captions[start:]), start > 0
}
