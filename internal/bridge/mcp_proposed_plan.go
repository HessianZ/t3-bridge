package bridge

import (
	"context"
	"strings"
	"unicode/utf16"
)

func completeProposedPlan(item threadItem, threadID, runID string) bool {
	return item.Type == "proposed_plan" && item.Status == "completed" &&
		item.Visibility == "local" && threadID != "" && item.SourceThreadID == threadID &&
		strings.TrimSpace(runID) != "" && item.RunID == runID &&
		item.ItemID != "" && strings.TrimSpace(item.ItemID) == item.ItemID && item.UpdatedAt != "" &&
		item.Channel == "" && item.MessageID == "" && item.CreatedBy == "" && item.CreationSource == "" &&
		strings.TrimSpace(item.Text) != ""
}

func (t *T3) readProposedPlan(ctx context.Context, target Target, threadID string, item threadItem) (string, bool) {
	var text strings.Builder
	part := item
	offset := 0
	for reads := 0; ; reads++ {
		if part.Text == "" || len(part.Text) > maxResultBytes-text.Len() {
			return "", false
		}
		text.WriteString(part.Text)
		if !part.TextTruncated {
			if part.NextTextOffset != nil {
				return "", false
			}
			return text.String(), true
		}
		// T3 measures offsets in UTF-16 code units. Count the text before trusting its cursor.
		for _, r := range part.Text {
			offset += utf16.RuneLen(r)
		}
		if reads == 4 || text.Len() == maxResultBytes || part.NextTextOffset == nil || *part.NextTextOffset != offset {
			return "", false
		}

		var page threadPage
		args := map[string]any{
			"environmentId": target.EnvironmentID, "threadId": threadID, "itemId": item.ItemID,
			"view": "messages", "textOffset": offset, "maxCharsPerItem": 20000,
		}
		if err := t.Calls.Call(ctx, "t3_thread_read", args, &page); err != nil {
			return "", false
		}
		if verifyPage(target, threadID, page) != nil || len(page.Items) != 1 {
			return "", false
		}
		part = page.Items[0]
		if part.ItemID != item.ItemID || part.SourceThreadID != item.SourceThreadID ||
			part.RunID != item.RunID || part.Position != item.Position || part.Type != item.Type ||
			part.Status != item.Status || part.UpdatedAt != item.UpdatedAt || part.Visibility != item.Visibility ||
			part.Channel != item.Channel || part.MessageID != item.MessageID ||
			part.CreatedBy != item.CreatedBy || part.CreationSource != item.CreationSource {
			return "", false
		}
	}
}
