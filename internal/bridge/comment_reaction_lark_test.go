package bridge

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	channel "github.com/larksuite/channel-sdk-go"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

func TestOfficialLarkSDKCommentWorkingReaction(t *testing.T) {
	var calls atomic.Int32
	actions := []string{"add", "delete"}
	f := newLoopbackLark(t, func(w http.ResponseWriter, r *http.Request) bool {
		i := int(calls.Add(1)) - 1
		if i >= len(actions) {
			t.Error("unexpected comment reaction request")
			w.WriteHeader(http.StatusBadRequest)
			return true
		}
		if r.Method != http.MethodPost || r.URL.Path != "/open-apis/drive/v2/files/docFake/comments/reaction" ||
			r.URL.RawQuery != "file_type=docx" || r.Header.Get("Authorization") != "Bearer synthetic-feishu-token" {
			t.Errorf("unexpected comment reaction request: %s %s", r.Method, r.URL.RequestURI())
		}
		want := map[string]string{"reply_id": "reply-1", "reaction_type": "Typing", "action": actions[i]}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		} else if !maps.Equal(body, want) {
			t.Errorf("reaction body = %v, want %v", body, want)
		}
		if actions[i] == "add" {
			io.WriteString(w, `{"code":1,"msg":"synthetic rejection"}`)
		} else {
			io.WriteString(w, `{"code":0}`)
		}
		return true
	})
	sdk, err := channel.New("synthetic-comment-reaction-app", "synthetic-secret", channel.WithDomain(f.url),
		channel.WithEnableTokenCache(true), channel.WithReqTimeout(3*time.Second), channel.WithLogLevel(larkcore.LogLevelError))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sdk.Stop(context.Background()) })
	ctx := context.Background()
	l := &Lark{Channel: sdk}
	l.updateCommentWorkingReaction(ctx, CommentSource{FileToken: "docFake", FileType: "docx", CommentID: "thread-1", ReplyID: "reply-1"}, true)
	l.updateCommentWorkingReaction(ctx, CommentSource{FileToken: "docFake", FileType: "docx", ReplyID: "reply-1"}, false)
	for _, ref := range []CommentSource{
		{FileToken: "docFake", FileType: "wiki", ReplyID: "reply-1"},
		{FileToken: "doc/Fake", FileType: "docx", ReplyID: "reply-1"},
		{FileType: "docx", ReplyID: "reply-1"},
		{FileToken: "docFake", FileType: "docx"},
		{FileToken: "docFake", FileType: "docx", ReplyID: "reply 1"},
		{FileToken: "docFake", FileType: "docx", CommentID: "thread/1", ReplyID: "reply-1"},
	} {
		l.updateCommentWorkingReaction(ctx, ref, true)
	}
	if got := int(calls.Load()); got != len(actions) {
		t.Fatalf("reaction calls = %d, want %d", got, len(actions))
	}
}
