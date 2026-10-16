package notify

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSMSReceiptClassification(t *testing.T) {
	sent := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name, phone, send, receive, code string
		status                           int
		want                             string
	}{
		{"delivered", "19900000000", "2026-10-16 08:00:00", "2026-10-16 08:00:03", "DELIVERED", 3, "delivered"},
		{"pending", "19900000000", "2026-10-16 08:00:00", "", "", 1, "receipt_pending"},
		{"failed", "19900000000", "2026-10-16 08:00:00", "", "isv.MOBILE_NOT_IN_SERVICE", 2, "delivery_failed"},
		{"wrong phone", "19900000001", "2026-10-16 08:00:00", "2026-10-16 08:00:03", "DELIVERED", 3, "receipt_unknown"},
		{"wrong time", "19900000000", "2026-10-15 08:00:00", "2026-10-16 08:00:03", "DELIVERED", 3, "receipt_unknown"},
		{"missing code", "19900000000", "2026-10-16 08:00:00", "2026-10-16 08:00:03", "", 3, "receipt_unknown"},
		{"conflicting failure", "19900000000", "2026-10-16 08:00:00", "", "DELIVERED", 2, "receipt_unknown"},
		{"missing receipt date", "19900000000", "2026-10-16 08:00:00", "", "DELIVERED", 3, "receipt_unknown"},
		{"receipt predates send", "19900000000", "2026-10-16 08:00:00", "2026-10-16 07:59:59", "DELIVERED", 3, "receipt_unknown"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := classifySMSReceipt("19900000000", sent, c.phone, c.send, c.receive, c.code, c.status)
			if got.Status != c.want {
				t.Fatalf("got %s want %s", got.Status, c.want)
			}
		})
	}
}

type receiptProbe struct {
	calls, sends int
	result       SMSReceipt
	err          error
}

func (p *receiptProbe) Send(context.Context, SMSConfig, string, string, map[string]string) (string, error) {
	p.sends++
	return "", errors.New("must never send")
}
func (p *receiptProbe) QueryReceipt(context.Context, SMSConfig, string, string, time.Time) (SMSReceipt, error) {
	p.calls++
	return p.result, p.err
}
func checkSMSReceiptsMySQL(t *testing.T, db *gorm.DB) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	probe := &receiptProbe{result: SMSReceipt{Status: "receipt_pending", Code: "WAITING_RECEIPT"}}
	manager := testManager(&fakeNotifyRepository{values: map[string]string{}, contact: Contact{Phone: "19900000000"}}, &digestMailProbe{})
	manager.sms = probe
	manager.now = func() time.Time { return now }
	manager.digest = &DigestService{db: db, manager: manager}
	var tasksBefore, quotasBefore int64
	db.Model(&DigestTask{}).Count(&tasksBefore)
	db.Model(&DigestQuota{}).Count(&quotasBefore)
	original := NotifyLog{Scene: "expiry", Channel: "sms", Target: "19900000000", Status: "submitted", RequestID: "isolated-biz-id", BusinessID: "receipt-fixture", CreatedAt: now.Add(-2 * time.Minute)}
	if e := db.Create(&original).Error; e != nil {
		t.Fatal(e)
	}
	first, e := manager.QuerySMSReceipt(context.Background(), original.ID, true)
	if e != nil || first.Status != "receipt_pending" {
		t.Fatalf("pending query: %v %#v", e, first)
	}
	if _, e = manager.QuerySMSReceipt(context.Background(), original.ID, true); e != nil || probe.calls != 1 {
		t.Fatal("cooldown not respected")
	}
	now = now.Add(2 * time.Minute)
	received := now
	probe.result = SMSReceipt{Status: "delivered", Code: "DELIVERED", ReceivedAt: &received}
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, e := manager.QuerySMSReceipt(context.Background(), original.ID, true)
			if e != nil || result.Status != "delivered" {
				t.Error("concurrent receipt result")
			}
		}()
	}
	wg.Wait()
	if probe.calls != 2 || probe.sends != 0 {
		t.Fatalf("duplicate query or resend: queries=%d sends=%d", probe.calls, probe.sends)
	}
	var after NotifyLog
	db.First(&after, "id=?", original.ID)
	if after.Status != "submitted" || after.RequestID != original.RequestID {
		t.Fatal("submission state changed")
	}
	var receipts []NotifyLog
	db.Where("scene='sms_receipt' AND business_id=?", original.ID).Find(&receipts)
	if len(receipts) != 2 {
		t.Fatal("receipt history incorrect")
	}
	for _, row := range receipts {
		if row.Target != "" || strings.Contains(row.ContentSnapshot, original.Target) {
			t.Fatal("recipient copied into receipt")
		}
	}
	failure := NotifyLog{Scene: "expiry", Channel: "sms", Target: original.Target, Status: "success", RequestID: "failure-biz-id", CreatedAt: now.Add(-time.Minute)}
	if e = db.Create(&failure).Error; e != nil {
		t.Fatal(e)
	}
	probe.err = errors.New("secret-phone-token-must-not-persist")
	result, e := manager.QuerySMSReceipt(context.Background(), failure.ID, true)
	if e != nil || result.Status != "receipt_unknown" {
		t.Fatal("query failure misclassified")
	}
	after = NotifyLog{}
	if e = db.Where("scene='sms_receipt' AND business_id=?", failure.ID).First(&after).Error; e != nil {
		t.Fatal(e)
	}
	if strings.Contains(after.ContentSnapshot, "secret-phone-token") || strings.Contains(after.Error, "secret-phone-token") {
		t.Fatal("raw provider error leaked")
	}
	calls := probe.calls
	stale := NotifyLog{Scene: "expiry", Channel: "sms", Target: original.Target, Status: "submitted", RequestID: "old-biz-id", CreatedAt: now.Add(-25 * time.Hour)}
	db.Create(&stale)
	result, e = manager.QuerySMSReceipt(context.Background(), stale.ID, false)
	if e != nil || result.Status != "receipt_unknown" || result.Code != "RECEIPT_TIMEOUT" || probe.calls != calls {
		t.Fatal("timeout queried or marked delivered")
	}
	var tasksAfter, quotasAfter int64
	db.Model(&DigestTask{}).Count(&tasksAfter)
	db.Model(&DigestQuota{}).Count(&quotasAfter)
	if tasksBefore != tasksAfter || quotasBefore != quotasAfter {
		t.Fatal("receipt mutated tasks or daily quota")
	}
	if e = manager.CheckSMSReceipts(context.Background()); e != nil {
		t.Fatal(e)
	}
	if probe.sends != 0 {
		t.Fatal("worker resent SMS")
	}
	maskedTask := DigestTask{ID: 9900771, CustomerID: 77, NotifyDate: now.Format("2006-01-02"), Channel: "sms", Status: "submitted", RequestID: "masked-fixture-biz", ScheduledAt: now, CreatedAt: now, UpdatedAt: now}
	if e = db.Create(&maskedTask).Error; e != nil {
		t.Fatal(e)
	}
	maskedLog := NotifyLog{Scene: "expiry_digest", Channel: "sms", Target: maskTarget(original.Target), Status: "submitted", BusinessID: "9900771", RequestID: maskedTask.RequestID, CreatedAt: now.Add(-2 * time.Minute)}
	if e = db.Create(&maskedLog).Error; e != nil {
		t.Fatal(e)
	}
	phone, e := manager.smsReceiptRecipient(context.Background(), maskedLog)
	if e != nil || phone != original.Target {
		t.Fatal("masked recipient cannot resolve through matching original task")
	}
	manager.repo.(*fakeNotifyRepository).contact.Phone = "18800000000"
	calls = probe.calls
	result, e = manager.QuerySMSReceipt(context.Background(), maskedLog.ID, true)
	if e != nil || result.Status != "receipt_unknown" || result.Code != "RECIPIENT_UNAVAILABLE" || probe.calls != calls {
		t.Fatal("changed phone was queried or marked delivered")
	}

	// A backlog of old logs must not take the bounded batch away from recent SMS.
	probe.err = nil
	probe.result = SMSReceipt{Status: "receipt_pending", Code: "WAITING_RECEIPT"}
	freshIDs := []int64{}
	for i := 0; i < 6; i++ {
		entry := NotifyLog{Scene: "expiry", Channel: "sms", Target: original.Target, Status: "submitted", RequestID: "fresh-biz", CreatedAt: now.Add(-2 * time.Minute)}
		if e = db.Create(&entry).Error; e != nil {
			t.Fatal(e)
		}
		freshIDs = append(freshIDs, entry.ID)
	}
	old := NotifyLog{Scene: "expiry", Channel: "sms", Target: original.Target, Status: "submitted", RequestID: "backlog-biz", CreatedAt: now.Add(-48 * time.Hour)}
	if e = db.Create(&old).Error; e != nil {
		t.Fatal(e)
	}
	calls = probe.calls
	if e = manager.CheckSMSReceipts(context.Background()); e != nil {
		t.Fatal(e)
	}
	if probe.calls-calls != 5 {
		t.Fatal("bounded worker did not query five recent messages")
	}
	var oldReceipts int64
	db.Model(&NotifyLog{}).Where("scene='sms_receipt' AND business_id=?", old.ID).Count(&oldReceipts)
	if oldReceipts != 0 {
		t.Fatal("old backlog starved fresh SMS")
	}

}

func TestSMSReceiptQueryDates(t *testing.T) {
	near := time.Date(2026, 10, 15, 16, 1, 0, 0, time.UTC)
	dates := receiptQueryDates(near)
	if len(dates) != 2 || dates[0] != "20261016" || dates[1] != "20261015" {
		t.Fatal("Shanghai midnight fallback missing")
	}
	if dates = receiptQueryDates(near.Add(time.Hour)); len(dates) != 1 {
		t.Fatal("unbounded date search")
	}
}
