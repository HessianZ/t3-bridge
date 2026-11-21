package bridge

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

type Config struct {
	AutoInvestigations   AutoInvestigations  `json:"autoInvestigations,omitempty"`
	InvestigationDB      string              `json:"investigationDatabase,omitempty"`
	ReportCommand        string              `json:"reportSubmitCommand,omitempty"`
	ReportSources        []ReportSource      `json:"reportSources,omitempty"`
	ReportOutputs        map[string]string   `json:"reportOutputs,omitempty"`
	ReportCopyCommands   map[string]string   `json:"reportCopyCommands,omitempty"`
	FollowupFlush        FollowupFlushConfig `json:"followupFlush,omitempty"`
	configFile           string
	additionalAccount    bool
	ReceiverMachine      string            `json:"receiverMachine"`
	ReceiverAuthorized   bool              `json:"receiverAuthorized"`
	StateDir             string            `json:"stateDir"`
	AppID                string            `json:"appId"`
	AppSecretFile        string            `json:"appSecretFile"`
	Tenant               string            `json:"tenant,omitempty"`
	CommentRoute         string            `json:"commentRoute,omitempty"`
	MeetingEnabled       bool              `json:"meetingEnabled,omitempty"`
	MeetingRoute         string            `json:"meetingRoute,omitempty"`
	MeetingSummaryTarget string            `json:"meetingSummaryTarget,omitempty"`
	MeetingRoomReplies   bool              `json:"meetingRoomReplies,omitempty"`
	MeetingAutoJoinUsers []string          `json:"meetingAutoJoinUsers,omitempty"`
	AdditionalAccounts   []Config          `json:"additionalAccounts,omitempty"`
	Users                []string          `json:"users"`
	Admins               []string          `json:"admins,omitempty"`
	Chats                []string          `json:"chats"`
	DirectChats          *[]string         `json:"directChats,omitempty"`
	Topics               []string          `json:"topics,omitempty"`
	MCP                  MCPConfig         `json:"mcp"`
	Classifier           Hy4Config         `json:"classifier"`
	CoordinationCommand  string            `json:"coordinationCommand"`
	Routes               map[string]Target `json:"routes"`
	ReviewRoute          string            `json:"reviewRoute,omitempty"`
	DefaultRoute         string            `json:"defaultRoute,omitempty"`
	EventRoutes          map[string]string `json:"eventRoutes,omitempty"`
	EventChats           []string          `json:"eventChats,omitempty"`
	Health               []HealthSource    `json:"health,omitempty"`
	AgentReportsFile     string            `json:"agentReportsFile,omitempty"`
	ReportNode           string            `json:"reportNode,omitempty"`
	access               *accessRuntime

	InitialRuntime *InitialRuntimeConfig `json:"initialRuntime,omitempty"`
}

type MCPConfig struct {
	Endpoint      string `json:"endpoint,omitempty"`
	RuntimeFile   string `json:"runtimeFile,omitempty"`
	TokenFile     string `json:"tokenFile"`
	EnvironmentID string `json:"environmentId"`
	projectSetup  bool
}

type Hy4Config struct {
	BaseURL  string `json:"baseUrl"`
	Model    string `json:"model"`
	KeyFile  string `json:"keyFile"`
	Verified bool   `json:"verified"`
}

type Target struct {
	HostName string   `json:"hostName,omitempty"`
	Aliases  []string `json:"aliases,omitempty"`

	CacheFollowupAttachments bool `json:"cacheFollowupAttachments,omitempty"`

	ToolContext       *ToolContext   `json:"toolContext,omitempty"`
	UnavailableReason string         `json:"unavailableReason,omitempty"`
	Kind              string         `json:"kind"`
	EnvironmentID     string         `json:"environmentId"`
	ProjectID         string         `json:"projectId"`
	ProjectName       string         `json:"projectName,omitempty"`
	ProjectPath       string         `json:"projectPath,omitempty"`
	Driver            string         `json:"driver"`
	ProviderID        string         `json:"providerId"`
	Model             string         `json:"model"`
	Options           map[string]any `json:"options"`
	RuntimeMode       string         `json:"runtimeMode"`
	InteractionMode   string         `json:"interactionMode"`
	Users             []string       `json:"users"`
	Checks            []string       `json:"checks"`
}

func (c Config) Validate() error {
	if err := c.validateAccount(); err != nil {
		return err
	}
	apps := map[string]bool{c.AppID: true}
	dirs := map[string]bool{filepath.Clean(c.StateDir): true}
	for _, account := range c.AdditionalAccounts {
		if len(account.AdditionalAccounts) != 0 {
			return errors.New("additionalAccounts cannot be nested")
		}
		if err := account.validateAccount(); err != nil {
			return fmt.Errorf("additional account: %w", err)
		}
		dir := filepath.Clean(account.StateDir)
		if apps[account.AppID] || dirs[dir] {
			return errors.New("each Feishu account requires a unique appId and stateDir")
		}
		apps[account.AppID], dirs[dir] = true, true
	}
	return c.validateToolProviders()
}

func (c Config) Accounts() []Config {
	additional := c.AdditionalAccounts
	c.AdditionalAccounts = nil
	accounts := append([]Config{c}, additional...)
	for i := 1; i < len(accounts); i++ {
		accounts[i].configFile = c.configFile
		accounts[i].additionalAccount = true
	}
	return accounts
}

func (c Config) validateAccount() error {
	if err := c.validateRouteNames(); err != nil {
		return err
	}
	if c.MeetingEnabled && len(c.namedRoutes("meeting", false)) != 0 {
		return errors.New("meetingEnabled reserves /meeting for meeting controls")
	}
	if err := c.validateInvestigations(); err != nil {
		return err
	}
	if !filepath.IsAbs(c.StateDir) || len(c.Users) == 0 || len(c.Routes) == 0 {
		return errors.New("stateDir, user ACL and routes required")
	}
	if err := c.MCP.validateConnection(); err != nil {
		return err
	}
	if c.MCP.EnvironmentID == "" {
		return errors.New("explicit MCP environment required")
	}
	if c.CoordinationCommand != "" && !filepath.IsAbs(c.CoordinationCommand) {
		return errors.New("coordinationCommand must be absolute")
	}
	for id, t := range c.Routes {
		if t.CacheFollowupAttachments && !c.MCP.LocalEnvironment(t.EnvironmentID) {
			return fmt.Errorf("route %s cacheFollowupAttachments requires a local T3 environment", id)
		}
		if t.ToolContext != nil {
			if err := t.ToolContext.Validate(t.EnvironmentID); err != nil {
				return err
			}
			if t.ToolContext.guarded() && !c.MCP.LocalEnvironment(t.EnvironmentID) {
				return errors.New("toolContext binding requires a local T3 environment")
			}
			if user := t.ToolContext.UserBinding; user != nil {
				tenant := c.Tenant
				if tenant == "" {
					tenant = "feishu"
				}
				if user.AppID != c.AppID || user.Tenant != tenant || !slices.Equal(t.Users, []string{user.UserOpenID}) {
					return errors.New("toolContext.userBinding requires the receiving app, its tenant and the single bound route user")
				}
			}
		}
		if id == "" || strings.ContainsAny(id, " /\n") || t.EnvironmentID == "" || t.ProjectID == "" || t.ProviderID == "" || t.Model == "" || len(t.Users) == 0 || t.Options == nil {
			return fmt.Errorf("route %s requires explicit target, model, options and user ACL", id)
		}
		if t.Kind != "" && t.Kind != "nas" && t.Kind != "mac" {
			return fmt.Errorf("route %s has invalid kind", id)
		}
		if len(t.Checks) > 0 && c.CoordinationCommand == "" {
			return errors.New("route checks require an explicit coordinationCommand")
		}
		if t.Driver == "" || !slices.Contains([]string{"approval-required", "auto-accept-edits", "auto", "full-access"}, t.RuntimeMode) || !slices.Contains([]string{"default", "plan"}, t.InteractionMode) {
			return fmt.Errorf("route %s requires explicit driver and permission modes", id)
		}
	}
	if c.ReviewRoute != "" {
		_, ok := c.Routes[c.ReviewRoute]
		if !ok || c.DefaultRoute == c.ReviewRoute {
			return errors.New("reviewRoute must name a dedicated configured route and cannot be default")
		}
	}
	if c.DefaultRoute != "" {
		if _, ok := c.Routes[c.DefaultRoute]; !ok {
			return errors.New("defaultRoute must name a configured route")
		}
	}
	if c.CommentRoute != "" {
		if _, ok := c.Routes[c.CommentRoute]; !ok || c.CommentRoute == c.ReviewRoute {
			return errors.New("commentRoute must name a configured non-review route")
		}
	}
	if c.MeetingRoute != "" {
		target, ok := c.Routes[c.MeetingRoute]
		if !ok || c.MeetingRoute == c.ReviewRoute || target.RuntimeMode != "approval-required" || target.InteractionMode != "plan" {
			return errors.New("meetingRoute must name a non-review route with approval-required runtime and plan interaction")
		}
	}
	switch c.MeetingSummaryTarget {
	case "":
	case "origin", "owner":
		if !c.MeetingEnabled || c.MeetingRoute == "" {
			return errors.New("meetingSummaryTarget requires an enabled meeting and meetingRoute")
		}
	default:
		return errors.New("meetingSummaryTarget must be empty, origin or owner")
	}
	if c.MeetingRoomReplies && (!c.MeetingEnabled || c.MeetingRoute == "") {
		return errors.New("meetingRoomReplies requires an enabled meeting and meetingRoute")
	}
	for _, user := range c.MeetingAutoJoinUsers {
		if user == "" || strings.TrimSpace(user) != user {
			return errors.New("meetingAutoJoinUsers requires explicit user Open IDs")
		}
	}
	for _, id := range c.EventRoutes {
		if t, ok := c.Routes[id]; !ok || t.Kind != "nas" || id == c.ReviewRoute || len(t.Checks) == 0 || c.CoordinationCommand == "" {
			return errors.New("eventRoutes require configured NAS routes with explicit checks and coordinationCommand")
		}
	}
	if !nodeName.MatchString(c.ReceiverMachine) {
		return errors.New("receiverMachine identity required")
	}
	if c.AppID == "" || !filepath.IsAbs(c.AppSecretFile) {
		return errors.New("appId and absolute appSecretFile required")
	}
	if c.Tenant != "" && c.Tenant != "feishu" && c.Tenant != "lark" {
		return errors.New("tenant must be feishu or lark")
	}
	if c.AgentReportsFile != "" && (!filepath.IsAbs(c.AgentReportsFile) || c.ReportNode == "") {
		return errors.New("agentReportsFile requires absolute path and reportNode")
	}
	for _, h := range c.Health {
		if err := h.Validate(); err != nil {
			return err
		}
	}
	if c.Classifier != (Hy4Config{}) {
		if err := validateHy4(c.Classifier); err != nil {
			return err
		}
	}
	if c.InitialRuntime != nil {
		return c.InitialRuntime.validate(c.Chats)
	}
	return nil
}

func Secret(path string) (string, error) {
	// A configured credential reference can name a symlink. Check the opened target itself.
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", errors.New("secret file missing or not private")
	}
	file, err := localfile.OpenPrivate(resolved)
	if err != nil {
		return "", errors.New("secret file missing or not private")
	}
	defer file.Close()
	b, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || len(b) == 0 || len(b) > 65536 {
		return "", errors.New("cannot read secret file")
	}
	value := strings.TrimSpace(string(b))
	if value == "" {
		return "", errors.New("empty secret file")
	}
	return value, nil
}

func (c Config) Allowed(m Message) bool {
	if m.Meeting != nil && m.Meeting.Room {
		return c.MeetingEnabled && c.MeetingRoomReplies && m.ChatType == "meeting" && m.ChatID == "meeting:"+m.Meeting.ID &&
			(c.userAllowed(m.UserID) || c.isAdmin(m.UserID)) && !c.userDenied(m.UserID)
	}
	if m.Comment != nil {
		return c.CommentRoute != "" && c.userAllowed(m.UserID)
	}
	if len(c.Topics) != 0 && !slices.Contains(c.Topics, m.TopicID) {
		return false
	}
	// Legacy durable messages have no chat type; retain their original ACL.
	if c.access == nil || m.ChatType == "" {
		return c.userAllowed(m.UserID) && slices.Contains(c.Chats, m.ChatID)
	}
	if c.isAdmin(m.UserID) {
		return m.ChatType == "group" || m.ChatType == "p2p"
	}
	if c.userDenied(m.UserID) {
		return false
	}
	if m.ChatType == "group" {
		return c.groupAllowed(m.ChatID)
	}
	direct := c.Chats
	if c.DirectChats != nil {
		direct = *c.DirectChats
	}
	return m.ChatType == "p2p" && c.userAllowed(m.UserID) && (len(direct) == 0 || slices.Contains(direct, m.ChatID))
}

func (c Config) TargetAllowed(m Message, t Target, route ...string) bool {
	if !c.Allowed(m) || c.userDenied(m.UserID) {
		return false
	}
	allowed := slices.Contains(t.Users, m.UserID)
	tool := t.ToolContext
	if len(route) > 0 {
		if m.Comment != nil && route[0] != c.CommentRoute {
			return false
		}
		base, ok := c.Routes[route[0]]
		if !ok || base.UnavailableReason != "" {
			return false
		}
		allowed = slices.Contains(base.Users, m.UserID)
		tool = base.ToolContext
		if c.access != nil {
			c.access.mu.RLock()
			if value, known := c.access.policy.Routes[route[0]][m.UserID]; known {
				allowed = value
			}
			c.access.mu.RUnlock()
		}
	}
	return allowed && tool.allowsToolUser(m)
}
