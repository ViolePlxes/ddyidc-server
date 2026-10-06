package notify

import (
	"strings"
	"testing"
	"time"
)

func TestDigestScheduleShanghaiAndUrgency(t *testing.T) {
	cases := []struct{ now, expiry, want string }{
		{"2026-10-06T00:30:00Z", "2026-10-09T00:30:00Z", "2026-10-06T01:00:00Z"},
		{"2026-10-06T02:00:00Z", "2026-10-09T00:30:00Z", "2026-10-06T02:15:00Z"},
		{"2026-10-06T02:00:00Z", "2026-10-06T02:19:00Z", "2026-10-06T02:00:00Z"},
		{"2026-10-06T00:30:00Z", "2026-10-06T00:49:00Z", "2026-10-06T00:30:00Z"},
	}
	for _, c := range cases {
		now, _ := time.Parse(time.RFC3339, c.now)
		expiry, _ := time.Parse(time.RFC3339, c.expiry)
		if got := digestSchedule(now, expiry).Format(time.RFC3339); got != c.want {
			t.Fatalf("now=%s: got %s want %s", c.now, got, c.want)
		}
	}
}

func TestDigestMailEscapesAndLimitsRows(t *testing.T) {
	var items []DigestItem
	due := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	for i := 0; i < 23; i++ {
		items = append(items, DigestItem{ProductName: "<script>alert(1)</script>", InstanceNo: "test & server", PrimaryIP: "127.0.0.1", ExpiresAt: due})
	}
	title, body := digestMail(items)
	if title != "您有23台服务器即将到期" || strings.Contains(body, "<script>") || !strings.Contains(body, "&lt;script&gt;") {
		t.Fatal("unsafe or incorrect merged email")
	}
	if strings.Count(body, "<tr>") != 21 || !strings.Contains(body, "其余3台") || !strings.Contains(body, "2026-10-09 09:00") {
		t.Fatal("truncation or Shanghai timestamp incorrect")
	}
}
