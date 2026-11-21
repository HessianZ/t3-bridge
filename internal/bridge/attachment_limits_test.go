package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	channel "github.com/larksuite/channel-sdk-go"
)

const attachmentLimitMiB = 1 << 20

type attachmentLimitDownloads struct {
	*fakeMessenger
	payloads map[string][]byte
	limits   []int
}

func (f *attachmentLimitDownloads) Image(_ context.Context, _ Message, im Image, limit int) ([]byte, error) {
	f.limits = append(f.limits, limit)
	return f.payloads[im.Key], nil
}

func (f *attachmentLimitDownloads) add(m *Message, kind string, payload []byte) {
	key := fmt.Sprintf("%s-%d", m.ID, len(m.Images)+len(m.Files))
	im := Image{Kind: kind, Key: key, Name: key + ".bin"}
	f.payloads[key] = payload
	if kind == "image" {
		m.Images = append(m.Images, im)
	} else {
		m.Files = append(m.Files, im)
	}
}

type attachmentLimitExecutor struct {
	*attachmentDispatchFixture
	api       *T3
	onDiscard func(string) error
}

func (f *attachmentLimitExecutor) Upload(_ context.Context, _ Target, im Image, payload []byte) (Attachment, error) {
	f.uploads++
	return Attachment{ID: im.Key, Type: im.Kind, Name: im.Name, MIME: http.DetectContentType(payload), Size: len(payload)}, nil
}

func (f *attachmentLimitExecutor) Send(ctx context.Context, target Target, task *Task) (Execution, error) {
	if f.api == nil {
		return f.fakeExecutor.Send(ctx, target, task)
	}
	f.sends++
	return f.api.Send(ctx, target, task)
}

func (f *attachmentLimitExecutor) Discard(_ context.Context, _ Target, id string) error {
	if f.onDiscard == nil {
		return nil
	}
	return f.onDiscard(id)
}

func TestAttachmentLimitsInboundCount(t *testing.T) {
	e, _, _, _ := testEngine(t)
	e.Config.Topics = nil
	lark := &Lark{Config: e.Config, Store: e.Store}
	for _, count := range []int{10, 11} {
		m := channel.NormalizedMessage{MessageID: fmt.Sprint(count), UserID: "user", ChatID: "chat", ChatType: "p2p", RawContentType: "post"}
		for i := 0; i < count; i++ {
			m.Resources = append(m.Resources, channel.Resource{Type: "file", FileKey: fmt.Sprint(i)})
		}
		if err := lark.receive(context.Background(), &m); err != nil {
			t.Fatal(err)
		}
	}
	inbox := e.Store.snapshot().Inbox
	if len(inbox) != 2 || len(inbox[0].Files) != 10 || inbox[0].AttachmentIssue != "" || len(inbox[1].Files) != 0 || inbox[1].AttachmentIssue == "" {
		t.Fatalf("unexpected ten/eleven-attachment ingress: %+v", inbox)
	}
}

func TestAttachmentLimitsQuotedCount(t *testing.T) {
	rows := []map[string]any{{"message_id": "forward", "msg_type": "merge_forward", "chat_id": "chat", "thread_id": "topic"}}
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("quoted-file-%d", i)
		rows = append(rows, map[string]any{
			"message_id": id, "upper_message_id": "forward", "msg_type": "file",
			"body": map[string]string{"content": fmt.Sprintf(`{"file_key":%q,"file_name":"report.txt"}`, id)},
		})
	}
	payload, err := json.Marshal(map[string]any{"code": 0, "data": map[string]any{"items": rows}})
	if err != nil {
		t.Fatal(err)
	}
	lark := quoteAPI(t, func(string) []byte { return payload })
	m := message("quote-limit", "检查引用资料")
	m.ParentID = "forward"
	resolved, err := lark.Resolve(context.Background(), m)
	if err != nil || len(resolved.Files) != 10 {
		t.Fatalf("ten quoted files were not resolved: files=%d err=%v", len(resolved.Files), err)
	}
	m.Images = []Image{{Key: "direct-image"}}
	if _, err := lark.Resolve(context.Background(), m); err == nil {
		t.Fatal("one direct image and ten quoted files exceeded the combined limit")
	}
}

func TestAttachmentLimitsNativeCount(t *testing.T) {
	for _, tc := range []struct {
		name, route, rejection string
		count                  int
		continuation, https    bool
	}{
		{name: "initial at limit", route: "nas", count: 8},
		{name: "initial over limit", route: "nas", count: 9, rejection: "8项"},
		{name: "local continuation", route: "nas", count: 8, continuation: true, rejection: "/new"},
		{name: "HTTPS initial", route: "nas", count: 8, https: true},
		{name: "HTTPS continuation", route: "nas", count: 8, continuation: true, https: true, rejection: "/new"},
		{name: "linked initial", route: "mac", count: 8, rejection: "关联 T3 环境"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, x, l, _ := testEngine(t)
			if tc.https {
				e.Config.MCP.Endpoint = "https://t3.example.invalid/mcp"
			}
			m := message("native-limit", "/"+tc.route+" 检查附件")
			var first *Task
			if tc.continuation {
				accept(t, e, message("native-first", "/"+tc.route+" 开始"))
				tick(t, e)
				first = onlyTask(t, e)
				m.ParentID = first.CardID
			}
			baseSends, baseOpens := x.sends, x.opens
			lark := &attachmentLimitDownloads{fakeMessenger: l, payloads: map[string][]byte{}}
			e.Lark = lark
			for i := 0; i < tc.count; i++ {
				lark.add(&m, "file", []byte("synthetic document"))
			}
			launches := 0
			e.T3 = &attachmentLimitExecutor{
				attachmentDispatchFixture: &attachmentDispatchFixture{fakeExecutor: x, launch: func(task *Task) (Execution, error) {
					launches++
					if task.ThreadID != "" || len(task.Attachments) != 8 {
						t.Fatalf("unexpected native launch: thread=%q attachments=%d", task.ThreadID, len(task.Attachments))
					}
					return Execution{ThreadID: "thread", RunID: "native-initial"}, nil
				}},
				onDiscard: func(id string) error {
					t.Fatalf("unexpected discard of %s", id)
					return nil
				},
			}
			accept(t, e, m)
			tick(t, e)
			if tc.continuation {
				if inbox := e.Store.snapshot().Inbox; len(inbox) != 1 || inbox[0].ID != m.ID || len(lark.limits) != 0 || x.uploads != 0 || x.sends != baseSends || launches != 0 {
					t.Fatal("attachments did not stay queued while the first run was active")
				}
				x.observation = Observation{Status: "completed", RunID: first.RunID, Summary: "已完成"}
				tick(t, e)
				tick(t, e)
			}
			task := onlyTask(t, e)
			if x.opens != baseOpens || x.sends != baseSends {
				t.Fatalf("attachment turn reached ordinary dispatch: opens=%d sends=%d", x.opens, x.sends)
			}
			if tc.rejection != "" {
				if task.Phase != "blocked" || !strings.Contains(task.Summary, tc.rejection) || len(lark.limits) != 0 || x.uploads != 0 || launches != 0 {
					t.Fatalf("attachment turn was not blocked before preparation: phase=%s summary=%q downloads=%v uploads=%d launches=%d", task.Phase, task.Summary, lark.limits, x.uploads, launches)
				}
				if first != nil && (task.ThreadID != first.ThreadID || task.DispatchSource.ID != m.ID) {
					t.Fatal("attachment rejection lost the quoted thread or message")
				}
				return
			}
			if task.Phase != "tracking" || len(lark.limits) != 8 || x.uploads != 8 || launches != 1 {
				t.Fatalf("eight attachments did not reach the native launch: phase=%s downloads=%v uploads=%d launches=%d", task.Phase, lark.limits, x.uploads, launches)
			}
		})
	}
}

func TestT3SendRejectsNativeAttachmentsBeforeMCP(t *testing.T) {
	c := testConfig(t)
	api := &T3{Config: c.MCP, Calls: scriptedCaller(func(name string, _ map[string]any) (any, error) {
		t.Fatalf("attachment send reached MCP tool %s", name)
		return nil, nil
	})}
	task := &Task{ThreadID: "thread", Attachments: []Attachment{{ID: "synthetic-receipt"}}}
	for _, route := range []string{"nas", "mac"} {
		execution, err := api.Send(context.Background(), c.Routes[route], task)
		if execution != (Execution{}) || err == nil || err.Error() != "T3 native attachments require a new thread launch" {
			t.Fatalf("unexpected attachment send result for %s: %+v, %v", route, execution, err)
		}
	}
}

func TestAttachmentLimitsCachedContinuation(t *testing.T) {
	const cachedImageBytes = 25 * attachmentLimitMiB
	imageData := make([]byte, cachedImageBytes+1)
	copy(imageData, "\x89PNG\r\n\x1a\n")
	for _, imageSize := range []int{cachedImageBytes, cachedImageBytes + 1} {
		t.Run(fmt.Sprint(imageSize), func(t *testing.T) {
			e, x, l, _ := testEngine(t)
			target := e.Config.Routes["nas"]
			target.CacheFollowupAttachments = true
			e.Config.Routes["nas"] = target
			accept(t, e, message("cache-limit-first", "/nas 开始"))
			tick(t, e)
			x.observation = Observation{Status: "completed", RunID: "run", Summary: "已完成"}
			tick(t, e)
			first := onlyTask(t, e)
			lark := &attachmentLimitDownloads{fakeMessenger: l, payloads: map[string][]byte{}}
			e.Lark = lark
			var refs []cachedFollowupAttachment
			api := &T3{Config: e.Config.MCP, Calls: scriptedCaller(func(name string, args map[string]any) (any, error) {
				if name != "t3_thread_send" || args["threadId"] != first.ThreadID || args["mode"] != "queue" || args["attachments"] != nil {
					t.Fatalf("unexpected cache submission: %s", name)
				}
				body := strings.TrimSpace(args["message"].(string))
				if !strings.Contains(body, "[本轮附件的本机私有缓存]") {
					t.Fatal("cache section missing from the submitted prompt")
				}
				if err := json.Unmarshal([]byte(body[strings.LastIndex(body, "\n")+1:]), &refs); err != nil {
					t.Fatalf("decode cache references: %v", err)
				}
				return map[string]string{"threadId": first.ThreadID, "runId": "cached-next"}, nil
			})}
			e.T3 = &attachmentLimitExecutor{attachmentDispatchFixture: &attachmentDispatchFixture{fakeExecutor: x}, api: api}
			at := e.now().Add(-time.Second)
			a := shortMessage("cache-limit-a", "检查这些资料", at)
			b := shortMessage("cache-limit-b", "以及这些资料", at)
			a.ParentID, b.ParentID = first.CardID, first.CardID
			lark.add(&a, "image", imageData[:imageSize])
			for _, batch := range []struct {
				m     *Message
				count int
			}{{&a, 4}, {&b, 5}} {
				for i := 0; i < batch.count; i++ {
					lark.add(batch.m, "file", []byte("synthetic document"))
				}
				accept(t, e, *batch.m)
			}
			if imageSize == cachedImageBytes {
				extra := shortMessage("cache-limit-extra", "最后一份资料", at)
				extra.ParentID = first.CardID
				lark.add(&extra, "file", []byte("next batch"))
				accept(t, e, extra)
			}
			tick(t, e)
			task := onlyTask(t, e)
			if imageSize > cachedImageBytes {
				if task.Phase != "blocked" || x.sends != 1 || x.uploads != 0 || !slices.Equal(lark.limits, []int{cachedImageBytes}) {
					t.Fatalf("oversize cached image reached submission: phase=%s sends=%d uploads=%d download limits=%v", task.Phase, x.sends, x.uploads, lark.limits)
				}
				return
			}
			if task.Phase != "tracking" || task.RunID != "cached-next" || x.sends != 2 || x.uploads != 0 || len(refs) != 10 || len(task.DispatchSource.Parts) != 2 || len(lark.limits) != 10 || lark.limits[0] != cachedImageBytes {
				t.Fatalf("ten merged attachments did not use the cache: phase=%s sends=%d uploads=%d refs=%d parts=%d download limits=%v", task.Phase, x.sends, x.uploads, len(refs), len(task.DispatchSource.Parts), lark.limits)
			}
			if inbox := e.Store.snapshot().Inbox; len(inbox) != 1 || inbox[0].ID != "cache-limit-extra" {
				t.Fatal("attachment eleven did not remain queued for the next turn")
			}
			dir := filepath.Join(e.Config.StateDir, followupCacheDir, task.RequestID)
			for i, ref := range refs {
				if filepath.Dir(ref.Path) != dir {
					t.Fatalf("cache reference is outside the request directory: %s", ref.Path)
				}
				info, err := os.Stat(ref.Path)
				if err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					if ref.Type != "image" || info.Size() != cachedImageBytes {
						t.Fatalf("cached image has type %q and size %d", ref.Type, info.Size())
					}
					file, err := os.Open(ref.Path)
					if err != nil {
						t.Fatal(err)
					}
					var signature [8]byte
					_, err = io.ReadFull(file, signature[:])
					file.Close()
					if err != nil || !slices.Equal(signature[:], imageData[:8]) {
						t.Fatalf("could not read the cached image: %x, %v", signature, err)
					}
				} else if ref.Type != "file" || info.Size() != int64(len("synthetic document")) || lark.limits[i] != 50*attachmentLimitMiB {
					t.Fatalf("cached file has type %q, size %d and download limit %d", ref.Type, info.Size(), lark.limits[i])
				}
			}
		})
	}
}

func TestAttachmentLimitsNativeBytes(t *testing.T) {
	const mib = attachmentLimitMiB
	// One backing slice is enough to exercise the byte budgets without retaining uploads.
	payload := make([]byte, 50*mib+1)
	for _, tc := range []struct {
		name, exceeded string
		imageBytes     int
		fileBytes      []int
		wantUploads    int
		keepReceipt    bool
	}{
		{name: "file and turn at limits", fileBytes: []int{50 * mib, 50 * mib}, wantUploads: 2},
		{name: "file over 50 MiB", fileBytes: []int{1, 50*mib + 1}, exceeded: "50 MiB", wantUploads: 1},
		{name: "turn over 100 MiB", fileBytes: []int{50 * mib, 50 * mib, 1}, exceeded: "100 MiB", wantUploads: 2, keepReceipt: true},
		{name: "image over 10 MiB", imageBytes: 10*mib + 1, exceeded: "10 MiB"},
		{name: "image MIME total at 80 MiB", imageBytes: 10 * mib, fileBytes: []int{50 * mib, 20 * mib}, wantUploads: 3},
		{name: "image MIME total over 80 MiB", imageBytes: 10 * mib, fileBytes: []int{50 * mib, 20*mib + 1}, exceeded: "80 MiB", wantUploads: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, x, l, _ := testEngine(t)
			lark := &attachmentLimitDownloads{fakeMessenger: l, payloads: map[string][]byte{}}
			e.Lark = lark
			m := message("byte-limit", "/nas 检查附件")
			clear(payload[:8])
			var wantLimits []int
			if tc.imageBytes != 0 {
				copy(payload, "\x89PNG\r\n\x1a\n")
				lark.add(&m, "image", payload[:tc.imageBytes])
				wantLimits = append(wantLimits, 10*mib)
			}
			// Mixed turns reuse the PNG payload for files to exercise MIME-based counting.
			for _, size := range tc.fileBytes {
				lark.add(&m, "file", payload[:size])
				wantLimits = append(wantLimits, 50*mib)
			}
			var discarded []string
			launches := 0
			var wantRetained []Attachment
			e.T3 = &attachmentLimitExecutor{
				attachmentDispatchFixture: &attachmentDispatchFixture{fakeExecutor: x, launch: func(task *Task) (Execution, error) {
					launches++
					if len(task.Attachments) != tc.wantUploads {
						t.Fatalf("launch has %d attachment receipts", len(task.Attachments))
					}
					return Execution{ThreadID: "thread", RunID: "native-bytes"}, nil
				}},
				onDiscard: func(id string) error {
					saved := onlyTask(t, e)
					if saved.Phase != "blocked" || len(saved.Attachments) != tc.wantUploads {
						t.Fatalf("discard began without saved receipts: phase=%s receipts=%v", saved.Phase, saved.Attachments)
					}
					discarded = append(discarded, id)
					if tc.keepReceipt && id == saved.Attachments[1].ID {
						wantRetained = append(wantRetained, saved.Attachments[1])
						return errors.New("synthetic unconfirmed discard")
					}
					return nil
				},
			}
			accept(t, e, m)
			tick(t, e)
			task := onlyTask(t, e)
			if x.uploads != tc.wantUploads || x.opens != 0 || x.sends != 0 || !slices.Equal(lark.limits, wantLimits) {
				t.Fatalf("unexpected preparation: uploads=%d opens=%d sends=%d download limits=%v", x.uploads, x.opens, x.sends, lark.limits)
			}
			if tc.exceeded == "" {
				if launches != 1 || len(discarded) != 0 || task.Phase != "tracking" {
					t.Fatalf("within-budget turn did not launch: phase=%s launches=%d discarded=%v", task.Phase, launches, discarded)
				}
				return
			}
			var wantDiscarded []string
			for _, im := range messageResources(m)[:tc.wantUploads] {
				wantDiscarded = append(wantDiscarded, im.Key)
			}
			if launches != 0 || task.Phase != "blocked" || !strings.Contains(task.Summary, tc.exceeded) || !slices.Equal(discarded, wantDiscarded) {
				t.Fatalf("over-budget turn was not cleanly blocked: phase=%s launches=%d discarded=%v summary=%q", task.Phase, launches, discarded, task.Summary)
			}
			if (tc.keepReceipt && len(wantRetained) != 1) || !slices.Equal(task.Attachments, wantRetained) {
				t.Fatalf("unexpected receipts after discard: got %v, want %v", task.Attachments, wantRetained)
			}
		})
	}
}
