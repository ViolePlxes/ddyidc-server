package payment

import (
	"context"
 "ddyidc/internal/domains"
	balance "ddyidc/internal/billing/balance"
	orderstate "ddyidc/internal/order/repository"
	"ddyidc/internal/shared/renewalquote"
	"errors"
	"fmt"
	"time"

	"ddyidc/internal/marketplace"
	"ddyidc/internal/order/inventory"
	dom "ddyidc/internal/payment/domain"
	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrBizNotFound   = errors.New("payment business object not found")
	ErrForbidden     = errors.New("payment business object forbidden")
	ErrNotPayable    = errors.New("payment business object is not payable")
	ErrPaymentExists = errors.New("payment already exists")
	ErrReceiptReview = errors.New("verified receipt awaits review")
	ErrInvalidState  = errors.New("invalid payment state transition")
)

type Prepared struct {
	Payment *dom.Payment
	Subject string
}

type Repository interface {
	PrepareOrder(context.Context, int64, int64, string, time.Time) (*Prepared, error)
	PrepareRecharge(context.Context, int64, int64, string, time.Time) (*Prepared, error)
	PrepareMarketplace(context.Context, int64, int64, string, time.Time) (*Prepared, error)
	FindByOutTradeNo(context.Context, string) (*dom.Payment, error)
	FindByPaymentNo(context.Context, string) (*dom.Payment, error)
	FindByBiz(context.Context, int64, dom.BizType, int64) (*dom.Payment, error)
	Complete(context.Context, dom.VerifiedNotification) (*dom.Payment, bool, error)
	Close(context.Context, int64, dom.BizType, int64) (*dom.Payment, error)
}

func (r *GormRepository) PrepareMarketplace(ctx context.Context, customerID, orderID int64, channel string, expireAt time.Time) (*Prepared, error) {
	var prepared *Prepared
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row struct {
			ID             int64
			OrderNo        string
			BuyerID        int64
			PayAmount      string
			Status         string
			SettlementMode string
			LockExpiresAt  time.Time
			ProductName    string
		}
		err := tx.Table("system.marketplace_orders AS o").Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("o.id,o.order_no,o.buyer_id,o.pay_amount,o.status,o.settlement_mode,o.lock_expires_at,l.product_name").
			Joins("JOIN system.marketplace_listings l ON l.id=o.listing_id").Where("o.id=?", orderID).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrBizNotFound
		}
		if err != nil {
			return err
		}
		if row.BuyerID != customerID {
			return ErrForbidden
		}
		if row.Status != "pending_pay" || !row.LockExpiresAt.After(time.Now()) {
			return ErrNotPayable
		}
		amount, err := marketplace.MoneyToCents(row.PayAmount)
		if err != nil {
			return ErrNotPayable
		}
		paymentNo := generatePaymentNo()
		payment := &dom.Payment{PaymentNo: paymentNo, BizType: dom.BizMarketplace, BizID: row.ID, CustomerID: customerID, Channel: channel, OutTradeNo: paymentNo, Amount: row.PayAmount, AmountFen: amount, Currency: "CNY", Status: dom.StatusPending, ExpireAt: expireAt}
		if row.SettlementMode == "balance" {
			payment.OrderID = &row.ID
		}
		if err := r.insertOrReuse(tx, payment); err != nil {
			return err
		}
		subject := row.ProductName
		if subject == "" {
			subject = "DDYIDC 交易市场订单"
		}
		prepared = &Prepared{Payment: payment, Subject: subject}
		return nil
	})
	return prepared, err
}

type GormRepository struct{ db *gorm.DB }

func NewRepository(database *gorm.DB) Repository { return &GormRepository{db: database} }

func (r *GormRepository) PrepareOrder(ctx context.Context, customerID, orderID int64, channel string, expireAt time.Time) (*Prepared, error) {
	var privateCoupons int64
	if err := r.db.WithContext(ctx).Table("order.order_items").Where("order_id=? AND module_type='promo_coupon'", orderID).Count(&privateCoupons).Error; err != nil {
		return nil, err
	}
	if privateCoupons > 0 {
		return nil, errors.New("推广发券订单仅支持原资产支付")
	}
	var marketCount int64
	if err := r.db.WithContext(ctx).Table("order.order_items").Where("order_id=? AND module_type='marketplace'", orderID).Count(&marketCount).Error; err != nil {
		return nil, err
	}
	if marketCount > 0 {
		return r.PrepareMarketplace(ctx, customerID, orderID, channel, expireAt)
	}
	var prepared *Prepared
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row struct {
			ID         int64
			OrderNo    string
			CustomerID int64
			PayAmount  int64
			Status     uint8
			Subject    string
		}
		err := tx.Table("order.orders AS o").Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("o.id,o.order_no,o.customer_id,o.pay_amount,o.status,(SELECT oi.product_name FROM `order`.order_items oi WHERE oi.order_id=o.id ORDER BY oi.id LIMIT 1) AS subject").
			Where("o.id = ?", orderID).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrBizNotFound
		}
		if err != nil {
			return err
		}
		if row.CustomerID != customerID {
			return ErrForbidden
		}
		if row.Status != 1 || row.PayAmount <= 0 {
			return ErrNotPayable
		}
 if err := domains.CheckPaymentTx(ctx, tx, customerID, orderID, row.PayAmount, time.Now()); err != nil { return err }
		if err := renewalquote.CheckTx(ctx, tx, customerID, orderID); err != nil {
			return err
		}

		deadline, err := renewalquote.PaymentDeadline(ctx, tx, expireAt)
		if err != nil {
			return err
		}
		orderIDCopy := row.ID
		paymentNo := generatePaymentNo()
		payment := &dom.Payment{
			PaymentNo:  paymentNo,
			BizType:    dom.BizOrder,
			BizID:      row.ID,
			OrderID:    &orderIDCopy,
			CustomerID: customerID,
			Channel:    channel,
			OutTradeNo: paymentNo,
			Amount:     centsToYuan(row.PayAmount),
			AmountFen:  row.PayAmount,
			Currency:   "CNY",
			Status:     dom.StatusPending,
			ExpireAt:   deadline,
		}
		if err := r.insertOrReuse(tx, payment); err != nil {
			return err
		}

		if err := renewalquote.BindPaymentTx(ctx, tx, customerID, orderID, payment.ID, payment.AmountFen); err != nil {
			return err
		}
		prepared = &Prepared{Payment: payment, Subject: row.Subject}
		if prepared.Subject == "" {
			prepared.Subject = "DDYIDC 云服务订单"
		}
		return nil
	})
	return prepared, err
}

func (r *GormRepository) PrepareRecharge(ctx context.Context, customerID, rechargeID int64, channel string, expireAt time.Time) (*Prepared, error) {
	var prepared *Prepared
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row struct {
			ID         int64
			RechargeNo string
			CustomerID int64
			Amount     int64
			Status     uint8
		}
		err := tx.Table("payment.recharge_orders").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", rechargeID).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrBizNotFound
		}
		if err != nil {
			return err
		}
		if row.CustomerID != customerID {
			return ErrForbidden
		}
		if row.Status != 1 || row.Amount <= 0 {
			return ErrNotPayable
		}
		paymentNo := generatePaymentNo()
		payment := &dom.Payment{
			PaymentNo:  paymentNo,
			BizType:    dom.BizRecharge,
			BizID:      row.ID,
			CustomerID: customerID,
			Channel:    channel,
			OutTradeNo: paymentNo,
			Amount:     centsToYuan(row.Amount),
			AmountFen:  row.Amount,
			Currency:   "CNY",
			Status:     dom.StatusPending,
			ExpireAt:   expireAt,
		}
		if err := r.insertOrReuse(tx, payment); err != nil {
			return err
		}
		prepared = &Prepared{Payment: payment, Subject: "DDYIDC 账户充值"}
		return nil
	})
	return prepared, err
}

func (r *GormRepository) insertOrReuse(tx *gorm.DB, payment *dom.Payment) error {
	var reviews int64
	if e := tx.Table(payment.TableName()).Where("biz_type=? AND biz_id=? AND status=?", payment.BizType, payment.BizID, dom.StatusReview).Count(&reviews).Error; e != nil {
		return e
	}
	if reviews > 0 {
		return ErrReceiptReview
	}
	var existing dom.Payment
	err := tx.Table(existing.TableName()).Where("biz_type = ? AND biz_id = ?", payment.BizType, payment.BizID).Order("created_at DESC, id DESC").First(&existing).Error
	if err == nil {
		if existing.Status == dom.StatusPaid {
			return ErrNotPayable
		}
		if existing.Status == dom.StatusPending && existing.ExpireAt.After(time.Now()) {
			if existing.Channel != payment.Channel {
				return ErrPaymentExists
			}
			*payment = existing
			return nil
		}
		if existing.Status == dom.StatusPending {
			now := time.Now()
			if updateErr := tx.Table(existing.TableName()).Where("id = ? AND status = ?", existing.ID, dom.StatusPending).Updates(map[string]any{"status": dom.StatusClosed, "closed_at": now, "updated_at": now}).Error; updateErr != nil {
				return updateErr
			}
		}
		err = gorm.ErrRecordNotFound
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return tx.Table(payment.TableName()).Create(payment).Error
}

func (r *GormRepository) FindByPaymentNo(ctx context.Context, paymentNo string) (*dom.Payment, error) {
	var payment dom.Payment
	err := r.db.WithContext(ctx).Table(payment.TableName()).Where("payment_no = ?", paymentNo).First(&payment).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &payment, err
}

func (r *GormRepository) FindByOutTradeNo(ctx context.Context, outTradeNo string) (*dom.Payment, error) {
	var payment dom.Payment
	err := r.db.WithContext(ctx).Table(payment.TableName()).Where("out_trade_no = ?", outTradeNo).First(&payment).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &payment, err
}

func (r *GormRepository) FindByBiz(ctx context.Context, customerID int64, bizType dom.BizType, bizID int64) (*dom.Payment, error) {
	if bizType == dom.BizOrder {
		var n int64
		if err := r.db.WithContext(ctx).Table("order.order_items").Where("order_id=? AND module_type='marketplace'", bizID).Count(&n).Error; err != nil {
			return nil, err
		}
		if n > 0 {
			bizType = dom.BizMarketplace
		}
	}
	var payment dom.Payment
	err := r.db.WithContext(ctx).Table(payment.TableName()).Where("customer_id = ? AND biz_type = ? AND biz_id = ?", customerID, bizType, bizID).Order("created_at DESC, id DESC").First(&payment).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &payment, err
}

func (r *GormRepository) Complete(ctx context.Context, notification dom.VerifiedNotification) (*dom.Payment, bool, error) {
	identity, err := r.FindByOutTradeNo(ctx, notification.OutTradeNo)
	if err != nil {
		return nil, false, err
	}
	if identity == nil {
		return nil, false, ErrBizNotFound
	}
	completed := *identity
	transitioned := false
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := r.lockPayment(tx, &completed); err != nil {
			return err
		}
		if completed.Channel != notification.Channel || completed.AmountFen != notification.AmountFen || completed.Currency != notification.Currency {
			return ErrInvalidState
		}
		if completed.Status == dom.StatusPaid || completed.Status == dom.StatusRefunded || completed.Status == dom.StatusReview {
			if completed.TransactionID != notification.TransactionID || notification.TransactionID == "" {
				return ErrInvalidState
			}
			return nil
		}
		if completed.Status != dom.StatusPending && completed.Status != dom.StatusClosed && completed.Status != dom.StatusFailed {
			return ErrInvalidState
		}
		if completed.Channel != notification.Channel || completed.AmountFen != notification.AmountFen || completed.Currency != notification.Currency {
			return ErrInvalidState
		}
		paidAt := notification.PaidAt
		if paidAt.IsZero() {
			paidAt = time.Now()
		}
		if err := recordReceipt(tx, &completed, notification, paidAt); err != nil {
			return err
		}
		if err := tx.SavePoint("before_receipt_business").Error; err != nil {
			return err
		}
		switch completed.BizType {
		case dom.BizOrder:
			if err := renewalquote.ReceiptAllowedTx(ctx, tx, completed.CustomerID, completed.BizID, completed.ID, completed.AmountFen, paidAt); err != nil {
				var gate *renewalquote.Error
				if !errors.As(err, &gate) {
					return err
				}
				return reviewReceipt(tx, &completed, notification, paidAt, "renewal_quote_unverified")
			}
			if err := tx.SavePoint("before_order_receipt").Error; err != nil {
				return err
			}
			changed, err := orderstate.AcceptPaymentTx(tx, completed.BizID, completed.CustomerID, completed.AmountFen, channelPayType(completed.Channel), paidAt)
			if err != nil {
				return err
			}
			if !changed {
				return reviewReceipt(tx, &completed, notification, paidAt, "order_not_payable")
			}
			if err := inventory.Consume(tx, completed.BizID); err != nil {
				if !errors.Is(err, inventory.ErrReleased) {
					return err
				}
				if err := tx.RollbackTo("before_order_receipt").Error; err != nil {
					return err
				}
				return reviewReceipt(tx, &completed, notification, paidAt, "stock_reservation_released")
			}
		case dom.BizRecharge:
			result := tx.Table("payment.recharge_orders").Where("id = ? AND customer_id = ? AND status = 1 AND amount = ?", completed.BizID, completed.CustomerID, completed.AmountFen).
				Updates(map[string]any{"status": 2, "channel": completed.Channel, "paid_at": paidAt, "updated_at": paidAt})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return reviewReceipt(tx, &completed, notification, paidAt, "recharge_not_payable")
			}

		case dom.BizMarketplace:
			if completed.Status != dom.StatusPending {
				return reviewReceipt(tx, &completed, notification, paidAt, "marketplace_late_receipt")
			}
			if err := tx.SavePoint("before_marketplace_transfer").Error; err != nil {
				return err
			}
			if err := marketplace.CompletePaidOrderTx(tx, completed.BizID, completed.CustomerID, paidAt, completed.Channel); err != nil {
				if rollbackErr := tx.RollbackTo("before_marketplace_transfer").Error; rollbackErr != nil {
					return errors.Join(err, rollbackErr)
				}
				if compensateErr := marketplace.CompensateFailedExternalPaymentTx(tx, completed.BizID, completed.CustomerID, paidAt, err.Error()); compensateErr != nil {
					if rollbackErr := tx.RollbackTo("before_marketplace_transfer").Error; rollbackErr != nil {
						return errors.Join(compensateErr, rollbackErr)
					}
					return reviewReceipt(tx, &completed, notification, paidAt, "marketplace_compensation_conflict")
				}
				result := tx.Table(completed.TableName()).Where("id = ? AND status = ?", completed.ID, completed.Status).Updates(map[string]any{
					"status": dom.StatusPaid, "transaction_id": notification.TransactionID,
					"notify_raw": notification.SafeRaw, "paid_at": paidAt, "updated_at": paidAt,
				})
				if result.Error != nil || result.RowsAffected != 1 {
					if result.Error != nil {
						return result.Error
					}
					return ErrInvalidState
				}
				completed.Status, completed.TransactionID, completed.NotifyRaw, completed.PaidAt = dom.StatusPaid, notification.TransactionID, notification.SafeRaw, &paidAt
				transitioned = true
				return enqueuePaid(tx, &completed)
			}
		default:
			return ErrInvalidState
		}
		result := tx.Table(completed.TableName()).Where("id = ? AND status = ?", completed.ID, completed.Status).Updates(map[string]any{
			"status": dom.StatusPaid, "transaction_id": notification.TransactionID,
			"notify_raw": notification.SafeRaw, "paid_at": paidAt, "updated_at": paidAt,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrInvalidState
		}
		completed.Status, completed.TransactionID, completed.NotifyRaw, completed.PaidAt = dom.StatusPaid, notification.TransactionID, notification.SafeRaw, &paidAt
		if completed.BizType == dom.BizRecharge {
			if err := creditRecharge(tx, &completed, paidAt); err != nil {
				if errors.Is(err, balance.ErrLedgerIntegrity) {
					if e := tx.RollbackTo("before_receipt_business").Error; e != nil {
						return errors.Join(err, e)
					}
					return reviewReceipt(tx, &completed, notification, paidAt, "balance_ledger_mismatch")
				}
				return err
			}
		} else if err := settleExternalPayment(tx, &completed); err != nil {
			if errors.Is(err, balance.ErrLedgerIntegrity) {
				if e := tx.RollbackTo("before_receipt_business").Error; e != nil {
					return errors.Join(err, e)
				}
				return reviewReceipt(tx, &completed, notification, paidAt, "external_balance_ledger_mismatch")
			}
			return err
		}
		transitioned = true
		return enqueuePaid(tx, &completed)
	})
	return &completed, transitioned, err
}

func creditRecharge(tx *gorm.DB, payment *dom.Payment, now time.Time) error {
	_, err := balance.New(tx).Change(tx.Statement.Context, balance.ChangeRequest{CustomerID: payment.CustomerID, Type: "recharge", Amount: balance.Money(payment.AmountFen).String(), RefType: "payment", RefID: payment.ID, Channel: "payment_callback", RequestID: fmt.Sprintf("RCT%d", payment.ID), BizTrace: payment.TransactionID, Notes: "在线充值到账"})
	return err
}
func (r *GormRepository) Close(ctx context.Context, customerID int64, bizType dom.BizType, bizID int64) (*dom.Payment, error) {
	payment, err := r.FindByBiz(ctx, customerID, bizType, bizID)
	if err != nil {
		return nil, err
	}
	if payment == nil {
		return nil, ErrBizNotFound
	}
	return r.ClosePayment(ctx, customerID, payment.ID)
}

func (r *GormRepository) ClosePayment(ctx context.Context, customerID, paymentID int64) (*dom.Payment, error) {
	var payment dom.Payment
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table(payment.TableName()).Clauses(clause.Locking{Strength: "UPDATE"}).Where("customer_id = ? AND id = ?", customerID, paymentID).First(&payment).Error; err != nil {
			return err
		}
		if payment.BizType == dom.BizMarketplace {
			return ErrNotPayable
		}
		if payment.Status == dom.StatusClosed {
			return nil
		}
		if payment.Status != dom.StatusPending {
			return ErrInvalidState
		}
		now := time.Now()
		if err := tx.Table(payment.TableName()).Where("id = ? AND status = ?", payment.ID, dom.StatusPending).Updates(map[string]any{"status": dom.StatusClosed, "closed_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		payment.Status, payment.ClosedAt = dom.StatusClosed, &now
		return nil
	})
	return &payment, err
}

func centsToYuan(cents int64) string { return fmt.Sprintf("%d.%02d", cents/100, cents%100) }

func channelPayType(channel string) uint8 {
	if channel == "offline" {
		return 4
	}
	if channel == "alipay" {
		return 1
	}
	return 2
}

func generatePaymentNo() string {
	return fmt.Sprintf("PAY%d", snowflake.GenID())
}

// settleExternalPayment records both legs in the business transaction. The
// account lock stays held until commit, so the intermediate credit cannot escape.
type externalLedgerActor struct {
	ID         int64
	Name, IP   string
	Historical bool
}

func settleExternalPayment(tx *gorm.DB, p *dom.Payment, actors ...externalLedgerActor) error {
	var actor externalLedgerActor
	if len(actors) > 0 {
		actor = actors[0]
	}
	if p.BizType != dom.BizOrder && p.BizType != dom.BizMarketplace {
		return ErrInvalidState
	}
	creditNotes, debitNotes := "在线付款到账", "购买产品扣款"
	if actor.Historical {
		if p.PaidAt == nil {
			return ErrInvalidState
		}
		original := p.PaidAt.In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("2006/01/02 15:04:05")
		creditNotes = "历史在线付款补记（原付款：" + original + "）"
		debitNotes = "历史购买扣款补记（原付款：" + original + "）"
	}
	credit, err := balance.New(tx).Change(tx.Statement.Context, balance.ChangeRequest{CustomerID: p.CustomerID, AdminID: actor.ID, AdminName: actor.Name, OperatorIP: actor.IP, Type: "recharge", Amount: balance.Money(p.AmountFen).String(), RefType: "external_payment", RefID: p.ID, Channel: "payment_callback", RequestID: fmt.Sprintf("EPC%d", p.ID), BizTrace: p.TransactionID, Notes: "订单在线付款到账", ClientNotes: creditNotes})
	if err != nil {
		return err
	}
	debit, err := balance.New(tx).Change(tx.Statement.Context, balance.ChangeRequest{CustomerID: p.CustomerID, AdminID: actor.ID, AdminName: actor.Name, OperatorIP: actor.IP, Type: "order_pay", Amount: balance.Money(-p.AmountFen).String(), RefType: "external_payment", RefID: p.ID, Channel: "payment_callback", RequestID: fmt.Sprintf("EPD%d", p.ID), BizTrace: p.TransactionID, Notes: "订单在线付款扣款", ClientNotes: debitNotes})
	if err != nil {
		return err
	}
	if credit.BalanceBefore != debit.BalanceAfter || credit.BalanceAfter != debit.BalanceBefore || credit.AccountVersion+1 != debit.AccountVersion {
		return balance.ErrLedgerIntegrity
	}
	return nil
}
