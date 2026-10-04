// Package order service: real order/recharge flows. Coupons are charged and
// distribution rewards triggered through injected hooks so the order module
// stays decoupled from marketing.
package order

import (
	"context"
	"crypto/rand"
	"ddyidc/internal/certification"

	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	instancedom "ddyidc/internal/instance/domain"
	dom "ddyidc/internal/order/domain"
	"ddyidc/internal/order/inventory"
	rep "ddyidc/internal/order/repository"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/tasks"
	upstreammodel "ddyidc/internal/upstream/model"
	upstreamcontracts "ddyidc/internal/upstream/provider/contracts"
	upstream "ddyidc/internal/upstream/service"
	cryptoutil "ddyidc/pkg/crypto"
	"github.com/zeromicro/go-zero/core/logx"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MarketingHooks are the marketing-side collaborators (implemented by
// internal/marketing/api and wired in main.go).
type MarketingHooks interface {
	// ChargeCoupon marks the coupon used inside the order transaction and
	// returns the server-computed discount in cents.
	ChargeCoupon(ctx context.Context, tx *gorm.DB, userID, userCouponID, orderID, amountCents int64) (int64, error)
	// OnOrderPaid fires distribution rewards after an order is paid.
	OnOrderPaid(ctx context.Context, orderID int64, orderNo string, customerID, payCents int64) error
	// OnRechargePaid fires distribution rewards after a recharge succeeds.
	OnRechargePaid(ctx context.Context, rechargeID int64, rechargeNo string, customerID, amountCents int64) error
	// ReleaseCoupon returns the coupon bound to a cancelled order.
	ReleaseCoupon(ctx context.Context, tx *gorm.DB, orderID int64) error
}

// OrderRepository is the narrow repository contract the service depends on.
type OrderRepository interface {
	Transaction(ctx context.Context, fn func(tx *gorm.DB) error) error
	CreateOrderTx(ctx context.Context, tx *gorm.DB, v *dom.Order) error
	UpdateOrderTx(ctx context.Context, tx *gorm.DB, v *dom.Order) error
	CancelPendingOrderTx(ctx context.Context, tx *gorm.DB, customerID, orderID int64) error
	CreateItemTx(ctx context.Context, tx *gorm.DB, v *dom.OrderItem) error
	FindByID(ctx context.Context, id int64) (*dom.Order, error)
	List(ctx context.Context, page, limit int, filters map[string]any) ([]*dom.Order, int64, error)
	ListItems(ctx context.Context, id int64) ([]*dom.OrderItem, error)
	FindSellableProduct(ctx context.Context, id int64) (*rep.SellableProduct, error)
	ListPricingSpecs(ctx context.Context, productID int64) ([]rep.PricingSpec, error)
	CreateRecharge(ctx context.Context, v *rep.Recharge) error
	FindRecharge(ctx context.Context, id int64) (*rep.Recharge, error)
	UpdateRecharge(ctx context.Context, v *rep.Recharge) error
	ListRecharges(ctx context.Context, customerID int64, page, pageSize int) ([]*rep.Recharge, int64, error)
}

// NotificationHooks is implemented by the notification service and keeps
// order business logic independent from delivery providers.
type NotificationHooks interface {
	NotifyCustomer(context.Context, string, int64, string, map[string]string) error
}

// Service 订单应用服务。
type Service struct {
	repo        OrderRepository
	hooks       MarketingHooks
	notifier    NotificationHooks
	snapshotKey []byte
}

func (s *Service) SetNotifier(notifier NotificationHooks) { s.notifier = notifier }

func NewService(repo OrderRepository, hooks MarketingHooks, snapshotKey ...[]byte) *Service {
	var key []byte
	if len(snapshotKey) > 0 {
		key = append([]byte(nil), snapshotKey[0]...)
	}
	return &Service{repo: repo, hooks: hooks, snapshotKey: key}
}

type persistedConfigSnapshot struct {
	ConfigOptions      map[string]ConfigOptionInput `json:"configOptions,omitempty"`
	Hostname           string                       `json:"hostname,omitempty"`
	BillingCycle       string                       `json:"billingCycle,omitempty"`
	PasswordCiphertext string                       `json:"passwordCiphertext,omitempty"`
}

var (
	hostnamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{2,63}$`)
	optionIDPattern = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
)

func buildConfigSnapshot(input *CreateOrderInput, key []byte) (string, error) {
	if len(input.ConfigOptions) == 0 {
		return `{}`, nil
	}
	if len(input.ConfigOptions) > 64 {
		return "", errors.New("配置项数量超出限制")
	}
	for optionID, option := range input.ConfigOptions {
		if !optionIDPattern.MatchString(optionID) || strings.TrimSpace(option.Value) == "" {
			return "", errors.New("配置项参数错误")
		}
		if len([]rune(option.Field)) > 64 || len([]rune(option.DisplayName)) > 100 || len([]rune(option.DisplayValue)) > 200 {
			return "", errors.New("配置项文本过长")
		}
		option.Value = strings.TrimSpace(option.Value)
		option.Field = strings.TrimSpace(option.Field)
		option.DisplayName = strings.TrimSpace(option.DisplayName)
		option.DisplayValue = strings.TrimSpace(option.DisplayValue)
		input.ConfigOptions[optionID] = option
	}
	hostname := strings.TrimSpace(input.Hostname)
	if hostname == "" {
		var entropy [12]byte
		if _, err := rand.Read(entropy[:]); err != nil {
			return "", errors.New("主机名生成失败，请重试")
		}
		hostname = fmt.Sprintf("ser%x", entropy[:])
		input.Hostname = hostname
	}
	if !hostnamePattern.MatchString(hostname) {
		return "", errors.New("主机名格式错误")
	}
	if len(input.Password) != 12 || !regexp.MustCompile(`[a-z]`).MatchString(input.Password) || !regexp.MustCompile(`[A-Z]`).MatchString(input.Password) || !regexp.MustCompile(`[0-9]`).MatchString(input.Password) {
		return "", errors.New("实例密码必须为12位且包含大小写字母和数字")
	}
	cycle := strings.TrimSpace(strings.ToLower(input.BillingCycle))
	cycleMonths := map[string]int{"monthly": 1, "quarterly": 3, "semiannually": 6, "annually": 12}
	months, ok := cycleMonths[cycle]
	if !ok || months != input.DurationMonth {
		return "", errors.New("计费周期与购买时长不一致")
	}
	if len(key) == 0 {
		return "", errors.New("订单快照加密密钥未配置")
	}
	ciphertext, err := cryptoutil.Encrypt(input.Password, key)
	if err != nil {
		return "", fmt.Errorf("加密实例密码: %w", err)
	}
	persisted := persistedConfigSnapshot{
		ConfigOptions: input.ConfigOptions, Hostname: hostname,
		BillingCycle: cycle, PasswordCiphertext: ciphertext,
	}
	raw, err := json.Marshal(persisted)
	if err != nil {
		return "", fmt.Errorf("编码订单配置: %w", err)
	}
	return string(raw), nil
}

func configSnapshotView(raw string) OrderConfigSnapshotView {
	var persisted persistedConfigSnapshot
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &persisted) != nil {
		return OrderConfigSnapshotView{}
	}
	// Compatibility with snapshots created before billingCycle was introduced.
	if persisted.BillingCycle == "" {
		var legacy struct {
			Cycle string `json:"cycle"`
		}
		if json.Unmarshal([]byte(raw), &legacy) == nil {
			persisted.BillingCycle = legacy.Cycle
		}
	}
	view := OrderConfigSnapshotView{
		ConfigOptions: persisted.ConfigOptions,
		Hostname:      persisted.Hostname,
		BillingCycle:  persisted.BillingCycle,
		PasswordSet:   persisted.PasswordCiphertext != "",
	}
	if view.PasswordSet {
		view.PasswordMasked = "************"
	}
	return view
}

func decryptConfigSnapshot(raw string, key []byte) (persistedConfigSnapshot, error) {
	var snapshot persistedConfigSnapshot
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "{}" {
		return snapshot, nil
	}
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return snapshot, fmt.Errorf("订单配置快照损坏: %w", err)
	}
	if snapshot.PasswordCiphertext != "" {
		if len(key) == 0 {
			return snapshot, errors.New("订单快照解密密钥未配置")
		}
		password, err := cryptoutil.Decrypt(snapshot.PasswordCiphertext, key)
		if err != nil {
			return snapshot, fmt.Errorf("解密实例密码失败: %w", err)
		}
		snapshot.PasswordCiphertext = password
	}
	return snapshot, nil
}

func normalizeAndPriceConfig(product *rep.SellableProduct, input *CreateOrderInput, skipPriceFields map[string]bool) (int64, *upstreamcontracts.OrderPage, error) {
	if product.SupplierID == "" || product.SupplierID == "0" || product.UpstreamProductID <= 0 {
		if len(input.ConfigOptions) > 0 {
			return 0, nil, errors.New("固定商品不接受选配参数")
		}
		return 0, nil, nil
	}
	page, err := upstream.Default.GetOrderPage(product.SupplierID, product.UpstreamProductID)
	if err != nil {
		return 0, nil, fmt.Errorf("读取商品实时配置失败: %w", err)
	}
	if len(page.ConfigOptions) == 0 {
		if len(input.ConfigOptions) > 0 {
			return 0, page, errors.New("固定商品不接受选配参数")
		}
		return 0, page, nil
	}
	price, err := normalizeConfigOptions(page.ConfigOptions, input, skipPriceFields)
	return price, page, err
}

func selectedPricingSpec(specs []rep.PricingSpec, rawID string) (*rep.PricingSpec, error) {
	if len(specs) == 0 {
		if strings.TrimSpace(rawID) != "" {
			return nil, errors.New("该商品没有可选人工规格")
		}
		return nil, nil
	}
	// Legacy drafts may predate the product's single specification.
	if len(specs) == 1 && strings.TrimSpace(rawID) == "" {
		return &specs[0], nil
	}
	id, err := parseID(strings.TrimSpace(rawID))
	if err != nil {
		return nil, errors.New("请选择有效商品规格")
	}
	for index := range specs {
		if specs[index].ID == id {
			return &specs[index], nil
		}
	}
	return nil, errors.New("所选商品规格不存在或已调整")
}

func periodBasePrice(product *rep.SellableProduct, spec *rep.PricingSpec, page *upstreamcontracts.OrderPage, cycle string, months int) (int64, int64, error) {
	monthlyBase := product.PriceYuan
	configuredMonthly := monthlyBase
	if spec != nil && spec.PriceMode == "fixed" {
		configuredMonthly = spec.PriceYuan
	}
	if configuredMonthly <= 0 {
		return 0, 0, errors.New("商品规格价格异常，暂不能下单")
	}
	setupFee := 0.0
	if page != nil {
		monthlyUpstream := page.Product.Price
		if monthlyUpstream <= 0 {
			monthlyUpstream = monthlyBase
		}
		for _, item := range page.Cycles {
			if strings.EqualFold(item.Billingcycle, cycle) {
				setupFee = item.SetupFee
				if product.SupplierID != "" && product.SupplierID != "0" || spec == nil || spec.PriceMode == "upstream" {
					return int64(item.Price*100 + 0.5), int64(setupFee*100 + 0.5), nil
				}
				if monthlyUpstream > 0 && months > 0 {
					discount := item.Price / (monthlyUpstream * float64(months))
					return int64(configuredMonthly*float64(months)*discount*100 + 0.5), int64(setupFee*100 + 0.5), nil
				}
			}
		}
	}
	if product.SupplierID != "" && product.SupplierID != "0" {
		return 0, 0, errors.New("商品周期原价未确认，暂不能下单")
	}
	return int64(configuredMonthly*float64(months)*100 + 0.5), int64(setupFee*100 + 0.5), nil
}

func normalizeConfigOptions(options []upstreamcontracts.ConfigOption, input *CreateOrderInput, skipPriceFields ...map[string]bool) (int64, error) {
	if len(input.ConfigOptions) == 0 {
		return 0, errors.New("该商品必须提交完整选配")
	}
	allowed := make(map[string]struct{}, len(options))
	var extraCents int64
	for position, option := range options {
		skipPrice := len(skipPriceFields) > 0 && skipPriceFields[0][option.Field]
		optionID := strconv.Itoa(option.ID)
		allowed[optionID] = struct{}{}
		selected, ok := input.ConfigOptions[optionID]
		if !ok || strings.TrimSpace(selected.Value) == "" {
			return 0, fmt.Errorf("缺少配置项：%s", option.Name)
		}
		selected.Field = option.Field
		selected.DisplayName = option.Name
		selected.Position = position
		if option.OptionType == "qty" {
			value, parseErr := strconv.ParseFloat(strings.TrimSpace(selected.Value), 64)
			if parseErr != nil || value < option.Min || value > option.Max {
				return 0, fmt.Errorf("配置项 %s 超出可选范围", option.Name)
			}
			step := option.Step
			if step <= 0 {
				step = 1
			}
			steps := (value - option.Min) / step
			if steps != float64(int64(steps)) {
				return 0, fmt.Errorf("配置项 %s 不符合步进规则", option.Name)
			}
			selected.Value = strconv.FormatFloat(value, 'f', -1, 64)
			selected.DisplayValue = selected.Value
			if option.Unit != "" {
				selected.DisplayValue += " " + option.Unit
			}
			for _, tier := range option.Options {
				if skipPrice {
					continue
				}
				if tier.Price == 0 || value < tier.Min {
					continue
				}
				upper := value
				if tier.Max > 0 && upper > tier.Max {
					upper = tier.Max
				}
				tierStep := tier.Step
				if tierStep <= 0 {
					tierStep = 1
				}
				units := (upper - tier.Min + tierStep) / tierStep
				if tier.Min == 0 {
					units = upper / tierStep
				}
				if units > 0 {
					extraCents += int64(tier.Price*units*100 + 0.5)
				}
			}
		} else {
			found := false
			for _, choice := range option.Options {
				if strconv.Itoa(choice.ID) == strings.TrimSpace(selected.Value) {
					selected.Value = strconv.Itoa(choice.ID)
					selected.DisplayValue = choice.Name
					if !skipPrice {
						extraCents += int64(choice.Price*100 + 0.5)
					}
					found = true
					break
				}
			}
			if !found {
				return 0, fmt.Errorf("配置项 %s 的选项无效", option.Name)
			}
		}
		input.ConfigOptions[optionID] = selected
	}
	for optionID := range input.ConfigOptions {
		if _, ok := allowed[optionID]; !ok {
			return 0, errors.New("包含未知配置项")
		}
	}
	return extraCents, nil
}

func parseID(raw string) (int64, error) {
	v := int64(0)
	if raw == "" {
		return 0, errors.New("ID不能为空")
	}
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			return 0, errors.New("ID格式错误")
		}
		v = v*10 + int64(ch-'0')
	}
	if v <= 0 {
		return 0, errors.New("ID格式错误")
	}
	return v, nil
}

// CreateOrder 创建订单：服务端定价 + 服务端核销优惠券（同事务）。
// PreparedOrder contains validated configuration; it is opaque to callers.
// Preparation may read a supplier. Persistence never performs network I/O.
type PreparedOrder struct {
	order    *dom.Order
	item     *dom.OrderItem
	couponID int64
}

func (s *Service) PrepareOrder(ctx context.Context, customerID int64, input *CreateOrderInput) (*PreparedOrder, error) {
	if err := s.repo.Transaction(ctx, func(tx *gorm.DB) error { return certification.CheckPurchase(tx, customerID) }); err != nil {
		return nil, err
	}
	productID, err := parseID(strings.TrimSpace(input.ProductID))
	if err != nil {
		return nil, errors.New("请选择有效商品")
	}
	duration := input.DurationMonth
	if duration < 1 || duration > 36 {
		return nil, errors.New("购买时长需为1-36个月")
	}
	qty := input.Quantity
	if qty < 1 || qty > 10 {
		return nil, errors.New("购买数量需为1-10台")
	}
	product, err := s.repo.FindSellableProduct(ctx, productID)
	if err != nil {
		return nil, fmt.Errorf("查询商品失败: %w", err)
	}
	if product == nil {
		return nil, errors.New("商品不存在或已下架")
	}
	specs, err := s.repo.ListPricingSpecs(ctx, productID)
	if err != nil {
		return nil, fmt.Errorf("读取商品规格失败: %w", err)
	}
	spec, err := selectedPricingSpec(specs, input.SpecID)
	if err != nil {
		return nil, err
	}
	skipPriceFields := map[string]bool{}
	if spec != nil && spec.PriceMode == "fixed" && (product.SupplierID == "" || product.SupplierID == "0") {
		skipPriceFields["cpu"] = true
		skipPriceFields["memory"] = true
	}
	optionPrice, page, err := normalizeAndPriceConfig(product, input, skipPriceFields)
	if err != nil {
		return nil, err
	}
	configSnapshot, err := buildConfigSnapshot(input, s.snapshotKey)
	if err != nil {
		return nil, err
	}
	periodPrice, setupFee, err := periodBasePrice(product, spec, page, input.BillingCycle, duration)
	if err != nil {
		return nil, err
	}
	unitPrice := periodPrice + optionPrice*int64(duration) + setupFee
	if unitPrice <= 0 {
		return nil, errors.New("商品价格异常，暂不能下单")
	}
	total := unitPrice * int64(qty)
	var couponID int64
	if strings.TrimSpace(input.UserCouponID) != "" {
		couponID, err = parseID(strings.TrimSpace(input.UserCouponID))
		if err != nil {
			return nil, errors.New("优惠券参数错误")
		}
	}

	orderRow := &dom.Order{
		OrderNo:     generateOrderNo(),
		CustomerID:  customerID,
		TotalAmount: total,
		PayAmount:   total,
		Status:      dom.OrderStatusPending,
	}
	item := &dom.OrderItem{
		ProductID:      productID,
		ProductName:    product.Name,
		ModuleType:     product.ModuleType,
		ConfigSnapshot: configSnapshot,
		DurationMonths: &duration,
		Qty:            uint(qty),
		UnitPrice:      unitPrice,
		Subtotal:       total,
	}
	if strings.TrimSpace(input.Remark) != "" {
		orderRow.CustomerRemark = strings.TrimSpace(input.Remark)
	}
	return &PreparedOrder{order: orderRow, item: item, couponID: couponID}, nil
}

func (s *Service) CreateOrder(ctx context.Context, customerID int64, input *CreateOrderInput) (*OrderView, error) {
	prepared, err := s.PrepareOrder(ctx, customerID, input)
	if err != nil {
		return nil, err
	}
	orderRow, item, couponID := prepared.order, prepared.item, prepared.couponID
	productID, qty, total := item.ProductID, int(item.Qty), orderRow.TotalAmount
	if e := s.repo.Transaction(ctx, func(tx *gorm.DB) error {
		if e := certification.CheckPurchase(tx, customerID); e != nil {
			return e
		}
		if e := s.repo.CreateOrderTx(ctx, tx, orderRow); e != nil {
			return e
		}
		if err := inventory.Reserve(tx, orderRow.ID, productID, qty); err != nil {
			return err
		}
		if couponID > 0 {
			discount, e := s.hooks.ChargeCoupon(ctx, tx, customerID, couponID, orderRow.ID, total)
			if e != nil {
				return e
			}
			orderRow.CouponAmount = discount
			orderRow.CouponID = &couponID
			orderRow.DiscountAmount = discount
			orderRow.PayAmount = total - discount
			if e := s.repo.UpdateOrderTx(ctx, tx, orderRow); e != nil {
				return e
			}
		}
		item.OrderID = orderRow.ID
		item.Subtotal = total
		if err := s.repo.CreateItemTx(ctx, tx, item); err != nil {
			return err
		}
		if orderRow.PayAmount < 0 {
			return errors.New("订单应付金额无效")
		}
		if orderRow.PayAmount == 0 {
			if total <= 0 || couponID <= 0 || orderRow.CouponAmount != total || orderRow.BalanceAmount != 0 {
				return errors.New("零应付订单抵扣依据无效")
			}
			now := time.Now()
			result := tx.Model(&dom.Order{}).Where("id=? AND status=? AND pay_amount=0", orderRow.ID, dom.OrderStatusPending).Updates(map[string]any{"status": dom.OrderStatusPaid, "paid_at": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return errors.New("零应付订单状态已变化")
			}
			orderRow.Status, orderRow.PaidAt = dom.OrderStatusPaid, &now
			if err := inventory.Consume(tx, orderRow.ID); err != nil {
				return err
			}
			return tasks.EnqueuePayment(tx, fmt.Sprintf("zero-order:%d", orderRow.ID), tasks.PaymentPayload{CustomerID: customerID, BusinessID: orderRow.ID, BusinessType: "order", Channel: "discount", AmountFen: 0})
		}
		return nil
	}); e != nil {
		return nil, fmt.Errorf("创建订单失败: %w", e)
	}
	view := orderToView(orderRow)
	view.Items = []OrderItemView{itemToView(item)}
	s.notifyOrderCreated(ctx, orderRow, item)
	return view, nil
}

func (s *Service) PayOrder(ctx context.Context, customerID, orderID int64, method string) (*OrderView, error) {
	return nil, errors.New("旧支付入口已停用，请通过真实支付通道创建支付单")
}

// ProvisionAfterPayment links a verified channel payment to the existing
// upstream provisioning flow. Payment state is already committed before this
// method runs; failures remain visible as delivery_status=3 for manual retry.
func (s *Service) provisionSingle(ctx context.Context, customerID, orderID int64, method string, unit *deliveryUnit) error {
	orderRow, err := s.repo.FindByID(ctx, orderID)
	if err != nil {
		return fmt.Errorf("查询订单失败: %w", err)
	}
	if orderRow == nil || orderRow.CustomerID != customerID {
		return errors.New("订单不存在")
	}
	if orderRow.Status == dom.OrderStatusCompleted {
		return nil
	}
	if orderRow.Status != dom.OrderStatusPaid {
		return errors.New("订单未处于已支付状态")
	}
	items, err := s.repo.ListItems(ctx, orderID)
	if err != nil {
		return fmt.Errorf("查询订单明细失败: %w", err)
	}
	if len(items) != 1 {
		return errors.New("真实开通当前仅支持单商品订单")
	}
	item := items[0]
	if item.DeliveryStatus == 2 {
		return nil
	}
	snapshot, err := decryptConfigSnapshot(item.ConfigSnapshot, s.snapshotKey)
	if err != nil {
		return err
	}
	cycle := snapshot.BillingCycle
	if cycle == "" && item.DurationMonths != nil {
		cycle = cycleForMonths(*item.DurationMonths)
	}
	if cycle == "" {
		return errors.New("订单计费周期缺失")
	}
	configValues := make(map[string]string, len(snapshot.ConfigOptions))
	for id, option := range snapshot.ConfigOptions {
		configValues[id] = option.Value
	}
	instanceID := unit.InstanceID
	if err := s.repo.Transaction(ctx, func(tx *gorm.DB) error {
		var lockedOrder dom.Order
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&lockedOrder, orderID).Error; err != nil {
			return err
		}
		if lockedOrder.Status != dom.OrderStatusPaid {
			return errors.New("订单已不处于可交付状态")
		}
		var refundCount int64
		if err := tx.Table("payment.original_refunds AS r").Joins("JOIN payment.payments p ON p.id=r.payment_id").Joins("LEFT JOIN payment.receipt_reviews rv ON rv.payment_id=p.id").Where("p.biz_type='order' AND p.biz_id=? AND rv.payment_id IS NULL", orderID).Count(&refundCount).Error; err != nil {
			return err
		}
		if refundCount != 0 {
			return errors.New("订单正在退款或已退款，禁止交付")
		}
		result := tx.WithContext(ctx).Model(&deliveryUnit{}).Where("id=? AND order_id=? AND order_item_id=? AND customer_id=? AND status='pending'", unit.ID, orderID, item.ID, customerID).Update("status", "running")
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("订单开通任务已被处理")
		}
		return nil
	}); err != nil {
		return fmt.Errorf("锁定开通任务失败: %w", err)
	}
	result, err := upstream.Default.ProvisionOrder(ctx, upstream.ProvisionInput{
		OrderID: formatID(orderID), LocalProductID: formatID(item.ProductID), CustomerID: formatID(customerID),
		InstanceID: formatID(instanceID), UnitID: formatID(unit.ID), Quantity: 1, Cycle: cycle,
		ConfigOptions: configValues, Hostname: snapshot.Hostname, Password: snapshot.PasswordCiphertext,
	})
	if err != nil {
		_ = s.repo.Transaction(ctx, func(tx *gorm.DB) error {
			return tx.WithContext(ctx).Table("order.order_items").Where("id = ?", item.ID).Update("delivery_status", 3).Error
		})
		return fmt.Errorf("支付成功但上游开通失败: %w", err)
	}
	if result.HostID == "" {
		return errors.New("采购已返回订单但实例关联尚未确认，需核对远端订单")
	}
	now := time.Now()
	active := result.Status == "active" && result.HostID != ""
	instanceStatus, deliveryStatus := uint8(2), uint8(1)
	orderStatus := dom.OrderStatusPaid
	if active {
		instanceStatus, deliveryStatus, orderStatus = 3, 2, dom.OrderStatusCompleted
	}
	host := &instancedom.Host{
		ID: instanceID, InstanceNo: generateInstanceNo(), CustomerID: customerID, OrderID: orderID,
		OrderItemID: item.ID, ProductID: item.ProductID, ProductName: item.ProductName,
		ModuleType: item.ModuleType, ConfigSnapshot: item.ConfigSnapshot, Status: instanceStatus,
		UpstreamInstanceID: result.HostID, ActivatedAt: result.ActivatedAt, ExpiresAt: result.ExpiresAt,
	}
	if supplierID, parseErr := parseID(result.SupplierID); parseErr == nil {
		host.UpstreamProviderID = &supplierID
	}
	upstreamHost := &upstreammodel.UpstreamHost{HostID: formatID(instanceID), SupplierID: result.SupplierID, UpstreamHostID: result.HostID, Status: result.Status, DueTime: 0}
	if result.ExpiresAt != nil {
		upstreamHost.DueTime = result.ExpiresAt.Unix()
	}
	payType := payTypeCode(method)
	allComplete := false
	if e := s.repo.Transaction(ctx, func(tx *gorm.DB) error {
		var locked dom.Order
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status=?", orderID, dom.OrderStatusPaid).First(&locked).Error; err != nil {
			return err
		}
		if e := tx.WithContext(ctx).Create(host).Error; e != nil {
			return e
		}
		if e := tx.WithContext(ctx).Create(upstreamHost).Error; e != nil {
			return e
		}
		state := "unknown"
		if active {
			state = "complete"
		}
		result := tx.Model(&deliveryUnit{}).Where("id=? AND status='running'", unit.ID).Updates(map[string]any{"status": state, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("交付单元状态变化，需核对")
		}
		var remaining int64
		if err := tx.Model(&deliveryUnit{}).Where("order_id=? AND status<>'complete'", orderID).Count(&remaining).Error; err != nil {
			return err
		}
		allComplete = remaining == 0
		orderStatus = dom.OrderStatusPaid
		deliveryStatus = 1
		if allComplete {
			orderStatus = dom.OrderStatusCompleted
			deliveryStatus = 2
		}
		var uncertain int64
		if err := tx.Model(&deliveryUnit{}).Where("order_id=? AND status='unknown'", orderID).Count(&uncertain).Error; err != nil {
			return err
		}
		if uncertain > 0 {
			deliveryStatus = 3
		}
		if err := tx.Model(&dom.Order{}).Where("id=?", orderID).Updates(map[string]any{"status": orderStatus, "pay_type": payType, "updated_at": now}).Error; err != nil {
			return err
		}
		fields := map[string]any{"delivery_status": deliveryStatus}
		if unit.UnitNo == 1 {
			fields["instance_id"] = instanceID
		}
		if err := tx.Table("order.order_items").Where("id=?", item.ID).Updates(fields).Error; err != nil {
			return err
		}
		if allComplete {
			if err := tasks.EnqueuePayment(tx, fmt.Sprintf("delivery-hooks:%d", orderID), tasks.PaymentPayload{CustomerID: customerID, BusinessID: orderID, BusinessType: "order_hooks", Channel: method}); err != nil {
				return err
			}
		}
		if active {
			return tasks.EnqueuePayment(tx, fmt.Sprintf("activation-notice:%d", instanceID), tasks.PaymentPayload{CustomerID: customerID, BusinessID: instanceID, OrderID: orderID, BusinessType: "activation_notice", Channel: method})
		}
		return nil
	}); e != nil {
		return fmt.Errorf("远端采购成功但本地交付保存失败: %w", e)
	}
	if !active {
		return errors.New("远端实例尚未确认开通，保留关联等待核对")
	}
	return nil
}

func cycleForMonths(months int) string {
	return map[int]string{1: "monthly", 3: "quarterly", 6: "semiannually", 12: "annually"}[months]
}

func generateInstanceNo() string {
	return fmt.Sprintf("INS%d", snowflake.GenID())
}

// CancelOrder 取消未支付订单并归还优惠券。
func (s *Service) CancelOrder(ctx context.Context, customerID, orderID int64) error {
	return s.repo.Transaction(ctx, func(tx *gorm.DB) error {
		if e := s.repo.CancelPendingOrderTx(ctx, tx, customerID, orderID); e != nil {
			return e
		}
		if err := s.hooks.ReleaseCoupon(ctx, tx, orderID); err != nil {
			return err
		}
		return inventory.Release(tx, orderID, false)
	})
}

// ListOrders 客户订单列表。
func (s *Service) ListOrders(ctx context.Context, customerID int64, page, pageSize int, status *uint8) ([]*OrderView, int64, error) {
	filters := map[string]any{"customer_id": customerID}
	if status != nil {
		filters["status"] = *status
	}
	rows, total, err := s.repo.List(ctx, page, pageSize, filters)
	if err != nil {
		return nil, 0, fmt.Errorf("查询订单失败: %w", err)
	}
	out := make([]*OrderView, 0, len(rows))
	for _, row := range rows {
		out = append(out, orderToView(row))
	}
	return out, total, nil
}

// GetOrder 订单详情（含明细）。
func (s *Service) GetOrder(ctx context.Context, customerID, orderID int64) (*OrderView, error) {
	orderRow, err := s.repo.FindByID(ctx, orderID)
	if err != nil {
		return nil, fmt.Errorf("查询订单失败: %w", err)
	}
	if orderRow == nil || orderRow.CustomerID != customerID {
		return nil, errors.New("订单不存在")
	}
	items, err := s.repo.ListItems(ctx, orderID)
	if err != nil {
		return nil, fmt.Errorf("查询订单明细失败: %w", err)
	}
	view := orderToView(orderRow)
	view.Items = make([]OrderItemView, 0, len(items))
	for _, item := range items {
		view.Items = append(view.Items, itemToView(item))
	}
	return view, nil
}

// CreateRecharge 创建充值单。
func (s *Service) CreateRecharge(ctx context.Context, customerID int64, amountCents int64, method string) (*RechargeView, error) {
	if amountCents < 100 || amountCents > 100000000 {
		return nil, errors.New("充值金额需在1元-100万元之间")
	}
	if payTypeCode(method) == 0 {
		return nil, errors.New("不支持的支付方式")
	}
	row := &rep.Recharge{
		RechargeNo: generateRechargeNo(),
		CustomerID: customerID,
		Amount:     amountCents,
		Channel:    method,
		Status:     1,
	}
	if err := s.repo.CreateRecharge(ctx, row); err != nil {
		return nil, fmt.Errorf("创建充值单失败: %w", err)
	}
	return rechargeToView(row), nil
}

func (s *Service) PayRecharge(ctx context.Context, customerID, rechargeID int64) (*RechargeView, error) {
	return nil, errors.New("旧充值支付入口已停用，请通过真实支付通道创建支付单")
}

// AfterRechargePayment runs non-balance side effects after the payment and
// balance ledger transaction has committed. The marketing hook is idempotent.
func (s *Service) AfterRechargePayment(ctx context.Context, rechargeID, customerID, amountCents int64) error {
	recharge, err := s.repo.FindRecharge(ctx, rechargeID)
	if err != nil {
		return fmt.Errorf("查询已支付充值单失败: %w", err)
	}
	if recharge == nil || recharge.CustomerID != customerID || recharge.Amount != amountCents || recharge.Status != 2 {
		return errors.New("已支付充值单校验失败")
	}
	hookErr := s.hooks.OnRechargePaid(ctx, rechargeID, recharge.RechargeNo, customerID, amountCents)
	if notifyErr := s.notifyRecharge(ctx, rechargeID, recharge.RechargeNo, customerID, amountCents); notifyErr != nil && hookErr == nil {
		hookErr = notifyErr
	}
	return hookErr
}

// ListRecharges 充值记录。
func (s *Service) ListRecharges(ctx context.Context, customerID int64, page, pageSize int) ([]*RechargeView, int64, error) {
	rows, total, err := s.repo.ListRecharges(ctx, customerID, page, pageSize)
	if err != nil {
		return nil, 0, fmt.Errorf("查询充值记录失败: %w", err)
	}
	out := make([]*RechargeView, 0, len(rows))
	for _, row := range rows {
		out = append(out, rechargeToView(row))
	}
	return out, total, nil
}

func payTypeCode(method string) uint8 {
	switch strings.TrimSpace(strings.ToLower(method)) {
	case "alipay":
		return 1
	case "wechat":
		return 2
	case "balance":
		return 3
	default:
		return 0
	}
}

func generateOrderNo() string {
	return fmt.Sprintf("DDY%d", snowflake.GenID())
}

func generateRechargeNo() string {
	return fmt.Sprintf("RCH%d", snowflake.GenID())
}

func orderToView(v *dom.Order) *OrderView {
	view := &OrderView{
		ID:             formatID(v.ID),
		OrderNo:        v.OrderNo,
		CustomerID:     formatID(v.CustomerID),
		ProductName:    v.ProductName,
		TotalAmount:    v.TotalAmount,
		DiscountAmount: v.DiscountAmount,
		CouponAmount:   v.CouponAmount,
		PayAmount:      v.PayAmount,
		Status:         v.Status,
		PayType:        v.PayType,
		Remark:         stringPtr(v.CustomerRemark),
		CreatedAt:      v.CreatedAt.Format("2006-01-02 15:04:05"),
	}
	if v.CouponID != nil {
		view.CouponID = formatID(*v.CouponID)
	}
	if v.PaidAt != nil {
		view.PaidAt = v.PaidAt.Format("2006-01-02 15:04:05")
	}
	if v.CancelledAt != nil {
		view.CancelledAt = v.CancelledAt.Format("2006-01-02 15:04:05")
	}
	return view
}

func itemToView(v *dom.OrderItem) OrderItemView {
	durationText := ""
	if v.DurationMonths != nil {
		durationText = fmt.Sprintf("%d 个月", *v.DurationMonths)
	}
	view := OrderItemView{
		ID: formatID(v.ID), ProductID: formatID(v.ProductID), ProductName: v.ProductName,
		UnitPrice: v.UnitPrice, Quantity: uint32(v.Qty), Subtotal: v.Subtotal,
		DurationText: durationText, ConfigSnapshot: configSnapshotView(v.ConfigSnapshot), DeliveryStatus: v.DeliveryStatus,
	}
	if v.InstanceID != nil {
		view.InstanceID = formatID(*v.InstanceID)
	}
	return view
}

func rechargeToView(v *rep.Recharge) *RechargeView {
	view := &RechargeView{
		ID: formatID(v.ID), RechargeNo: v.RechargeNo, Amount: v.Amount,
		Status: v.Status, CreatedAt: v.CreatedAt.Format("2006-01-02 15:04:05"),
	}
	if v.PaidAt != nil {
		view.PaidAt = v.PaidAt.Format("2006-01-02 15:04:05")
	}
	return view
}

func formatID(v int64) string {
	return fmt.Sprintf("%d", v)
}

func formatYuan(cents int64) string { return fmt.Sprintf("¥%.2f", float64(cents)/100) }

func (s *Service) notifyOrderCreated(ctx context.Context, order *dom.Order, item *dom.OrderItem) {
	if s.notifier == nil {
		return
	}
	params := map[string]string{"order_no": order.OrderNo, "product_name": item.ProductName, "amount": formatYuan(order.PayAmount)}
	if err := s.notifier.NotifyCustomer(ctx, "order", order.CustomerID, formatID(order.ID), params); err != nil {
		logx.Errorf("order notification failed order_id=%d: %v", order.ID, err)
	}
}

func (s *Service) notifyActivation(ctx context.Context, order *dom.Order, host *instancedom.Host) error {
	if s.notifier == nil || host.ExpiresAt == nil {
		return nil
	}
	params := map[string]string{"order_no": order.OrderNo, "instance_no": host.InstanceNo, "product_name": host.ProductName, "expires_at": host.ExpiresAt.Format("2006-01-02 15:04")}
	if err := s.notifier.NotifyCustomer(ctx, "activation", order.CustomerID, formatID(host.ID), params); err != nil {
		logx.Errorf("activation notification failed instance_id=%d: %v", host.ID, err)
		return err
	}
	return nil
}

func (s *Service) notifyRecharge(ctx context.Context, rechargeID int64, rechargeNo string, customerID, amountCents int64) error {
	if s.notifier == nil {
		return nil
	}
	params := map[string]string{"recharge_no": rechargeNo, "amount": formatYuan(amountCents)}
	if err := s.notifier.NotifyCustomer(ctx, "recharge", customerID, formatID(rechargeID), params); err != nil {
		logx.Errorf("recharge notification failed recharge_id=%d: %v", rechargeID, err)
		return err
	}
	return nil
}

func stringPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
