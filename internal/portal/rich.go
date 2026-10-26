package portal

import (
	"ddyidc/internal/shared/richtext"
	"encoding/json"
	"html"
	"html/template"
	"strconv"
	"strings"
)

// Only the shared validated document is rendered. User HTML is always escaped;
// tags and attributes below are fixed, never taken directly from editor input.
func renderRichBody(m Module) (template.HTML, error) {
	canonical, _, err := richtext.Validate(m.RichContent, nil)
	if err != nil {
		return "", err
	}
	if len(canonical) == 0 {
		return template.HTML(html.EscapeString(m.Text)), nil
	}
	var document richtext.Document
	if err = json.Unmarshal(canonical, &document); err != nil {
		return "", err
	}
	var render func(richtext.Node) string
	render = func(node richtext.Node) string {
		var children strings.Builder
		for _, child := range node.Content {
			children.WriteString(render(child))
		}
		content := children.String()
		if node.Type == "text" {
			content = html.EscapeString(node.Text)
			for _, mark := range node.Marks {
				tag := map[string]string{"bold": "strong", "italic": "em", "strike": "s", "code": "code"}[mark.Type]
				if tag != "" {
					content = "<" + tag + ">" + content + "</" + tag + ">"
				}
				if mark.Type == "link" {
					var href string
					_ = json.Unmarshal(mark.Attrs["href"], &href)
					content = `<a href="` + html.EscapeString(href) + `" target="_blank" rel="noopener noreferrer">` + content + `</a>`
				}
			}
			return content
		}
		switch node.Type {
		case "doc":
			return content
		case "hardBreak":
			return "<br>"
		case "codeBlock":
			return "<pre><code>" + content + "</code></pre>"
		case "orderedList":
			start := 1
			if raw, ok := node.Attrs["start"]; ok {
				_ = json.Unmarshal(raw, &start)
			}
			return `<ol start="` + strconv.Itoa(start) + `">` + content + `</ol>`
		default:
			tag := map[string]string{"paragraph": "p", "blockquote": "blockquote", "bulletList": "ul", "listItem": "li"}[node.Type]
			return "<" + tag + ">" + content + "</" + tag + ">"
		}
	}
	return template.HTML(render(document.Doc)), nil
}
