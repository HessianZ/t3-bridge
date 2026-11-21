package bridge

import (
	"net/url"
	"strings"
	"unicode"
)

// taskThreadURL accepts a hosted URL returned by T3 for this exact thread.
func taskThreadURL(t *Task) string {
	if t == nil {
		return ""
	}
	return verifiedThreadURL(t.Link, t.Target.EnvironmentID, t.ThreadID)
}

func verifiedThreadURL(address, environmentID, threadID string) string {
	if environmentID == "" || threadID == "" || len(address) > 2048 || strings.ContainsAny(address, "?#\\") ||
		strings.ContainsFunc(address, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return ""
	}
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return ""
	}
	path, ok := strings.CutPrefix(u.EscapedPath(), "/threads/")
	if !ok {
		return ""
	}
	environment, thread, ok := strings.Cut(path, "/")
	if !ok || strings.ContainsRune(thread, '/') {
		return ""
	}
	environment, environmentErr := url.PathUnescape(environment)
	thread, threadErr := url.PathUnescape(thread)
	if environmentErr != nil || threadErr != nil ||
		environment != environmentID || thread != threadID ||
		environment == "." || environment == ".." || thread == "." || thread == ".." {
		return ""
	}
	return address
}

func taskThreadText(t *Task) string {
	if address := taskThreadURL(t); address != "" {
		return address
	}
	if t == nil || t.Target.EnvironmentID == "" || strings.ContainsRune(t.Target.EnvironmentID, '/') || t.ThreadID == "" || len(t.Link) > 2048 {
		return ""
	}
	// T3's older internal links may be wrapped as Markdown, with a mutable title.
	want := "t3-thread://v2/" + t.Target.EnvironmentID + "/" + t.ThreadID
	if strings.ContainsFunc(want, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) ||
		strings.ContainsAny(want, "<>?#\\") {
		return ""
	}
	address := t.Link
	if label, destination, ok := strings.Cut(address, "]("); ok && strings.HasPrefix(label, "[") {
		address, _ = strings.CutSuffix(destination, ")")
	}
	if address == want || address == "<"+want+">" {
		return want
	}
	return ""
}
