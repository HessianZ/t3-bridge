package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

type Message struct {
	routeNames map[string][]string

	EventInherited  bool                   `json:"eventInherited,omitempty"`
	UserMentions    map[string]string      `json:"userMentions,omitempty"`
	FormRequestID   string                 `json:"formRequestId,omitempty"`
	RouteAtReceive  string                 `json:"routeAtReceive,omitempty"`
	ProjectChoices  map[string]Workspace   `json:"projectChoices,omitempty"`
	ReceivedAt      string                 `json:"receivedAt,omitempty"`
	Cancelled       bool                   `json:"cancelled,omitempty"`
	Parts           []Message              `json:"parts,omitempty"`
	MergedIDs       []string               `json:"mergedIds,omitempty"`
	InputKind       string                 `json:"inputKind,omitempty"`
	History         string                 `json:"history,omitempty"`
	Comment         *CommentSource         `json:"comment,omitempty"`
	Meeting         *meetingSource         `json:"meeting,omitempty"`
	MeetingInvite   *pendingMeetingInvite  `json:"meetingInvite,omitempty"`
	MeetingSummary  *meetingSummaryRequest `json:"meetingSummary,omitempty"`
	ID              string                 `json:"id"`
	ChatID          string                 `json:"chatId"`
	ChatType        string                 `json:"chatType,omitempty"`
	TopicID         string                 `json:"topicId,omitempty"`
	UserID          string                 `json:"userId"`
	Text            string                 `json:"text"`
	RoutingText     string                 `json:"routingText,omitempty"`
	Quote           string                 `json:"quote,omitempty"`
	ParentID        string                 `json:"parentId,omitempty"`
	RootID          string                 `json:"rootId,omitempty"`
	Images          []Image                `json:"images,omitempty"`
	Files           []Image                `json:"files,omitempty"`
	AttachmentIssue string                 `json:"attachmentIssue,omitempty"`
	EventID         string                 `json:"eventId,omitempty"`
	EventRoute      string                 `json:"eventRoute,omitempty"`
	EventNode       string                 `json:"eventNode,omitempty"`
	EventCheck      string                 `json:"eventCheck,omitempty"`
	ControlTask     string                 `json:"controlTask,omitempty"`
	ControlCard     string                 `json:"controlCard,omitempty"`
	CommandAction   string                 `json:"commandAction,omitempty"`
	SessionTask     string                 `json:"sessionTask,omitempty"`

	RunSettingsForm *RunSettingsFormSubmission `json:"runSettingsForm,omitempty"`
	TimeoutSettings *timeoutSettingsSubmission `json:"timeoutSettings,omitempty"`
}
type Image struct {
	MessageID string `json:"messageId,omitempty"`
	Key       string `json:"key"`
	Name      string `json:"name,omitempty"`
	Kind      string `json:"kind,omitempty"`
}
type Attachment struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
	MIME string `json:"mimeType"`
	Size int    `json:"sizeBytes"`
}

type Task struct {
	OpenRequestID         string       `json:"openRequestId,omitempty"`
	EventContext          string       `json:"eventContext,omitempty"`
	ReportInstructions    string       `json:"reportInstructions,omitempty"`
	MaintenanceNotice     string       `json:"maintenanceNotice,omitempty"`
	Report                *AgentReport `json:"report,omitempty"`
	ReportIssue           string       `json:"reportIssue,omitempty"`
	ReceiverAppID         string       `json:"receiverAppId,omitempty"`
	LegacySessionPath     string       `json:"legacySessionPath,omitempty"`
	TimeoutSeconds        int          `json:"timeoutSeconds,omitempty"`
	DeadlineAt            string       `json:"deadlineAt,omitempty"`
	TimeoutExpired        bool         `json:"timeoutExpired,omitempty"`
	IdleTimeoutMinutes    int          `json:"idleTimeoutMinutes,omitempty"`
	IdleTimeoutExpired    bool         `json:"idleTimeoutExpired,omitempty"`
	InputOpen             bool         `json:"inputOpen,omitempty"`
	FormEcho              string       `json:"formEcho,omitempty"`
	BaseTargetID          string       `json:"baseTargetId,omitempty"`
	ID                    string       `json:"id"`
	Source                Message      `json:"source"`
	RouteID               string       `json:"routeId,omitempty"`
	Target                Target       `json:"target"`
	Phase                 string       `json:"phase"`
	Summary               string       `json:"summary"`
	ThreadID              string       `json:"threadId,omitempty"`
	Link                  string       `json:"link,omitempty"`
	RunID                 string       `json:"runId,omitempty"`
	ResultRunID           string       `json:"resultRunId,omitempty"`
	PendingApprovals      int          `json:"pendingApprovals,omitempty"`
	RunActivity           *runActivity `json:"runActivity,omitempty"`
	ChildThreads          []string     `json:"childThreads,omitempty"`
	TaskIDs               []string     `json:"taskIds,omitempty"`
	CardID                string       `json:"cardId,omitempty"`    // Reply receipt for IM and document comments.
	TextReply             bool         `json:"textReply,omitempty"` // Use a single terminal reply. Legacy rounds use cards.
	MarkdownReply         bool         `json:"markdownReply,omitempty"`
	TextReplyBody         string       `json:"textReplyBody,omitempty"` // Retained until the send is confirmed.
	CardAttemptAt         string       `json:"cardAttemptAt,omitempty"`
	NotificationIssue     string       `json:"notificationIssue,omitempty"`
	NotifyHash            string       `json:"notifyHash,omitempty"`
	FinalReplyHash        string       `json:"finalReplyHash,omitempty"`
	RequestID             string       `json:"requestId,omitempty"`
	DispatchStartedAt     time.Time    `json:"dispatchStartedAt,omitzero"`
	Pending               *Message     `json:"pending,omitempty"`
	Attachments           []Attachment `json:"attachments,omitempty"`
	RequestMessage        string       `json:"requestMessage,omitempty"`
	StopRequested         bool         `json:"stopRequested,omitempty"`
	Updated               string       `json:"updated,omitempty"`
	DispatchSource        Message      `json:"dispatchSource"`
	StopSent              bool         `json:"stopSent,omitempty"`
	StopBackgroundPending bool         `json:"stopBackgroundPending,omitempty"`
	StopSettledRequest    string       `json:"stopSettledRequest,omitempty"`
	NeedsReconcile        bool         `json:"needsReconcile,omitempty"`
	ReplySource           *Message     `json:"replySource,omitempty"`
	CardRequestID         string       `json:"cardRequestId,omitempty"`
	Result                string       `json:"result,omitempty"`
	ResultIssue           string       `json:"resultIssue,omitempty"`
	Replies               []ReplyRound `json:"replies,omitempty"`

	ThreadMeta *ThreadMetadata `json:"threadMeta,omitempty"`
	Process    *processMessage `json:"process,omitempty"`

	CardPreviewEnabled bool   `json:"cardPreviewEnabled,omitempty"`
	CardPreview        string `json:"cardPreview,omitempty"`

	AttachmentLaunch *attachmentLaunchReceipt `json:"attachmentLaunch,omitempty"`
	WorkingReaction  *workingReactionReceipt  `json:"workingReaction,omitempty"`
	CommentReaction  *CommentSource           `json:"commentReaction,omitempty"`

	LegacyGroupSeed *legacySessionSeed `json:"legacyGroupSeed,omitempty"`
}
type State struct {
	ReportCopyGaps    map[string]string               `json:"reportCopyGaps,omitempty"`
	ReportCopyStatus  map[string]string               `json:"reportCopyStatus,omitempty"`
	FollowupIssue     string                          `json:"followupIssue,omitempty"`
	FollowupStatus    string                          `json:"followupStatus,omitempty"`
	AutoNodes         map[string]string               `json:"autoNodes,omitempty"`
	AutoEvents        map[string]string               `json:"autoEvents,omitempty"`
	AutoIncidents     map[string]string               `json:"autoIncidents,omitempty"`
	RegisteredEvents  map[string]bool                 `json:"registeredEvents,omitempty"`
	ReportSubmissions map[string]*ReportSubmission    `json:"reportSubmissions,omitempty"`
	ReportCache       map[string][]AgentReport        `json:"reportCache,omitempty"`
	ReportGaps        map[string]string               `json:"reportGaps,omitempty"`
	AccountID         string                          `json:"accountId,omitempty"`
	ReceiverStarted   bool                            `json:"receiverStarted,omitempty"`
	AccessPolicy      AccessPolicy                    `json:"accessPolicy,omitempty"`
	Settings          RunSettings                     `json:"settings,omitempty"`
	Timeouts          map[string]int                  `json:"timeouts,omitempty"`
	IdleTimeouts      map[string]int                  `json:"idleTimeouts,omitempty"`
	Workspaces        map[string]Workspace            `json:"workspaces,omitempty"`
	WorkspaceNames    map[string]map[string]Workspace `json:"workspaceNames,omitempty"`
	Version           int                             `json:"version"`
	GroupAccess       GroupAccess                     `json:"groupAccess,omitempty"`
	Current           map[string]string               `json:"current,omitempty"`
	LegacySessions    map[string]legacySessionSeed    `json:"legacySessions,omitempty"`
	Selected          map[string]string               `json:"selected,omitempty"`
	Commands          map[string]*SessionCommand      `json:"commands,omitempty"`
	Meetings          map[string]meetingRecord        `json:"meetings,omitempty"`
	EarlyMeetingEnds  map[string][]string             `json:"earlyMeetingEnds,omitempty"`
	PendingReconnect  string                          `json:"pendingReconnect,omitempty"`
	ReconnectRuns     map[string]string               `json:"reconnectRuns,omitempty"`
	ReconnectStatus   string                          `json:"reconnectStatus,omitempty"`
	EventRecords      map[string]Event                `json:"eventRecords"`
	ReportIssue       string                          `json:"reportIssue,omitempty"`
	HealthGaps        map[string]string               `json:"healthGaps,omitempty"`
	RecoveryFloors    map[string]string               `json:"recoveryFloors"`
	Inbox             []Message                       `json:"inbox"`
	Seen              map[string]bool                 `json:"seen"`
	Bindings          map[string]string               `json:"bindings"`
	Tasks             map[string]*Task                `json:"tasks"`
	Events            map[string]string               `json:"events"`
}
type Store struct {
	mu         sync.Mutex
	state      State
	dir        string
	lock       *os.File
	writeState func(string, []byte, bool) (bool, error)
	// Recent captions are process memory. The same Store survives client reconnects.
	meetingCaptions map[string]meetingCaptionWindow
}

func stateLockPath(dir string) string { return filepath.Join(dir, "lock") }

func OpenStore(dir string) (*Store, error) {
	if runtime.GOOS == "windows" {
		if err := localfile.MkdirAllPrivate(dir); err != nil {
			return nil, err
		}
	} else {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		if err := os.Chmod(dir, 0700); err != nil {
			return nil, err
		}
	}
	f, err := localfile.Lock(stateLockPath(dir), false)
	if errors.Is(err, localfile.ErrLocked) {
		return nil, errors.New("state already opened by another bridge")
	}
	if err != nil {
		return nil, err
	}
	s := &Store{dir: dir, lock: f, state: State{Version: 1, EventRecords: map[string]Event{}, HealthGaps: map[string]string{}, RecoveryFloors: map[string]string{}, Seen: map[string]bool{}, Bindings: map[string]string{}, Tasks: map[string]*Task{}, Events: map[string]string{}}}
	stateFile, err := localfile.OpenRegular(filepath.Join(dir, "state.json"))
	if err == nil {
		data, readErr := io.ReadAll(stateFile)
		err = errors.Join(readErr, stateFile.Close())
		if err == nil {
			err = json.Unmarshal(data, &s.state)
		}
		if err == nil && (s.state.Version != 1 || s.state.Seen == nil || s.state.Tasks == nil || s.state.Bindings == nil || s.state.Events == nil) {
			err = errors.New("unsupported state")
		}
	}
	if err != nil && !os.IsNotExist(err) {
		s.Close()
		return nil, errors.New("cannot load durable state; refusing replay")
	}
	if s.state.EventRecords == nil {
		s.state.EventRecords = map[string]Event{}
	}
	if s.state.HealthGaps == nil {
		s.state.HealthGaps = map[string]string{}
	}
	if s.state.RecoveryFloors == nil {
		s.state.RecoveryFloors = map[string]string{}
	}
	return s, nil
}
func (s *Store) Close() error { return s.lock.Close() }

// readStateSnapshot reads a published state file without modifying its directory.
// Callers requiring an offline view must hold the state lock.
func readStateSnapshot(dir string) (State, error) {
	file, err := localfile.OpenRegular(filepath.Join(dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	defer file.Close()
	var state State
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&state); err != nil || decoder.Decode(new(json.RawMessage)) != io.EOF || state.Version != 1 ||
		state.Seen == nil || state.Tasks == nil || state.Bindings == nil || state.Events == nil {
		return State{}, errors.New("cannot load durable state snapshot")
	}
	return state, nil
}

func (s *Store) save() (bool, error) {
	b, err := json.Marshal(s.state)
	if err != nil {
		return false, err
	}
	write := s.writeState
	if write == nil {
		write = localfile.WritePrivateAtomic
	}
	return write(filepath.Join(s.dir, "state.json"), b, false)
}
func key(m Message, id string) string { return m.ChatID + "\x00" + m.TopicID + "\x00" + id }

func inboundSeenKey(m Message, id string) string {
	if m.ChatType == "group" && m.ControlCard == "" {
		// Feishu may replay the same message with different topic metadata.
		return key(Message{ChatID: m.ChatID}, id)
	}
	return key(m, id)
}

func stable(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}
func (s *Store) Accept(c Config, m Message) error {
	if !c.Allowed(m) {
		if m.CommandAction != "" {
			return nil
		}
		return errors.New("message denied by ACL")
	}
	if m.ID == "" || m.UserID == "" || m.ChatID == "" || len(m.Text) > 24000 || len(m.Images)+len(m.Files) > maxTurnAttachments {
		return errors.New("message outside bounds")
	}
	return s.update(func(st *State) error {
		k := key(m, m.ID)
		seenKey := inboundSeenKey(m, m.ID)
		if st.Seen[k] || st.Seen[seenKey] {
			return nil
		}
		if m.ControlCard != "" {
			if t := st.Tasks[m.ControlTask]; t != nil && t.TextReply {
				return nil
			}
		}
		if m.InputKind == "form" {
			t := st.Tasks[m.ControlTask]
			if t == nil || !t.InputOpen || t.RequestID != m.FormRequestID || t.CardID != m.ControlCard || t.Source.UserID != m.UserID || t.Source.ChatID != m.ChatID || t.Source.TopicID != m.TopicID || !c.TargetAllowed(m, t.Target, t.RouteID) {
				return nil
			}
			t.InputOpen = false
			t.FormEcho = display(m.Text, 1000)
			t.NotifyHash = ""
			t.NotificationIssue = ""
		}
		if len(st.Inbox) >= 256 && (m.Meeting == nil || !m.Meeting.Room || m.InputKind != meetingRoomStop) {
			return errors.New("inbox full")
		}
		if m.Meeting != nil && m.Meeting.Room {
			acceptMeetingRoom(st, c, m)
			return nil
		}
		if !immediate(c, st, m) {
			m.RouteAtReceive = selectedRoute(c, st, m)
			choices := map[string]Workspace{}
			for route := range c.Routes {
				t := effectiveTarget(c, st, m, route)
				choices[workspaceKey(c, m, route)] = Workspace{t.ProjectID, t.ProjectName, t.ProjectPath}
			}
			m.ProjectChoices = choices
		}
		bound := boundTask(st, m)
		if m.ParentID == "" && m.ControlTask == "" && m.SessionTask == "" {
			bound = currentTask(c, st, m, "")
		}
		reset := strings.TrimSpace(routeText(m)) == "/new" || strings.TrimSpace(routeText(m)) == "/reset"
		if isStop(c, routeText(m), bound) || (reset && bound != nil && !terminal(bound.Phase)) {
			if t := bound; t != nil && c.TargetAllowed(m, t.Target, t.RouteID) && t.Source.UserID == m.UserID && currentStopReference(t, m) {
				t.StopRequested = true
			}
		}
		validBound := bound != nil && c.TargetAllowed(m, bound.Target, bound.RouteID) && bound.Source.UserID == m.UserID && currentStopReference(bound, m)
		unboundControl := bound == nil && m.ParentID == "" && m.ControlTask == "" && c.TargetAllowed(m, c.Routes[selectedRoute(c, st, m)], selectedRoute(c, st, m))
		if (reset || isStop(c, routeText(m), bound)) && (validBound || unboundControl) {
			cancelQueued(c, st, m, bound)
		}
		st.Seen[k] = true
		st.Seen[seenKey] = true
		st.Inbox = append(st.Inbox, m)
		return nil
	})
}

func (s *Store) update(fn func(*State) error) error {
	_, err := s.updatePublished(fn)
	return err
}

// No network calls run under this lock. Once the file is published, memory
// follows it even if syncing the directory fails.
func (s *Store) updatePublished(fn func(*State) error) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, err := json.Marshal(s.state)
	if err != nil {
		return false, err
	}
	var previous State
	if err = json.Unmarshal(old, &previous); err != nil {
		return false, err
	}
	if err := fn(&s.state); err != nil {
		s.state = previous
		return false, err
	}
	published, err := s.save()
	if !published {
		s.state = previous
	}
	return published, err
}

// Inspect only scheduling flags under the lock; do not copy or hash retained answers.
func (s *Store) hasImmediateWork(c Config) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	waitingToReconnect := s.state.PendingReconnect != ""
	for _, m := range s.state.Inbox {
		if !waitingToReconnect || immediate(c, &s.state, m) {
			return true
		}
	}
	for _, t := range s.state.Tasks {
		if waitingToReconnect && !t.StopRequested && !terminal(t.Phase) && !reconnectTaskInFlight(t) {
			continue
		}
		stopPending := t.StopRequested && !t.StopSent && t.Phase != "stopped" && t.Phase != "failed"
		if t.Phase == "ready" || stopPending || (t.NotifyHash == "" && t.NotificationIssue == "" &&
			((!t.TextReply && replySource(t).Comment == nil) || terminal(t.Phase))) {
			return true
		}
	}
	return false
}

// Clone only the requested task; callers still re-read it after network work.
func (s *Store) task(id string) *Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(s.state.Tasks[id])
	var task *Task
	_ = json.Unmarshal(b, &task)
	return task
}

func (s *Store) snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(s.state)
	var st State
	_ = json.Unmarshal(b, &st)
	return st
}
func boundTask(st *State, m Message) *Task {
	if m.SessionTask != "" {
		t := st.Tasks[m.SessionTask]
		if t != nil && t.Source.UserID == m.UserID && t.Source.ChatID == m.ChatID && t.Source.TopicID == m.TopicID {
			return t
		}
		return nil
	}
	if m.ControlTask != "" {
		t := st.Tasks[m.ControlTask]
		if t != nil && !t.TextReply && t.CardID == m.ControlCard && t.Source.ChatID == m.ChatID && t.Source.TopicID == m.TopicID {
			return t
		}
		return nil
	}
	for _, id := range []string{m.ParentID, m.RootID} {
		if id != "" {
			if t := st.Tasks[st.Bindings[key(m, id)]]; t != nil {
				return t
			}
		}
	}
	if m.EventID != "" {
		keys := []string{eventBindingKey(m, m.EventID), m.EventID}
		if ev, ok := st.EventRecords[m.EventID]; ok {
			keys = append([]string{eventBindingKey(m, incidentKey(ev)), incidentKey(ev)}, keys...)
		}
		for _, k := range keys {
			if t := st.Tasks[st.Events[k]]; t != nil && t.Source.UserID == m.UserID && t.Source.ChatID == m.ChatID && t.Source.TopicID == m.TopicID {
				return t
			}
		}
	}
	return nil
}
