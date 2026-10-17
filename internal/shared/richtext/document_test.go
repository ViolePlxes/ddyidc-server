package richtext

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	cases := []struct {
		name, raw, plain string
		images           map[string]bool
		valid            bool
	}{
		{"format", `{"version":1,"doc":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"hello","marks":[{"type":"bold"}]}]}]}}`, "hello", nil, true},
		{"unknown version", `{"version":2,"doc":{"type":"doc"}}`, "", nil, false},
		{"unknown field", `{"version":1,"html":"<script>","doc":{"type":"doc"}}`, "", nil, false},
		{"script node", `{"version":1,"doc":{"type":"doc","content":[{"type":"script"}]}}`, "", nil, false},
		{"javascript link", `{"version":1,"doc":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"click","marks":[{"type":"link","attrs":{"href":"javascript:alert(1)"}}]}]}]}}`, "", nil, false},
		{"https link", `{"version":1,"doc":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"click","marks":[{"type":"link","attrs":{"href":"https://example.com"}}]}]}]}}`, "click", nil, true},
		{"bound image", `{"version":1,"doc":{"type":"doc","content":[{"type":"image","attrs":{"attachmentId":"owned"}}]}}`, "[图片]", map[string]bool{"owned": true}, true},
		{"unbound image", `{"version":1,"doc":{"type":"doc","content":[{"type":"image","attrs":{"attachmentId":"other"}}]}}`, "", map[string]bool{"owned": true}, false},
		{"external src", `{"version":1,"doc":{"type":"doc","content":[{"type":"image","attrs":{"attachmentId":"owned","src":"https://external.invalid/x"}}]}}`, "", map[string]bool{"owned": true}, false},
		{"empty", `{"version":1,"doc":{"type":"doc","content":[{"type":"paragraph"}]}}`, "", nil, false},
		{"invalid nesting", `{"version":1,"doc":{"type":"doc","content":[{"type":"text","text":"wrong"}]}}`, "", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, plain, err := Validate(json.RawMessage(c.raw), c.images)
			if (err == nil) != c.valid {
				t.Fatalf("valid=%v err=%v", c.valid, err)
			}
			if c.valid && (plain != c.plain || len(raw) == 0) {
				t.Fatalf("plain %q", plain)
			}
		})
	}
}
func TestLimitsAndPlainCompatibility(t *testing.T) {
	if raw, _, err := Validate(nil, nil); raw != nil || err != nil {
		t.Fatal("legacy content changed")
	}
	n := Node{Type: "paragraph", Content: []Node{{Type: "text", Text: strings.Repeat("a", 20001)}}}
	d := Document{Version: 1, Doc: Node{Type: "doc", Content: []Node{n}}}
	raw, _ := json.Marshal(d)
	if _, _, err := Validate(raw, nil); err == nil {
		t.Fatal("accepted too long text")
	}
	for i := 0; i < 20; i++ {
		n = Node{Type: "blockquote", Content: []Node{n}}
	}
	d.Doc.Content = []Node{n}
	raw, _ = json.Marshal(d)
	if _, _, err := Validate(raw, nil); err == nil {
		t.Fatal("accepted excessive depth")
	}
}
