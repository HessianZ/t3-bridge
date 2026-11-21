package bridge

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// A settled wait can name a thread-wide result. Use the original run when it
// had no delegates, or a contiguous chain of wakes attributable to it.
func (t *T3) attributedResultRun(ctx context.Context, target Target, task *Task) (string, error) {
	var delegations []threadItem
	inputs := map[string][]threadItem{}
	position := -1
	for n := 0; n < 100; n++ {
		var page struct {
			threadPage
			More *bool `json:"hasMore"`
		}
		args := map[string]any{
			"environmentId": target.EnvironmentID, "threadId": task.ThreadID,
			"view": "activity", "afterPosition": position, "limit": 100, "runLimit": 50, "maxCharsPerItem": 512,
		}
		if err := t.Calls.Call(ctx, "t3_thread_read", args, &page); err != nil {
			return "", err
		}
		if err := verifyPage(target, task.ThreadID, page.threadPage); err != nil {
			return "", err
		}
		if page.More == nil || page.Items == nil || len(page.Items) > 100 || len(page.RecentRuns) > 50 {
			return "", errors.New("invalid T3 delegated activity page")
		}
		for _, item := range page.Items {
			if item.Position <= position {
				return "", errors.New("invalid T3 delegated activity pagination")
			}
			position = item.Position
			local := item.Visibility == "local" && item.SourceThreadID == task.ThreadID
			switch item.Type {
			case "subagent":
				delegations = append(delegations, item)
			case "notification":
				inputs[item.RunID] = append(inputs[item.RunID], item)
			case "user_message":
				if local {
					inputs[item.RunID] = append(inputs[item.RunID], item)
				}
			}
		}
		if !*page.More {
			// Every page reports the thread's latest run rows, independently of the item cursor.
			return lastDelegatedWake(target, task, page.RecentRuns, delegations, inputs), nil
		}
		if len(page.Items) == 0 || page.NextPosition == nil || *page.NextPosition != position {
			return "", errors.New("invalid T3 delegated activity pagination")
		}
	}
	return "", errors.New("T3 delegated activity exceeds bounded reconciliation scan")
}

func lastDelegatedWake(target Target, task *Task, recent []threadRun, delegations []threadItem, inputs map[string][]threadItem) string {
	runs := map[int]threadRun{}
	runOrdinals := map[string]int{}
	original, latest := 0, 0
	for _, run := range recent {
		if run.Ordinal <= 0 || run.RunID == "" || runOrdinals[run.RunID] != 0 {
			return ""
		}
		ordinal := run.Ordinal
		latest = max(latest, ordinal)
		runOrdinals[run.RunID] = ordinal
		if run.RunID == task.RunID {
			original = ordinal
		}
		if _, duplicate := runs[ordinal]; duplicate {
			return ""
		}
		runs[ordinal] = run
	}
	verified := func(run threadRun) bool {
		return run.RunID != "" && nativeTerminal(run.Status) &&
			run.Model == target.Model && run.ProviderID == target.ProviderID &&
			(run.ExecutedModel == nil || *run.ExecutedModel == target.Model)
	}
	if runs[original].RunID != task.RunID || !verified(runs[original]) {
		return ""
	}
	// Earlier completed wakes can account for a reused title only when their
	// entire delivery was itself attributable to one exact cohort.
	positions := make([]int, 0, len(runs))
	for ordinal := range runs {
		if ordinal < original {
			positions = append(positions, ordinal)
		}
	}
	slices.Sort(positions)
	accounted := map[string]bool{}
	for _, ordinal := range positions {
		run := runs[ordinal]
		if verified(run) && run.Status == "completed" {
			_, ids := matchDelegatedWake(task.ThreadID, ordinal, inputs[run.RunID], delegations, runOrdinals, accounted, nil)
			for _, id := range ids {
				accounted[id] = true
			}
		}
	}
	owned := map[string]bool{task.RunID: true}
	var result string
	for ordinal := original + 1; verified(runs[ordinal]); ordinal++ {
		run := runs[ordinal]
		ok, ids := matchDelegatedWake(task.ThreadID, ordinal, inputs[run.RunID], delegations, runOrdinals, accounted, owned)
		if !ok {
			break
		}
		if run.Status == "completed" {
			for _, id := range ids {
				accounted[id] = true
			}
		}
		owned[run.RunID] = true
		result = run.RunID
	}
	if result == "" && len(task.ChildThreads) == 0 && len(task.TaskIDs) == 0 &&
		!slices.ContainsFunc(delegations, func(item threadItem) bool {
			return item.RunID == "" || item.RunID == task.RunID
		}) {
		return task.RunID
	}
	if result != "" && runOrdinals[result] < latest {
		// Recovering an older wake is only safe after all of the owned
		// delegation evidence has been accounted for.
		for _, item := range delegations {
			if owned[item.RunID] && item.SourceThreadID == task.ThreadID && !accounted[item.SubagentID] {
				return ""
			}
		}
	}
	return result
}

func matchDelegatedWake(threadID string, ordinal int, inputs, delegations []threadItem, runOrdinals map[string]int, accounted, owned map[string]bool) (bool, []string) {
	if len(inputs) == 0 {
		return false, nil
	}
	var consumed []string
	for _, input := range inputs {
		if input.Type != "notification" || input.Status != "completed" || input.Visibility != "local" ||
			input.SourceThreadID != threadID || input.TextTruncated || input.NextTextOffset != nil {
			return false, nil
		}
		labels, denominator := delegatedNoticeLabels(input.Text)
		if len(labels) == 0 {
			return false, nil
		}
		matches := delegatedNoticeCohorts(input.Position, ordinal, labels, denominator, delegations, runOrdinals, accounted)
		if len(matches) == 0 {
			return false, nil
		}
		for _, match := range matches {
			if !match.certain || match.threadID != threadID || (owned != nil && !owned[match.runID]) {
				return false, nil
			}
		}
		if len(matches) == 1 {
			consumed = append(consumed, matches[0].ids...)
		}
	}
	return true, consumed
}

type delegatedCohort struct {
	threadID, runID string
	certain         bool
	ids             []string
}

func delegatedNoticeCohorts(position, ordinal int, labels []string, denominator int, delegations []threadItem, runOrdinals map[string]int, accounted map[string]bool) []delegatedCohort {
	type origin struct{ thread, run string }
	wanted := map[string]int{}
	for _, label := range labels {
		wanted[label]++
	}
	groups := map[origin][]threadItem{}
	for _, item := range delegations {
		if item.Position < position && runOrdinals[item.RunID] < ordinal {
			key := origin{item.SourceThreadID, item.RunID}
			groups[key] = append(groups[key], item)
		}
	}
	var matches []delegatedCohort
	for key, group := range groups {
		seen := map[string]bool{}
		candidates := map[string][]string{}
		unknown, total := 0, 0
		ambiguousLabels := false
		for _, item := range group {
			id := item.SubagentID
			if id != "" {
				if seen[id] {
					continue
				}
				seen[id] = true
			}
			total++
			if accounted[id] {
				continue
			}
			label := delegatedNoticeLabel(item.Title)
			if len(labels) > 1 && strings.Contains(label, ", ") {
				ambiguousLabels = true
			}
			if id == "" || label == "" || !nativeTerminal(item.Status) || item.Visibility != "local" {
				unknown++
				continue
			}
			candidates[label] = append(candidates[label], id)
		}
		if total < denominator {
			continue
		}
		// T3 joins labels without escaping. A source title containing the
		// separator prevents unique segmentation of its possible notice.
		if ambiguousLabels {
			matches = append(matches, delegatedCohort{threadID: key.thread, runID: key.run})
			continue
		}
		missing := 0
		var ids []string
		for label, count := range wanted {
			have := candidates[label]
			if len(have) < count {
				missing += count - len(have)
			}
			if len(have) == count {
				ids = append(ids, have...)
			}
		}
		if missing <= unknown {
			certain := missing == 0 && key.run != "" && key.thread != "" && runOrdinals[key.run] != 0
			if !certain || unknown != 0 || len(ids) != len(labels) {
				ids = nil
			}
			matches = append(matches, delegatedCohort{key.thread, key.run, certain, ids})
		}
	}
	return matches
}

// These are the public summaries emitted by T3's delegated completion renderer.
// Group labels are usable only when the rendered label count binds the entire list.
func delegatedNoticeLabels(text string) ([]string, int) {
	if strings.Contains(text, "\n") {
		return nil, 0
	}
	if label, ok := strings.CutPrefix(text, `Delegated task "`); ok {
		for _, verb := range []string{"finished", "failed", "stopped"} {
			if value, matched := strings.CutSuffix(label, `" `+verb); matched && value != "" {
				return []string{value}, 1
			}
		}
		return nil, 0
	}
	countText, rest, ok := strings.Cut(text, " delegated tasks ")
	if !ok {
		return nil, 0
	}
	countText, totalText, partial := strings.Cut(countText, " of ")
	count, err := strconv.Atoi(countText)
	if err != nil || count < 2 || strconv.Itoa(count) != countText {
		return nil, 0
	}
	total := count
	if partial {
		total, err = strconv.Atoi(totalText)
		if err != nil || total <= count || strconv.Itoa(total) != totalText {
			return nil, 0
		}
	}
	verb, names, found := strings.Cut(rest, ": ")
	if !found || (verb != "finished" && verb != "failed" && verb != "stopped") {
		return nil, 0
	}
	labels := strings.Split(names, ", ")
	if len(labels) != count || slices.Contains(labels, "") {
		return nil, 0
	}
	return labels, total
}

func delegatedNoticeLabel(title string) string {
	// Match ECMAScript trim before applying T3's UTF-16 label length.
	space := func(r rune) bool { return r == '\uFEFF' || (r != '\u0085' && unicode.IsSpace(r)) }
	line, _, _ := strings.Cut(strings.TrimFunc(title, space), "\n")
	line = strings.TrimFunc(line, space)
	units, cutoff := 0, len(line)
	for offset, r := range line {
		units++
		if r >= 0x10000 {
			units++
		}
		if units >= 80 && cutoff == len(line) {
			cutoff = offset
		}
		if units > 80 {
			return line[:cutoff] + "…"
		}
	}
	// An untitled task's fallback display title can have been clipped
	// before the notification renderer sees its original prompt.
	if (units == 71 || units == 72) && strings.HasSuffix(line, "...") {
		return ""
	}
	return line
}
