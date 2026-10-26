package portal

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNavigationThreeLevelsAndDepthLimit(t *testing.T) {
	d := Seed()
	d.Site.Navigation = []Link{{Label: "一级", Href: "/", Children: []Link{{Label: "二级", Href: "/", Children: []Link{{Label: "三级", Href: "/"}}}}}}
	if err := Validate(d); err != nil {
		t.Fatal(err)
	}
	d.Site.Navigation[0].Children[0].Children[0].Children = []Link{{Label: "四级", Href: "/"}}
	if Validate(d) == nil {
		t.Fatal("accepted fourth navigation level")
	}
}

func TestPreviewRemovesScriptsWithoutAlteringEscapedText(t *testing.T) {
	raw := []byte(`<p>&lt;script&gt;plain text&lt;/script&gt;</p><script src="/assets/runtime.js" defer></script><script>unsafe()</script>`)
	result := string(previewScriptTags.ReplaceAll(raw, nil))
	if strings.Contains(result, "<script") || !strings.Contains(result, "&lt;script&gt;plain text") {
		t.Fatal(result)
	}
}

func TestRichBodyEscapesHTMLAndKeepsAllowedFormatting(t *testing.T) {
	m := Module{RichContent: json.RawMessage(`{"version":1,"doc":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"<script>alert(1)</script>","marks":[{"type":"bold"},{"type":"link","attrs":{"href":"https://example.invalid/?x=1&y=2"}}]}]}]}}`)}
	output, err := renderRichBody(m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(output), "<script>") || !strings.Contains(string(output), "<strong>&lt;script&gt;") || !strings.Contains(string(output), "&amp;y=2") {
		t.Fatal(output)
	}
	for _, raw := range []string{
		`{"version":1,"doc":{"type":"doc","content":[{"type":"script"}]}}`,
		`{"version":1,"doc":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"unsafe","marks":[{"type":"link","attrs":{"href":"javascript:alert(1)"}}]}]}]}}`,
		`{"version":1,"doc":{"type":"doc","content":[{"type":"image","attrs":{"attachmentId":"unverified"}}]}}`,
	} {
		m.RichContent = json.RawMessage(raw)
		if _, err = renderRichBody(m); err == nil {
			t.Fatal("unsafe rich body accepted")
		}
		d := Seed()
		d.Pages[0].Modules[0].RichContent = m.RichContent
		if Validate(d) == nil {
			t.Fatal("unsafe rich body saved")
		}
	}
}
