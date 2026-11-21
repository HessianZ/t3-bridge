package bridge

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

type meetingCaptionReadFunc func(context.Context, string, string) ([]meetingCaption, string, error)

func (read meetingCaptionReadFunc) ReadMeetingCaptionPage(ctx context.Context, meetingID, cursor string) ([]meetingCaption, string, error) {
	return read(ctx, meetingID, cursor)
}

func TestReadMeetingCaptionsMergesPages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const meetingID = "9000000000000000001"
	noID := meetingCaption{SpeakerID: "human", Text: "收到", StartTime: "9", EndTime: "10"}
	repeatedLater := meetingCaption{SpeakerID: "human", Text: "收到", StartTime: "12", EndTime: "13"}
	unknownSpeaker := meetingCaption{Text: "未知发言人", StartTime: "14"}
	var cursors []string
	reader := meetingCaptionReadFunc(func(gotCtx context.Context, id, cursor string) ([]meetingCaption, string, error) {
		if gotCtx != ctx || id != meetingID {
			t.Fatal("reader received a different context or meeting ID")
		}
		cursors = append(cursors, cursor)
		switch cursor {
		case "":
			return []meetingCaption{
				{SentenceID: "updated", SpeakerID: "human", Text: "准备", StartTime: "10"},
				{SentenceID: "speaker-corrected", Text: "机器人发言", StartTime: "11"},
				noID,
			}, "next", nil
		case "next":
			return []meetingCaption{
				{SentenceID: "updated", Text: "准备就绪", EndTime: "11"},
				{SentenceID: "updated", Text: "  "},
				{SentenceID: "speaker-corrected", SpeakerID: "bot"},
				{SpeakerID: "other-bot", Text: "另一条机器人发言"},
				{SpeakerID: "human", Text: "\n "},
				noID, repeatedLater, unknownSpeaker,
			}, "", nil
		default:
			t.Fatalf("unexpected cursor %q", cursor)
			return nil, "", nil
		}
	})
	result, err := readMeetingCaptions(ctx, reader, meetingID, "bot", "other-bot", "")
	want := []meetingCaption{
		noID,
		{SentenceID: "updated", SpeakerID: "human", Text: "准备就绪", StartTime: "10", EndTime: "11"},
		repeatedLater,
		unknownSpeaker,
	}
	if err != nil || result.Incomplete || result.Truncated || !slices.Equal(result.Captions, want) {
		t.Fatalf("result = %+v, error = %v, want captions %+v", result, err, want)
	}
	if !slices.Equal(cursors, []string{"", "next"}) {
		t.Fatalf("requested cursors = %q", cursors)
	}
}

func TestReadMeetingCaptionsPageStops(t *testing.T) {
	readFailure := errors.New("synthetic read failure")
	for _, tc := range []struct {
		name         string
		next         []string
		failAt       int
		cancelAt     int
		empty        bool
		wantErr      error
		wantPages    int
		wantCaptions int
		incomplete   bool
	}{
		{name: "complete empty", next: []string{""}, empty: true, wantPages: 1},
		{name: "empty pages with a cursor", next: []string{"a", ""}, empty: true, wantPages: 2},
		{name: "repeated cursor", next: []string{"a", "a"}, wantPages: 2, wantCaptions: 2, incomplete: true},
		{name: "cursor cycle", next: []string{"a", "b", "a"}, wantPages: 3, wantCaptions: 3, incomplete: true},
		{name: "page limit", next: []string{"1", "2", "3", "4", "5", "6", "7", "8"}, wantPages: 8, wantCaptions: 8, incomplete: true},
		{name: "ends at page limit", next: []string{"1", "2", "3", "4", "5", "6", "7", ""}, wantPages: 8, wantCaptions: 8},
		{name: "read failure without captions on first page", next: []string{""}, empty: true, failAt: 1, wantPages: 1, wantErr: readFailure, incomplete: true},
		{name: "a failed cursor can still return usable captions", next: []string{"a", ""}, failAt: 2, wantPages: 2, wantCaptions: 2, wantErr: readFailure, incomplete: true},
		{name: "canceled between pages", next: []string{"a"}, cancelAt: 1, wantPages: 1, wantCaptions: 1, wantErr: context.Canceled, incomplete: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			reader := meetingCaptionReadFunc(func(_ context.Context, _, cursor string) ([]meetingCaption, string, error) {
				if calls == len(tc.next) || (calls != 0 && cursor != tc.next[calls-1]) {
					t.Fatalf("unexpected cursor %q on request %d", cursor, calls+1)
				}
				calls++
				var page []meetingCaption
				if !tc.empty {
					page = []meetingCaption{{Text: strconv.Itoa(calls)}}
				}
				if calls == tc.failAt {
					return page, "", readFailure
				}
				if calls == tc.cancelAt {
					cancel()
				}
				return page, tc.next[calls-1], nil
			})
			result, err := readMeetingCaptions(ctx, reader, "9000000000000000001")
			if !errors.Is(err, tc.wantErr) || calls != tc.wantPages || len(result.Captions) != tc.wantCaptions || result.Incomplete != tc.incomplete || result.Truncated {
				t.Fatalf("result = %+v, error = %v, calls = %d", result, err, calls)
			}
		})
	}
}

func TestReadMeetingCaptionsOutputLimits(t *testing.T) {
	t.Run("recent sentences", func(t *testing.T) {
		var page []meetingCaption
		for i := 32; i > 0; i-- {
			page = append(page, meetingCaption{Text: strconv.Itoa(i), StartTime: strconv.Itoa(i)})
		}
		reader := meetingCaptionReadFunc(func(context.Context, string, string) ([]meetingCaption, string, error) {
			return page, "", nil
		})
		result, err := readMeetingCaptions(context.Background(), reader, "9000000000000000001")
		if err != nil || result.Incomplete || !result.Truncated || len(result.Captions) != 30 {
			t.Fatalf("result = %+v, error = %v", result, err)
		}
		for i, caption := range result.Captions {
			if caption.Text != strconv.Itoa(i+3) {
				t.Fatalf("caption %d = %+v", i, caption)
			}
		}
	})
	t.Run("total text bytes", func(t *testing.T) {
		text := strings.Repeat("字", 1000)
		reader := meetingCaptionReadFunc(func(context.Context, string, string) ([]meetingCaption, string, error) {
			return []meetingCaption{{Text: text, StartTime: "1"}, {Text: text, StartTime: "2"}, {Text: text, StartTime: "3"}}, "", nil
		})
		result, err := readMeetingCaptions(context.Background(), reader, "9000000000000000001")
		if err != nil || result.Incomplete || !result.Truncated || len(result.Captions) != 2 ||
			result.Captions[0].StartTime != "2" || result.Captions[1].StartTime != "3" ||
			len(result.Captions[0].Text)+len(result.Captions[1].Text) != meetingCaptionMaxTextBytes {
			t.Fatalf("expected exactly the two newest sentences within the byte limit, got %d captions, error = %v", len(result.Captions), err)
		}
	})
	t.Run("oversized latest sentence", func(t *testing.T) {
		text := "a" + strings.Repeat("字", 2000)
		reader := meetingCaptionReadFunc(func(context.Context, string, string) ([]meetingCaption, string, error) {
			return []meetingCaption{{Text: "older"}, {Text: text}}, "", nil
		})
		result, err := readMeetingCaptions(context.Background(), reader, "9000000000000000001")
		if err != nil || result.Incomplete || !result.Truncated || len(result.Captions) != 1 {
			t.Fatalf("expected one shortened caption, got %d captions, error = %v", len(result.Captions), err)
		}
		got := result.Captions[0].Text
		if !utf8.ValidString(got) || len(got) != meetingCaptionMaxTextBytes-2 || !strings.HasPrefix(text, got) {
			t.Fatalf("expected a valid UTF-8 prefix within the byte limit, got %d bytes", len(got))
		}
	})
}
