package instance

import (
	"context"
	invoice "ddyidc/internal/billing/api"
	balance "ddyidc/internal/billing/balance"
	dom "ddyidc/internal/instance/domain"
	order "ddyidc/internal/order/domain"
	orderstate "ddyidc/internal/order/repository"
	"ddyidc/internal/shared/renewalquote"
	schemamode "ddyidc/internal/shared/schema"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/tasks"
	"ddyidc/internal/upstream/provider/contracts"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"math/big"
	"strconv"
	"strings"
	"time"
)

//go:embed renewal_schema.sql
var renewalSchema string

type RenewalSettings struct {
	InstanceID int64 `gorm:"primaryKey"`
	CustomerID int64
	Enabled    bool
	UpdatedAt  time.Time
}

func (RenewalSettings) TableName() string { return "instance.instance_renewal_settings" }

type Renewal struct {
	ID               int64 `gorm:"primaryKey"`
	OrderID          int64 `gorm:"uniqueIndex"`
	InstanceID       int64 `gorm:"index"`
	CustomerID       int64
	OwnershipVersion int64
	SupplierID       int64
	HostID           string `gorm:"size:64"`
	Cycle            string `gorm:"size:32"`
	Duration         int64
	Amount           balance.Money `gorm:"type:decimal(14,2)"`
	Cost             balance.Money `gorm:"type:decimal(14,2)"`
	BaseExpiry       time.Time
	InvoiceID        string `gorm:"size:64"`
	State            string `gorm:"size:32;index"`
	LastError        string `gorm:"size:255"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (Renewal) TableName() string { return "instance.instance_renewals" }
func (s *Service) EnsureManagementSchema(ctx context.Context) error {
	if !schemamode.MigrationsEnabled() {
		return nil
	}
	if s.db == nil {
		return ErrUnsupported
	}
	if err := s.ensureAdminSchema(ctx); err != nil {
		return err
	}
	for _, statement := range strings.Split(renewalSchema, ";") {
		if strings.TrimSpace(statement) != "" {
			if e := s.db.WithContext(ctx).Exec(statement).Error; e != nil {
				return e
			}
		}
	}
	return nil
}

type RenewalOption struct {
	ExchangeRate string `json:"-"`

	QuoteRevision string `json:"quoteRevision"`

	Cycle    string        `json:"cycle"`
	Label    string        `json:"label"`
	Amount   string        `json:"amount"`
	ListAmount string      `json:"listAmount,omitempty"`
	Discount   string      `json:"discount,omitempty"`
	OverrideID string      `json:"overrideId,omitempty"`
	Duration int64         `json:"duration"`
	Cost     balance.Money `json:"-"`
}
type RenewalOrder struct {
	ID      string `json:"id"`
	OrderID string `json:"orderId"`
	OrderNo string `json:"orderNo"`
	Amount  string `json:"amount"`
	State   string `json:"state"`
}

func retailRenewal(cost balance.Money, rate, profit string, kind int) (balance.Money, error) {
	r, ok := new(big.Rat).SetString(rate)
	if !ok || r.Sign() <= 0 {
		return 0, errors.New("供应商汇率无效")
	}
	p, ok := new(big.Rat).SetString(profit)
	if !ok {
		return 0, errors.New("续费加价无效")
	}
	v := new(big.Rat).Mul(new(big.Rat).SetInt64(int64(cost)), r)
	if kind == 1 {
		v.Add(v, new(big.Rat).Mul(p, big.NewRat(100, 1)))
	} else {
		v.Mul(v, new(big.Rat).Add(big.NewRat(1, 1), new(big.Rat).Quo(p, big.NewRat(100, 1))))
	}
	whole := new(big.Int).Quo(v.Num(), v.Denom())
	if new(big.Int).Mod(v.Num(), v.Denom()).Sign() > 0 {
		whole.Add(whole, big.NewInt(1))
	}
	if !whole.IsInt64() || whole.Sign() <= 0 || whole.Int64() > 99999999999999 {
		return 0, errors.New("续费价格无效")
	}
	return balance.Money(whole.Int64()), nil
}
func (s *Service) RenewalOptions(ctx context.Context, customer, id int64) ([]RenewalOption, error) {
	return s.joinRenewalQuoteRead(ctx, fmt.Sprintf("%d:%d", customer, id), func(shared context.Context) ([]RenewalOption, error) { return s.renewalOptions(shared, customer, id) })
}
func (s *Service) renewalOptions(ctx context.Context, customer, id int64) ([]RenewalOption, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	h, e := s.owned(ctx, customer, id)
	if e != nil {
		return nil, e
	}
	if h.Status == 6 {
		return nil, ErrUnsupported
	}
	if s.db == nil {
		return nil, ErrUnsupported
	}
	p, warning := s.providerContext(ctx, h)
	if warning != "" {
		return nil, errors.New("服务配置不可用")
	}
	rp, ok := p.(contracts.RenewalProvider)
	if !ok {
		return nil, errors.New("此服务不支持在线续费")
	}
	quotes, e := rp.RenewalQuotes(parseHostID(h.UpstreamInstanceID))
	if e != nil {
		return nil, errors.New("读取续费报价失败，请稍后重试")
	}
	var price struct {
		Rate   string
		Profit string
		Kind   int
	}
	e = s.db.WithContext(ctx).Raw("SELECT s.rate,u.renew_profit_value profit,u.renew_profit_type kind FROM common.suppliers s JOIN product.upstream_products u ON u.supplier_id=s.id WHERE u.product_id=? AND s.id=?", h.ProductID, h.UpstreamProviderID).Scan(&price).Error
	if e != nil {
		return nil, e
	}
	out := []RenewalOption{}
	for _, q := range quotes {
		cost, e := balance.ParseMoney(q.Amount)
		if e != nil || cost <= 0 {
			continue
		}
		original, e := balance.ParseMoney(q.OriginalAmount)
		if e != nil || original <= 0 || original < cost {
			return nil, errors.New("供应商续费原价未确认")
		}
		amount, e := retailRenewal(original, price.Rate, "0", 0)
		if e != nil {
			return nil, e
		}
		final, discount, overrideID := s.resolveRenewalPrice(ctx, h.ID, q.Cycle, amount, cost)
		option := RenewalOption{ExchangeRate: price.Rate, Cycle: q.Cycle, Label: q.Label, Amount: final.String(), ListAmount: amount.String(), Duration: q.Duration, Cost: cost}
		if discount > 0 {
			option.Discount = discount.String()
		}
		if overrideID > 0 {
			option.OverrideID = strconv.FormatInt(overrideID, 10)
		}
		option.QuoteRevision = renewalIntentRevision(*h, option)
		out = append(out, option)
	}
	return out, nil
}

type RenewalIntent struct {
	QuoteRevision  string `json:"quoteRevision"`
	ReplaceOrderID string `json:"replaceOrderId,omitempty"`
}

func (s *Service) CreateRenewal(ctx context.Context, customer, id int64, cycle string) (*RenewalOrder, error) {
	return s.CreateRenewalIntent(ctx, customer, id, cycle, RenewalIntent{})
}
func (s *Service) CreateRenewalIntent(ctx context.Context, customer, id int64, cycle string, intent RenewalIntent) (*RenewalOrder, error) {
	h, e := s.owned(ctx, customer, id)
	if e != nil {
		return nil, e
	}
	locked, e := s.repo.IsMarketplaceLocked(ctx, id)
	if e != nil {
		return nil, e
	}
	if locked {
		return nil, ErrTradingLocked
	}
	if h.Status == 6 || h.UpstreamProviderID == nil {
		return nil, ErrUnsupported
	}
	options, e := s.RenewalOptions(ctx, customer, id)
	if e != nil {
		return nil, e
	}
	var selected *RenewalOption
	for i := range options {
		if options[i].Cycle == cycle {
			selected = &options[i]
			break
		}
	}
	if selected == nil {
		return nil, errors.New("请选择有效续费周期")
	}
	// Client revisions are display snapshots. Always price this intent from the
	// fresh server quote; payment still rechecks the order and actual amount.

	p, _ := s.provider(h)
	real, e := p.GetInstanceDetail(parseHostID(h.UpstreamInstanceID))
	if e != nil || real == nil || real.ID != h.UpstreamInstanceID || real.ExpiresAt == nil {
		return nil, errors.New("实例到期信息未确认，不能创建续费订单")
	}
	if unavailableLifecycle(real, h.UpstreamInstanceID) == "deleted" {
		return nil, ErrUnsupported
	}
	expiry := *real.ExpiresAt
	amount, _ := balance.ParseMoney(selected.Amount)
	var result RenewalOrder
	var priorOrder int64
	if e = s.db.WithContext(ctx).Model(&Renewal{}).Where("instance_id=? AND state NOT IN ?", id, []string{"completed", "cancelled"}).Order("created_at DESC").Limit(1).Select("order_id").Scan(&priorOrder).Error; e != nil {
		return nil, e
	}
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// All payment and replacement paths lock order before renewal/instance.
		if priorOrder > 0 {
			var locked order.Order
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=?", priorOrder, customer).Take(&locked).Error; e != nil {
				return e
			}
		}
		var current dom.Host
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=?", id, customer).Take(&current).Error; e != nil {
			return ErrNotFound
		}
		if current.OwnershipVersion != h.OwnershipVersion || current.UpstreamInstanceID != h.UpstreamInstanceID || renewalIntentRevision(current, *selected) != renewalIntentRevision(*h, *selected) {
			return errors.New("实例归属或绑定已变更")
		}
		if current.Status == 6 {
			return ErrUnsupported
		}
		if err := s.renewalRefundAllowed(ctx, tx, id); err != nil {
			return err
		}
		if err := s.upgradeMutationAllowed(ctx, tx, id); err != nil {
			return err
		}
		var busy int64
		if err := tx.Table("system.push_transfers").Where("instance_id=? AND status='pending'", id).Count(&busy).Error; err != nil {
			return err
		}
		if busy > 0 {
			return errors.New("实例转让中，不能续费")
		}
		if err := tx.Table("system.marketplace_listings").Where("product_id=? AND status IN ('pending','on_sale','locked')", id).Count(&busy).Error; err != nil {
			return err
		}
		if busy > 0 {
			return ErrTradingLocked
		}

		var previous Renewal
		err := tx.Where("instance_id=? AND state NOT IN ?", id, []string{"completed", "cancelled"}).Order("created_at DESC").Take(&previous).Error
		if err == nil {
			// A concurrent creator won the instance lock. Retry outside this tx;
			// never acquire its order lock while holding an instance row lock.
			if previous.OrderID != priorOrder {
				return &renewalquote.Error{Code: "RENEWAL_QUOTE_EXPIRED"}
			}
			var o order.Order
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", previous.OrderID).Take(&o).Error; e != nil {
				return e
			}
			if o.Status != order.OrderStatusCancelled {
				if previous.OwnershipVersion != h.OwnershipVersion || previous.SupplierID != *h.UpstreamProviderID || previous.HostID != h.UpstreamInstanceID {
					return renewalquote.Unverified()
				}
				mismatch := previous.Cycle != cycle || previous.Duration != selected.Duration || previous.Cost != selected.Cost || o.TotalAmount != int64(amount)
				if o.Status == 1 && mismatch && previous.Cycle == cycle && s.renewalPriceCorrector != nil {
					if previous.State != "unpaid" || previous.InvoiceID != "" || !previous.BaseExpiry.Equal(expiry) {
						return renewalquote.Unverified()
					}
					net, e := s.renewalPriceCorrector(ctx, tx, customer, previous.OrderID, int64(amount))
					if e != nil {
						return e
					}
					if e = tx.Model(&previous).Updates(map[string]any{"amount": balance.Money(net).String(), "cost": selected.Cost.String(), "duration": selected.Duration}).Error; e != nil {
						return e
					}
					result = RenewalOrder{ID: strconv.FormatInt(previous.ID, 10), OrderID: strconv.FormatInt(o.ID, 10), OrderNo: o.OrderNo, Amount: balance.Money(net).String(), State: "unpaid"}
					return nil
				}
				if o.Status == 1 && mismatch {
					if previous.State != "unpaid" || previous.InvoiceID != "" || s.renewalReplacementCancel == nil {
						return &renewalquote.Error{Code: "RENEWAL_PRICE_CHANGED", OldOrderID: strconv.FormatInt(o.ID, 10), OldAmount: balance.Money(o.TotalAmount).String(), NewAmount: selected.Amount}
					}
					intent.ReplaceOrderID = strconv.FormatInt(o.ID, 10)
					if e := s.renewalReplacementCancel(ctx, tx, customer, o.ID); e != nil {
						return e
					}
				} else {
					result = RenewalOrder{ID: strconv.FormatInt(previous.ID, 10), OrderID: strconv.FormatInt(o.ID, 10), OrderNo: o.OrderNo, Amount: previous.Amount.String(), State: previous.State}
					return nil
				}
			}
			if e := tx.Model(&previous).Update("state", "cancelled").Error; e != nil {
				return e
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		now := time.Now()
		oid, rid := snowflake.GenID(), snowflake.GenID()
		o := order.Order{ID: oid, OrderNo: fmt.Sprintf("REN%d", oid), CustomerID: customer, TenantID: current.TenantID, TotalAmount: int64(amount), PayAmount: int64(amount), Status: 1, CreatedAt: now, UpdatedAt: now}
		if e := tx.Create(&o).Error; e != nil {
			return e
		}
		snapshot, _ := json.Marshal(map[string]any{"billingCycle": cycle, "instanceId": strconv.FormatInt(id, 10), "type": "instance_renewal", "replacesOrderId": intent.ReplaceOrderID, "pricing": map[string]any{"listAmount": selected.ListAmount, "discount": selected.Discount, "overrideId": selected.OverrideID}})
		item := order.OrderItem{OrderID: oid, ProductID: h.ProductID, ProductName: h.ProductName + "（续费）", ModuleType: "instance_renewal", ConfigSnapshot: string(snapshot), Qty: 1, UnitPrice: int64(amount), Subtotal: int64(amount), InstanceID: &id, CreatedAt: now, UpdatedAt: now}
		if e := tx.Create(&item).Error; e != nil {
			return e
		}
		if e := invoice.IssueOrderTx(tx, item.OrderID); e != nil {
			return e
		}
		row := Renewal{ID: rid, OrderID: oid, InstanceID: id, CustomerID: customer, OwnershipVersion: h.OwnershipVersion, SupplierID: *h.UpstreamProviderID, HostID: h.UpstreamInstanceID, Cycle: cycle, Duration: selected.Duration, Amount: amount, Cost: selected.Cost, BaseExpiry: expiry, State: "unpaid", CreatedAt: now, UpdatedAt: now}
		if e := tx.Create(&row).Error; e != nil {
			return e
		}
		result = RenewalOrder{ID: strconv.FormatInt(rid, 10), OrderID: strconv.FormatInt(oid, 10), OrderNo: o.OrderNo, Amount: amount.String(), State: row.State}
		return nil
	})
	return &result, e
}
func (s *Service) PayRenewalBalance(ctx context.Context, customer, id, orderID int64, ip string) error {
	if _, e := s.owned(ctx, customer, id); e != nil {
		return e
	}
	locked, e := s.repo.IsMarketplaceLocked(ctx, id)
	if e != nil {
		return e
	}
	if locked {
		return ErrTradingLocked
	}
	var paid order.Order
	if e = s.db.WithContext(ctx).Where("id=? AND customer_id=?", orderID, customer).Take(&paid).Error; e != nil {
		return e
	}
	if paid.Status == 2 || paid.Status == 6 {
		return nil
	}
	ctx, e = s.VerifyRenewalPayment(ctx, customer, orderID)
	if e != nil {
		return e
	}
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var o order.Order
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=?", orderID, customer).Take(&o).Error; e != nil {
			return ErrNotFound
		}
		var r Renewal
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_id=? AND instance_id=? AND customer_id=?", orderID, id, customer).Take(&r).Error; e != nil {
			return ErrNotFound
		}
		if o.Status == 2 || o.Status == 6 {
			return nil
		}
		if o.Status != 1 || (r.State != "unpaid" && r.State != "invoiced_verified") {
			return errors.New("续费订单当前不可支付")
		}
		if e := renewalquote.CheckTx(ctx, tx, customer, orderID); e != nil {
			return e
		}

		var h dom.Host
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=? AND ownership_version=? AND upstream_instance_id=?", id, customer, r.OwnershipVersion, r.HostID).Take(&h).Error; e != nil {
			return ErrForbidden
		}
		var trading int64
		if err := s.renewalRefundAllowed(ctx, tx, id); err != nil {
			return err
		}
		if automatic, _ := ctx.Value(autoRenewPaymentKey{}).(bool); automatic {
			var settings RenewalSettings
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("instance_id=? AND customer_id=?", id, customer).Take(&settings).Error; err != nil || !settings.Enabled {
				return errors.New("自动续费已关闭，未扣款")
			}
			var approved AutoRenewalQuote
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("instance_id=? AND customer_id=? AND ownership_version=?", id, customer, h.OwnershipVersion).Take(&approved).Error; err != nil {
				return renewalquote.Unverified()
			}
			if approved.QuoteRevision != autoRenewalApprovalRevision(h, RenewalOption{Cycle: r.Cycle, Duration: r.Duration, Amount: balance.Money(o.TotalAmount).String()}) {
				return renewalquote.Unverified()
			}
		}
		if e := tx.Table("system.marketplace_listings").Where("product_id=? AND status IN ('pending','on_sale','locked')", id).Count(&trading).Error; e != nil {
			return e
		}
		if trading > 0 {
			return ErrTradingLocked
		}
		var pending int64
		if e := tx.Table("payment.payments").Where("biz_type='order' AND biz_id=? AND status='pending'", orderID).Count(&pending).Error; e != nil {
			return e
		}
		if pending > 0 {
			return errors.New("请先关闭正在进行的在线支付单")
		}
		actor, admin := actionActor(ctx, customer)
		if actor != 2 {
			admin = 0
		}
		_, e := balance.New(tx).Change(ctx, balance.ChangeRequest{CustomerID: customer, Type: "instance_renewal_pay", Amount: (-r.Amount).String(), RefType: "instance_renewal", RefID: r.ID, Channel: "instance_renewal", RequestID: fmt.Sprintf("instance-renewal-%d", r.ID), AdminID: admin, OperatorIP: ip, Notes: "实例续费订单余额支付"})
		if e != nil {
			return e
		}
		now := time.Now()
		changed, err := orderstate.AcceptBalancePaymentTx(tx, orderID, customer, int64(r.Amount), now)
		if err != nil {
			return err
		}
		if !changed {
			return errors.New("续费订单状态或金额已变化")
		}
		return tasks.EnqueuePayment(tx, fmt.Sprintf("renewal-balance:%d", r.ID), tasks.PaymentPayload{CustomerID: customer, BusinessID: orderID, BusinessType: "order", Channel: "balance", AmountFen: int64(r.Amount)})
	})
	if e != nil {
		return e
	}
	return nil
}
func (s *Service) SetAutoRenew(ctx context.Context, customer, id int64, enabled bool, expectedRevision ...string) error {
	revision := ""
	if len(expectedRevision) == 1 {
		revision = expectedRevision[0]
	}
	return s.SetAutoRenewCycle(ctx, customer, id, enabled, "", revision)
}

func (s *Service) SetAutoRenewCycle(ctx context.Context, customer, id int64, enabled bool, cycle, revision string) error {
	h, e := s.owned(ctx, customer, id)
	if e != nil {
		return e
	}
	var approved *RenewalOption
	if enabled {
		quotes, err := s.RenewalOptions(ctx, customer, id)
		if err != nil {
			return err
		}
		if cycle == "" {
			cycle, e = s.OriginalRenewalCycle(ctx, customer, id)
			if e != nil {
				return e
			}
		}
		for i := range quotes {
			if quotes[i].Cycle == cycle {
				approved = &quotes[i]
				break
			}
		}
		if approved == nil {
			return renewalquote.Unverified()
		}
		if h.ExpiresAt == nil {
			return errors.New("缺少到期时间")
		}
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current dom.Host
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=?", id, customer).Take(&current).Error; e != nil {
			return ErrForbidden
		}
		settings := RenewalSettings{InstanceID: id, CustomerID: customer, Enabled: enabled, UpdatedAt: time.Now()}
		if err := s.renewalRefundAllowed(ctx, tx, id); err != nil {
			return err
		}
		if enabled && current.Status == 6 {
			return ErrUnsupported
		}
		if enabled {
			if approved == nil || renewalIntentRevision(current, *approved) != approved.QuoteRevision {
				return renewalquote.Unverified()
			}
			if e := s.approveAutoRenewalTx(tx, current, *approved); e != nil {
				return e
			}
		}

		if e := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "instance_id"}}, DoUpdates: clause.AssignmentColumns([]string{"customer_id", "enabled", "updated_at"})}).Create(&settings).Error; e != nil {
			return e
		}
		actor, operator := actionActor(ctx, customer)
		now := time.Now()
		return tx.Create(&dom.Event{InstanceID: id, EventType: "auto_renew", OperatorType: actor, OperatorID: &operator, Status: 2, UpstreamRequest: `{}`, UpstreamResponse: fmt.Sprintf(`{"enabled":%t}`, enabled), CreatedAt: now, UpdatedAt: now, ProcessedAt: &now}).Error
	})
}
