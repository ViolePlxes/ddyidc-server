package withdrawal

import (
	"context"
	billing "ddyidc/internal/billing/balance"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"fmt"
	"testing"
	"time"
)

type fakeWithdrawalNotifier struct{ calls int }

func (n *fakeWithdrawalNotifier) NotifyCustomer(context.Context, string, int64, string, map[string]string) error {
	n.calls++
	return nil
}

func TestWithdrawalPolicyAndNotificationMySQL(t *testing.T) {
	db := testenv.MySQL(t).Begin()
	defer db.Rollback()
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	id := snowflake.GenID()
	key := []byte("0123456789abcdef0123456789abcdef")
	must(db.Table("customer.customers").Create(map[string]any{"id": id, "tenant_id": 0, "username": fmt.Sprintf("policy-%d", id), "password_hash": "isolated", "status": 1, "created_at": time.Now(), "updated_at": time.Now()}).Error)
	config := func(k, v string) {
		must(db.Exec("INSERT INTO system.system_configs(id,`key`,value) VALUES(?,?,?) ON DUPLICATE KEY UPDATE value=VALUES(value)", snowflake.GenID(), "todos.withdrawals."+k, v).Error)
	}
	config("enabled", "1")
	config("fee_policy", "percent_2")
	config("allowed_sources", "[\"manual_credit\"]")
	config("daily_count", "1")
	config("daily_amount", "100.00")
	_, err := billing.New(db).Change(ctx, billing.ChangeRequest{CustomerID: id, Type: "manual_credit", Amount: "100.00", RefType: "customer", RefID: id, Channel: "admin_manual", RequestID: fmt.Sprintf("POLICYCREDIT%d", id), AdminID: 1, AdminName: "isolated", OperatorIP: "127.0.0.1", Notes: "isolated policy test"})
	must(err)
	quote, err := GetQuote(ctx, db, id, "10.00")
	must(err)
	if quote.Fee != 20 || quote.Net != 980 || quote.MaximumWithdrawable != 10000 {
		t.Fatalf("invalid fee quote: %+v", quote)
	}
	config("maximum", "20.00")
	config("daily_amount", "15.00")
	capped, err := GetQuote(ctx, db, id, "")
	must(err)
	if capped.MaximumWithdrawable != 1500 {
		t.Fatalf("all withdrawal ignored caps: %+v", capped)
	}
	config("maximum", "50000.00")
	config("daily_amount", "100.00")
	request := fmt.Sprintf("POLICY%d", id)
	row, err := SubmitFrom(ctx, db, key, id, request, "10.00", "isolated account", "test_module", fmt.Sprint(id))
	must(err)
	fee, net, err := SavedAmounts(ctx, db, row.ID, row.Amount)
	must(err)
	if fee != "0.20" || net != "9.80" {
		t.Fatalf("snapshot fee %s net %s", fee, net)
	}
	q, err := GetQuote(ctx, db, id, "")
	must(err)
	if q.WithdrawalFrozen != 1000 || q.Eligible != 9000 || q.MaximumWithdrawable != 0 {
		t.Fatalf("invalid quote: %+v", q)
	}
	if _, err := SubmitWithdrawal(ctx, db, key, id, fmt.Sprintf("LIMIT%d", id), "1.00", "isolated account"); err == nil {
		t.Fatal("daily limit ignored")
	}
	if _, err := SubmitFrom(ctx, db, key, id, request, "10.00", "isolated account", "other_module", fmt.Sprint(id)); err == nil {
		t.Fatal("module replay accepted")
	}
	config("enabled", "0")
	config("fee_policy", "free")
	replay, err := SubmitFrom(ctx, db, key, id, request, "10.00", "isolated account", "test_module", fmt.Sprint(id))
	must(err)
	fee, net, err = SavedAmounts(ctx, db, replay.ID, replay.Amount)
	must(err)
	if fee != "0.20" || net != "9.80" {
		t.Fatal("historical fees changed")
	}
	if replay.ID != row.ID {
		t.Fatal("disabled replay duplicated application")
	}
	if _, err := SubmitWithdrawal(ctx, db, key, id, fmt.Sprintf("CLOSED%d", id), "1.00", "isolated account"); err == nil {
		t.Fatal("closed policy accepted new request")
	}
	reject := fmt.Sprintf("REJECTPOLICY%d", id)
	_, err = ReviewWithdrawal(ctx, db, row.ID, 1, 1, "isolated", "127.0.0.1", "rejected", "reject", reject, "", "", false)
	must(err)
	_, err = ReviewWithdrawal(ctx, db, row.ID, 1, 1, "isolated", "127.0.0.1", "rejected", "reject", reject, "", "", false)
	must(err)
	q, err = GetQuote(ctx, db, id, "")
	must(err)
	if q.WithdrawalFrozen != 0 || q.Eligible != 10000 || q.MaximumWithdrawable != 0 {
		t.Fatalf("rejection failed to restore eligibility: %+v", q)
	}
	var count int64
	must(db.Table("payment.withdrawal_notifications").Where("withdrawal_id=?", row.ID).Count(&count).Error)
	if count != 1 {
		t.Fatalf("duplicate notification: %d", count)
	}
	// Restrict the queue to this rollback fixture; unrelated isolated rows are not sent.
	must(db.Exec("UPDATE payment.withdrawal_notifications SET status='needs_review' WHERE withdrawal_id<>?", row.ID).Error)
	n := &fakeWithdrawalNotifier{}
	s := WithdrawalService{DB: db, Key: key}
	must(s.SendNext(ctx, n))
	must(s.SendNext(ctx, n))
	if n.calls != 1 {
		t.Fatalf("duplicate notification delivery: %d", n.calls)
	}
	var state string
	must(db.Table("payment.withdrawal_notifications").Select("status").Where("withdrawal_id=?", row.ID).Scan(&state).Error)
	if state != "sent" {
		t.Fatalf("notice state %s", state)
	}
	must(s.Reconcile(ctx))
}
