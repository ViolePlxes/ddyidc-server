package marketplace

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type CreateOrderInput struct{ ListingID int64 }

func (s *Service) CreateOrder(ctx context.Context, buyerID int64, in CreateOrderInput) (*Order, error) {
	settings, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	if settings.Maintenance {
		return nil, ErrMaintenance
	}
	if err = s.checkUserRisk(ctx, buyerID, settings, false); err != nil {
		return nil, err
	}
	release, err := s.acquire(ctx, "marketplace:listing:"+strconv.FormatInt(in.ListingID, 10), 20*time.Second)
	if err != nil {
		return nil, err
	}
	defer release()
	var out Order
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var listing Listing
		if err := tx.Table(listingsTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", in.ListingID).First(&listing).Error; err != nil {
			return ErrNotFound
		}
		if listing.SellerID == buyerID {
			return errors.New("不能购买自己发布的商品")
		}
		if listing.Status != "on_sale" || listing.AuditStatus != "passed" || !listing.ExpiresAt.After(s.now()) {
			return ErrInvalid
		}
		amount, err := parsePositiveMoney(listing.Price)
		if err != nil {
			return err
		}
		fee, buyerPay, sellerIncome, err := calculateFee(amount, settings)
		if err != nil {
			return err
		}
		out = Order{OrderNo: s.newNo("TL"), ListingID: listing.ID, BuyerID: buyerID, SellerID: listing.SellerID, Amount: formatMoney(amount), PayAmount: formatMoney(buyerPay), Fee: formatMoney(fee), FeeBearer: settings.FeeBearer, SellerIncome: formatMoney(sellerIncome), Status: "pending_pay", LockExpiresAt: s.now().Add(time.Duration(settings.OrderLockMinutes) * time.Minute)}
		if err := tx.Table(ordersTable).Create(&out).Error; err != nil {
			return err
		}
		res := tx.Table(listingsTable).Where("id=? AND status='on_sale'", listing.ID).Updates(map[string]any{"status": "locked", "updated_at": s.now()})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrInvalid
		}
		return nil
	})
	return &out, err
}

func (s *Service) PayBalance(ctx context.Context, buyerID, orderID int64) (*Order, error) {
	release, err := s.acquire(ctx, "marketplace:order:"+strconv.FormatInt(orderID, 10), 30*time.Second)
	if err != nil {
		return nil, err
	}
	defer release()
	var out Order
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table(ordersTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", orderID).First(&out).Error; err != nil {
			return ErrNotFound
		}
		if out.BuyerID != buyerID {
			return ErrForbidden
		}
		if out.Status != "pending_pay" || !out.LockExpiresAt.After(s.now()) {
			return ErrInvalid
		}
		pay, err := parsePositiveMoney(out.PayAmount)
		if err != nil {
			return err
		}
		var account struct{ ID, Balance, TotalConsume int64 }
		if err := tx.Table("billing.accounts").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id,balance,total_consume").Where("customer_id=?", buyerID).First(&account).Error; err != nil {
			return errors.New("账户余额不足")
		}
		if account.Balance < pay {
			return errors.New("账户余额不足")
		}
		after := account.Balance - pay
		now := s.now()
		if err := tx.Table("billing.accounts").Where("id=?", account.ID).Updates(map[string]any{"balance": after, "total_consume": account.TotalConsume + pay, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Exec("INSERT INTO billing.transactions (id,tx_no,customer_id,type,direction,amount,balance_before,balance_after,related_type,related_id,remark,created_at,updated_at) VALUES (?,?,?,2,2,?,?,?,?,?,'交易市场购买',?,?)", snowflake.GenID(), "MT"+fmt.Sprintf("%d", snowflake.GenID()), buyerID, pay, account.Balance, after, "marketplace", orderID, now, now).Error; err != nil {
			return err
		}
		if err := CompletePaidOrderTx(tx, orderID, buyerID, now, "balance"); err != nil {
			return err
		}
		return tx.Table(ordersTable).Where("id=?", orderID).First(&out).Error
	})
	if err == nil {
		s.notifyTrade(&out)
	}
	return &out, err
}

// CompletePaidOrderTx is shared with the existing payment repository. The
// payment state, resource ownership, seller freeze and immutable flows commit
// in one database transaction.
func CompletePaidOrderTx(tx *gorm.DB, orderID, buyerID int64, paidAt time.Time, method string) error {
	var order Order
	if err := tx.Table(ordersTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", orderID).First(&order).Error; err != nil {
		return err
	}
	if order.BuyerID != buyerID {
		return ErrForbidden
	}
	if order.Status == "paid" || order.Status == "settled" {
		return nil
	}
	if order.Status != "pending_pay" || paidAt.After(order.LockExpiresAt) {
		return ErrInvalid
	}
	var listing Listing
	if err := tx.Table(listingsTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", order.ListingID).First(&listing).Error; err != nil {
		return err
	}
	if listing.Status != "locked" || listing.SellerID != order.SellerID {
		return ErrInvalid
	}
	var host struct {
		ID, CustomerID int64
		Status         uint8
	}
	if err := tx.Table("instance.instances").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id,customer_id,status").Where("id=? AND deleted_at IS NULL", listing.ProductID).First(&host).Error; err != nil {
		return err
	}
	if host.CustomerID != order.SellerID {
		return errors.New("资源归属已变化，交易终止")
	}
	if err := tx.Exec("INSERT IGNORE INTO "+walletTable+" (user_id,balance,frozen_balance,total_income,total_withdrawn,updated_at) VALUES (?,0,0,0,0,?)", order.SellerID, paidAt).Error; err != nil {
		return err
	}
	var wallet Wallet
	if err := tx.Table(walletTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=?", order.SellerID).First(&wallet).Error; err != nil {
		return err
	}
	frozen, _ := parseUnsignedMoney(wallet.FrozenBalance)
	income, err := parsePositiveMoney(order.SellerIncome)
	if err != nil {
		return err
	}
	frozenAfter := frozen + income
	if err := tx.Table(walletTable).Where("user_id=?", order.SellerID).Updates(map[string]any{"frozen_balance": formatMoney(frozenAfter), "updated_at": paidAt}).Error; err != nil {
		return err
	}
	if err := tx.Table("instance.instances").Where("id=? AND customer_id=?", host.ID, order.SellerID).Updates(map[string]any{"customer_id": buyerID, "updated_at": paidAt}).Error; err != nil {
		return err
	}
	operator := buyerID
	eventID := snowflake.GenID()
	if err := tx.Exec("INSERT INTO instance.instance_events (id,instance_id,event_type,from_status,to_status,operator_type,operator_id,status,upstream_request,upstream_response,error_msg,created_at,processed_at,updated_at) VALUES (?,?, 'marketplace_transfer',?,?,1,?,2,JSON_OBJECT('sellerId',?,'buyerId',?,'upstreamWrite',false),JSON_OBJECT('localOwnershipChanged',true),'',?,?,?)", eventID, host.ID, host.Status, host.Status, operator, order.SellerID, buyerID, paidAt, paidAt, paidAt).Error; err != nil {
		return err
	}
	settleAt := paidAt
	refundEnd := paidAt
	var settings Settings
	if err := tx.Table(settingsTable).Order("id").First(&settings).Error; err != nil {
		return err
	}
	settleAt = paidAt.AddDate(0, 0, settings.SettleFreezeDays)
	refundEnd = paidAt.AddDate(0, 0, settings.RefundWindowDays)
	if err := tx.Table(ordersTable).Where("id=? AND status='pending_pay'", order.ID).Updates(map[string]any{"status": "paid", "payment_method": method, "paid_at": paidAt, "settle_at": settleAt, "refund_window_end": refundEnd, "updated_at": paidAt}).Error; err != nil {
		return err
	}
	if err := tx.Table(listingsTable).Where("id=? AND status='locked'", listing.ID).Updates(map[string]any{"status": "sold", "updated_at": paidAt}).Error; err != nil {
		return err
	}
	if err := appendFlow(tx, order.BuyerID, "buyer_paid", "out", order.PayAmount, "0.00", order.ID, "买家支付", paidAt); err != nil {
		return err
	}
	return appendFlow(tx, order.SellerID, "seller_pending", "in", order.SellerIncome, wallet.Balance, order.ID, "卖家待结算", paidAt)
}

// CompensateFailedExternalPaymentTx converts a captured external payment into
// local account credit when the ownership transfer cannot be committed. Callers
// must roll back to a savepoint created before CompletePaidOrderTx first.
func CompensateFailedExternalPaymentTx(tx *gorm.DB, orderID, buyerID int64, paidAt time.Time, _ string) error {
	var order Order
	if err := tx.Table(ordersTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", orderID).First(&order).Error; err != nil {
		return err
	}
	if order.BuyerID != buyerID || order.Status != "pending_pay" {
		return ErrInvalid
	}
	amount, err := parsePositiveMoney(order.PayAmount)
	if err != nil {
		return err
	}
	if err := creditBilling(tx, buyerID, amount, orderID, paidAt); err != nil {
		return err
	}
	var account struct{ Balance int64 }
	if err := tx.Table("billing.accounts").Select("balance").Where("customer_id=?", buyerID).First(&account).Error; err != nil {
		return err
	}
	if err := tx.Table(ordersTable).Where("id=? AND status='pending_pay'", orderID).Updates(map[string]any{"status": "cancelled", "payment_method": "local_refund", "updated_at": paidAt}).Error; err != nil {
		return err
	}
	if err := tx.Table(listingsTable).Where("id=? AND status='locked'", order.ListingID).Updates(map[string]any{"status": "on_sale", "updated_at": paidAt}).Error; err != nil {
		return err
	}
	return appendFlow(tx, buyerID, "refund_reversed", "in", order.PayAmount, formatMoney(account.Balance), orderID, "第三方支付后过户失败，已退至账户余额", paidAt)
}

func (s *Service) AfterExternalPayment(ctx context.Context, orderID, buyerID int64) error {
	var row Order
	if err := s.db.WithContext(ctx).Table(ordersTable).Where("id=? AND buyer_id=?", orderID, buyerID).First(&row).Error; err != nil {
		return err
	}
	s.notifyTrade(&row)
	return nil
}

func (s *Service) ListOrders(ctx context.Context, userID int64, admin bool, status string, page, size int) ([]Order, int64, error) {
	page, size = normalizePage(page, size)
	q := s.db.WithContext(ctx).Table(ordersTable + " AS o")
	if !admin {
		q = q.Where("o.buyer_id=? OR o.seller_id=?", userID, userID)
	}
	if status != "" {
		q = q.Where("o.status=?", status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Order
	err := q.Select("o.*").Order("o.created_at DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error
	if err == nil {
		for i := range rows {
			var listing Listing
			if e := s.db.WithContext(ctx).Table(listingsTable).Where("id=?", rows[i].ListingID).First(&listing).Error; e == nil {
				if !admin {
					listing.SellerID = 0
				}
				rows[i].Listing = &listing
			}
		}
	}
	return rows, total, err
}

func (s *Service) Refund(ctx context.Context, actorID, orderID int64, admin bool) (*Order, error) {
	release, err := s.acquire(ctx, "marketplace:order:"+strconv.FormatInt(orderID, 10), 30*time.Second)
	if err != nil {
		return nil, err
	}
	defer release()
	var out Order
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table(ordersTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", orderID).First(&out).Error; err != nil {
			return ErrNotFound
		}
		if !admin && out.BuyerID != actorID {
			return ErrForbidden
		}
		var settings Settings
		if err := tx.Table(settingsTable).Order("id").First(&settings).Error; err != nil {
			return err
		}
		if !settings.RefundEnabled {
			return errors.New("退款功能未开启")
		}
		if out.Status != "paid" && out.Status != "settled" {
			return ErrInvalid
		}
		if !admin && out.RefundWindowEnd != nil && s.now().After(*out.RefundWindowEnd) {
			return errors.New("退款窗口已结束")
		}
		var listing Listing
		if err := tx.Table(listingsTable).Where("id=?", out.ListingID).First(&listing).Error; err != nil {
			return err
		}
		var host struct {
			ID, CustomerID int64
			Status         uint8
		}
		if err := tx.Table("instance.instances").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id,customer_id,status").Where("id=?", listing.ProductID).First(&host).Error; err != nil {
			return err
		}
		if host.CustomerID != out.BuyerID {
			return errors.New("资源归属不一致，需平台人工处理")
		}
		var wallet Wallet
		if err := tx.Table(walletTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=?", out.SellerID).First(&wallet).Error; err != nil {
			return err
		}
		income, _ := parsePositiveMoney(out.SellerIncome)
		balance, _ := parseUnsignedMoney(wallet.Balance)
		frozen, _ := parseUnsignedMoney(wallet.FrozenBalance)
		updates := map[string]any{"updated_at": s.now()}
		if out.Status == "settled" {
			if balance < income {
				return errors.New("卖家钱包余额不足，需平台仲裁")
			}
			balance -= income
			updates["balance"] = formatMoney(balance)
		} else {
			if frozen < income {
				return errors.New("冻结金额不足，需平台仲裁")
			}
			frozen -= income
			updates["frozen_balance"] = formatMoney(frozen)
		}
		if err := tx.Table(walletTable).Where("user_id=?", out.SellerID).Updates(updates).Error; err != nil {
			return err
		}
		pay, _ := parsePositiveMoney(out.PayAmount)
		if err := creditBilling(tx, out.BuyerID, pay, orderID, s.now()); err != nil {
			return err
		}
		if err := tx.Table("instance.instances").Where("id=? AND customer_id=?", host.ID, out.BuyerID).Updates(map[string]any{"customer_id": out.SellerID, "updated_at": s.now()}).Error; err != nil {
			return err
		}
		operator := actorID
		if err := tx.Exec("INSERT INTO instance.instance_events (id,instance_id,event_type,from_status,to_status,operator_type,operator_id,status,upstream_request,upstream_response,error_msg,created_at,processed_at,updated_at) VALUES (?,?, 'marketplace_refund_transfer',?,?,1,?,2,JSON_OBJECT('buyerId',?,'sellerId',?,'upstreamWrite',false),JSON_OBJECT('localOwnershipChanged',true),'',?,?,?)", snowflake.GenID(), host.ID, host.Status, host.Status, operator, out.BuyerID, out.SellerID, s.now(), s.now(), s.now()).Error; err != nil {
			return err
		}
		if err := tx.Table(ordersTable).Where("id=?", out.ID).Updates(map[string]any{"status": "refunded", "updated_at": s.now()}).Error; err != nil {
			return err
		}
		if err := tx.Table(listingsTable).Where("id=?", listing.ID).Updates(map[string]any{"status": "refunded", "updated_at": s.now()}).Error; err != nil {
			return err
		}
		if err := appendFlow(tx, out.SellerID, "refund_reversed", "out", out.SellerIncome, formatMoney(balance), out.ID, "退款回滚", s.now()); err != nil {
			return err
		}
		out.Status = "refunded"
		return nil
	})
	if err == nil {
		s.notify("marketplace_refund", out.BuyerID, out.ID, map[string]string{"order_no": out.OrderNo})
		s.notify("marketplace_refund", out.SellerID, out.ID, map[string]string{"order_no": out.OrderNo})
	}
	return &out, err
}

func (s *Service) Wallet(ctx context.Context, userID int64) (*Wallet, error) {
	if err := s.db.WithContext(ctx).Exec("INSERT IGNORE INTO "+walletTable+" (user_id,balance,frozen_balance,total_income,total_withdrawn,updated_at) VALUES (?,0,0,0,0,?)", userID, s.now()).Error; err != nil {
		return nil, err
	}
	var row Wallet
	err := s.db.WithContext(ctx).Table(walletTable).Where("user_id=?", userID).First(&row).Error
	return &row, err
}
func (s *Service) ListFlows(ctx context.Context, userID int64, admin bool, flowType, direction string, page, size int) ([]Flow, int64, error) {
	page, size = normalizePage(page, size)
	q := s.db.WithContext(ctx).Table(flowsTable)
	if !admin {
		q = q.Where("user_id=?", userID)
	} else if userID > 0 {
		q = q.Where("user_id=?", userID)
	}
	if flowType != "" {
		q = q.Where("type=?", flowType)
	}
	if direction != "" {
		q = q.Where("direction=?", direction)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Flow
	err := q.Order("created_at DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error
	return rows, total, err
}

func (s *Service) CreateWithdrawal(ctx context.Context, userID int64, amountText string) (*Withdrawal, error) {
	return s.createWithdrawal(ctx, userID, amountText)
}

func (s *Service) createWithdrawal(ctx context.Context, userID int64, amountText string) (*Withdrawal, error) {
	amount, err := parsePositiveMoney(amountText)
	if err != nil {
		return nil, errors.New("提现金额格式无效")
	}
	settings, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	fee := int64(0)
	switch settings.WithdrawFeeType {
	case "fixed":
		fee, err = parseUnsignedMoney(settings.WithdrawFeeFixed)
	case "ratio":
		var rate int64
		rate, err = parsePercent(settings.WithdrawFeeRate)
		fee = amount * rate / 10000
	}
	if err != nil || fee >= amount {
		return nil, errors.New("提现手续费配置无效")
	}
	row := &Withdrawal{WithdrawNo: s.newNo("TX"), UserID: userID, Amount: formatMoney(amount), Fee: formatMoney(fee), ActualAmount: formatMoney(amount - fee), Status: "pending"}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("INSERT IGNORE INTO "+walletTable+" (user_id,balance,frozen_balance,total_income,total_withdrawn,updated_at) VALUES (?,0,0,0,0,?)", userID, s.now()).Error; err != nil {
			return err
		}
		var wallet Wallet
		if err := tx.Table(walletTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=?", userID).First(&wallet).Error; err != nil {
			return err
		}
		balance, _ := parseUnsignedMoney(wallet.Balance)
		frozen, _ := parseUnsignedMoney(wallet.FrozenBalance)
		if balance < amount {
			return errors.New("交易账户余额不足")
		}
		if err := tx.Table(walletTable).Where("user_id=?", userID).Updates(map[string]any{"balance": formatMoney(balance - amount), "frozen_balance": formatMoney(frozen + amount), "updated_at": s.now()}).Error; err != nil {
			return err
		}
		if err := tx.Table(withdrawalsTable).Create(row).Error; err != nil {
			return err
		}
		if !settings.WithdrawalAuditEnabled {
			return s.completeWithdrawalTx(tx, row, 0, "自动通过")
		}
		return nil
	})
	return row, err
}

func (s *Service) ListWithdrawals(ctx context.Context, userID int64, admin bool, status string, page, size int) ([]Withdrawal, int64, error) {
	page, size = normalizePage(page, size)
	q := s.db.WithContext(ctx).Table(withdrawalsTable)
	if !admin {
		q = q.Where("user_id=?", userID)
	} else if userID > 0 {
		q = q.Where("user_id=?", userID)
	}
	if status != "" {
		q = q.Where("status=?", status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Withdrawal
	err := q.Order("created_at DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error
	return rows, total, err
}

func (s *Service) AuditWithdrawal(ctx context.Context, adminID, id int64, approved bool, remark string) (*Withdrawal, error) {
	var out Withdrawal
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table(withdrawalsTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).First(&out).Error; err != nil {
			return ErrNotFound
		}
		if out.Status != "pending" {
			return ErrInvalid
		}
		if approved {
			return s.completeWithdrawalTx(tx, &out, adminID, remark)
		}
		amount, _ := parsePositiveMoney(out.Amount)
		var wallet Wallet
		if err := tx.Table(walletTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=?", out.UserID).First(&wallet).Error; err != nil {
			return err
		}
		balance, _ := parseUnsignedMoney(wallet.Balance)
		frozen, _ := parseUnsignedMoney(wallet.FrozenBalance)
		if frozen < amount {
			return errors.New("冻结金额不足")
		}
		if err := tx.Table(walletTable).Where("user_id=?", out.UserID).Updates(map[string]any{"balance": formatMoney(balance + amount), "frozen_balance": formatMoney(frozen - amount), "updated_at": s.now()}).Error; err != nil {
			return err
		}
		if err := tx.Table(withdrawalsTable).Where("id=?", id).Updates(map[string]any{"status": "rejected", "auditor_id": adminID, "remark": strings.TrimSpace(remark), "updated_at": s.now()}).Error; err != nil {
			return err
		}
		if err := appendFlow(tx, out.UserID, "withdraw_reject", "in", out.Amount, formatMoney(balance+amount), out.ID, "提现拒绝退回", s.now()); err != nil {
			return err
		}
		out.Status = "rejected"
		return nil
	})
	if err == nil {
		s.notify("marketplace_withdraw_result", out.UserID, out.ID, map[string]string{"withdraw_no": out.WithdrawNo, "status": out.Status})
	}
	return &out, err
}

func (s *Service) completeWithdrawalTx(tx *gorm.DB, row *Withdrawal, adminID int64, remark string) error {
	amount, _ := parsePositiveMoney(row.Amount)
	var wallet Wallet
	if err := tx.Table(walletTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=?", row.UserID).First(&wallet).Error; err != nil {
		return err
	}
	frozen, _ := parseUnsignedMoney(wallet.FrozenBalance)
	withdrawn, _ := parseUnsignedMoney(wallet.TotalWithdrawn)
	if frozen < amount {
		return errors.New("冻结金额不足")
	}
	if err := tx.Table(walletTable).Where("user_id=?", row.UserID).Updates(map[string]any{"frozen_balance": formatMoney(frozen - amount), "total_withdrawn": formatMoney(withdrawn + amount), "updated_at": s.now()}).Error; err != nil {
		return err
	}
	updates := map[string]any{"status": "paid", "remark": strings.TrimSpace(remark), "updated_at": s.now()}
	if adminID > 0 {
		updates["auditor_id"] = adminID
	}
	if err := tx.Table(withdrawalsTable).Where("id=?", row.ID).Updates(updates).Error; err != nil {
		return err
	}
	row.Status = "paid"
	return appendFlow(tx, row.UserID, "withdraw", "out", row.Amount, wallet.Balance, row.ID, "提现已处理", s.now())
}

func (s *Service) CreateReport(ctx context.Context, userID int64, row *Report) (*Report, error) {
	var n int64
	if err := s.db.WithContext(ctx).Table(listingsTable).Where("id=?", row.ListingID).Count(&n).Error; err != nil || n == 0 {
		return nil, ErrNotFound
	}
	row.ID = 0
	row.ReporterID = userID
	row.Reason = strings.TrimSpace(row.Reason)
	row.Detail = strings.TrimSpace(row.Detail)
	row.Status = "pending"
	if row.Reason == "" {
		return nil, errors.New("请选择举报原因")
	}
	if err := s.db.WithContext(ctx).Table(reportsTable).Create(row).Error; err != nil {
		return nil, err
	}
	return row, nil
}
func (s *Service) ListReports(ctx context.Context, status string, page, size int) ([]Report, int64, error) {
	page, size = normalizePage(page, size)
	q := s.db.WithContext(ctx).Table(reportsTable)
	if status != "" {
		q = q.Where("status=?", status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Report
	err := q.Order("created_at DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error
	return rows, total, err
}
func (s *Service) HandleReport(ctx context.Context, adminID, id int64, status string) error {
	if status != "processed" && status != "ignored" {
		return errors.New("处理状态无效")
	}
	res := s.db.WithContext(ctx).Table(reportsTable).Where("id=? AND status='pending'", id).Updates(map[string]any{"status": status, "handler_id": adminID, "updated_at": s.now()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrInvalid
	}
	return nil
}

func (s *Service) AuditListing(ctx context.Context, adminID, id int64, pass bool, remark string) error {
	status, audit := "rejected", "rejected"
	if pass {
		status, audit = "on_sale", "passed"
	}
	var seller int64
	var no string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row Listing
		if err := tx.Table(listingsTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).First(&row).Error; err != nil {
			return ErrNotFound
		}
		if row.Status != "pending" || row.AuditStatus != "pending" {
			return ErrInvalid
		}
		seller, no = row.SellerID, row.ListingNo
		return tx.Table(listingsTable).Where("id=?", id).Updates(map[string]any{"status": status, "audit_status": audit, "audit_remark": strings.TrimSpace(remark), "auditor_id": adminID, "updated_at": s.now()}).Error
	})
	if err == nil && pass {
		s.notify("marketplace_listing_passed", seller, id, map[string]string{"listing_no": no})
	}
	return err
}
func (s *Service) RecommendListing(ctx context.Context, id int64, on bool) error {
	res := s.db.WithContext(ctx).Table(listingsTable).Where("id=?", id).Updates(map[string]any{"is_recommended": on, "updated_at": s.now()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) Dashboard(ctx context.Context) (map[string]any, error) {
	out := map[string]any{}
	queries := []struct{ key, table, where string }{{"onSale", listingsTable, "status='on_sale'"}, {"sold", listingsTable, "status='sold'"}, {"wants", wantsTable, "status='open'"}, {"pendingListings", listingsTable, "audit_status='pending'"}, {"pendingWithdrawals", withdrawalsTable, "status='pending'"}, {"pendingReports", reportsTable, "status='pending'"}}
	for _, q := range queries {
		var n int64
		if err := s.db.WithContext(ctx).Table(q.table).Where(q.where).Count(&n).Error; err != nil {
			return nil, err
		}
		out[q.key] = n
	}
	var fee string
	if err := s.db.WithContext(ctx).Table(ordersTable).Select("COALESCE(SUM(fee),0)").Where("status IN ('paid','settled')").Scan(&fee).Error; err != nil {
		return nil, err
	}
	out["feeIncome"] = fee
	settings, err := s.Settings(ctx)
	if err == nil {
		out["feeRate"] = settings.FeeRate
	}
	return out, nil
}

func (s *Service) RunMaintenance(ctx context.Context) error {
	enabled, err := s.Enabled(ctx)
	if err != nil || !enabled {
		return err
	}
	now := s.now()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var expired []Order
		if err := tx.Table(ordersTable).Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("status='pending_pay' AND lock_expires_at<=?", now).Limit(100).Find(&expired).Error; err != nil {
			return err
		}
		for _, o := range expired {
			if err := tx.Table(ordersTable).Where("id=? AND status='pending_pay'", o.ID).Updates(map[string]any{"status": "cancelled", "updated_at": now}).Error; err != nil {
				return err
			}
			if err := tx.Table(listingsTable).Where("id=? AND status='locked'", o.ListingID).Updates(map[string]any{"status": "on_sale", "updated_at": now}).Error; err != nil {
				return err
			}
		}
		if err := tx.Table(listingsTable).Where("status IN ('pending','on_sale') AND expires_at<=?", now).Updates(map[string]any{"status": "off_shelf", "updated_at": now}).Error; err != nil {
			return err
		}
		var orders []Order
		if err := tx.Table(ordersTable).Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("status='paid' AND settle_at<=? AND refund_window_end<=?", now, now).Limit(100).Find(&orders).Error; err != nil {
			return err
		}
		for i := range orders {
			if err := settleOrderTx(tx, &orders[i], now); err != nil {
				return err
			}
		}
		return nil
	})
}
func settleOrderTx(tx *gorm.DB, o *Order, now time.Time) error {
	var wallet Wallet
	if err := tx.Table(walletTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=?", o.SellerID).First(&wallet).Error; err != nil {
		return err
	}
	income, _ := parsePositiveMoney(o.SellerIncome)
	balance, _ := parseUnsignedMoney(wallet.Balance)
	frozen, _ := parseUnsignedMoney(wallet.FrozenBalance)
	total, _ := parseUnsignedMoney(wallet.TotalIncome)
	if frozen < income {
		return errors.New("待结算资金不足")
	}
	after := balance + income
	if err := tx.Table(walletTable).Where("user_id=?", o.SellerID).Updates(map[string]any{"balance": formatMoney(after), "frozen_balance": formatMoney(frozen - income), "total_income": formatMoney(total + income), "updated_at": now}).Error; err != nil {
		return err
	}
	if err := tx.Table(ordersTable).Where("id=? AND status='paid'", o.ID).Updates(map[string]any{"status": "settled", "updated_at": now}).Error; err != nil {
		return err
	}
	return appendFlow(tx, o.SellerID, "settled", "in", o.SellerIncome, formatMoney(after), o.ID, "交易结算", now)
}
func (s *Service) StartMaintenance(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = s.RunMaintenance(ctx)
			}
		}
	}()
}

func appendFlow(tx *gorm.DB, userID int64, flowType, direction, amount, balance string, refID int64, remark string, at time.Time) error {
	return tx.Table(flowsTable).Create(&Flow{FlowNo: "MF" + at.Format("20060102150405") + fmt.Sprintf("%06d", snowflake.GenID()%1000000), UserID: userID, Type: flowType, Direction: direction, Amount: amount, BalanceAfter: balance, RefID: refID, Remark: remark, CreatedAt: at}).Error
}
func creditBilling(tx *gorm.DB, userID, amount, refID int64, now time.Time) error {
	if err := tx.Exec("INSERT IGNORE INTO billing.accounts (id,customer_id,balance,frozen_amount,credit_limit,total_recharge,total_consume,created_at,updated_at) VALUES (?,?,0,0,0,0,0,?,?)", snowflake.GenID(), userID, now, now).Error; err != nil {
		return err
	}
	var account struct{ ID, Balance int64 }
	if err := tx.Table("billing.accounts").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id,balance").Where("customer_id=?", userID).First(&account).Error; err != nil {
		return err
	}
	after := account.Balance + amount
	if err := tx.Table("billing.accounts").Where("id=?", account.ID).Updates(map[string]any{"balance": after, "updated_at": now}).Error; err != nil {
		return err
	}
	return tx.Exec("INSERT INTO billing.transactions (id,tx_no,customer_id,type,direction,amount,balance_before,balance_after,related_type,related_id,remark,created_at,updated_at) VALUES (?,?,?,4,1,?,?,?,?,?,'交易市场退款',?,?)", snowflake.GenID(), "MR"+fmt.Sprintf("%d", snowflake.GenID()), userID, amount, account.Balance, after, "marketplace", refID, now, now).Error
}
func (s *Service) acquire(ctx context.Context, key string, ttl time.Duration) (func(), error) {
	if s.redis == nil {
		return func() {}, nil
	}
	token := strconv.FormatInt(snowflake.GenID(), 10)
	ok, err := s.redis.SetNX(ctx, key, token, ttl).Result()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("操作处理中，请稍后重试")
	}
	return func() {
		_, _ = s.redis.Eval(context.Background(), "if redis.call('get',KEYS[1])==ARGV[1] then return redis.call('del',KEYS[1]) else return 0 end", []string{key}, token).Result()
	}, nil
}
func (s *Service) notify(scene string, userID, businessID int64, params map[string]string) {
	if s.notifier == nil {
		return
	}
	go func() {
		_ = s.notifier.NotifyCustomer(context.Background(), scene, userID, strconv.FormatInt(businessID, 10), params)
	}()
}
func (s *Service) notifyTrade(o *Order) {
	var listing Listing
	_ = s.db.Table(listingsTable).Where("id=?", o.ListingID).First(&listing).Error
	params := map[string]string{"order_no": o.OrderNo, "product_name": listing.ProductName}
	s.notify("marketplace_trade_success", o.BuyerID, o.ID, params)
	s.notify("marketplace_transfer_complete", o.BuyerID, o.ID, params)
	s.notify("marketplace_transfer_complete", o.SellerID, o.ID, params)
}

func MoneyToCents(v string) (int64, error) { return parsePositiveMoney(v) }
