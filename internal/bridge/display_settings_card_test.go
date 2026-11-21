package bridge

import (
	"strings"
	"testing"
)

func TestDisplaySettingsCardInTextModeAndSingleUse(t *testing.T) {
	e, l, ch, x, f := setupAccess(t)
	e.Config.Users = append(e.Config.Users, "admin")
	l.Config.Users = e.Config.Users
	replies := captureReplyModes(t, f)
	accessCommand(t, e, ch, "text-mode", "user", "chat", "/config reply text")
	accessCommand(t, e, ch, "group-display", "admin", "chat", displaySettingsCommand)
	if got := replies["group-display"]; len(got) != 1 || got[0].MsgType != "text" || !strings.Contains(got[0].Content, "已授权私聊") {
		t.Fatalf("group display exposed a settings card: %+v", got)
	}
	m := message("display-card", displaySettingsCommand)
	m.ChatType, m.UserID = "p2p", "admin"
	accept(t, e, m)
	tick(t, e)
	replyID := key(m, m.ID)
	cmd := e.Store.snapshot().Commands[replyID]
	if got := replies[m.ID]; cmd == nil || cmd.Card == nil || cmd.Kind != displaySettingsCommand || cmd.RouteID != "" ||
		len(got) != 1 || got[0].MsgType != "interactive" || !strings.Contains(got[0].Content, runReplyCommand) ||
		!strings.Contains(got[0].Content, "独立卡正文") || !strings.Contains(got[0].Content, "正文＋简略工具") ||
		!strings.Contains(got[0].Content, "正文＋详细工具") || !strings.Contains(got[0].Content, "card 在结果卡预览，需开启进度并使用卡片回复") {
		t.Fatalf("explicit settings card unavailable in text mode: %+v", got)
	}
	action := commandCardClick(t, cmd, replies[m.ID][0].Content, "终态 Markdown", "display-click")
	other := action
	other.Operator.OpenID = "user"
	clickCommandCard(t, l, other)
	if len(e.Store.snapshot().Inbox) != 0 {
		t.Fatal("another owner was accepted on the original administrator's card")
	}
	clickCommandCard(t, l, action)
	if err := e.Store.update(func(st *State) error {
		st.AccessPolicy.Admins = map[string]bool{"admin": false}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.publishPolicy()
	tick(t, e)
	if st := e.Store.snapshot(); st.Settings.ReplyMode != "text" || st.Commands[replyID].Card.Used {
		t.Fatal("revoked administrator applied or consumed a queued selection")
	}
	if err := e.Store.update(func(st *State) error { st.AccessPolicy.Admins = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	e.publishPolicy()
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e.Store, l.Store = store, store
	action.EventID = "display-accepted"
	clickCommandCard(t, l, action)
	tick(t, e)
	if st := store.snapshot(); st.Settings.ReplyMode != "markdown" || !st.Commands[replyID].Card.Used || st.Settings.ProcessMode != "" {
		t.Fatal("selection did not update only its field and consume the card")
	}
	accessCommand(t, e, ch, "new-value", "user", "chat", "/config reply card")
	action.EventID = "display-replayed"
	clickCommandCard(t, l, action)
	tick(t, e)
	if store.runSettings().ReplyMode != "card" || x.sends != 0 {
		t.Fatal("used card overwrote a later change or started a run")
	}

	m.ID = "display-process-card"
	accept(t, e, m)
	tick(t, e)
	replyID = key(m, m.ID)
	cmd = store.snapshot().Commands[replyID]
	got := replies[m.ID]
	if cmd == nil || cmd.Card == nil || len(got) != 1 || got[0].MsgType != "interactive" {
		t.Fatalf("process settings card unavailable: %+v", got)
	}
	action = commandCardClick(t, cmd, got[0].Content, "本卡正文", "select-card-preview")
	clickCommandCard(t, l, action)
	tick(t, e)
	if st := store.snapshot(); st.Settings.ProcessMode != processCard || st.Settings.ReplyMode != "card" || !st.Commands[replyID].Card.Used || x.sends != 0 {
		t.Fatal("selecting 本卡正文 did not save only the process mode and consume the card")
	}
}
