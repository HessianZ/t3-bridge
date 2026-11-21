package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type cacheFollowupFixture struct{ *fakeMessenger }

func (f cacheFollowupFixture) Image(_ context.Context, _ Message, im Image, _ int) ([]byte, error) {
	if im.Kind == "file" {
		return []byte("synthetic report"), nil
	}
	var pngBytes bytes.Buffer
	err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	return pngBytes.Bytes(), err
}

func TestCachedFollowupUsesQueueAndKeepsFilesOnUnconfirmedSend(t *testing.T) {
	e, x, l, _ := testEngine(t)
	e.Lark = cacheFollowupFixture{l}
	target := e.Config.Routes["nas"]
	oldIdentity := targetIdentity(target)
	target.CacheFollowupAttachments = true
	if targetIdentity(target) != oldIdentity {
		t.Fatal("cache preference changed the thread identity")
	}
	e.Config.Routes["nas"] = target
	var sends int
	var refs []cachedFollowupAttachment
	api := &T3{Config: e.Config.MCP, Calls: scriptedCaller(func(name string, args map[string]any) (any, error) {
		if name != "t3_thread_send" || args["mode"] != "queue" || args["threadId"] != "thread" || args["clientRequestId"] == "" || args["attachments"] != nil {
			t.Fatalf("unexpected attachment dispatch: %s %v", name, args)
		}
		sends++
		if sends == 1 {
			return map[string]string{"threadId": "thread", "runId": "first"}, nil
		}
		body := strings.TrimSpace(args["message"].(string))
		lines := strings.Split(body, "\n")
		if !strings.Contains(body, "[本轮附件的本机私有缓存]") || json.Unmarshal([]byte(lines[len(lines)-1]), &refs) != nil {
			t.Fatal("queued prompt omitted the private cache references")
		}
		return nil, errors.New("synthetic lost queued reply")
	})}
	e.T3 = &p1aNativeExecutor{fakeExecutor: x, api: api}
	accept(t, e, message("cache-first", "/nas 开始"))
	tick(t, e)
	x.observation = Observation{Status: "completed", RunID: "first", Summary: "已完成"}
	tick(t, e)
	m := message("cache-next", "读取这两份资料")
	m.ParentID = onlyTask(t, e).CardID
	m.Files = []Image{{Kind: "file", Key: "report", Name: `..\report.txt`}}
	m.Images = []Image{{Key: "image", Name: "image.png"}}
	accept(t, e, m)
	tick(t, e)
	before := onlyTask(t, e)
	if sends != 2 || len(refs) != 2 || before.Phase != "needs_reconcile" || len(before.Attachments) != 0 {
		t.Fatalf("unexpected cache send state: sends=%d refs=%d phase=%s", sends, len(refs), before.Phase)
	}
	dir := filepath.Join(e.Config.StateDir, followupCacheDir, before.RequestID)
	for _, ref := range refs {
		if filepath.Dir(ref.Path) != dir {
			t.Fatal("cache path escaped the request directory")
		}
		data, err := os.ReadFile(ref.Path)
		if err != nil || len(data) == 0 || (ref.Type == "file" && string(data) != "synthetic report") ||
			(ref.Type == "image" && !bytes.HasPrefix(data, []byte("\x89PNG"))) {
			t.Fatalf("simulated executor could not read the attachment: %s, %v", ref.Name, err)
		}
	}
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e.Store = store
	x.observation = Observation{Status: "queued", RunID: "queued", FoundRequest: true}
	tick(t, e)
	after := onlyTask(t, e)
	if after.RequestID != before.RequestID || after.RequestMessage != before.RequestMessage || after.RunID != "queued" || sends != 2 {
		t.Fatal("queued cache task did not recover the original prompt and run")
	}
}

func TestCachedFollowupUsesCurrentRouteForExistingThread(t *testing.T) {
	e, x, l, _ := testEngine(t)
	e.Lark = cacheFollowupFixture{l}
	accept(t, e, message("cache-toggle-first", "/nas 开始"))
	tick(t, e)
	x.observation.Status = "completed"
	tick(t, e)
	first := onlyTask(t, e)
	if first.Target.CacheFollowupAttachments || first.CardID == "" || first.ThreadID == "" {
		t.Fatal("first turn must establish a thread and card with caching disabled")
	}

	target := e.Config.Routes["nas"]
	target.CacheFollowupAttachments = true
	e.Config.Routes["nas"] = target
	reply := message("cache-toggle-enabled", "读取图片")
	reply.ParentID = first.CardID
	reply.Images = []Image{{Key: "synthetic-image", Name: "image.png"}}
	accept(t, e, reply)
	tick(t, e)
	cached := onlyTask(t, e)
	if cached.Phase != "tracking" || cached.ThreadID != first.ThreadID || cached.CardID != first.CardID || !cached.Target.CacheFollowupAttachments ||
		x.sends != 2 || x.uploads != 0 || x.opens != 1 || !strings.Contains(x.request, "[本轮附件的本机私有缓存]") {
		t.Fatalf("quoted reply did not adopt caching on the same thread: phase=%s summary=%q sends=%d uploads=%d", cached.Phase, cached.Summary, x.sends, x.uploads)
	}
	root := filepath.Join(e.Config.StateDir, followupCacheDir)
	if data, err := os.ReadFile(filepath.Join(root, cached.RequestID, "01.png")); err != nil || !bytes.HasPrefix(data, []byte("\x89PNG")) {
		t.Fatalf("enabled reply did not write its private image: %v", err)
	}
	tick(t, e)

	reply.ID = "cache-toggle-disabled"
	accept(t, e, reply)
	// Persist a ready turn while the old route is still in effect.
	inbox := e.Store.snapshot().Inbox
	if len(inbox) != 1 || inbox[0].ID != reply.ID {
		t.Fatal("fixture did not queue the next quoted reply")
	}
	if err := e.consume(context.Background(), inbox[0]); err != nil {
		t.Fatal(err)
	}
	pending := onlyTask(t, e)
	if pending.Phase != "ready" || !pending.Target.CacheFollowupAttachments || pending.ThreadID != first.ThreadID || pending.DispatchSource.ID != reply.ID {
		t.Fatalf("fixture did not save a continuation with caching enabled: phase=%s summary=%q", pending.Phase, pending.Summary)
	}
	target.CacheFollowupAttachments = false
	e.Config.Routes["nas"] = target
	tick(t, e)
	blocked := onlyTask(t, e)
	if blocked.Phase != "blocked" || blocked.RequestID != pending.RequestID || !strings.Contains(blocked.Summary, "/new") || x.sends != 2 || x.uploads != 0 || x.opens != 1 {
		t.Fatalf("disabled route did not block the saved continuation: phase=%s summary=%q sends=%d uploads=%d", blocked.Phase, blocked.Summary, x.sends, x.uploads)
	}
	if _, err := os.Stat(filepath.Join(root, pending.RequestID)); !os.IsNotExist(err) {
		t.Fatalf("disabled continuation created a cache directory: %v", err)
	}
}

func TestCachedFollowupRetentionAndLinkedRouteValidation(t *testing.T) {
	e, _, _, _ := testEngine(t)
	remote := e.Config.Routes["mac"]
	remote.CacheFollowupAttachments = true
	e.Config.Routes["mac"] = remote
	if err := e.Config.Validate(); err == nil || !strings.Contains(err.Error(), "cacheFollowupAttachments") {
		t.Fatal("linked route accepted the local attachment cache", err)
	}
	active, finished := stable("active-cache"), stable("finished-cache")
	for _, id := range []string{active, finished} {
		file, err := e.cacheFollowupAttachment(id, 0, Image{Kind: "file", Name: "readme.txt"}, []byte("fixture"))
		if err != nil {
			t.Fatal(err)
		}
		old := e.now().Add(-25 * time.Hour)
		if err := os.Chtimes(filepath.Dir(file.Path), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Store.update(func(st *State) error {
		st.Tasks["unknown-cache"] = &Task{ID: "unknown-cache", RequestID: active, Phase: "stopped", NeedsReconcile: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.pruneFollowupAttachments()
	root := filepath.Join(e.Config.StateDir, followupCacheDir)
	if _, err := os.Stat(filepath.Join(root, active)); err != nil {
		t.Fatal("cleanup removed an unconfirmed request", err)
	}
	if _, err := os.Stat(filepath.Join(root, finished)); !os.IsNotExist(err) {
		t.Fatal("cleanup retained expired inactive request", err)
	}
}
