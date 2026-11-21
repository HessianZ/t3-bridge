package bridge

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"sort"
	"time"
)

type AutoInvestigations struct {
	Enabled                 bool   `json:"enabled"`
	LegacyDispatcherStopped bool   `json:"legacyDispatcherStopped"`
	UserID                  string `json:"userId"`
	ChatID                  string `json:"chatId"`
	ChatType                string `json:"chatType"`
	TopicID                 string `json:"topicId,omitempty"`
}
type ReportSource struct {
	Node       string `json:"node"`
	Command    string `json:"command"`
	ConfigFile string `json:"configFile"`
}

func (c Config) validateInvestigations() error {
	if len(c.ReportSources) > 2 {
		return errors.New("at most two bounded report sources are supported")
	}
	if c.FollowupFlush.Enabled && (!c.AutoInvestigations.LegacyDispatcherStopped || c.InvestigationDB == "" || !filepath.IsAbs(c.FollowupFlush.Command) || !filepath.IsAbs(c.FollowupFlush.ConfigFile)) {
		return errors.New("followup flush requires stopped combined legacy timer and explicit integration/command/config")
	}
	if c.InvestigationDB != "" && (!filepath.IsAbs(c.InvestigationDB) || !filepath.IsAbs(c.ReportCommand)) {
		return errors.New("investigationDatabase and reportSubmitCommand require explicit absolute paths")
	}
	if c.InvestigationDB != "" && !c.AutoInvestigations.LegacyDispatcherStopped {
		return errors.New("new event registration/report publishing requires the combined legacy timer stopped; use read-only reportSources before cutover")
	}
	nodes := map[string]bool{}
	for _, source := range c.ReportSources {
		if !nodeName.MatchString(source.Node) || nodes[source.Node] || !filepath.IsAbs(source.Command) || !filepath.IsAbs(source.ConfigFile) {
			return errors.New("reportSources require unique nodes and absolute command/config paths")
		}
		nodes[source.Node] = true
	}
	if path := c.ReportOutputs[c.ReportNode]; c.AgentReportsFile != "" && path != "" && path != c.AgentReportsFile {
		return errors.New("report node has conflicting output paths")
	}
	paths := map[string]bool{}
	for node, path := range c.reportOutputs() {
		if !nodes[node] || !filepath.IsAbs(path) || paths[path] {
			return errors.New("report output requires a matching explicit report source and absolute path")
		}
		paths[path] = true
	}
	for node, command := range c.ReportCopyCommands {
		if c.reportOutputs()[node] == "" || !filepath.IsAbs(command) {
			return errors.New("report copy requires an existing node output and explicit absolute command")
		}
	}
	a := c.AutoInvestigations
	if !a.Enabled {
		return nil
	}
	if !c.Allowed(Message{UserID: a.UserID, ChatID: a.ChatID, ChatType: a.ChatType, TopicID: a.TopicID}) {
		return errors.New("automatic actor scope is outside configured ACL")
	}
	if !a.LegacyDispatcherStopped || c.InvestigationDB == "" || !slices.Contains(c.Users, a.UserID) || !slices.Contains(c.Chats, a.ChatID) || !slices.Contains(c.EventChats, a.ChatID) || !slices.Contains([]string{"p2p", "group"}, a.ChatType) || a.TopicID != "" || len(c.Health) == 0 {
		return errors.New("automatic investigation requires stopped legacy dispatcher, explicit actor/chat ACL, health and report integration")
	}
	for _, h := range c.Health {
		route := c.eventRoute(Event{Node: h.Node})
		t, ok := c.Routes[route]
		if !ok || t.Kind != "nas" || route == c.ReviewRoute || !slices.Contains(t.Users, a.UserID) || !nodes[h.Node] {
			return errors.New("automatic health node requires explicit NAS event route, actor ACL and report source")
		}
	}
	return nil
}

// Offline re-arming resets only node baselines; retain every seen event, task and
// incident/stop identity. A non-drained receiver must not move this cursor.
func (s *Store) PrepareAutoBaseline(appID string) error {
	if s.snapshot().AccountID != appID {
		return errors.New("automatic baseline requires the original bound app identity")
	}
	if err := s.DrainCheck(appID); err != nil {
		return err
	}
	return s.update(func(st *State) error { st.AutoNodes = nil; return nil })
}
func (c Config) eventRoute(event Event) string {
	if route := c.EventRoutes[event.Node+"/"+event.Check]; route != "" {
		return route
	}
	return c.EventRoutes[event.Node]
}
func (c Config) reportOutputs() map[string]string {
	outputs := map[string]string{}
	for node, path := range c.ReportOutputs {
		outputs[node] = path
	}
	if c.AgentReportsFile != "" {
		outputs[c.ReportNode] = c.AgentReportsFile
	}
	return outputs
}

// Called inside the same transaction that accepts a verified health sample.
func (e *Engine) enqueueAutomatic(st *State, node string, events []Event) error {
	a := e.Config.AutoInvestigations
	if !a.Enabled {
		return nil
	}
	if st.AutoNodes == nil {
		st.AutoNodes = map[string]string{}
	}
	if st.AutoEvents == nil {
		st.AutoEvents = map[string]string{}
	}
	if st.AutoIncidents == nil {
		st.AutoIncidents = map[string]string{}
	}
	baseline := st.AutoNodes[node] == ""
	if baseline {
		st.AutoNodes[node] = e.now().UTC().Format(time.RFC3339Nano)
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Phase == "fault" && events[j].Phase != "fault" })
	for _, event := range events {
		if st.AutoEvents[event.ID] != "" {
			continue
		}
		if baseline {
			st.AutoEvents[event.ID] = "baseline-only"
			continue
		}
		start, _ := time.Parse(time.RFC3339Nano, event.FailureStart)
		armed, _ := time.Parse(time.RFC3339Nano, st.AutoNodes[node])
		if start.Before(armed) {
			st.AutoEvents[event.ID] = "pre-activation"
			continue
		}
		incident := incidentKey(event)
		taskID := st.AutoIncidents[incident]
		if event.Phase == "recovery" && taskID == "" {
			st.AutoEvents[event.ID] = "no-new-fault"
			continue
		}
		stopped := false
		for _, task := range st.Tasks {
			current := task.DispatchSource
			if current.ID == "" {
				current = task.Source
			}
			if ev, ok := st.EventRecords[current.EventID]; ok && incidentKey(ev) == incident && task.Source.UserID == a.UserID && (task.Source.Cancelled || task.DispatchSource.Cancelled || task.StopRequested || task.Phase == "blocked" || task.Phase == "failed" || task.Phase == "stopped") {
				stopped = true
			}
		}
		if stopped {
			st.AutoEvents[event.ID] = "stopped-no-revival"
			continue
		}
		route := e.Config.eventRoute(event)
		m := Message{ID: "auto:" + event.ID, UserID: a.UserID, ChatID: a.ChatID, ChatType: a.ChatType, TopicID: a.TopicID, InputKind: "automatic", EventID: event.ID, EventNode: event.Node, EventCheck: event.Check, EventRoute: route, RouteAtReceive: route, SessionTask: taskID, Text: "自动监控事件：请只读核对证据并提交真实调查报告；不执行生产修改。"}
		if !e.Config.TargetAllowed(m, e.Config.Routes[route], route) {
			st.AutoEvents[event.ID] = "actor-denied"
			continue
		}
		identity := event
		identity.Evidence = nil
		b, _ := json.Marshal(identity)
		m.Quote = string(b)
		target := e.Config.Routes[route]
		m.ProjectChoices = map[string]Workspace{workspaceKey(e.Config, m, route): {target.ProjectID, target.ProjectName, target.ProjectPath}}
		if len(st.Inbox) >= 256 {
			return errors.New("automatic event inbox full; cursor not advanced")
		}
		if taskID == "" {
			st.AutoIncidents[incident] = stable(m.ChatID, m.TopicID, m.ID)
		}
		st.Seen[key(m, m.ID)] = true
		st.Inbox = append(st.Inbox, m)
		st.AutoEvents[event.ID] = "queued"
	}
	return nil
}
