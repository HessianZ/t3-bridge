package bridge

import (
	"errors"
	"io"
	"net/http"
	"testing"
)

func TestMeetingSummaryVerifiesCurrentOwnerBeforeCreatingMessage(t *testing.T) {
	_, l, _, _, f := setupAccess(t)
	l.RefreshOwner(t.Context())
	if l.Config.ownerID() != "user" {
		t.Fatal("fixture did not load cached owner")
	}
	posts := 0
	f.extra = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/open-apis/im/v1/messages" {
			return false
		}
		posts++
		io.WriteString(w, `{"code":0,"data":{"message_id":"synthetic-summary"}}`)
		return true
	}
	f.owner = "new-owner"
	var rejected *meetingRejection
	if id, err := l.SendMeetingSummary(t.Context(), "open_id", "user", "summary-uuid", "fixture notes"); id != "" || !errors.As(err, &rejected) || posts != 0 {
		t.Fatalf("owner transfer allowed stale delivery: id=%q err=%v posts=%d", id, err, posts)
	}
	f.ownerFail = true
	if id, err := l.SendMeetingSummary(t.Context(), "open_id", "user", "summary-uuid", "fixture notes"); id != "" || !errors.Is(err, errMeetingUnconfirmed) || posts != 0 {
		t.Fatalf("owner lookup failure allowed delivery: id=%q err=%v posts=%d", id, err, posts)
	}
	f.owner, f.ownerFail = "user", false
	if id, err := l.SendMeetingSummary(t.Context(), "open_id", "user", "summary-uuid", "fixture notes"); id != "synthetic-summary" || err != nil || posts != 1 {
		t.Fatalf("verified delivery: id=%q err=%v posts=%d", id, err, posts)
	}
}
