package main

import (
	"fmt"
	"strings"
)

type configChange struct {
	Header, Removed, Added string
}

func configChanges(original, candidate string) []configChange {
	if original == candidate {
		return nil
	}
	before := strings.SplitAfter(original, "\n")
	after := strings.SplitAfter(candidate, "\n")
	if before[len(before)-1] == "" {
		before = before[:len(before)-1]
	}
	if after[len(after)-1] == "" {
		after = after[:len(after)-1]
	}
	for len(before) > 0 && len(after) > 0 && before[len(before)-1] == after[len(after)-1] {
		before = before[:len(before)-1]
		after = after[:len(after)-1]
	}
	var changes []configChange
	for i, j := 0, 0; i < len(before) || j < len(after); {
		if i < len(before) && j < len(after) && before[i] == after[j] {
			i++
			j++
			continue
		}
		startI, startJ := i, j
		for i < len(before) && j < len(after) && before[i] != after[j] {
			i, j = nextConfigMatch(before, after, i, j)
		}
		if i == len(before) || j == len(after) {
			i, j = len(before), len(after)
		}
		changes = append(changes, configChange{
			Header:  fmt.Sprintf("@@ -%s +%s @@", diffRange(startI, i), diffRange(startJ, j)),
			Removed: diffLines(before[startI:i], "-"),
			Added:   diffLines(after[startJ:j], "+"),
		})
	}
	return changes
}

// Look ahead for nearby insertions and deletions without comparing every pair of lines.
func nextConfigMatch(before, after []string, i, j int) (int, int) {
	for offset := 1; offset <= 32; offset++ {
		if i+offset < len(before) && before[i+offset] == after[j] {
			return i + offset, j
		}
		if j+offset < len(after) && before[i] == after[j+offset] {
			return i, j + offset
		}
	}
	return i + 1, j + 1
}

func diffRange(start, end int) string {
	if start == end {
		return fmt.Sprintf("%d,0", start)
	}
	return fmt.Sprintf("%d,%d", start+1, end-start)
}

func diffLines(lines []string, prefix string) string {
	var marked strings.Builder
	for _, line := range lines {
		marked.WriteString(prefix)
		marked.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			marked.WriteString("\n\\ No newline at end of file\n")
		}
	}
	return marked.String()
}
