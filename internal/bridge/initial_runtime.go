package bridge

import (
	"errors"
	"fmt"
	"maps"
	"slices"
)

// InitialRuntimeConfig supplies one-time settings for a new receiver state.
type InitialRuntimeConfig struct {
	Settings       RunSettingsPatch `json:"settings,omitempty"`
	RequireMention *bool            `json:"requireMention,omitempty"`
	GroupMentions  map[string]bool  `json:"groupMentions,omitempty"`
}

func (c InitialRuntimeConfig) validate(chats []string) error {
	if err := c.Settings.validate(); err != nil {
		return fmt.Errorf("initialRuntime.settings: %w", err)
	}
	for chat := range c.GroupMentions {
		if !groupChatIDPattern.MatchString(chat) || !slices.Contains(chats, chat) {
			return errors.New("initialRuntime.groupMentions requires explicit oc_ group IDs in chats")
		}
	}
	return nil
}

// BindReceiver binds the validated account and records its first startup in the same write.
func (s *Store) BindReceiver(c Config) error {
	return s.update(func(st *State) error {
		initialize := c.InitialRuntime != nil && st.canInitializeRuntime()
		if err := st.bindAccount(c.AppID, false); err != nil {
			return err
		}
		if initialize {
			c.InitialRuntime.Settings.apply(&st.Settings)
			if required := c.InitialRuntime.RequireMention; required != nil {
				value := *required
				st.GroupAccess.RequireMention = &value
			}
			st.GroupAccess.Mentions = maps.Clone(c.InitialRuntime.GroupMentions)
		}
		st.ReceiverStarted = true
		return nil
	})
}

func (st *State) canInitializeRuntime() bool {
	// Offline imports can bind the account before startup and leave only aliases
	// or pending session transfers. Older receivers also left an account binding.
	if st.ReceiverStarted || (st.AccountID != "" && len(st.WorkspaceNames) == 0 && len(st.LegacySessions) == 0) {
		return false
	}
	return st.Settings == (RunSettings{}) && st.GroupAccess.RequireMention == nil &&
		len(st.GroupAccess.Chats) == 0 && len(st.GroupAccess.Mentions) == 0 &&
		len(st.AccessPolicy.Users) == 0 && len(st.AccessPolicy.Admins) == 0 && len(st.AccessPolicy.Routes) == 0 &&
		len(st.Timeouts) == 0 && len(st.IdleTimeouts) == 0 && len(st.Workspaces) == 0 &&
		len(st.Current) == 0 && len(st.Selected) == 0 && len(st.Commands) == 0 && len(st.Meetings) == 0 &&
		len(st.Inbox) == 0 && len(st.Seen) == 0 && len(st.Bindings) == 0 && len(st.Tasks) == 0 &&
		st.PendingReconnect == "" && len(st.ReconnectRuns) == 0 && st.ReconnectStatus == "" &&
		len(st.Events) == 0 && len(st.EventRecords) == 0 && len(st.RegisteredEvents) == 0 &&
		len(st.AutoNodes) == 0 && len(st.AutoEvents) == 0 && len(st.AutoIncidents) == 0 &&
		len(st.RecoveryFloors) == 0 && len(st.HealthGaps) == 0 &&
		len(st.ReportSubmissions) == 0 && len(st.ReportCache) == 0 && len(st.ReportGaps) == 0 && st.ReportIssue == "" &&
		len(st.ReportCopyGaps) == 0 && len(st.ReportCopyStatus) == 0 && st.FollowupIssue == "" && st.FollowupStatus == ""
}
