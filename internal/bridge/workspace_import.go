package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode"
)

type workspaceImportMapping struct {
	SourceKey string `json:"sourceKey"`
	UserID    string `json:"userId"`
	ChatID    string `json:"chatId"`
	ChatType  string `json:"chatType"`
	TopicID   string `json:"topicId,omitempty"`
	RouteID   string `json:"routeId"`
	ProjectID string `json:"projectId,omitempty"`
}

type workspaceImportAlias struct {
	workspaceImportMapping
	Name   string `json:"name"`
	Path   string `json:"path"`
	Action string `json:"action"`
}

type WorkspaceImportResult struct {
	AppID    string                 `json:"appId"`
	Applied  bool                   `json:"applied"`
	Unmapped int                    `json:"unmapped"`
	Aliases  []workspaceImportAlias `json:"aliases"`
}

type LegacyWorkspaceAlias struct {
	SourceKey     string `json:"sourceKey"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	TopicRequired bool   `json:"topicRequired,omitempty"`
}

// InspectLegacyWorkspaces lists source aliases without reading destination state.
func InspectLegacyWorkspaces(raw []byte) ([]LegacyWorkspaceAlias, int, error) {
	named, err := readLegacyWorkspaceNames(raw)
	if err != nil {
		return nil, 0, err
	}
	aliases := make([]LegacyWorkspaceAlias, 0, len(named))
	for _, key := range slices.Sorted(maps.Keys(named)) {
		if alias, err := parseLegacyWorkspaceAlias(key, named[key]); err == nil {
			aliases = append(aliases, alias)
		}
	}
	return aliases, len(named) - len(aliases), nil
}

// ImportLegacyWorkspaces imports explicitly mapped names into one configured account.
// Only apply opens the Store for writing. Both paths validate against current state.
func ImportLegacyWorkspaces(c Config, legacyJSON, mappingJSON []byte, apply bool) (WorkspaceImportResult, error) {
	return importLegacyWorkspaces(c, legacyJSON, mappingJSON, apply, nil)
}

// ApplyReviewedLegacyWorkspaces verifies the current plan while holding the state lock.
func ApplyReviewedLegacyWorkspaces(c Config, legacyJSON, mappingJSON []byte, verify func(WorkspaceImportResult) error) (WorkspaceImportResult, error) {
	if verify == nil {
		return WorkspaceImportResult{}, errors.New("workspace import review is required")
	}
	return importLegacyWorkspaces(c, legacyJSON, mappingJSON, true, verify)
}

func importLegacyWorkspaces(c Config, legacyJSON, mappingJSON []byte, apply bool, verify func(WorkspaceImportResult) error) (WorkspaceImportResult, error) {
	named, err := readLegacyWorkspaceNames(legacyJSON)
	if err != nil {
		return WorkspaceImportResult{}, err
	}
	var mappings []workspaceImportMapping
	if err := decodeConfigJSON(mappingJSON, &mappings); err != nil || len(mappings) == 0 {
		return WorkspaceImportResult{}, errors.New("workspace mapping must be a nonempty JSON array with exact field names")
	}

	if !apply {
		state, err := readStateSnapshot(c.StateDir)
		if err != nil {
			return WorkspaceImportResult{}, err
		}
		result, _, err := planWorkspaceImport(c, state, named, mappings)
		return result, err
	}

	store, err := OpenStore(c.StateDir)
	if err != nil {
		return WorkspaceImportResult{}, err
	}
	defer store.Close()
	result, changes, err := planWorkspaceImport(c, store.state, named, mappings)
	if err != nil {
		return WorkspaceImportResult{}, err
	}
	if verify != nil {
		if err := verify(result); err != nil {
			return WorkspaceImportResult{}, err
		}
	}
	if len(changes) > 0 || store.state.AccountID == "" {
		// This Store is private to the import and holds the directory lock. Stage
		// the aliases in memory so BindAccount persists identity and names together.
		if store.state.WorkspaceNames == nil {
			store.state.WorkspaceNames = make(map[string]map[string]Workspace)
		}
		maps.Copy(store.state.WorkspaceNames, changes)
		if err := store.BindAccount(c.AppID); err != nil {
			return WorkspaceImportResult{}, err
		}
	}
	result.Applied = true
	return result, nil
}

func readLegacyWorkspaceNames(raw []byte) (map[string]string, error) {
	var legacy struct {
		Chats json.RawMessage   `json:"chats"`
		Named map[string]string `json:"named"`
	}
	if err := decodeConfigObject(raw, &legacy); err != nil || legacy.Named == nil {
		return nil, errors.New("legacy workspaces must contain a named object of aliases and paths")
	}
	return legacy.Named, nil
}

func planWorkspaceImport(c Config, state State, named map[string]string, mappings []workspaceImportMapping) (WorkspaceImportResult, map[string]map[string]Workspace, error) {
	if err := checkWorkspaceImportAccount(state, c.AppID); err != nil {
		return WorkspaceImportResult{}, nil, err
	}
	// Imports require configured scope access and respect revocations in state.
	// Neither an old bot owner nor an admin bypass establishes a destination ACL.
	c.Admins = nil
	c.access = &accessRuntime{}
	current := c
	policy := state.AccessPolicy
	policy.Admins = nil
	current.access = &accessRuntime{groups: state.GroupAccess, policy: policy}

	result := WorkspaceImportResult{AppID: c.AppID}
	mapped := make(map[string]bool)
	changes := make(map[string]map[string]Workspace)
	destinations := make(map[[2]string]bool)
	for i, mapping := range mappings {
		alias, message, project, err := resolveWorkspaceImport(c, current, named, mapping)
		if err != nil {
			return WorkspaceImportResult{}, nil, fmt.Errorf("workspace mapping %d (%q): %w", i+1, mapping.SourceKey, err)
		}
		scope := workspaceKey(c, message, mapping.RouteID)
		destination := [2]string{scope, alias.Name}
		if destinations[destination] {
			return WorkspaceImportResult{}, nil, fmt.Errorf("workspace mapping %d: conflict for %q: multiple mappings target the same alias", i+1, alias.Name)
		}
		destinations[destination] = true
		aliases := state.WorkspaceNames[scope]
		if existing, ok := aliases[alias.Name]; ok {
			if existing.ProjectID != project.ProjectID || !sameWorkspacePath(existing.Path, project.Path) {
				return WorkspaceImportResult{}, nil, fmt.Errorf("workspace mapping %d: conflict for %q: existing project %q at %q, mapped project %q at %q", i+1, alias.Name, existing.ProjectID, existing.Path, project.ProjectID, project.Path)
			}
			alias.Action = "unchanged"
		} else {
			if changes[scope] == nil {
				changes[scope] = make(map[string]Workspace)
				maps.Copy(changes[scope], aliases)
			}
			if len(changes[scope]) >= 32 {
				return WorkspaceImportResult{}, nil, fmt.Errorf("workspace mapping %d: destination scope holds at most 32 aliases", i+1)
			}
			changes[scope][alias.Name] = project
			alias.Action = "add"
		}
		mapped[mapping.SourceKey] = true
		result.Aliases = append(result.Aliases, alias)
	}
	result.Unmapped = len(named) - len(mapped)
	return result, changes, nil
}

func checkWorkspaceImportAccount(state State, appID string) error {
	if state.AccountID != "" && state.AccountID != appID {
		return errors.New("state belongs to another app; use a separate private stateDir")
	}
	if state.AccountID == "" && (len(state.Tasks) > 0 || len(state.Inbox) > 0 || len(state.Seen) > 0) {
		return errors.New("legacy state has no app identity; verify and bind the original app before import")
	}
	for _, task := range state.Tasks {
		if task == nil || (task.ReceiverAppID != "" && task.ReceiverAppID != appID) {
			return errors.New("state contains an invalid or foreign task")
		}
	}
	return nil
}

func resolveWorkspaceImport(c, current Config, named map[string]string, mapping workspaceImportMapping) (workspaceImportAlias, Message, Workspace, error) {
	alias, err := parseLegacyWorkspaceAlias(mapping.SourceKey, named[mapping.SourceKey])
	if err != nil {
		return workspaceImportAlias{}, Message{}, Workspace{}, err
	}
	m := Message{UserID: mapping.UserID, ChatID: mapping.ChatID, ChatType: mapping.ChatType, TopicID: mapping.TopicID}
	if !workspaceImportValue(m.UserID) || !workspaceImportValue(m.ChatID) || (m.TopicID != "" && !workspaceImportValue(m.TopicID)) || (m.ChatType != "group" && m.ChatType != "p2p") {
		return workspaceImportAlias{}, Message{}, Workspace{}, errors.New("explicit userId, chatId and chatType (p2p or group) are required, with an exact topicId for a topic")
	}
	if alias.TopicRequired && m.TopicID == "" {
		return workspaceImportAlias{}, Message{}, Workspace{}, errors.New("a legacy topic requires an explicit destination topicId")
	}
	t, exists := c.Routes[mapping.RouteID]
	if !exists || mapping.RouteID == c.ReviewRoute ||
		!c.userAllowed(m.UserID) || !c.TargetAllowed(m, t, mapping.RouteID) ||
		!current.userAllowed(m.UserID) || !current.TargetAllowed(m, t, mapping.RouteID) {
		return workspaceImportAlias{}, Message{}, Workspace{}, errors.New("destination user, chat/topic and route must be authorized for this app")
	}
	project, err := configuredWorkspaceImportProject(c, t.EnvironmentID, mapping.ProjectID, alias.Path)
	if err != nil {
		return workspaceImportAlias{}, Message{}, Workspace{}, err
	}
	mapping.ProjectID = project.ProjectID
	return workspaceImportAlias{workspaceImportMapping: mapping, Name: alias.Name, Path: alias.Path}, m, project, nil
}

func parseLegacyWorkspaceAlias(key, cwd string) (LegacyWorkspaceAlias, error) {
	if normalizeWorkspacePath(cwd) == "" {
		return LegacyWorkspaceAlias{}, errors.New("source key must identify a named alias with an absolute working directory")
	}
	parts := strings.Split(key, "\x1f")
	if len(parts) != 1 && (len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[2] == "") {
		return LegacyWorkspaceAlias{}, errors.New("invalid legacy alias scope")
	}
	name := parts[len(parts)-1]
	if !workspaceImportValue(name) || len([]rune(name)) > 64 {
		return LegacyWorkspaceAlias{}, errors.New("alias name must have 1 to 64 visible characters without surrounding whitespace")
	}
	return LegacyWorkspaceAlias{
		SourceKey: key, Name: name, Path: cwd,
		TopicRequired: len(parts) == 4 && strings.Contains(parts[2], ":"),
	}, nil
}

func workspaceImportValue(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && !strings.ContainsFunc(value, unicode.IsControl)
}

func configuredWorkspaceImportProject(c Config, environmentID, projectID, cwd string) (Workspace, error) {
	if projectID == "" {
		for _, target := range c.Routes {
			if target.EnvironmentID != environmentID || target.ProjectID == "" || !sameWorkspacePath(target.ProjectPath, cwd) {
				continue
			}
			if projectID != "" && target.ProjectID != projectID {
				return Workspace{}, fmt.Errorf("specify projectId for legacy cwd %q matching multiple configured projects in the destination route's T3 environment", cwd)
			}
			projectID = target.ProjectID
		}
		if projectID == "" {
			return Workspace{}, fmt.Errorf("cannot infer projectId: no configured project in the destination route's T3 environment matches legacy cwd %q", cwd)
		}
	}
	project := Workspace{ProjectID: projectID, Path: cwd}
	known := false
	for _, id := range slices.Sorted(maps.Keys(c.Routes)) {
		target := c.Routes[id]
		if target.EnvironmentID != environmentID || target.ProjectID != projectID || target.ProjectPath == "" {
			continue
		}
		if !sameWorkspacePath(target.ProjectPath, cwd) {
			return Workspace{}, fmt.Errorf("project %q has a configured path different from legacy cwd %q", projectID, cwd)
		}
		known = true
		if project.Name == "" {
			project.Name = target.ProjectName
		}
	}
	if !known {
		return Workspace{}, fmt.Errorf("project %q requires a configured projectId and projectPath in the destination route's T3 environment", projectID)
	}
	if project.Name == "" {
		project.Name = projectID
	}
	return project, nil
}
