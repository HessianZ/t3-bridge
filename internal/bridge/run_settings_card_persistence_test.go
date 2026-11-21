package bridge

import (
	"context"
	"errors"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestRunSettingsCardPublishesRuntimePolicyWithDurableState(t *testing.T) {
	for _, tc := range []struct {
		name      string
		published bool
	}{
		{"before-publication", false},
		{"after-publication", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, executor, messenger, _ := testEngine(t)
			e.Lark = commandCardRecorder{messenger}
			e.Config.Admins = []string{"user"}
			e.Config.access = &accessRuntime{}
			open := message("open", "/config run")
			open.ChatType = "p2p"
			accept(t, e, open)
			tick(t, e)
			openKey := key(open, open.ID)
			origin := e.Store.snapshot().Commands[openKey]
			if origin == nil || origin.Card == nil || origin.Card.ReplyID == "" {
				t.Fatalf("missing settings card: %+v", origin)
			}
			n, mention := 4, false
			submit := Message{
				ID: "card:submit", ChatID: open.ChatID, TopicID: open.TopicID, ChatType: open.ChatType,
				UserID: open.UserID, ParentID: origin.Card.ReplyID, CommandAction: commandButtonID(origin, 0),
				RunSettingsForm: &RunSettingsFormSubmission{Settings: RunSettingsPatch{MaxConcurrent: &n}, RequireMention: &mention},
			}
			accept(t, e, submit)
			failure := errors.New("synthetic state publication failure")
			e.Store.writeState = func(path string, data []byte, createOnly bool) (bool, error) {
				if !tc.published {
					return false, failure
				}
				published, err := localfile.WritePrivateAtomic(path, data, createOnly)
				return published, errors.Join(err, failure)
			}
			if handled, err := e.sessionControl(context.Background(), submit); !handled || !errors.Is(err, failure) {
				t.Fatalf("expected injected failure, handled=%v, error=%v", handled, err)
			}
			state := e.Store.snapshot()
			disk, err := readStateSnapshot(e.Config.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			for _, view := range []State{state, disk} {
				if (view.Settings.MaxConcurrent == n) != tc.published || view.GroupAccess.defaultRequiresMention() == tc.published ||
					view.Commands[openKey].Card.Used != tc.published || (view.Commands[key(submit, submit.ID)] != nil) != tc.published {
					t.Fatal("settings, group policy, card use and receipt did not publish together")
				}
			}
			if e.Config.requireMention("oc_other") == tc.published || executor.sends != 0 || len(messenger.replies) != 1 {
				t.Fatal("runtime group policy or reply escaped the publication boundary")
			}
		})
	}
}
