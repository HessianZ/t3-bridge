package bridge

import (
	"encoding/json"
	"errors"
	"strings"
)

type quoteCard struct {
	Schema   string          `json:"schema"`
	UserDSL  json.RawMessage `json:"user_dsl"`
	Header   json.RawMessage `json:"header"`
	Body     json.RawMessage `json:"body"`
	Elements json.RawMessage `json:"elements"`
}

var errInvalidQuotedCard = errors.New("invalid quoted card")

func quotedCardText(raw []byte) (string, error) {
	var card quoteCard
	if json.Unmarshal(raw, &card) != nil {
		return "", errInvalidQuotedCard
	}
	if card.UserDSL != nil {
		// The envelope's elements can be a client downgrade message.
		var embedded string
		if json.Unmarshal(card.UserDSL, &embedded) != nil || len(embedded) > maxQuoteRawBytes {
			return "", errInvalidQuotedCard
		}
		raw = []byte(embedded)
		card = quoteCard{}
		if json.Unmarshal(raw, &card) != nil || card.UserDSL != nil {
			return "", errInvalidQuotedCard
		}
	}

	elements := card.Elements
	switch card.Schema {
	case "2.0":
		var body struct {
			Elements json.RawMessage `json:"elements"`
		}
		if card.Body != nil && json.Unmarshal(card.Body, &body) != nil {
			return "", errInvalidQuotedCard
		}
		elements = body.Elements
	case "", "1.0":
		var rows quoteRows
		if json.Unmarshal(raw, &rows) == nil &&
			(len(rows.Elements) > 0 || rows.Title != "" || card.Header == nil) {
			return quotedRows(rows.Title, rows.Elements, false, "", nil)
		}
	default:
		return "", errInvalidQuotedCard
	}

	w := quoteCardWalker{legacy: card.Schema != "2.0"}
	if card.Header != nil {
		var header struct {
			Title    json.RawMessage `json:"title"`
			Subtitle json.RawMessage `json:"subtitle"`
		}
		if json.Unmarshal(card.Header, &header) != nil {
			return "", errInvalidQuotedCard
		}
		for _, part := range []json.RawMessage{header.Title, header.Subtitle} {
			if err := w.addNode(part, 0, quoteCardTextSlot); err != nil {
				return "", err
			}
		}
	}
	if err := w.addNodes(elements, 0, quoteCardElementSlot); err != nil {
		return "", err
	}
	return strings.TrimSpace(w.text.String()), nil
}

type quoteCardSlot uint8

const (
	quoteCardElementSlot quoteCardSlot = iota
	quoteCardTextSlot
	quoteCardFieldSlot
	quoteCardColumnSlot
)

type quoteCardWalker struct {
	text   strings.Builder
	nodes  int
	legacy bool
}

func (w *quoteCardWalker) addNodes(raw json.RawMessage, depth int, slot quoteCardSlot) error {
	if raw == nil {
		return nil
	}
	var nodes []json.RawMessage
	if json.Unmarshal(raw, &nodes) != nil {
		return errInvalidQuotedCard
	}
	for _, node := range nodes {
		if err := w.addNode(node, depth, slot); err != nil {
			return err
		}
	}
	return nil
}

func (w *quoteCardWalker) addNode(raw json.RawMessage, depth int, slot quoteCardSlot) error {
	if raw == nil {
		return nil
	}
	if depth > maxQuoteDepth {
		return errors.New("quoted content nesting outside bounds")
	}
	w.nodes++
	if w.nodes > maxQuoteNodes {
		return errors.New("too many quoted elements")
	}
	var node struct {
		Tag      string          `json:"tag"`
		Content  json.RawMessage `json:"content"`
		Text     json.RawMessage `json:"text"`
		Fields   json.RawMessage `json:"fields"`
		Elements json.RawMessage `json:"elements"`
		Columns  json.RawMessage `json:"columns"`
	}
	if json.Unmarshal(raw, &node) != nil {
		return errInvalidQuotedCard
	}
	switch slot {
	case quoteCardFieldSlot:
		if node.Tag == "" {
			return w.addNode(node.Text, depth+1, quoteCardTextSlot)
		}
		return nil
	case quoteCardColumnSlot:
		if node.Tag == "column" {
			return w.addNodes(node.Elements, depth+1, quoteCardElementSlot)
		}
		return nil
	}
	switch node.Tag {
	case "markdown", "plain_text", "lark_md":
		var content string
		if node.Content != nil && json.Unmarshal(node.Content, &content) != nil {
			return errInvalidQuotedCard
		}
		if strings.TrimSpace(content) != "" {
			if w.text.Len() > 0 {
				w.text.WriteByte('\n')
			}
			if w.text.Len()+len(content) > maxQuoteTextBytes {
				return errors.New("quoted text outside bounds")
			}
			w.text.WriteString(content)
		}
	case "div":
		if slot == quoteCardElementSlot {
			if err := w.addNode(node.Text, depth+1, quoteCardTextSlot); err != nil {
				return err
			}
			if w.legacy {
				return w.addNodes(node.Fields, depth+1, quoteCardFieldSlot)
			}
		}
	case "note":
		if slot == quoteCardElementSlot && w.legacy {
			return w.addNodes(node.Elements, depth+1, quoteCardTextSlot)
		}
	case "column_set":
		if slot == quoteCardElementSlot {
			return w.addNodes(node.Columns, depth+1, quoteCardColumnSlot)
		}
	}
	return nil
}
