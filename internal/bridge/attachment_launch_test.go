package bridge

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type attachmentDispatchFixture struct {
	*fakeExecutor
	expectedID string
	launch     func(*Task) (Execution, error)
	recover    func(*Task) (Execution, error)
}

func (f *attachmentDispatchFixture) attachmentLaunchThreadID(string) string { return f.expectedID }
func (f *attachmentDispatchFixture) launchWithAttachments(_ context.Context, _ Target, task *Task) (Execution, error) {
	return f.launch(task)
}
func (f *attachmentDispatchFixture) recoverAttachmentLaunch(_ context.Context, _ Target, task *Task) (Execution, error) {
	return f.recover(task)
}

func TestAttachmentDispatchDoesNotReplaceOldUnconfirmedOpening(t *testing.T) {
	e, executor, _, _ := testEngine(t)
	target := e.Config.Routes["nas"]
	source := message("old-opening", "/nas 阅读附件")
	source.Files = []Image{{Key: "file-key", Kind: "file"}}
	task := &Task{
		ID: "old-task", Phase: "opening", RouteID: "nas", Target: target, OpenRequestID: "original-open",
		BaseTargetID: targetIdentity(target), ReceiverAppID: e.Config.AppID, Source: source, DispatchSource: source,
	}
	if err := e.Store.update(func(st *State) error {
		st.Tasks[task.ID] = task
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.advance(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	got := e.Store.task(task.ID)
	if got.Phase != "blocked" || got.OpenRequestID != "original-open" || !strings.Contains(got.Summary, "/new") ||
		executor.preflights != 0 || executor.opens != 0 || executor.uploads != 0 || executor.sends != 0 {
		t.Fatalf("previous opening was submitted as a fresh attachment launch: %+v", got)
	}
}

func TestAttachmentDispatchConfirmsInitialRunAndKeepsTextContinuation(t *testing.T) {
	e, x, _, _ := testEngine(t)
	var launches int
	e.T3 = &attachmentDispatchFixture{fakeExecutor: x, launch: func(task *Task) (Execution, error) {
		launches++
		saved := e.Store.task(task.ID)
		if saved == nil || saved.AttachmentLaunch == nil || saved.RequestMessage != task.RequestMessage || saved.Phase != "dispatching" ||
			saved.DispatchStartedAt.IsZero() || len(saved.Attachments) != 1 || saved.ThreadID != "" {
			t.Fatal("launch occurred before the durable request and attachment receipt")
		}
		return Execution{ThreadID: "thread", RunID: "run"}, nil
	}}
	m := message("first-attachment", "/nas 阅读附件")
	m.Images = []Image{{Key: "image-key"}}
	accept(t, e, m)
	tick(t, e)
	first := onlyTask(t, e)
	if launches != 1 || x.opens != 0 || x.uploads != 1 || x.sends != 0 || first.Phase != "tracking" || first.RunID != "run" {
		t.Fatalf("first request did not use a single native launch: %+v", first)
	}
	x.observation = Observation{Status: "completed", RunID: "run", Summary: "已读取"}
	tick(t, e)
	m = message("next-text", "/nas 补充问题")
	m.ParentID = first.CardID
	accept(t, e, m)
	tick(t, e)
	second := onlyTask(t, e)
	if launches != 1 || x.sends != 1 || x.opens != 0 || second.AttachmentLaunch != nil || second.ThreadID != first.ThreadID ||
		second.RequestID == first.RequestID || !strings.Contains(x.request, "补充问题") {
		t.Fatalf("text continuation did not reuse the ordinary thread send: %+v", second)
	}
}

func TestAttachmentDispatchOnlyRecoversOriginalLaunchAfterRestart(t *testing.T) {
	e, x, _, _ := testEngine(t)
	var launches, reads int
	f := &attachmentDispatchFixture{fakeExecutor: x, expectedID: "original-thread", launch: func(*Task) (Execution, error) {
		launches++
		return Execution{}, errors.New("synthetic lost response")
	}}
	e.T3 = f
	m := message("lost-attachment-ack", "/nas 阅读附件")
	m.Files = []Image{{Key: "file-key", Kind: "file"}}
	accept(t, e, m)
	tick(t, e)
	before := onlyTask(t, e)
	if before.Phase != "needs_reconcile" || !before.NeedsReconcile || before.AttachmentLaunch == nil || before.ThreadID != "" {
		t.Fatalf("lost reply was not kept pending: %+v", before)
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
	f.expectedID = "different-oauth-session"
	f.recover = func(task *Task) (Execution, error) {
		reads++
		if task.AttachmentLaunch.ExpectedThreadID != "original-thread" || task.RequestID != before.RequestID || task.RequestMessage != before.RequestMessage {
			t.Fatal("recovery switched launch identity")
		}
		if reads == 1 {
			return Execution{}, errors.New("synthetic recovery still pending")
		}
		return Execution{ThreadID: "original-thread", RunID: "original-run"}, nil
	}
	if err := store.update(func(s *State) error {
		s.Tasks[before.ID].StopRequested = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	tick(t, e)
	if t1 := onlyTask(t, e); x.stops != 0 || !t1.StopRequested || !t1.NeedsReconcile || t1.StopSent {
		t.Fatal("stop was attempted or lost before recovery confirmed the original run")
	}
	tick(t, e)
	tick(t, e)
	after := onlyTask(t, e)
	if launches != 1 || x.opens != 0 || x.sends != 0 || x.uploads != 1 || reads != 2 || x.stops != 1 ||
		after.Phase != "stopped" || after.ThreadID != "original-thread" || after.RunID != "original-run" || after.NeedsReconcile {
		t.Fatalf("restart lost or replayed the attachment launch: %+v", after)
	}
}
