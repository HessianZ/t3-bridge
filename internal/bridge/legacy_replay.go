package bridge

import (
	"context"
	"slices"
	"time"
)

type legacyReplayThreadReader interface {
	readLegacyReplayEvidence(context.Context, Target, string, Message, string) (legacyGroupEvidence, error)
	verifyLegacyGroupEvidence(context.Context, Target, string, string) error
}

func legacyReplayInput(m Message) bool {
	return (m.ChatType == "p2p" || m.ChatType == "group") && validLegacySessionScopeID(m.ID, "om_") &&
		m.Comment == nil && m.Meeting == nil && m.MeetingInvite == nil && m.MeetingSummary == nil && m.EventID == "" &&
		m.ControlCard == "" && m.ControlTask == "" && m.CommandAction == "" && m.SessionTask == "" &&
		slices.Contains([]string{"", "text", "post", "image", "file", "merge_forward"}, m.InputKind)
}

type legacyReplayRestore struct {
	taskID, runID, currentID string
}

func legacyReplayTask(t *Task, appID string) bool {
	return t != nil && t.LegacySessionPath != "" && t.ThreadID != "" && t.RunID != "" &&
		terminal(t.Phase) && !t.NeedsReconcile && !t.StopRequested &&
		(t.ReceiverAppID == "" || t.ReceiverAppID == appID)
}

func restoredLegacyReplayTasks(state *State) map[string]legacyReplayRestore {
	restored := make(map[string]legacyReplayRestore)
	for id, task := range state.Tasks {
		if !legacyReplayTask(task, state.AccountID) {
			continue
		}
		scope := sessionKey(task.Source, task.RouteID, task.Target)
		currentID := state.Current[scope]
		if id != currentID {
			current := state.Tasks[currentID]
			if current == nil || current.ThreadID == "" || current.ThreadID == task.ThreadID || current.LegacySessionPath != "" ||
				current.Source.ChatType != task.Source.ChatType || sessionKey(current.Source, current.RouteID, current.Target) != scope {
				continue
			}
			// A separate current thread leaves multiple old threads ambiguous.
			if _, exists := restored[scope]; exists {
				restored[scope] = legacyReplayRestore{}
				continue
			}
		}
		restored[scope] = legacyReplayRestore{taskID: id, runID: task.RunID, currentID: currentID}
	}
	return restored
}

// Resolve evidence before coalescing so a replay cannot consume the seed or join
// a newer input. Unknown IDs still go through normal routing and transfer checks.
func (e *Engine) suppressLegacyReplay(ctx context.Context, inbound *State, m Message) (bool, error) {
	if inbound.AccountID != e.Config.AppID || !legacyReplayInput(m) {
		return false, nil
	}
	thread, threadOK := e.T3.(legacyReplayThreadReader)
	feishu, feishuOK := e.Lark.(legacyGroupFeishuReader)
	if !threadOK || !feishuOK {
		return false, nil
	}
	if e.legacyReplayRestored == nil {
		// Capture the resumed mappings before this receiver handles new inputs.
		e.legacyReplayRestored = restoredLegacyReplayTasks(inbound)
	}
	if len(inbound.LegacySessions) == 0 && len(e.legacyReplayRestored) == 0 {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	chatType := ""
	if m.ChatType == "group" {
		chatType = m.ChatType
	}
	var botID string
	for route := range e.Config.Routes {
		target := effectiveTarget(e.Config, inbound, m, route)
		scope := sessionKey(m, route, target)
		currentID := inbound.Current[scope]
		seed, pending := inbound.LegacySessions[scope]
		var restored *Task
		if pending {
			if seed.ReplayChecked || seed.ChatType != chatType || currentID != "" {
				continue
			}
		} else {
			restore := e.legacyReplayRestored[scope]
			task := inbound.Tasks[restore.taskID]
			if restore.taskID == "" || currentID != restore.currentID || !legacyReplayTask(task, inbound.AccountID) || task.RunID != restore.runID ||
				task.Source.ChatType != m.ChatType || !scopedTask(e.Config, m, task, route) ||
				targetIdentity(task.Target) != targetIdentity(target) {
				continue
			}
			restored = task
			seed = legacySessionSeed{ThreadID: task.ThreadID, ProjectPath: task.LegacySessionPath}
		}
		if !sameWorkspacePath(seed.ProjectPath, target.ProjectPath) || !e.Config.TargetAllowed(m, target, route) {
			continue
		}
		if botID == "" {
			var err error
			botID, err = feishu.legacyGroupBotID(ctx)
			if err != nil {
				return false, nil
			}
		}
		proof, err := thread.readLegacyReplayEvidence(ctx, target, seed.ThreadID, m, botID)
		if err != nil {
			continue
		}
		verified := make(map[string]bool, len(proof.MessageIDs))
		for _, id := range proof.MessageIDs {
			if ctx.Err() != nil {
				return false, nil
			}
			if feishu.verifyLegacyGroupMessages(ctx, m, []string{id}) == nil {
				verified[id] = true
			}
		}
		if err := thread.verifyLegacyGroupEvidence(ctx, target, seed.ThreadID, proof.Revision); err != nil {
			continue
		}
		removed, err := e.Store.recordLegacyReplayEvidence(m, scope, currentID, seed, restored, verified)
		if restored != nil && err == nil {
			delete(e.legacyReplayRestored, scope)
		}
		if removed || err != nil {
			return removed, err
		}
	}
	return false, nil
}

func (s *Store) recordLegacyReplayEvidence(m Message, scope, currentID string, seed legacySessionSeed, restored *Task, verified map[string]bool) (bool, error) {
	removed := false
	err := s.update(func(st *State) error {
		if st.Current[scope] != currentID {
			return nil
		}
		if restored == nil {
			if st.LegacySessions[scope] != seed {
				return nil
			}
			seed.ReplayChecked = true
			st.LegacySessions[scope] = seed
		} else {
			task := st.Tasks[restored.ID]
			if !legacyReplayTask(task, st.AccountID) || task.ID != restored.ID || task.RunID != restored.RunID ||
				task.ThreadID != seed.ThreadID || task.LegacySessionPath != seed.ProjectPath ||
				targetIdentity(task.Target) != targetIdentity(restored.Target) {
				return nil
			}
		}
		for id := range verified {
			st.Seen[key(m, id)] = true
			st.Seen[inboundSeenKey(m, id)] = true
		}
		st.Inbox = slices.DeleteFunc(st.Inbox, func(q Message) bool {
			if !verified[q.ID] || q.ChatType != m.ChatType || q.ChatID != m.ChatID || q.TopicID != m.TopicID ||
				q.UserID != m.UserID || !legacyReplayInput(q) {
				return false
			}
			removed = removed || q.ID == m.ID
			return true
		})
		return nil
	})
	return removed, err
}
