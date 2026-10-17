package ticket

import (
	"encoding/json"
	"testing"
)

func TestRichImageUsesCanonicalMetadata(t *testing.T) {
	raw := json.RawMessage(`{"version":1,"doc":{"type":"doc","content":[{"type":"image","attrs":{"attachmentId":"image"}}]}}`)
	for _, mime := range []string{"image/png", "text/plain"} {
		a := json.RawMessage(`[{"id":"image","contentType":"` + mime + `"}]`)
		err := validateRichAttachments(raw, a)
		if (err == nil) != (mime == "image/png") {
			t.Fatalf("mime=%s err=%v", mime, err)
		}
	}
	if validateRichAttachments(raw, json.RawMessage(`[]`)) == nil {
		t.Fatal("accepted unrelated image")
	}
}
