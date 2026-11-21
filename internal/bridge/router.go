package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

type Decision struct {
	RouteID string `json:"routeId"`
	Action  string `json:"action"`
}
type Classifier interface {
	Classify(context.Context, Message, []string, string) (Decision, error)
}

func control(text string) bool {
	return slices.Contains([]string{"停止", "取消", "拒绝", "不要执行", "先别执行", "/stop", "/cancel", "stop", "cancel", "停止这个任务", "取消这个任务", "不要继续", "拒绝执行", "先停止", "不用了"}, strings.ToLower(strings.TrimSpace(text)))
}

func routeText(m Message) string {
	if m.Comment != nil {
		return ""
	}
	if m.RoutingText != "" {
		return m.RoutingText
	}
	return m.Text
}

var restrictedExecution = regexp.MustCompile(`^(请)?(不要执行|先别执行)命令[，,；;]`)
var boundRefusal = regexp.MustCompile(`^(请)?(停止|取消|拒绝|先别执行|不要执行|不要继续|别再执行|先别升级|不要升级|先别重启|不要重启)`)

// Only a leading, configured command selects a route; its body remains user text.
func explicitRoute(c Config, text string) (string, string) {
	text = strings.TrimSpace(text)
	if split := strings.IndexFunc(text, unicode.IsSpace); split >= 0 {
		command, body := text[:split], text[split:]
		if ids := c.namedRoutes(strings.TrimPrefix(command, "/"), false); strings.HasPrefix(command, "/") && len(ids) == 1 {
			return ids[0], strings.TrimSpace(body)
		}
	}
	return "", ""
}

func isStop(c Config, text string, bound *Task) bool {
	if id, body := explicitRoute(c, text); id != "" {
		return (bound == nil || bound.RouteID == "" || bound.RouteID == id) && control(body)
	}
	if control(text) {
		return true
	}
	text = strings.TrimSpace(text)
	if bound == nil || !boundRefusal.MatchString(text) {
		return false
	}
	// Limiting command execution while asking for an answer is not task cancellation.
	return !restrictedExecution.MatchString(text)
}

// Without a default route, discussion and quotations retain the legacy conservative rules.
func Rules(c Config, m Message, bound *Task) (Decision, bool) {
	text := strings.TrimSpace(routeText(m))
	if isStop(c, text, bound) {
		return Decision{Action: "stop"}, true
	}
	if m.EventRoute != "" && bound != nil && bound.RouteID != "" && bound.RouteID != m.EventRoute {
		return Decision{Action: "clarify"}, true
	}
	if id, body := explicitRoute(c, text); id != "" {
		if targets, op := c.operationRoutes(body); op && (len(targets) != 1 || targets[0] != id) {
			return Decision{Action: "clarify"}, true
		}
		if id == c.ReviewRoute || (m.EventRoute != "" && id != m.EventRoute) {
			return Decision{Action: "clarify"}, true
		}
		if bound != nil {
			if bound.RouteID != id {
				return Decision{Action: "clarify"}, true
			}
			return Decision{RouteID: id, Action: "continue"}, true
		}
		return Decision{RouteID: id, Action: "new_task"}, true
	}
	ids, operation := c.operationRoutes(text)
	if operation && (len(ids) != 1 || ids[0] == c.ReviewRoute) {
		if len(ids) > 1 && bound == nil && m.EventRoute == "" {
			return Decision{Action: "coordinate"}, true
		}
		return Decision{Action: "clarify"}, true
	}
	if m.EventRoute != "" {
		if m.EventRoute == c.ReviewRoute || (operation && ids[0] != m.EventRoute) {
			return Decision{Action: "clarify"}, true
		}
		return Decision{RouteID: m.EventRoute, Action: "new_task"}, true
	}
	if bound != nil {
		if operation && ids[0] != bound.RouteID {
			return Decision{Action: "clarify"}, true
		}
		return Decision{RouteID: bound.RouteID, Action: "continue"}, true
	}
	if c.DefaultRoute == "" {
		lower := strings.ToLower(text)
		for _, word := range []string{"不要", "别执行", "不升级", "不重启", "不部署", "写文章", "写一篇", "写篇", "文章", "介绍", "教程", "假设", "引用", "例如", "不在", "先讨论", "方案", "```", "“", "\"", ">"} {
			if strings.Contains(lower, word) {
				return Decision{Action: "clarify"}, true
			}
		}
		if len(c.mentionedRoutes(text)) > 1 {
			return Decision{Action: "coordinate"}, true
		}
	}
	if operation {
		return Decision{RouteID: ids[0], Action: "new_task"}, true
	}

	return Decision{}, false
}

func Route(ctx context.Context, c Config, m Message, bound *Task, classifier Classifier) (Decision, error) {
	if m.Comment != nil {
		if bound != nil && bound.RouteID == c.CommentRoute {
			return Decision{RouteID: c.CommentRoute, Action: "continue"}, nil
		}
		return Decision{RouteID: c.CommentRoute, Action: "new_task"}, nil
	}
	if d, ok := Rules(c, m, bound); ok {
		return d, nil
	}
	if c.DefaultRoute != "" && !c.TargetAllowed(m, c.Routes[c.DefaultRoute], c.DefaultRoute) {
		return Decision{Action: "clarify"}, nil
	}
	if classifier == nil {
		if c.Classifier == (Hy4Config{}) && c.DefaultRoute != "" && !unknownDefaultTarget(c, routeText(m)) {
			return Decision{RouteID: c.DefaultRoute, Action: "new_task"}, nil
		}
		return Decision{Action: "clarify"}, nil
	}
	ids := []string{}
	for id, t := range c.Routes {
		if id != c.ReviewRoute && c.TargetAllowed(m, t, id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	m.routeNames = map[string][]string{}
	for _, id := range ids {
		m.routeNames[id] = c.routeNames(id)
	}
	d, err := classifier.Classify(ctx, m, ids, c.DefaultRoute)
	if err != nil {
		return Decision{}, err
	}
	if c.ReviewRoute != "" && d.RouteID == c.ReviewRoute {
		return Decision{}, errors.New("review route requires the explicit review entry")
	}
	if !slices.Contains([]string{"new_task", "continue", "coordinate", "clarify"}, d.Action) {
		return Decision{}, errors.New("invalid classifier action")
	}
	if d.Action == "continue" {
		return Decision{Action: "clarify"}, nil
	} // Only a durable, scoped binding can select a thread.
	if d.Action == "new_task" && !slices.Contains(ids, d.RouteID) {
		return Decision{}, errors.New("classifier selected an unapproved route")
	}
	return d, nil
}

func decodeStrict(b []byte, out any) error {
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if d.More() {
		return errors.New("extra JSON")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("extra JSON")
	}
	return nil
}
