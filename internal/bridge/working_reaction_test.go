package bridge

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestWorkingReactionLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name, reply, process, response string
		adds, deletes                  int
	}{
		{"markdown", "markdown", "off", `{"code":0,"data":{"reaction_id":"synthetic-reaction"}}`, 1, 1},
		{"scope unavailable", "text", "off", `{"code":99991672,"msg":"permission denied"}`, 1, 0},
		{"process card", "text", "stream", "", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, l, ch, x, f := setupAccess(t)
			replies := captureReplyModes(t, f)
			handleReply := f.extra
			const messageID = "om_working_fixture"
			const reactionPath = "/open-apis/im/v1/messages/" + messageID + "/reactions"
			var adds, deletes int
			f.extra = func(w http.ResponseWriter, r *http.Request) bool {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == reactionPath:
					adds++
					var body struct {
						Reaction struct {
							Emoji string `json:"emoji_type"`
						} `json:"reaction_type"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Reaction.Emoji != "Typing" {
						t.Errorf("unexpected typing reaction request: %#v, %v", body, err)
					}
					io.WriteString(w, tc.response)
					return true
				case r.Method == http.MethodDelete && r.URL.Path == reactionPath+"/synthetic-reaction":
					deletes++
					io.WriteString(w, `{"code":0}`)
					return true
				default:
					return handleReply(w, r)
				}
			}
			accessCommand(t, e, ch, "reaction-mode", "user", "chat", "/config reply "+tc.reply)
			accessCommand(t, e, ch, "reaction-process", "admin", "chat", "/config process "+tc.process)
			accept(t, e, Normalize(groupMessage(messageID, "user", "chat", "/nas 阅读资料", true)))
			tick(t, e)
			if adds != tc.adds || deletes != 0 {
				t.Fatalf("wrong reaction after dispatch: adds=%d deletes=%d", adds, deletes)
			}
			if err := e.Store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err := OpenStore(e.Config.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			e.Store, l.Store = store, store
			x.observation = Observation{Status: "completed", RunID: "run", Tasks: []string{"synthetic-pending"}}
			tick(t, e)
			if adds != tc.adds || deletes != 0 {
				t.Fatal("restart or pending child work changed the reaction")
			}
			x.observation = Observation{Status: "completed", RunID: "run", Summary: "完成"}
			tick(t, e)
			tick(t, e)
			finished := onlyTask(t, e)
			if adds != tc.adds || deletes != tc.deletes || finished.Phase != "completed" || finished.WorkingReaction != nil || finished.CardID == "" || len(replies[messageID]) == 0 {
				t.Fatalf("wrong reaction or final delivery: adds=%d deletes=%d task=%+v", adds, deletes, finished)
			}
		})
	}
}
