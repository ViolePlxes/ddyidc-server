package admintodo

import (
	"context"
	billing "ddyidc/internal/billing/balance"
	payment "ddyidc/internal/payment/domain"
	payrepo "ddyidc/internal/payment/repository"
	"ddyidc/internal/shared/snowflake"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

type OfflineTransfer struct {
	ID            int64     `json:"id,string" gorm:"primaryKey"`
	RequestID     string    `json:"requestId"`
	CustomerID    int64     `json:"customerId,string"`
	OrderID       int64     `json:"orderId,string"`
	PaymentID     int64     `json:"paymentId,string"`
	Amount        string    `json:"amount"`
	Payer         string    `json:"payer"`
	TransactionNo string    `json:"transactionNo"`
	TransferredAt time.Time `json:"transferredAt"`
	Evidence      string    `json:"evidence"`
	EvidenceID    string    `json:"evidenceId"`
	Status        string    `json:"status"`
	Revision      int64     `json:"revision"`
	Reason        string    `json:"reason"`
	AdminID       int64     `json:"adminId,string"`
	OperatorIP    string    `json:"-"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func (OfflineTransfer) TableName() string { return "payment.offline_transfers" }

var ErrConflict = errors.New("业务状态已变化，请刷新核对后处理")

func SubmitOffline(ctx context.Context, db *gorm.DB, customer int64, in OfflineTransfer) (OfflineTransfer, error) {
	in.TransferredAt = in.TransferredAt.UTC().Truncate(time.Millisecond)
	amount, e := billing.ParseMoney(in.Amount)
	if e != nil || amount <= 0 || customer <= 0 || in.OrderID <= 0 || len(in.RequestID) < 8 || len(in.RequestID) > 64 || strings.TrimSpace(in.Payer) == "" || len([]rune(in.Payer)) > 128 || strings.TrimSpace(in.TransactionNo) == "" || len(in.TransactionNo) > 128 || strings.TrimSpace(in.Evidence) == "" || len(in.Evidence) > 512 || in.TransferredAt.IsZero() || in.TransferredAt.After(time.Now().Add(5*time.Minute)) {
		return in, ErrQuery
	}
	var out OfflineTransfer
	e = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		settings, e := loadSettings(tx)
		if e != nil {
			return e
		}
		if !settings.OfflineEnabled || settings.OfflineInstructions == "" {
			return ErrConflict
		}
		err := tx.Where("request_id=?", in.RequestID).Take(&out).Error
		if err == nil {
			if out.CustomerID != customer || out.OrderID != in.OrderID || out.TransactionNo != in.TransactionNo || out.Amount != amount.String() || out.EvidenceID != in.EvidenceID || out.Evidence != in.Evidence || out.Payer != in.Payer || !out.TransferredAt.Equal(in.TransferredAt) {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var proof EvidenceFile
		if err := tx.Where("id=? AND customer_id=?", in.EvidenceID, customer).Take(&proof).Error; err != nil {
			return ErrQuery
		}
		var used int64
		if err := tx.Model(&OfflineTransfer{}).Where("evidence_id=? AND status<>'rejected'", in.EvidenceID).Count(&used).Error; err != nil {
			return err
		}
		if used != 0 {
			return ErrConflict
		}
		prepared, err := payrepo.NewRepository(tx).PrepareOrder(ctx, customer, in.OrderID, "offline", time.Now().Add(24*time.Hour))
		if err != nil {
			return err
		}
		if prepared.Payment.AmountFen != int64(amount) || prepared.Payment.BizType != payment.BizOrder {
			return ErrConflict
		}
		var n int64
		if err = tx.Model(&OfflineTransfer{}).Where("order_id=? AND status IN ('pending','processing','approved','unknown')", in.OrderID).Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return ErrConflict
		}
		now := time.Now().UTC()
		out = OfflineTransfer{ID: snowflake.GenID(), RequestID: in.RequestID, CustomerID: customer, OrderID: in.OrderID, PaymentID: prepared.Payment.ID, Amount: amount.String(), Payer: in.Payer, TransactionNo: in.TransactionNo, TransferredAt: in.TransferredAt, Evidence: in.Evidence, Status: "pending", Revision: 1, CreatedAt: now, UpdatedAt: now}
		out.EvidenceID = in.EvidenceID
		return tx.Create(&out).Error
	})
	return out, e
}

func ReviewOffline(ctx context.Context, db *gorm.DB, id, revision, admin int64, ip, reason, action, request string, confirmed bool, key []byte) (OfflineTransfer, error) {
	var out OfflineTransfer
	if id <= 0 || revision < 1 || admin <= 0 || ip == "" || strings.TrimSpace(reason) == "" || len([]rune(reason)) > 500 || len(request) < 8 || len(request) > 64 || (action != "approve" && action != "reject") {
		return out, ErrQuery
	}
	e := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&out, id).Error; e != nil {
			return e
		}
		replay, err := auditReplay(tx, request, "offline-transfers", "offline", id, admin, action, reason, out.Evidence)
		if err != nil {
			return err
		}
		if replay {
			return nil
		}
		if out.Status != "pending" || out.Revision != revision {
			return ErrConflict
		}
		next := "rejected"
		if action == "approve" {
			var proof EvidenceFile
			if e := tx.Where("id=? AND customer_id=?", out.EvidenceID, out.CustomerID).Take(&proof).Error; e != nil {
				return ErrQuery
			}
			if _, e := validateEvidence(proof, key); e != nil {
				return ErrConflict
			}
			amount, e := billing.ParseMoney(out.Amount)
			if e != nil {
				return e
			}
			if amount > 5000000 && (!confirmed) {
				return ErrConflict
			}
			var p payment.Payment
			if e := tx.First(&p, out.PaymentID).Error; e != nil {
				return e
			}
			if p.Channel != "offline" || p.BizID != out.OrderID || p.CustomerID != out.CustomerID || p.Status != payment.StatusPending {
				return ErrConflict
			}
			paid, _, e := payrepo.NewRepository(tx).Complete(ctx, payment.VerifiedNotification{Channel: "offline", OutTradeNo: p.OutTradeNo, TransactionID: out.TransactionNo, AmountFen: p.AmountFen, Currency: "CNY", PaidAt: out.TransferredAt, SafeRaw: "manual offline receipt verified"})
			if e != nil {
				return e
			}
			if paid.Status != payment.StatusPaid {
				return ErrConflict
			}
			next = "approved"
		}
		audit := map[string]any{"id": snowflake.GenID(), "request_id": request, "kind": "offline-transfers", "source": "offline", "object_id": id, "action": action, "admin_id": admin, "operator_ip": ip, "notes": reason, "evidence": out.Evidence, "created_at": time.Now().UTC()}
		if e := tx.Table("system.todo_audits").Create(audit).Error; e != nil {
			return e
		}
		out.Status = next
		out.Revision++
		out.Reason = reason
		out.AdminID = admin
		out.OperatorIP = ip
		out.UpdatedAt = time.Now().UTC()
		return tx.Save(&out).Error
	})
	return out, e
}
