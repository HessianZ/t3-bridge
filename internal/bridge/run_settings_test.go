package bridge

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalRunSettingsSharesFeishuState(t *testing.T) {
	ctx := context.Background()
	e, _, lark, _ := testEngine(t)
	e.Config.Admins = []string{"user"}
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	defaults := RunSettingsView{Progress: true, ReplyMode: "card", ToolActivity: "default", ProcessMode: "off"}
	if view, err := e.LocalRunSettings(ctx, RunSettingsPatch{}); err != nil || view != defaults {
		t.Fatalf("defaults = %+v, err = %v", view, err)
	}

	maxConcurrent, idleMinutes := 16, 120
	progress := false
	replyMode, tools, process := "text", "detailed", "brief"
	view, err := e.LocalRunSettings(ctx, RunSettingsPatch{
		MaxConcurrent: &maxConcurrent, IdleTimeoutMinutes: &idleMinutes, Progress: &progress,
		ReplyMode: &replyMode, ToolActivity: &tools, ProcessMode: &process,
	})
	want := RunSettingsView{MaxConcurrent: 16, IdleTimeoutMinutes: 120, ReplyMode: "text", ToolActivity: "detailed", ProcessMode: "brief"}
	if err != nil || view != want {
		t.Fatalf("saved settings = %+v, err = %v, want %+v", view, err, want)
	}
	progress = true // A patch's pointer must not become mutable Store state.
	accept(t, e, message("query-local", "/config reply"))
	tick(t, e)
	if len(lark.replies) != 1 || !strings.Contains(lark.replies[0], "当前回复模式：text") {
		t.Fatalf("Feishu did not read the local write: %v", lark.replies)
	}

	setProcess := message("set-process", "/config process card")
	accept(t, e, setProcess)
	idleMinutes, tools = 0, "default"
	localWrite := make(chan error, 1)
	go func() {
		_, err := e.LocalRunSettings(ctx, RunSettingsPatch{IdleTimeoutMinutes: &idleMinutes, ToolActivity: &tools})
		localWrite <- err
	}()
	tick(t, e)
	if err := <-localWrite; err != nil {
		t.Fatal(err)
	}
	want.IdleTimeoutMinutes, want.ToolActivity, want.ProcessMode = 0, "default", "card"
	if view, err := e.LocalRunSettings(ctx, RunSettingsPatch{}); err != nil || view != want {
		t.Fatalf("merged settings = %+v, err = %v, want %+v", view, err, want)
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
	if view, err := e.LocalRunSettings(ctx, RunSettingsPatch{}); err != nil || view != want {
		t.Fatalf("settings after reopen = %+v, err = %v, want %+v", view, err, want)
	}
	if cmd := store.snapshot().Commands[key(setProcess, setProcess.ID)]; cmd == nil ||
		!strings.Contains(cmd.Reply, "已保存过程显示：card。适用于后续飞书聊天轮次。") {
		t.Fatalf("Feishu command receipt after reopen = %+v", cmd)
	}
}

func TestLocalRunSettingsRejectsInvalidAndFailedWrites(t *testing.T) {
	ctx := context.Background()
	e, _, lark, _ := testEngine(t)
	e.Config.Admins = []string{"user"}
	n := 3
	patch := RunSettingsPatch{MaxConcurrent: &n}
	if _, err := e.LocalRunSettings(ctx, patch); err == nil {
		t.Fatal("an unbound store accepted a write")
	}
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	other := &Engine{Config: Config{AppID: "other-app"}, Store: e.Store}
	if _, err := other.LocalRunSettings(ctx, RunSettingsPatch{}); err == nil {
		t.Fatal("another App could read the store")
	}
	if _, err := other.LocalRunSettings(ctx, patch); err == nil {
		t.Fatal("another App could write the store")
	}
	tooMany, tooLong := MaxRunConcurrency+1, 121
	empty, unsupported, stream, card := "", "default", "stream", "card"
	for _, invalid := range []RunSettingsPatch{
		{MaxConcurrent: &tooMany}, {MaxConcurrent: &n, IdleTimeoutMinutes: &tooLong},
		{ReplyMode: &empty}, {ToolActivity: &empty}, {ToolActivity: &stream}, {ToolActivity: &card}, {ProcessMode: &unsupported},
	} {
		if _, err := e.LocalRunSettings(ctx, invalid); err == nil {
			t.Fatalf("invalid patch accepted: %+v", invalid)
		}
	}

	setProcess := message("failed-process", "/config process brief")
	accept(t, e, setProcess)
	originalDir := e.Store.dir
	e.Store.dir = filepath.Join(t.TempDir(), "missing")
	defer func() { e.Store.dir = originalDir }()
	if _, err := e.LocalRunSettings(ctx, patch); err == nil {
		t.Fatal("expected local persistence failure")
	}
	if err := e.Tick(ctx); err == nil {
		t.Fatal("expected Feishu persistence failure")
	}
	want := RunSettingsView{Progress: true, ReplyMode: "card", ToolActivity: "default", ProcessMode: "off"}
	if view, err := e.LocalRunSettings(ctx, RunSettingsPatch{}); err != nil || view != want {
		t.Fatalf("read after failed writes = %+v, err = %v, want %+v", view, err, want)
	}
	if st := e.Store.snapshot(); st.Commands[key(setProcess, setProcess.ID)] != nil || len(st.Inbox) != 1 || len(lark.replies) != 0 {
		t.Fatal("failed Feishu setting write recorded or sent a success reply, or lost the pending command")
	}
}
