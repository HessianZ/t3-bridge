package bridge

import (
	"cmp"
	"errors"
	"slices"
	"strings"
)

type LegacySessionMapping struct {
	ThreadID, ChatID, UserID, RouteID string
	ChatType, TopicID                 string
}

type LegacySessionImportResult struct {
	AppID           string `json:"appId"`
	ChatID          string `json:"chatId"`
	UserID          string `json:"userId"`
	RouteID         string `json:"routeId"`
	ThreadID        string `json:"threadId"`
	EnvironmentID   string `json:"environmentId"`
	ProjectID       string `json:"projectId"`
	ProjectPath     string `json:"projectPath"`
	Action          string `json:"action"`
	Applied         bool   `json:"applied"`
	ArchivedContext bool   `json:"archivedContext,omitempty"`
	ChatType        string `json:"chatType,omitempty"`
	TopicID         string `json:"topicId,omitempty"`
}

type legacySessionPlan struct {
	result LegacySessionImportResult
	scope  string
	target Target
}

type legacySessionSeed struct {
	ThreadID      string `json:"threadId"`
	ProjectPath   string `json:"projectPath"`
	ChatType      string `json:"chatType,omitempty"`
	ReplayChecked bool   `json:"replayChecked,omitempty"`
}

type LegacySessionCandidate struct {
	ThreadID    string `json:"threadId"`
	ProjectPath string `json:"projectPath"`
	UpdatedAt   int64  `json:"updatedAt"`
}

type LegacySessionScope struct {
	ChatID        string `json:"chatId"`
	TopicID       string `json:"topicId,omitempty"`
	ActiveThreads int    `json:"activeThreads"`
	UpdatedAt     int64  `json:"updatedAt"`
}

type legacySessionEntry struct {
	Key               string `json:"key"`
	Scope             string `json:"scopeId"`
	Agent             string `json:"agentId"`
	Path              string `json:"cwdRealpath"`
	PolicyFingerprint string `json:"policyFingerprint"`
	Status            string `json:"status"`
	UpdatedAt         int64  `json:"updatedAt"`
	SessionID         string `json:"sessionId,omitempty"`
	ThreadID          string `json:"threadId,omitempty"`
	LastSummary       string `json:"lastSummary,omitempty"`
}

func readLegacySessionEntries(catalog []byte) ([]legacySessionEntry, error) {
	var entries []legacySessionEntry
	if err := decodeConfigJSON(catalog, &entries); err != nil {
		return nil, errors.New("invalid legacy session catalog")
	}
	return entries, nil
}

func (entry legacySessionEntry) active() bool {
	return entry.Status == "active" && entry.SessionID == "" &&
		entry.PolicyFingerprint != "" && normalizeWorkspacePath(entry.Path) != "" &&
		entry.Key == strings.Join([]string{entry.Scope, entry.Agent, entry.Path, entry.PolicyFingerprint}, "\x1f")
}

// InspectLegacySessions exposes only unambiguous T3 thread metadata for the exact
// supplied chat scope. A legacy scope does not establish chat type or membership.
func InspectLegacySessions(catalog []byte, chatID string) ([]LegacySessionCandidate, error) {
	return InspectLegacySessionScope(catalog, LegacySessionMapping{ChatID: chatID})
}

func InspectLegacySessionScope(catalog []byte, mapping LegacySessionMapping) ([]LegacySessionCandidate, error) {
	scope, err := mapping.catalogScope()
	if err != nil {
		return nil, err
	}
	entries, err := readLegacySessionEntries(catalog)
	if err != nil {
		return nil, err
	}
	threadCounts := legacySessionThreadCounts(entries)
	candidates := make([]LegacySessionCandidate, 0)
	for _, entry := range entries {
		if entry.Scope == scope && entry.inspectable(threadCounts) {
			candidates = append(candidates, LegacySessionCandidate{ThreadID: entry.ThreadID, ProjectPath: entry.Path, UpdatedAt: entry.UpdatedAt})
		}
	}
	slices.SortFunc(candidates, func(a, b LegacySessionCandidate) int {
		if order := cmp.Compare(b.UpdatedAt, a.UpdatedAt); order != 0 {
			return order
		}
		return strings.Compare(a.ThreadID, b.ThreadID)
	})
	return candidates, nil
}

// InspectLegacySessionScopes groups candidate T3 threads by their original
// catalog scope. A chat without a topic has unknown chat type and membership.
func InspectLegacySessionScopes(catalog []byte) ([]LegacySessionScope, error) {
	entries, err := readLegacySessionEntries(catalog)
	if err != nil {
		return nil, err
	}
	threadCounts := legacySessionThreadCounts(entries)
	groups := map[string]LegacySessionScope{}
	for _, entry := range entries {
		chatID, topicID, hasTopic := strings.Cut(entry.Scope, ":")
		if !validLegacySessionScopeID(chatID, "oc_") ||
			(hasTopic && !validLegacyTopicID(topicID)) ||
			!entry.inspectable(threadCounts) {
			continue
		}
		group := groups[entry.Scope]
		group.ChatID, group.TopicID = chatID, topicID
		group.ActiveThreads++
		group.UpdatedAt = max(group.UpdatedAt, entry.UpdatedAt)
		groups[entry.Scope] = group
	}
	scopes := make([]LegacySessionScope, 0, len(groups))
	for _, group := range groups {
		scopes = append(scopes, group)
	}
	slices.SortFunc(scopes, func(a, b LegacySessionScope) int {
		if order := cmp.Compare(b.UpdatedAt, a.UpdatedAt); order != 0 {
			return order
		}
		if order := strings.Compare(a.ChatID, b.ChatID); order != 0 {
			return order
		}
		return strings.Compare(a.TopicID, b.TopicID)
	})
	return scopes, nil
}

func legacySessionThreadCounts(entries []legacySessionEntry) map[string]int {
	counts := make(map[string]int)
	for _, entry := range entries {
		if entry.Agent == "t3" {
			counts[entry.ThreadID]++
		}
	}
	return counts
}

func (entry legacySessionEntry) inspectable(counts map[string]int) bool {
	return entry.Agent == "t3" && counts[entry.ThreadID] == 1 && len(entry.ThreadID) <= 256 &&
		workspaceImportValue(entry.ThreadID) && entry.active()
}

// ImportLegacySession records a one-time continuation for an explicitly selected
// same-App scope. Only confirmed imports open the offline Store.
func ImportLegacySession(c Config, catalog []byte, mapping LegacySessionMapping, verify func(LegacySessionImportResult) error) (LegacySessionImportResult, error) {
	cwd, err := legacySessionPath(catalog, mapping)
	if err != nil {
		return LegacySessionImportResult{}, err
	}
	if verify == nil {
		plan, err := previewLegacySessionPlan(c, mapping, cwd)
		return plan.result, err
	}
	store, err := OpenStore(c.StateDir)
	if err != nil {
		return LegacySessionImportResult{}, err
	}
	defer store.Close()
	plan, err := planLegacySession(c, &store.state, mapping, cwd)
	if err != nil {
		return LegacySessionImportResult{}, err
	}
	result := plan.result
	if err := verify(result); err != nil {
		return LegacySessionImportResult{}, err
	}
	if result.Action == "add" {
		if err := store.update(func(state *State) error {
			if state.LegacySessions == nil {
				state.LegacySessions = map[string]legacySessionSeed{}
			}
			state.AccountID = c.AppID
			state.LegacySessions[plan.scope] = legacySessionSeed{ThreadID: result.ThreadID, ProjectPath: cwd, ChatType: result.ChatType}
			return nil
		}); err != nil {
			return LegacySessionImportResult{}, err
		}
	}
	result.Applied = true
	return result, nil
}

func previewLegacySessionPlan(c Config, mapping LegacySessionMapping, cwd string) (legacySessionPlan, error) {
	state, err := readStateSnapshot(c.StateDir)
	if err != nil {
		return legacySessionPlan{}, err
	}
	return planLegacySession(c, &state, mapping, cwd)
}

func legacySessionPath(catalog []byte, mapping LegacySessionMapping) (string, error) {
	catalogScope, err := mapping.catalogScope()
	if err != nil {
		return "", err
	}
	if !workspaceImportValue(mapping.ThreadID) || len(mapping.ThreadID) > 256 || !validLegacySessionScopeID(mapping.UserID, "ou_") {
		return "", errors.New("a thread ID and explicit user ID are required")
	}
	entries, err := readLegacySessionEntries(catalog)
	if err != nil {
		return "", err
	}
	cwd := ""
	for _, entry := range entries {
		if entry.ThreadID != mapping.ThreadID || entry.Agent != "t3" {
			continue
		}
		if cwd != "" || entry.Scope != catalogScope || !entry.active() {
			return "", errors.New("legacy thread must have one active T3 entry for the selected chat scope")
		}
		cwd = entry.Path
	}
	if cwd == "" {
		return "", errors.New("no matching legacy T3 session")
	}
	return cwd, nil
}

func (mapping LegacySessionMapping) catalogScope() (string, error) {
	if !validLegacySessionScopeID(mapping.ChatID, "oc_") {
		return "", errors.New("an explicit chat ID is required")
	}
	switch mapping.ChatType {
	case "", "p2p":
		if mapping.TopicID != "" {
			return "", errors.New("a topic requires explicit group chat type")
		}
	case "group":
		if mapping.TopicID != "" {
			if !validLegacyTopicID(mapping.TopicID) {
				return "", errors.New("invalid topic ID")
			}
			return mapping.ChatID + ":" + mapping.TopicID, nil
		}
	default:
		return "", errors.New("chat type must be p2p or group")
	}
	return mapping.ChatID, nil
}

func validLegacyTopicID(id string) bool {
	return validLegacySessionScopeID(id, "omt_") || validLegacySessionScopeID(id, "om_")
}

func validLegacySessionScopeID(id, prefix string) bool {
	suffix, ok := strings.CutPrefix(id, prefix)
	return ok && suffix != "" && len(id) <= 128 && !strings.ContainsFunc(suffix, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
	})
}

func planLegacySession(c Config, state *State, mapping LegacySessionMapping, cwd string) (legacySessionPlan, error) {
	if err := checkWorkspaceImportAccount(*state, c.AppID); err != nil {
		return legacySessionPlan{}, err
	}
	if _, ok := c.Routes[mapping.RouteID]; !ok || mapping.RouteID == c.ReviewRoute {
		return legacySessionPlan{}, errors.New("legacy session requires a configured conversation route")
	}
	m := Message{UserID: mapping.UserID, ChatID: mapping.ChatID, ChatType: "p2p", TopicID: mapping.TopicID}
	if mapping.ChatType == "group" {
		m.ChatType = "group"
	}
	target := effectiveTarget(c, state, m, mapping.RouteID)
	if !sameWorkspacePath(target.ProjectPath, cwd) {
		return legacySessionPlan{}, errors.New("selected project path differs from the legacy directory")
	}
	c.Admins, c.access = nil, &accessRuntime{}
	current := c
	policy := state.AccessPolicy
	policy.Admins = nil
	current.access = &accessRuntime{groups: state.GroupAccess, policy: policy}
	if !c.TargetAllowed(m, target, mapping.RouteID) || !current.TargetAllowed(m, target, mapping.RouteID) {
		return legacySessionPlan{}, errors.New("chat scope, user or route is not authorized")
	}
	scope := sessionKey(m, mapping.RouteID, target)
	currentID, known := state.Current[scope]
	if currentID != "" || currentTask(current, state, m, mapping.RouteID) != nil || workspaceBusy(current, state, m, mapping.RouteID) {
		return legacySessionPlan{}, errors.New("destination already has a session or pending work")
	}
	for _, task := range state.Tasks {
		if task.Target.EnvironmentID == target.EnvironmentID &&
			(task.ThreadID == mapping.ThreadID || task.LegacyGroupSeed != nil && task.LegacyGroupSeed.ThreadID == mapping.ThreadID) {
			return legacySessionPlan{}, errors.New("thread already has a Bridge task")
		}
	}
	result := LegacySessionImportResult{
		AppID: c.AppID, ChatID: m.ChatID, UserID: m.UserID, RouteID: mapping.RouteID,
		ThreadID: mapping.ThreadID, EnvironmentID: target.EnvironmentID, ProjectID: target.ProjectID, ProjectPath: cwd, Action: "add",
		ArchivedContext: known,
	}
	if m.ChatType == "group" {
		result.ChatType, result.TopicID = m.ChatType, m.TopicID
	}
	for key, entry := range state.LegacySessions {
		if key == scope && entry.ThreadID == mapping.ThreadID && entry.ChatType == result.ChatType && sameWorkspacePath(entry.ProjectPath, cwd) {
			result.Action = "unchanged"
		} else if key == scope || entry.ThreadID == mapping.ThreadID {
			return legacySessionPlan{}, errors.New("thread or destination already has a pending migration")
		}
	}
	return legacySessionPlan{result: result, scope: scope, target: target}, nil
}

func takeLegacySession(state *State, task *Task, scope string) {
	m := task.Source
	if (m.ParentID != "" && (m.ChatType != "group" || m.TopicID == "")) || m.Comment != nil || m.Meeting != nil ||
		m.EventID != "" || m.ControlTask != "" || m.SessionTask != "" {
		return
	}
	switch m.InputKind {
	case "", "text", "post", "image", "file", "merge_forward":
	default:
		return
	}
	seed, pending := state.LegacySessions[scope]
	private := m.ChatType == "p2p" && m.TopicID == "" && seed.ChatType == ""
	group := m.ChatType == "group" && seed.ChatType == "group"
	if !pending || (!private && !group) {
		return
	}
	delete(state.LegacySessions, scope)
	if state.Current[scope] == "" {
		if group {
			task.LegacyGroupSeed = &seed
		} else {
			task.LegacySessionPath = seed.ProjectPath
			task.ThreadID = seed.ThreadID
		}
	}
}
