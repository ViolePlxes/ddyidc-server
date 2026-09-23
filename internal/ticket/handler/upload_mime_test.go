package ticket

import "testing"

func TestUploadMIMEAllowlist(t *testing.T) {
	for _, v := range []struct {
		ext, mime string
		want      bool
	}{
		{".txt", "text/html; charset=utf-8", false}, {".png", "application/octet-stream", false},
		{".log", "text/plain; charset=utf-8", true}, {".jpg", "image/jpeg", true},
		{".zip", "application/zip", true}, {".pdf", "application/pdf", true},
		{".svg", "image/svg+xml", false},
	} {
		if got := validUploadMIME(v.ext, v.mime); got != v.want {
			t.Errorf("%s %s: got %v", v.ext, v.mime, got)
		}
	}
}
