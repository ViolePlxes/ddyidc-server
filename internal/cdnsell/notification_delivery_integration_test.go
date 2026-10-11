package cdnsell

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type deliveryProbe struct {
	calls atomic.Int32
	fail  bool
}

func (p *deliveryProbe) NotifyCustomer(context.Context, string, int64, string, map[string]string) error {
	panic("multi-channel delivery must not be used")
}
func (p *deliveryProbe) NotifyCustomerChannel(_ context.Context, _ string, _ int64, _ string, ch string, _ map[string]string) error {
	if ch != "email" {
		panic("disabled channel sent")
	}
	p.calls.Add(1)
	if p.fail {
		return errors.New("isolated delivery failed")
	}
	return nil
}
func TestCDNNotificationReservationMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	probe := &deliveryProbe{}
	service := NewService(db, nil, probe)
	service.now = func() time.Time { return now }
	user := snowflake.GenID()
	if err := db.Exec("INSERT INTO customer.customers(id,username,password_hash,status) VALUES(?,?,'disabled',1)", user, fmt.Sprintf("notice-%d", user)).Error; err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- service.sendCDNWarning(ctx, user, "usage", "cdnsell_usage", "isolated", nil, map[string]bool{"email": true}, 1)
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if probe.calls.Load() != 1 {
		t.Fatal("concurrent duplicate delivery", probe.calls.Load())
	}
	if err := service.sendCDNWarning(ctx, user, "certificate_expiry", "cdnsell_cert_expiry", "isolated", nil, map[string]bool{"email": true}, 1); err != nil {
		t.Fatal(err)
	}
	if probe.calls.Load() != 1 {
		t.Fatal("different event bypassed cooldown")
	}
	now = now.Add(24*time.Hour + time.Second)
	probe.fail = true
	if err := service.sendCDNWarning(ctx, user, "usage", "cdnsell_usage", "isolated", nil, map[string]bool{"email": true}, 1); err == nil {
		t.Fatal("failure hidden")
	}
	var status string
	if err := db.Table("system.cdnsell_notify_records").Where("user_id=?", user).Order("created_at DESC").Select("status").Take(&status).Error; err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatal("failure falsely marked sent", status)
	}
	if err := service.sendCDNWarning(ctx, user, "usage", "cdnsell_usage", "isolated", nil, map[string]bool{"email": true}, 1); err != nil {
		t.Fatal(err)
	}
	if probe.calls.Load() != 2 {
		t.Fatal("uncertain attempt automatically repeated")
	}
	t.Log("12 concurrent requests send once; disabled channels untouched; daily cross-event limit; failures recorded and not retried")
}
