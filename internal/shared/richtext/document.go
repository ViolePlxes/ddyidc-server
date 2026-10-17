// Package richtext validates versioned editor documents without accepting HTML.
package richtext

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
	"unicode/utf8"
)

type Document struct {
	Version int  `json:"version"`
	Doc     Node `json:"doc"`
}
type Node struct {
	Type    string                     `json:"type"`
	Text    string                     `json:"text,omitempty"`
	Attrs   map[string]json.RawMessage `json:"attrs,omitempty"`
	Marks   []Mark                     `json:"marks,omitempty"`
	Content []Node                     `json:"content,omitempty"`
}
type Mark struct {
	Type  string                     `json:"type"`
	Attrs map[string]json.RawMessage `json:"attrs,omitempty"`
}

var ErrInvalid = errors.New("富文本内容格式无效或包含不允许的内容")

func SafeLink(value string) bool {
	u, e := url.Parse(value)
	return e == nil && len(value) <= 2048 && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil && !strings.ContainsAny(value, "\r\n\t")
}

// Validate checks structure, resource bounds and module-provided image IDs.
// The caller must verify attachment ownership in its own write transaction.
func Validate(raw json.RawMessage, images map[string]bool) (json.RawMessage, string, error) {
	if len(raw) == 0 {
		return nil, "", nil
	}
	if len(raw) > 65536 {
		return nil, "", ErrInvalid
	}
	var d Document
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&d) != nil || d.Version != 1 || d.Doc.Type != "doc" {
		return nil, "", ErrInvalid
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, "", ErrInvalid
	}
	count := 0
	pictureCount := 0
	var text strings.Builder
	var walk func(Node, string, int) bool
	walk = func(n Node, parent string, depth int) bool {
		count++
		if depth > 16 || count > 2000 {
			return false
		}
		inline := n.Type == "text" || n.Type == "hardBreak"
		switch parent {
		case "":
			if n.Type != "doc" {
				return false
			}
		case "doc", "blockquote":
			if inline || n.Type == "listItem" || n.Type == "doc" {
				return false
			}
		case "paragraph":
			if !inline {
				return false
			}
		case "codeBlock":
			if n.Type != "text" || len(n.Marks) > 0 {
				return false
			}
		case "bulletList", "orderedList":
			if n.Type != "listItem" {
				return false
			}
		case "listItem":
			if inline || n.Type == "listItem" || n.Type == "doc" {
				return false
			}
		default:
			return false
		}
		if n.Type != "text" && (n.Text != "" || len(n.Marks) > 0) {
			return false
		}
		for k, v := range n.Attrs {
			switch {
			case n.Type == "orderedList" && k == "start":
				var i int
				if json.Unmarshal(v, &i) != nil || i < 1 || i > 10000 {
					return false
				}
			case n.Type == "codeBlock" && k == "language":
				var lang string
				if string(v) != "null" && (json.Unmarshal(v, &lang) != nil || len(lang) > 32) {
					return false
				}
			case n.Type == "image" && k == "attachmentId":
				var id string
				if json.Unmarshal(v, &id) != nil || !images[id] {
					return false
				}
			default:
				return false
			}
		}
		switch n.Type {
		case "doc", "paragraph", "blockquote", "bulletList", "orderedList", "listItem", "codeBlock":
		case "text":
			if n.Text == "" || len(n.Content) > 0 || len(n.Attrs) > 0 {
				return false
			}
			text.WriteString(n.Text)
		case "hardBreak":
			if len(n.Content) > 0 || len(n.Attrs) > 0 {
				return false
			}
			text.WriteString("\n")
		case "image":
			pictureCount++
			if pictureCount > 5 || len(n.Content) > 0 || len(n.Attrs) != 1 {
				return false
			}
			text.WriteString("[图片]")
		default:
			return false
		}
		seen := map[string]bool{}
		for _, m := range n.Marks {
			if seen[m.Type] {
				return false
			}
			seen[m.Type] = true
			switch m.Type {
			case "bold", "italic", "strike", "code":
				if len(m.Attrs) > 0 {
					return false
				}
			case "link":
				if len(m.Attrs) != 1 {
					return false
				}
				var href string
				if json.Unmarshal(m.Attrs["href"], &href) != nil || !SafeLink(href) {
					return false
				}
			default:
				return false
			}
		}
		for _, child := range n.Content {
			if !walk(child, n.Type, depth+1) {
				return false
			}
		}
		if n.Type == "paragraph" || n.Type == "codeBlock" || n.Type == "image" {
			text.WriteString("\n")
		}
		return true
	}
	if !walk(d.Doc, "", 0) {
		return nil, "", ErrInvalid
	}
	plain := strings.TrimSpace(text.String())
	if plain == "" || utf8.RuneCountInString(plain) > 20000 {
		return nil, "", ErrInvalid
	}
	out, _ := json.Marshal(d)
	return out, plain, nil
}
