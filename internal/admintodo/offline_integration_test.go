package admintodo

import (
	"context"
	"crypto/sha256"
	payment "ddyidc/internal/payment/domain"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"ddyidc/pkg/crypto"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
)

func TestOfflineReceiptLifecycleMySQL(t *testing.T) {
	tx := testenv.MySQL(t).Begin()
	defer tx.Rollback()
	ctx := context.Background()
	key := []byte("0123456789abcdef0123456789abcdef")
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	customer, order := snowflake.GenID(), snowflake.GenID()
	must(tx.Table("customer.customers").Create(map[string]any{"id": customer, "tenant_id": 0, "username": fmt.Sprintf("offline-%d", customer), "password_hash": "isolated", "status": 1, "created_at": now, "updated_at": now}).Error)
	must(tx.Table("`order`.orders").Create(map[string]any{"id": order, "order_no": fmt.Sprintf("O%d", order), "customer_id": customer, "status": 1, "pay_amount": 1000, "total_amount": 1000, "balance_amount": 0, "created_at": now, "updated_at": now}).Error)
	must(tx.Table("`order`.order_items").Create(map[string]any{"id": snowflake.GenID(), "order_id": order, "product_id": snowflake.GenID(), "product_name": "isolated", "module_type": "cloud", "config_snapshot": "{}", "qty": 1, "unit_price": 1000, "subtotal": 1000, "delivery_status": 0, "created_at": now, "updated_at": now}).Error)
	for k, v := range map[string]string{"todos.offline.enabled": "1", "todos.offline.instructions": "isolated test only"} {
		must(tx.Exec("INSERT INTO system.system_configs(id,`key`,value) VALUES(?,?,?) ON DUPLICATE KEY UPDATE value=VALUES(value)", snowflake.GenID(), k, v).Error)
	}
	raw := "%PDF-1.7\nisolated receipt\n"
	sum := sha256.Sum256([]byte(raw))
	cipher, e := crypto.Encrypt(raw, key)
	must(e)
	proof := EvidenceFile{ID: fmt.Sprint(snowflake.GenID()), CustomerID: customer, Mime: "application/pdf", Size: int64(len(raw)), Checksum: hex.EncodeToString(sum[:]), Ciphertext: cipher, CreatedAt: now}
	must(tx.Create(&proof).Error)
	in := OfflineTransfer{OrderID: order, Amount: "10.00", RequestID: fmt.Sprintf("OFF%d", order), Payer: "isolated payer", TransactionNo: fmt.Sprintf("BANK%d", order), TransferredAt: now, Evidence: "isolated fixture only", EvidenceID: proof.ID}
	row, e := SubmitOffline(ctx, tx, customer, in)
	must(e)
	again, e := SubmitOffline(ctx, tx, customer, in)
	must(e)
	if row.ID != again.ID {
		t.Fatal("submission replay duplicated")
	}
	if _, e := SubmitOffline(ctx, tx, customer+1, in); e == nil {
		t.Fatal("foreign customer accepted")
	}
	_, e = ReviewOffline(ctx, tx, row.ID, 1, 1, "127.0.0.1", "missing statement", "reject", fmt.Sprintf("REJECT%d", row.ID), false, key)
	must(e)
	in.RequestID = fmt.Sprintf("AGAIN%d", row.ID)
	row, e = SubmitOffline(ctx, tx, customer, in)
	must(e)
	must(tx.Model(&proof).Update("ciphertext", "corrupt").Error)
	if _, e := ReviewOffline(ctx, tx, row.ID, 1, 1, "127.0.0.1", "checked", "approve", fmt.Sprintf("BAD%d", row.ID), false, key); e == nil {
		t.Fatal("corrupt evidence approved")
	}
	must(tx.Model(&proof).Update("ciphertext", cipher).Error)
	request := fmt.Sprintf("APPROVE%d", row.ID)
	_, e = ReviewOffline(ctx, tx, row.ID, 1, 1, "127.0.0.1", "checked isolated bank receipt", "approve", request, false, key)
	must(e)
	_, e = ReviewOffline(ctx, tx, row.ID, 1, 1, "127.0.0.1", "checked isolated bank receipt", "approve", request, false, key)
	must(e)
	var p payment.Payment
	must(tx.First(&p, row.PaymentID).Error)
	if p.Status != payment.StatusPaid {
		t.Fatal("receipt not linked to paid order")
	}
	var count int64
	must(tx.Table("payment.receipts").Where("payment_id=?", p.ID).Count(&count).Error)
	if count != 1 {
		t.Fatal("receipt duplicated")
	}
	page, e := List(ctx, tx, "offline-transfers", Filter{Page: 1, PageSize: 20, CustomerID: fmt.Sprint(customer)})
	must(e)
	if page.Total != 0 {
		t.Fatal("approved receipt remains pending")
	}
	t.Log("isolated SQL fixtures: proof ownership, submission replay, rejected proof resubmission, corruption blocked, manual receipt completion/replay once; no bank/provider call; rollback")
}
