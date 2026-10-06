package notify

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestDigestSceneAndTestChannelApproval(t *testing.T) {
	if !validScene("expiry_digest") || validVerificationScene("expiry_digest") {
		t.Fatal("digest must be a notification scene, not a verification scene")
	}
	repo := &fakeNotifyRepository{values: map[string]string{}, templates: map[string]*NotificationTemplate{"expiry_digest": {Enabled: true, Channel: ChannelBoth, Content: "您有{{server_count}}台服务器即将到期，时间{{earliest_expiry}}"}}}
	mail := &fakeMailChannel{}
	manager := testManager(repo, mail)
	params := map[string]string{"server_count": "2", "earliest_expiry": "2026-10-09 09:00"}
	if err := manager.SendTestNotify(context.Background(), "expiry_digest", "isolated@example.test", params); err != nil || mail.calls != 1 {
		t.Fatalf("digest email test failed: %v", err)
	}
	manager.yaml.SMS.Templates["expiry_digest"] = "SMS_UNAPPROVED_FIXTURE"
	if err := manager.SendTestNotify(context.Background(), "expiry_digest", "18631428991", params); err == nil {
		t.Fatal("unapproved test SMS accepted")
	}
}

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
