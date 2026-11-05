package domains

import (
	"context"
	invoice "ddyidc/internal/billing/api"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	balance "ddyidc/internal/billing/balance"
	od "ddyidc/internal/order/domain"
	state "ddyidc/internal/order/repository"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/tasks"
	"ddyidc/pkg/crypto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maximumPrice balance.Money = 500000000

func moneyValue(v any) (balance.Money, error) {
	m, e := balance.ParseMoney(stringValue(v))
	if e != nil || m <= 0 || m > maximumPrice {
		return 0, errors.New("当前域名报价无效，请重新查询")
	}
	return m, nil
}
func sellingPrice(cost balance.Money, cfg Settings) (balance.Money, error) {
	if cost <= 0 || cost > maximumPrice || cfg.MarkupBPS < 0 || cfg.MarkupBPS > 100000 || cfg.ServiceFee < 0 || cfg.ServiceFee > maximumPrice {
		return 0, errors.New("域名定价无效")
	}
	v := cost + (cost*balance.Money(cfg.MarkupBPS)+9999)/10000 + cfg.ServiceFee
	if v > maximumPrice {
		return 0, errors.New("域名报价超出范围")
	}
	return v, nil
}
func retailAvailability(value any, cfg Settings) any {
	rows, ok := value.([]any)
	if !ok {
		return scrub(value, false)
	}
	for _, v := range rows {
		if row, ok := v.(map[string]any); ok {
			if cost, e := moneyValue(row["price"]); e == nil {
				if price, e := sellingPrice(cost, cfg); e == nil {
					row["amount"] = price.String()
				}
			}
			delete(row, "price")
		}
	}
	return scrub(rows, false)
}
func (s *Service) retailResult(op OperationSpec, input map[string]string, value any, cfg Settings) (*ActionResult, error) {
	row, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("未取得有效的域名报价")
	}
	var cost balance.Money
	var e error
	if op.Key == "1.1" {
		cost, e = moneyValue(row["buyprice"])
		if e == nil {
			if renew, err := moneyValue(row["renewprice"]); err == nil {
				retail, err := sellingPrice(renew, cfg)
				if err != nil {
					return nil, err
				}
				row["renewAmount"] = retail.String()
				years := 1
				if input["year"] != "" {
					years, _ = strconv.Atoi(input["year"])
				}
				cost += balance.Money(years-1) * renew
				row["buyyear"] = years
			} else {
				return nil, err
			}
		}
	} else {
		key := "price"
		if op.Key == "5.2" {
			key = "needprice"
		}
		cost, e = moneyValue(row[key])
	}
	if e != nil {
		return nil, e
	}
	amount, e := sellingPrice(cost, cfg)
	if e != nil {
		return nil, e
	}
	safe, _ := scrub(row, false).(map[string]any)
	safe["amount"] = amount.String()
	return &ActionResult{Status: "succeeded", Data: safe}, nil
}

type purchasePayload struct {
	Params        map[string]string `json:"params"`
	OldExpiry     string            `json:"oldExpiry,omitempty"`
	OldLockExpiry string            `json:"oldLockExpiry,omitempty"`
}

func domainTime(value string) (time.Time, error) {
	zone, _ := time.LoadLocation("Asia/Shanghai")
	for _, format := range []string{"2006-01-02 15:04:05", "2006-01-02", time.RFC3339} {
		if t, e := time.ParseInLocation(format, value, zone); e == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("域名到期时间无效")
}
func anniversary(value time.Time, years int) time.Time {
	last := time.Date(value.Year()+years, value.Month()+1, 0, 0, 0, 0, 0, value.Location()).Day()
	day := value.Day()
	if day > last {
		day = last
	}
	return time.Date(value.Year()+years, value.Month(), day, value.Hour(), value.Minute(), value.Second(), value.Nanosecond(), value.Location())
}
func (s *Service) providerData(ctx context.Context, cfg Settings, key string, input map[string]string) (map[string]any, error) {
	op, e := operation(key)
	if e != nil {
		return nil, e
	}
	v, e := s.call(ctx, cfg, op, input)
	if e != nil {
		return nil, e
	}
	row, ok := decodeData(v.Data).(map[string]any)
	if !ok {
		return nil, ErrConnection
	}
	return row, nil
}
func (s *Service) cost(ctx context.Context, cfg Settings, action string, wire map[string]string, payload *purchasePayload) (balance.Money, error) {
	switch action {
	case "4.6":
		parts := strings.SplitN(wire["domain"], ".", 2)
		result, e := s.call(ctx, cfg, mustOperation("3.1"), map[string]string{"domain": parts[0], "suffix": "." + parts[1]})
		if e != nil {
			return 0, e
		}
		items, ok := decodeData(result.Data).([]any)
		if !ok || len(items) != 1 {
			return 0, ErrConnection
		}
		entry, ok := items[0].(map[string]any)
		if !ok || stringValue(entry["name"]) != wire["domain"] || stringValue(entry["avail"]) != "1" {
			return 0, errors.New("当前域名不可注册，请重新查询")
		}
		years, _ := strconv.Atoi(wire["regyear"])
		if premium := entry["price"]; premium != nil {
			if cost, e := moneyValue(premium); e == nil {
				if years != 1 {
					return 0, errors.New("溢价域名请先选择一年注册")
				}
				return cost, nil
			}
		}
		row, e := s.providerData(ctx, cfg, "1.1", map[string]string{"type": "domain", "value": wire["domain"], "year": "1"})
		if e != nil {
			return 0, e
		}
		buy, e := moneyValue(row["buyprice"])
		if e != nil {
			return 0, e
		}
		renew, e := moneyValue(row["renewprice"])
		if e != nil {
			return 0, e
		}
		total := buy + balance.Money(years-1)*renew
		if total <= 0 || total > maximumPrice {
			return 0, errors.New("注册报价超出范围")
		}
		return total, nil
	case "3.2":
		info, e := s.providerData(ctx, cfg, "4.8", map[string]string{"domain": wire["domain"]})
		if e != nil {
			return 0, e
		}
		if stringValue(info["domain"]) != wire["domain"] {
			return 0, ErrOwnership
		}
		expiry := stringValue(info["rexpiredate"])
		if _, e = domainTime(expiry); e != nil {
			return 0, e
		}
		if payload.OldExpiry != "" && payload.OldExpiry != expiry {
			return 0, errors.New("域名到期时间已变化，请重新创建续费订单")
		}
		payload.OldExpiry = expiry
		row, e := s.providerData(ctx, cfg, "3.14", map[string]string{"domain": wire["domain"], "year": wire["year"]})
		if e != nil {
			return 0, e
		}
		return moneyValue(row["price"])
	case "5.1":
		row, e := s.providerData(ctx, cfg, "5.2", map[string]string{"domain": wire["domain"], "authcode": wire["authcode"]})
		if e != nil {
			return 0, e
		}
		return moneyValue(row["needprice"])
	case "market.buy":
		row, e := s.providerData(ctx, cfg, "market.detail", map[string]string{"domain": wire["domain"]})
		if e != nil {
			return 0, e
		}
		if stringValue(row["domain"]) != wire["domain"] {
			return 0, ErrConnection
		}
		return moneyValue(row["price"])
	case "3.16", "3.39":
		if action == "3.39" {
			old, e := s.providerData(ctx, cfg, "3.37", map[string]string{"domain": wire["domain"]})
			if e != nil {
				return 0, e
			}
			expiry := stringValue(old["endtime"])
			if _, e = domainTime(expiry); e != nil {
				return 0, e
			}
			if payload.OldLockExpiry != "" && expiry != payload.OldLockExpiry {
				return 0, errors.New("安全锁到期时间已变化，请重新报价")
			}
			payload.OldLockExpiry = expiry
		}
		row, e := s.providerData(ctx, cfg, "3.17", map[string]string{"domain": wire["domain"], "buyyear": wire["buyyear"], "ver": wire["ver"]})
		if e != nil {
			return 0, e
		}
		return moneyValue(row["price"])
	case "3.28":
		if cfg.RedemptionCost <= 0 || cfg.RedemptionCost > maximumPrice {
			return 0, errors.New("暂未取得赎回费用，请联系客服核实")
		}
		return cfg.RedemptionCost, nil
	}
	return 0, errors.New("该操作不支持创建收费订单")
}
func mustOperation(key string) OperationSpec { v, _ := operation(key); return v }
func (s *Service) Quote(ctx context.Context, user int64, req ActionRequest) (*Quote, error) {
	op, e := operation(req.Action)
	if e != nil || !op.Paid {
		return nil, errors.New("该操作无需收费报价")
	}
	cfg, e := s.ready(ctx)
	if e != nil {
		return nil, e
	}
	input, e := validateInput(op, req.Params)
	if e != nil {
		return nil, e
	}
	if e = s.authorize(ctx, s.db, user, op, input); e != nil {
		return nil, e
	}
	if op.Scope == "new-domain" {
		var n int64
		if e = s.db.WithContext(ctx).Model(&Asset{}).Where("domain=?", input["domain"]).Count(&n).Error; e != nil {
			return nil, e
		}
		if n > 0 {
			return nil, errors.New("该域名已有业务记录，请从我的域名进入管理")
		}
	}
	payload := purchasePayload{Params: req.Params}
	cost, e := s.cost(ctx, cfg, req.Action, input, &payload)
	if e != nil {
		return nil, e
	}
	amount, e := sellingPrice(cost, cfg)
	if e != nil {
		return nil, e
	}
	raw, _ := json.Marshal(payload)
	cipher, e := crypto.Encrypt(string(raw), s.key)
	if e != nil {
		return nil, e
	}
	q := Quote{ID: snowflake.GenID(), CustomerID: user, Action: req.Action, Domain: input["domain"], PayloadCipher: cipher, Cost: cost, Amount: amount, ConfigRevision: cfg.Revision, ExpiresAt: s.now().Add(5 * time.Minute), CreatedAt: s.now()}
	if e = s.db.WithContext(ctx).Create(&q).Error; e != nil {
		return nil, e
	}
	return &q, nil
}

type PurchaseResult struct {
	OrderID string        `json:"orderId"`
	OrderNo string        `json:"orderNo"`
	Amount  balance.Money `json:"amount"`
}

func (s *Service) CreateOrder(ctx context.Context, user, quoteID int64, confirmed bool) (*PurchaseResult, error) {
	if !confirmed {
		return nil, errors.New("请先确认报价及购买内容")
	}
	cfg, e := s.ready(ctx)
	if e != nil {
		return nil, e
	}
	var result PurchaseResult
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var q Quote
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&q, "id=? AND customer_id=?", quoteID, user).Error; e != nil {
			return ErrOwnership
		}
		if q.OrderID != nil {
			var old od.Order
			if e := tx.Take(&old, "id=? AND customer_id=?", *q.OrderID, user).Error; e != nil {
				return e
			}
			result = PurchaseResult{fmt.Sprint(old.ID), old.OrderNo, balance.Money(old.TotalAmount)}
			return nil
		}
		if !q.ExpiresAt.After(s.now()) || q.ConfigRevision != cfg.Revision {
			return errors.New("报价已变化或过期，请重新查询")
		}
		var customer struct{ TenantID int64 }
		if e := tx.Table("customer.customers").Select("tenant_id").Clauses(clause.Locking{Strength: "UPDATE"}).Take(&customer, "id=?", user).Error; e != nil {
			return ErrOwnership
		}
		var open int64
		if e := tx.Table("system.domain_orders d").Joins("JOIN `order`.orders o ON o.id=d.id").Where("d.customer_id=? AND d.domain=? AND d.action=? AND ((d.status='pending' AND o.status IN (1,2)) OR d.status IN ('executing','submitted','unknown','review'))", user, q.Domain, q.Action).Count(&open).Error; e != nil {
			return e
		}
		if open > 0 {
			return ErrPending
		}
		now := s.now()
		id := snowflake.GenID()
		no := fmt.Sprintf("DOM%d", id)
		o := od.Order{ID: id, OrderNo: no, CustomerID: user, TenantID: customer.TenantID, TotalAmount: int64(q.Amount), PayAmount: int64(q.Amount), Status: od.OrderStatusPending, CreatedAt: now, UpdatedAt: now}
		if e := tx.Create(&o).Error; e != nil {
			return e
		}
		snapshot, _ := json.Marshal(map[string]string{"domain": q.Domain, "action": q.Action, "amount": q.Amount.String()})
		if e := tx.Create(&od.OrderItem{OrderID: id, ProductName: "域名服务 · " + q.Domain, ModuleType: "domains", ConfigSnapshot: string(snapshot), Qty: 1, UnitPrice: int64(q.Amount), Subtotal: int64(q.Amount), CreatedAt: now, UpdatedAt: now}).Error; e != nil {
			return e
		}
		if e := tx.Create(&DomainOrder{ID: id, CustomerID: user, QuoteID: q.ID, Action: q.Action, Domain: q.Domain, PayloadCipher: q.PayloadCipher, Cost: q.Cost, Amount: q.Amount, Status: "pending", CreatedAt: now, UpdatedAt: now}).Error; e != nil {
			return e
		}
		if e := tx.Model(&q).Update("order_id", id).Error; e != nil {
			return e
		}
		if e := invoice.IssueOrderTx(tx, id); e != nil {
			return e
		}
		result = PurchaseResult{fmt.Sprint(id), no, q.Amount}
		return nil
	})
	return &result, e
}
func CheckPaymentTx(ctx context.Context, tx *gorm.DB, user, order, amount int64, at time.Time) error {
	var count int64
	if e := tx.WithContext(ctx).Table("order.order_items").Where("order_id=? AND module_type='domains'", order).Count(&count).Error; e != nil {
		return e
	}
	if count == 0 {
		return nil
	}
	var valid int64
	e := tx.Table("system.domain_orders d").Joins("JOIN system.domain_quotes q ON q.id=d.quote_id").Joins("JOIN system.domain_settings c ON c.id=1").Where("d.id=? AND d.customer_id=? AND d.amount=? AND d.status='pending' AND q.expires_at>? AND q.config_revision=c.revision AND c.enabled=1 AND c.username<>'' AND c.password_cipher<>''", order, user, balance.Money(amount).String(), at).Count(&valid).Error
	if e != nil {
		return e
	}
	if count != 1 || valid != 1 {
		return errors.New("域名订单报价或服务状态已变化，请重新下单")
	}
	return nil
}
func (s *Service) payload(row DomainOrder) (purchasePayload, error) {
	var p purchasePayload
	raw, e := crypto.Decrypt(row.PayloadCipher, s.key)
	if e != nil {
		return p, e
	}
	e = json.Unmarshal([]byte(raw), &p)
	return p, e
}
func (s *Service) PayBalance(ctx context.Context, user, order int64) error {
	var existing od.Order
	if e := s.db.WithContext(ctx).Take(&existing, "id=? AND customer_id=?", order, user).Error; e != nil {
		return ErrOwnership
	}
	if existing.Status == od.OrderStatusPaid || existing.Status == od.OrderStatusCompleted {
		if existing.PayType != nil && *existing.PayType == 3 && existing.BalanceAmount == existing.PayAmount {
			return nil
		}
		return errors.New("订单已通过其他方式付款")
	}
	cfg, e := s.ready(ctx)
	if e != nil {
		return e
	}
	var candidate DomainOrder
	if e = s.db.WithContext(ctx).Take(&candidate, "id=? AND customer_id=?", order, user).Error; e != nil {
		return ErrOwnership
	}
	p, e := s.payload(candidate)
	if e != nil {
		return e
	}
	op, _ := operation(candidate.Action)
	wire, e := validateInput(op, p.Params)
	if e != nil {
		return e
	}
	if e = s.authorize(ctx, s.db, user, op, wire); e != nil {
		return e
	}
	cost, e := s.cost(ctx, cfg, candidate.Action, wire, &p)
	if e != nil {
		return e
	}
	if cost != candidate.Cost {
		return errors.New("当前价格已变化，请重新获取报价")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var o od.Order
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&o, "id=? AND customer_id=?", order, user).Error; e != nil {
			return ErrOwnership
		}
		if o.Status == od.OrderStatusPaid || o.Status == od.OrderStatusCompleted {
			if o.PayType != nil && *o.PayType == 3 && o.BalanceAmount == o.PayAmount {
				return nil
			}
			return errors.New("订单已通过其他方式付款")
		}
		if o.Status != od.OrderStatusPending {
			return errors.New("订单当前不可支付")
		}
		if e := CheckPaymentTx(ctx, tx, user, order, o.PayAmount, s.now()); e != nil {
			return e
		}
		if balance.Money(o.PayAmount) != candidate.Amount {
			return errors.New("订单金额已变化，请重新下单")
		}
		var pending int64
		if e := tx.Table("payment.payments").Where("biz_type='order' AND biz_id=? AND status='pending'", order).Count(&pending).Error; e != nil {
			return e
		}
		if pending > 0 {
			return errors.New("请先关闭正在进行的在线支付单")
		}
		if _, e := balance.New(tx).Change(ctx, balance.ChangeRequest{CustomerID: user, Type: "order_pay", Amount: balance.Money(-o.PayAmount).String(), RefType: "order", RefID: order, Channel: "order", RequestID: fmt.Sprintf("order-balance:%d", order), Notes: "域名订单余额支付"}); e != nil {
			return e
		}
		changed, e := state.AcceptBalancePaymentTx(tx, order, user, o.PayAmount, s.now())
		if e != nil {
			return e
		}
		if !changed {
			return errors.New("订单支付状态已变化")
		}
		return tasks.EnqueuePayment(tx, fmt.Sprintf("order-balance:%d", order), tasks.PaymentPayload{CustomerID: user, BusinessID: order, BusinessType: "order", Channel: "balance", AmountFen: o.PayAmount})
	})
}
