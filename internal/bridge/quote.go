package bridge

import (
	"encoding/json"
	"errors"
	"strings"
)

const (
	maxQuoteRawBytes  = 32768
	maxQuoteTextBytes = 8000
	maxQuoteNodes     = 256
	maxQuoteDepth     = 4
)

// Normalized post/card content uses rows.
// Only visible fields are decoded; value/actions/URLs never enter the quote.
type quoteNode struct {
	Tag      string      `json:"tag"`
	Text     string      `json:"text"`
	ImageKey string      `json:"image_key"`
	Elements []quoteNode `json:"elements"`
}

type quoteRows struct {
	Title    string        `json:"title"`
	Content  [][]quoteNode `json:"content"`
	Elements [][]quoteNode `json:"elements"`
}

func quotedContent(kind, raw, parent string) (string, []Image, error) {
	if len(raw) > maxQuoteRawBytes {
		return "", nil, errors.New("quoted content outside bounds")
	}
	var text string
	images := []Image{}
	switch kind {
	case "text":
		var body struct {
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(raw), &body) != nil {
			return "", nil, errors.New("invalid quoted text")
		}
		text = body.Text
	case "file":
		var body struct {
			Key  string `json:"file_key"`
			Name string `json:"file_name"`
		}
		if json.Unmarshal([]byte(raw), &body) != nil || body.Key == "" {
			return "", nil, errors.New("invalid quoted file")
		}
		images = append(images, Image{Key: body.Key, Name: body.Name, MessageID: parent, Kind: "file"})
		text = "[文件资料：" + attachmentName(body.Name, "file") + "]"
	case "image":
		var body struct {
			ImageKey string `json:"image_key"`
		}
		if json.Unmarshal([]byte(raw), &body) != nil || body.ImageKey == "" {
			return "", nil, errors.New("invalid quoted image")
		}
		images = append(images, Image{Key: body.ImageKey, MessageID: parent})
	case "post", "interactive":
		var err error
		if kind == "interactive" {
			text, err = quotedCardText([]byte(raw))
		} else {
			var body quoteRows
			if json.Unmarshal([]byte(raw), &body) != nil {
				return "", nil, errors.New("invalid quoted post/card")
			}
			text, err = quotedRows(body.Title, body.Content, true, parent, &images)
		}
		if err != nil {
			return "", nil, err
		}
		if text == "" && len(images) == 0 {
			return "", nil, errors.New("quote has no supported visible content")
		}
	default:
		return "", nil, errors.New("unsupported quote type")
	}
	if len(text) > maxQuoteTextBytes || len(images) > 4 {
		return "", nil, errors.New("quote outside bounds")
	}
	return text, images, nil
}

func quotedRows(title string, rows [][]quoteNode, post bool, parent string, images *[]Image) (string, error) {
	parts := []string{title}
	nodes := 0
	for _, row := range rows {
		line, err := quoteNodes(row, post, parent, 0, &nodes, images)
		if err != nil {
			return "", err
		}
		parts = append(parts, line)
	}
	return strings.TrimSpace(strings.Join(parts, "\n")), nil
}

func quoteNodes(nodes []quoteNode, post bool, parent string, depth int, count *int, images *[]Image) (string, error) {
	if depth > maxQuoteDepth {
		return "", errors.New("quoted content nesting outside bounds")
	}
	var text strings.Builder
	for _, node := range nodes {
		*count += 1
		if *count > maxQuoteNodes {
			return "", errors.New("too many quoted elements")
		}
		switch node.Tag {
		case "text", "a", "code_block":
			text.WriteString(node.Text)
		case "img":
			if post && node.ImageKey != "" {
				*images = append(*images, Image{Key: node.ImageKey, MessageID: parent})
				if len(*images) > 4 {
					return "", errors.New("too many quoted images")
				}
			}
		case "note":
			if !post {
				note, err := quoteNodes(node.Elements, false, parent, depth+1, count, images)
				if err != nil {
					return "", err
				}
				text.WriteString(note)
			}
		}
		if text.Len() > maxQuoteTextBytes {
			return "", errors.New("quoted text outside bounds")
		}
	}
	return text.String(), nil
}
