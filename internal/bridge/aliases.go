package bridge

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

func (c Config) validateRouteNames() error {
	owners := map[string]string{}
	for id := range c.Routes {
		for name, old := range owners {
			if strings.EqualFold(name, id) && old != id {
				return errors.New("route IDs must be distinct ignoring case")
			}
		}
		owners[id] = id
	}
	for id, t := range c.Routes {
		if len(t.Aliases) > 16 {
			return errors.New("route aliases exceed limit")
		}
		seen := map[string]bool{}
		for i, name := range append([]string{t.HostName}, t.Aliases...) {
			if i == 0 && name == "" {
				continue
			}
			if name == "" || utf8.RuneCountInString(name) > 64 || strings.ContainsFunc(name, func(r rune) bool {
				return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("._-", r)
			}) || slices.Contains(sessionCommands, "/"+strings.ToLower(name)) {
				return errors.New("hostName/aliases require bounded names, not commands or whitespace")
			}
			folded := strings.ToLower(name)
			if t.Kind != "" && (folded == "nas" || folded == "mac") && folded != t.Kind {
				return errors.New("alias conflicts with machine kind")
			}
			for prior := range seen {
				if strings.EqualFold(prior, name) {
					return errors.New("duplicate route hostName/alias")
				}
			}
			seen[name] = true
			for prior, old := range owners {
				if strings.EqualFold(prior, name) && old != id {
					return errors.New("hostName/alias conflicts with another route")
				}
			}
			owners[name] = id
		}
	}
	return nil
}
func (c Config) routeNames(id string) []string {
	t := c.Routes[id]
	names := []string{id}
	if t.HostName != "" {
		names = append(names, t.HostName)
	}
	return append(names, t.Aliases...)
}
func (c Config) namedRoutes(name string, kind bool) []string {
	ids := []string{}
	for id, t := range c.Routes {
		match := kind && id != c.ReviewRoute && strings.EqualFold(name, t.Kind)
		for _, n := range c.routeNames(id) {
			match = match || strings.EqualFold(name, n)
		}
		if match {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

// Preserve the existing operation grammar; only its machine vocabulary is configured.
const operationVerb = `(?:检查|查看|排查|升级|更新|重启|部署|修复|安装|停止|备份|清理|执行|运行)`
const operationLead = `(?i)^(?:请(?:帮我)?|帮我|麻烦)?\s*(?:现在\s*)?`

var unknownHostOperation = regexp.MustCompile(operationLead + `(?:在|去)\s*([a-z0-9_.-]+)上[，,:：\s]*` + operationVerb)

func (c Config) operationRoutes(text string) ([]string, bool) {
	names := []string{"nas", "mac"} // Legacy kinds remain recognizable, including unavailable kinds.
	for id := range c.Routes {
		names = append(names, c.routeNames(id)...)
	}
	slices.SortFunc(names, func(a, b string) int { return len(b) - len(a) })
	escaped := make([]string, 0, len(names))
	for _, name := range names {
		escaped = append(escaped, regexp.QuoteMeta(name))
	}
	token := `(?:` + strings.Join(escaped, "|") + `)`
	group := `(` + token + `(?:\s*(?:和|与|及|、|,|，|&|and)\s*` + token + `)*)`
	prefix := regexp.MustCompile(operationLead + `(?:在\s*|去\s*)?` + group + `\s*(?:上)?(?:都|分别)?[，,:：\s]*` + operationVerb)
	match := prefix.FindStringSubmatch(text)
	if match == nil {
		suffix := regexp.MustCompile(operationLead + operationVerb + `\s*` + group + `(?:上|的|[，,:：\s]|$)`)
		match = suffix.FindStringSubmatch(text)
	}
	if match == nil {
		return nil, unknownHostOperation.MatchString(text)
	}
	ids := []string{}
	for _, name := range regexp.MustCompile(`(?i)`+token).FindAllString(match[1], -1) {
		found := c.namedRoutes(name, true)
		if len(found) != 1 {
			return nil, true
		} // Unknown/ambiguous kind must never fall back to default.
		if !slices.Contains(ids, found[0]) {
			ids = append(ids, found[0])
		}
	}
	return ids, true
}

func requestedRoute(c Config, text string) string {
	if id, _ := explicitRoute(c, text); id != "" {
		return id
	}
	ids, ok := c.operationRoutes(strings.TrimSpace(text))
	if ok && len(ids) == 1 {
		return ids[0]
	}
	return ""
}

// Legacy no-default ambiguity stays conservative, but uses configured vocabulary.
func (c Config) mentionedRoutes(text string) []string {
	ids := []string{}
	for id, t := range c.Routes {
		if id == c.ReviewRoute {
			continue
		}
		for _, name := range append(c.routeNames(id), t.Kind) {
			if name != "" && strings.Contains(strings.ToLower(text), strings.ToLower(name)) {
				ids = append(ids, id)
				break
			}
		}
	}
	return ids
}

// With no semantic classifier, uncertain leading machine operations cannot be
// silently delivered to the default. Quoted/discussion text is not searched.
var unknownMachinePrefix = regexp.MustCompile(operationLead + `(?:在|去)\s*[\p{L}\p{N}_.-]+?(?:上)?(?:都|分别)?[，,:：\s]*` + operationVerb)
var unknownMachineSuffix = regexp.MustCompile(operationLead + operationVerb + `\s*[\p{L}\p{N}_.-]+?上`)
var unknownMultiMachine = regexp.MustCompile(operationLead + `(?:在|去)?[\p{L}\p{N}_.-]+?(?:和|与|及|、|&)[\p{L}\p{N}_.-]+?(?:都|分别)?[，,:：\s]*` + operationVerb)

func unknownDefaultTarget(c Config, text string) bool {
	text = strings.TrimSpace(text)
	return slashWord(c, text) != "" || unknownMachinePrefix.MatchString(text) || unknownMachineSuffix.MatchString(text) || unknownMultiMachine.MatchString(text)
}
