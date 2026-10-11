package cdnsell

import (
	"context"
	orderstate "ddyidc/internal/order/repository"
	"ddyidc/internal/shared/distsync"
	schemamode "ddyidc/internal/shared/schema"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ddyidc/internal/cdnsell/provider"
	"ddyidc/internal/cdnsell/provider/ksyun"
	orderdom "ddyidc/internal/order/domain"
	"ddyidc/internal/shared/snowflake"
	cryptoutil "ddyidc/pkg/crypto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

//go:embed schema.sql
var schemaSQL string

//go:embed p2_schema.sql
var p2SchemaSQL string

var ErrNotFound = errors.New("记录不存在")

type Notifier interface {
	NotifyCustomer(context.Context, string, int64, string, map[string]string) error
}
type Service struct {
	db            *gorm.DB
	key           []byte
	notifier      Notifier
	cloud         provider.Provider
	now           func() time.Time
	cloudMu       sync.Mutex
	lastCloudCall time.Time
}

func NewService(db *gorm.DB, key []byte, notifier Notifier) *Service {
	return &Service{db: db, key: key, notifier: notifier, cloud: ksyun.New(), now: time.Now}
}

func (s *Service) EnsureSchema(ctx context.Context) error {
	if !schemamode.MigrationsEnabled() {
		return nil
	}
	if err := s.db.WithContext(ctx).Exec(schemaSQL).Error; err != nil {
		return err
	}
	if err := s.db.WithContext(ctx).Exec(p2SchemaSQL).Error; err != nil {
		return err
	}
	items := []struct {
		api, cap, note string
		available      *bool
	}{
		{"TestConnection", "连通性", "已由官方 CDN V3 SDK 支持", boolPtr(true)},
		{"GetUsage", "用量查询", "5 分钟粒度流量查询已由官方 CDN V3 SDK 支持", boolPtr(true)},
		{"GetResourcePackages", "资源包库存", "公开 CDN SDK 未提供资源包库存查询", boolPtr(false)},
		{"AddDomain", "域名接入", "P2 已接入官方 SDK，待授权实测", nil}, {"SetDomainConfig", "配置下发", "P2 已接入官方 SDK，待授权实测", nil},
		{"DeployCertificate", "证书部署", "P2 已接入官方 SDK，待授权实测", nil}, {"SubmitRefresh", "缓存刷新与预热", "P2 已接入官方 SDK，待授权实测", nil},
		{"GetLogs", "日志下载", "P2 已接入官方 SDK，待有域名后实测", boolPtr(true)}, {"CheckIP", "节点 IP 检测", "P2 已接入官方 SDK，待实测", boolPtr(true)},
	}
	for _, v := range items {
		id := snowflake.GenID()
		_ = s.db.WithContext(ctx).Exec("INSERT INTO system.cdnsell_api_capabilities(id,api_name,capability,available,note) VALUES(?,?,?,?,?) ON DUPLICATE KEY UPDATE capability=VALUES(capability),available=COALESCE(available,VALUES(available)),note=IF(checked_at IS NULL,VALUES(note),note)", id, v.api, v.cap, v.available, v.note).Error
	}
	_ = s.db.WithContext(ctx).Exec("INSERT IGNORE INTO system.notification_templates(id,scene,channel,title,content,enabled) VALUES(?,'cdnsell_usage','both','CDN 流量预警','您的 CDN 可用流量剩余 {{remaining}}，当前余量 {{percent}}%，请及时购买流量包。',1)", snowflake.GenID()).Error
	_ = s.db.WithContext(ctx).Exec("INSERT IGNORE INTO system.notification_templates(id,scene,channel,title,content,enabled) VALUES(?,'cdnsell_cert_expiry','both','CDN 证书即将到期','您的 CDN 证书 {{name}} 将于 {{expireAt}} 到期，请及时更新。',1)", snowflake.GenID()).Error
	return nil
}
func boolPtr(v bool) *bool { return &v }
func (s *Service) waitCloudGap(ctx context.Context) error {
	s.cloudMu.Lock()
	defer s.cloudMu.Unlock()
	if wait := 2*time.Second - time.Since(s.lastCloudCall); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	s.lastCloudCall = time.Now()
	return nil
}
func (s *Service) Enabled(ctx context.Context) (bool, error) {
	var v bool
	e := s.db.WithContext(ctx).Table(settingsTable).Select("enabled").Where("id=1").Scan(&v).Error
	return v, e
}

func (s *Service) Accounts(ctx context.Context) ([]UpstreamAccount, error) {
	var rows []UpstreamAccount
	e := s.db.WithContext(ctx).Table(accountsTable).Order("sort,id").Find(&rows).Error
	for i := range rows {
		rows[i].sanitize()
	}
	return rows, e
}
func (s *Service) SaveAccount(ctx context.Context, v *UpstreamAccount) error {
	if strings.TrimSpace(v.Name) == "" {
		return errors.New("账号名称不能为空")
	}
	if v.ID == 0 {
		if v.AccessKey == "" || v.SecretKey == "" {
			return errors.New("AK 和 SK 不能为空")
		}
		v.ID = snowflake.GenID()
	}
	data := map[string]any{"name": strings.TrimSpace(v.Name), "status": v.Status, "sort": v.Sort}
	if v.AccessKey != "" {
		enc, e := cryptoutil.Encrypt(v.AccessKey, s.key)
		if e != nil {
			return e
		}
		data["ak_encrypted"] = enc
	}
	if v.SecretKey != "" {
		enc, e := cryptoutil.Encrypt(v.SecretKey, s.key)
		if e != nil {
			return e
		}
		data["sk_encrypted"] = enc
	}
	if v.AccessKey != "" || v.SecretKey != "" {
		data["last_test_time"] = nil
		data["last_test_result"] = nil
	}
	if v.ID != 0 {
		var n int64
		s.db.WithContext(ctx).Table(accountsTable).Where("id=?", v.ID).Count(&n)
		if n > 0 {
			return s.db.WithContext(ctx).Table(accountsTable).Where("id=?", v.ID).Updates(data).Error
		}
	}
	data["id"] = v.ID
	return s.db.WithContext(ctx).Table(accountsTable).Create(data).Error
}
func (s *Service) DeleteAccount(ctx context.Context, id int64) error {
	for _, table := range []string{packagesTable, domainsTable} {
		var count int64
		if e := s.db.WithContext(ctx).Table(table).Where("account_id=?", id).Count(&count).Error; e != nil {
			return e
		}
		if count > 0 {
			return errors.New("账号仍有关联资源，不能删除，请保留账号记录")
		}
	}
	return s.db.WithContext(ctx).Table(accountsTable).Where("id=?", id).Delete(nil).Error
}
func (s *Service) accountSecret(ctx context.Context, id int64) (UpstreamAccount, provider.Account, error) {
	var row UpstreamAccount
	e := s.db.WithContext(ctx).Table(accountsTable).Where("id=?", id).Take(&row).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return row, provider.Account{}, ErrNotFound
	}
	if e != nil {
		return row, provider.Account{}, e
	}
	ak, e := cryptoutil.Decrypt(row.AKEncrypted, s.key)
	if e != nil {
		return row, provider.Account{}, e
	}
	sk, e := cryptoutil.Decrypt(row.SKEncrypted, s.key)
	return row, provider.Account{AccessKey: ak, SecretKey: sk}, e
}
func (s *Service) TestAccount(ctx context.Context, id int64) error {
	_, account, e := s.accountSecret(ctx, id)
	if e != nil {
		return e
	}
	start := s.now()
	if e = s.waitCloudGap(ctx); e != nil {
		return e
	}
	e = s.cloud.TestConnection(ctx, account)
	success := e == nil
	_ = s.updateCapability(ctx, "TestConnection", success, "账号真实连接测试")
	result, _ := json.Marshal(map[string]any{"success": success, "message": map[bool]string{true: "连接成功", false: "连接失败"}[success]})
	now := s.now()
	_ = s.db.WithContext(ctx).Table(accountsTable).Where("id=?", id).Updates(map[string]any{"last_test_time": now, "last_test_result": result}).Error
	errText := ""
	if e != nil {
		errText = e.Error()
	}
	_ = s.db.WithContext(ctx).Exec("INSERT INTO system.cdnsell_api_logs(id,account_id,api_name,request,response,status_code,cost_ms,success,error) VALUES(?,?, 'TestConnection',JSON_OBJECT(),JSON_OBJECT(),?,?,?,?)", snowflake.GenID(), id, map[bool]int{true: 200, false: 0}[success], time.Since(start).Milliseconds(), success, errText).Error
	return e
}

func (s *Service) Packages(ctx context.Context) ([]UpstreamPackage, error) {
	var rows []UpstreamPackage
	e := s.db.WithContext(ctx).Table(packagesTable).Order("expire_time,id").Find(&rows).Error
	return rows, e
}
func (s *Service) SavePackage(ctx context.Context, v *UpstreamPackage) error {
	if v.Name == "" || v.AccountID <= 0 || v.CapacityBytes <= 0 || v.UsedBytes < 0 || v.UsedBytes > v.CapacityBytes || (v.Status != "active" && v.Status != "expired") || !v.ExpireTime.After(v.EffectiveTime) {
		return errors.New("资源包参数无效")
	}
	var n int64
	if e := s.db.WithContext(ctx).Table(accountsTable).Where("id=?", v.AccountID).Count(&n).Error; e != nil {
		return e
	}
	if n != 1 {
		return ErrNotFound
	}
	if v.ID == 0 {
		v.ID = snowflake.GenID()
		return s.db.WithContext(ctx).Table(packagesTable).Create(v).Error
	}
	return s.db.WithContext(ctx).Table(packagesTable).Where("id=?", v.ID).Updates(map[string]any{"account_id": v.AccountID, "name": v.Name, "capacity_bytes": v.CapacityBytes, "used_bytes": v.UsedBytes, "effective_time": v.EffectiveTime, "expire_time": v.ExpireTime, "status": v.Status, "remark": v.Remark}).Error
}
func (s *Service) DeletePackage(ctx context.Context, id int64) error {
	return s.db.WithContext(ctx).Table(packagesTable).Where("id=?", id).Delete(nil).Error
}
func (s *Service) Products(ctx context.Context, admin bool) ([]Product, error) {
	var rows []Product
	q := s.db.WithContext(ctx).Table(productsTable)
	if !admin {
		q = q.Where("status=1")
	}
	e := q.Order("sort,id").Find(&rows).Error
	return rows, e
}
func (s *Service) SaveProduct(ctx context.Context, v *Product) error {
	if v.Name == "" || v.CapacityBytes <= 0 || v.ValidDays <= 0 || v.ValidDays > 3650 || v.Price <= 0 || v.Price > 5000000 || v.MaxQty > 1000 || v.MinQty < 1 || v.MaxQty < v.MinQty {
		return errors.New("产品参数无效")
	}
	if len(v.TierPrices) == 0 {
		v.TierPrices = json.RawMessage(`{}`)
	}
	var tiers map[string]Yuan
	if json.Unmarshal(v.TierPrices, &tiers) != nil {
		return errors.New("会员价格 JSON 无效")
	}
	for key, price := range tiers {
		if _, err := strconv.ParseUint(key, 10, 32); err != nil || price <= 0 {
			return errors.New("会员价格等级或金额无效")
		}
	}
	normalized, _ := json.Marshal(tiers)
	v.TierPrices = normalized
	if v.ID == 0 {
		v.ID = snowflake.GenID()
		return s.db.WithContext(ctx).Table(productsTable).Create(v).Error
	}
	updated := s.db.WithContext(ctx).Table(productsTable).Where("id=?", v.ID).Updates(map[string]any{"name": v.Name, "capacity_bytes": v.CapacityBytes, "valid_days": v.ValidDays, "price": v.Price, "tier_prices": v.TierPrices, "min_qty": v.MinQty, "max_qty": v.MaxQty, "description": v.Description, "is_hot": v.IsHot, "status": v.Status, "sort": v.Sort})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected == 0 {
		var count int64
		if err := s.db.WithContext(ctx).Table(productsTable).Where("id=?", v.ID).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return ErrNotFound
		}
	}
	return nil
}
func (s *Service) DeleteProduct(ctx context.Context, id int64) error {
	for _, table := range []string{ordersTable, batchesTable} {
		var count int64
		if e := s.db.WithContext(ctx).Table(table).Where("product_id=?", id).Count(&count).Error; e != nil {
			return e
		}
		if count > 0 {
			return errors.New("商品已有订单或流量批次，请下架并保留记录")
		}
	}
	return s.db.WithContext(ctx).Table(productsTable).Where("id=?", id).Delete(nil).Error
}
func priceFor(p Product, level int) int64 {
	var tiers map[string]Yuan
	if json.Unmarshal(p.TierPrices, &tiers) == nil {
		if v, ok := tiers[strconv.Itoa(level)]; ok {
			return int64(v)
		}
	}
	return int64(p.Price)
}
func (s *Service) Purchase(ctx context.Context, userID, productID int64, qty int) (*PurchaseResult, error) {
	enabled, err := s.Enabled(ctx)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, errors.New("CDN服务暂未开放")
	}
	if qty <= 0 {
		return nil, errors.New("购买数量必须大于零")
	}
	var p Product
	if e := s.db.WithContext(ctx).Table(productsTable).Where("id=? AND status=1", productID).Take(&p).Error; e != nil {
		return nil, ErrNotFound
	}
	if qty < p.MinQty || qty > p.MaxQty {
		return nil, errors.New("购买数量超出允许范围")
	}
	var customer struct {
		Level    int
		TenantID int64
	}
	if e := s.db.WithContext(ctx).Table("customer.customers").Select("level,tenant_id").Where("id=?", userID).Take(&customer).Error; e != nil {
		return nil, e
	}
	unit := priceFor(p, customer.Level)
	if unit <= 0 || unit > 5000000/int64(qty) || p.CapacityBytes <= 0 || p.CapacityBytes > 9223372036854775807/int64(qty) {
		return nil, errors.New("产品价格或流量总额超出范围")
	}
	amount := unit * int64(qty)
	now := s.now()
	mainID, bizID := snowflake.GenID(), snowflake.GenID()
	no := fmt.Sprintf("CDN%d", bizID)
	snap, _ := json.Marshal(map[string]any{"productName": p.Name, "capacityBytes": p.CapacityBytes, "unitPrice": Yuan(unit), "customerLevel": customer.Level, "quantity": qty, "validDays": p.ValidDays})
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		order := orderdom.Order{ID: mainID, OrderNo: no, TenantID: customer.TenantID, CustomerID: userID, TotalAmount: amount, PayAmount: amount, Status: orderdom.OrderStatusPending, CreatedAt: now, UpdatedAt: now}
		if e := tx.Create(&order).Error; e != nil {
			return e
		}
		item := orderdom.OrderItem{OrderID: mainID, ProductID: p.ID, ProductName: p.Name, ModuleType: "cdnsell", ConfigSnapshot: string(snap), Qty: uint(qty), UnitPrice: unit, Subtotal: amount, DeliveryStatus: 0, CreatedAt: now, UpdatedAt: now}
		if e := tx.Create(&item).Error; e != nil {
			return e
		}
		return tx.Exec("INSERT INTO system.cdnsell_orders(id,order_no,user_id,order_type,ref_order_id,product_id,traffic_bytes,amount,snapshot,status,created_at) VALUES(?,?,?,'traffic_pack',?,?,?,?,?,'pending',?)", bizID, no, userID, mainID, p.ID, p.CapacityBytes*int64(qty), Yuan(amount), string(snap), now).Error
	})
	if e != nil {
		return nil, e
	}
	return &PurchaseResult{OrderID: mainID, OrderNo: no, Amount: Yuan(amount)}, nil
}

// ProvisionPlatformOrder is the idempotent bridge called by the existing payment dispatcher.
func (s *Service) ProvisionPlatformOrder(ctx context.Context, userID, mainOrderID int64, method string) (bool, error) {
	matched := false
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Identify the module without taking its row lock, then follow the
		// platform-order -> module-order lock order used by payment recovery.
		var candidate struct{ ID int64 }
		if err := tx.Table(ordersTable).Select("id").Where("ref_order_id=?", mainOrderID).Take(&candidate).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		matched = true
		var platform orderdom.Order
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=?", mainOrderID, userID).Take(&platform).Error; err != nil {
			return err
		}
		if platform.Status != orderdom.OrderStatusPaid && platform.Status != orderdom.OrderStatusCompleted {
			return errors.New("平台订单未支付或状态已变化，不能发放流量")
		}
		var row struct {
			ID, UserID, ProductID, TrafficBytes int64
			BillID                              *int64
			OrderType                           string
			Amount                              Yuan
			Status                              string
			BatchID                             *int64
			Snapshot                            json.RawMessage
		}
		e := tx.Table(ordersTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("ref_order_id=?", mainOrderID).Take(&row).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return errors.New("流量订单已变更，请核对后继续交付")
		}
		if e != nil {
			return e
		}
		matched = true
		if row.UserID != userID {
			return errors.New("CDN 订单归属不匹配")
		}
		if row.Amount <= 0 || int64(row.Amount) != platform.TotalAmount {
			return errors.New("CDN 订单金额与平台订单不一致，请核对")
		}
		if row.OrderType == "overage" {
			if row.Status != "pending" && row.Status != "paid" {
				return errors.New("超额订单已取消或状态无效")
			}
			if row.BillID == nil {
				return errors.New("超额订单缺少账单关联")
			}
			return s.provisionOverage(tx, userID, mainOrderID, row.ID, *row.BillID, row.Amount, row.Status == "paid", platform)
		}
		if row.Status == "paid" && row.BatchID != nil {
			if platform.Status == orderdom.OrderStatusCompleted {
				return nil
			}
			return errors.New("流量交付与平台订单状态不一致，请核对")
		}
		if row.Status != "pending" || platform.Status != orderdom.OrderStatusPaid {
			return errors.New("流量订单状态已变化，不能重复交付")
		}
		var snap struct {
			ValidDays int `json:"validDays"`
		}
		_ = json.Unmarshal(row.Snapshot, &snap)
		if snap.ValidDays < 1 {
			snap.ValidDays = 30
		}
		now := s.now()
		batchID := snowflake.GenID()
		if e := tx.Exec("INSERT IGNORE INTO system.cdnsell_batches(id,user_id,product_id,order_id,original_bytes,consumed_bytes,remaining_bytes,effective_time,expire_time,status,created_at) VALUES(?,?,?,?,?,0,?,?,?,'running',?)", batchID, userID, row.ProductID, row.ID, row.TrafficBytes, row.TrafficBytes, now, now.AddDate(0, 0, snap.ValidDays), now).Error; e != nil {
			return e
		}
		var actual int64
		if e := tx.Table(batchesTable).Select("id").Where("order_id=?", row.ID).Scan(&actual).Error; e != nil {
			return e
		}
		if e := tx.Table(ordersTable).Where("id=?", row.ID).Updates(map[string]any{"status": "paid", "batch_id": actual, "paid_at": now}).Error; e != nil {
			return e
		}
		if e := tx.Table("order.order_items").Where("order_id=? AND module_type='cdnsell'", mainOrderID).Updates(map[string]any{"delivery_status": 2, "updated_at": now}).Error; e != nil {
			return e
		}
		changed, err := orderstate.CompletePaidTx(tx, mainOrderID, userID, now)
		if err != nil {
			return err
		}
		if !changed {
			return errors.New("平台订单状态已变化，交付已回滚")
		}
		return nil
	})
	_ = method
	return matched, e
}

func (s *Service) Overview(ctx context.Context, userID int64) (*Overview, error) {
	var v Overview
	db := s.db.WithContext(ctx)
	if e := db.Table(batchesTable).Where("user_id=? AND status='running' AND expire_time>?", userID, s.now()).Select("COALESCE(SUM(remaining_bytes),0) remaining_bytes,COUNT(*) active_batches,MIN(expire_time) expiring_at").Scan(&v).Error; e != nil {
		return nil, e
	}
	if e := db.Table(batchesTable).Where("user_id=?", userID).Select("COALESCE(SUM(consumed_bytes),0)").Scan(&v.UsedBytes).Error; e != nil {
		return nil, e
	}
	if e := db.Table("system.cdnsell_domains").Where("user_id=? AND enabled=1 AND deleted=0", userID).Count(&v.ActiveDomains).Error; e != nil {
		return nil, e
	}
	now := s.now().In(time.FixedZone("Asia/Shanghai", 8*3600))
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if e := db.Table(meterTable).Where("user_id=? AND status='confirmed' AND window_start>=?", userID, day).Select("COALESCE(SUM(confirmed_bytes),0)").Scan(&v.TodayBytes).Error; e != nil {
		return nil, e
	}
	if e := db.Table(usageWindowsTable).Where("user_id=?", userID).Select("COALESCE(SUM(overage_bytes),0)").Scan(&v.OverageBytes).Error; e != nil {
		return nil, e
	}
	trend := make([]TrendPoint, 0)
	if e := db.Table(meterTable).Where("user_id=? AND status='confirmed'", userID).Select("window_start time,SUM(confirmed_bytes) bytes").Group("window_start").Order("window_start DESC").Limit(288).Scan(&trend).Error; e != nil {
		return nil, e
	}
	sort.Slice(trend, func(i, j int) bool { return trend[i].Time.Before(trend[j].Time) })
	v.Trend = trend
	return &v, nil
}
func (s *Service) Batches(ctx context.Context, userID int64) ([]Batch, error) {
	var rows []Batch
	e := s.db.WithContext(ctx).Table(batchesTable).Where("user_id=?", userID).Order("expire_time,id").Find(&rows).Error
	return rows, e
}
func (s *Service) Usage(ctx context.Context, userID int64) ([]MeterRecord, error) {
	var rows []MeterRecord
	e := s.db.WithContext(ctx).Table(meterTable).Where("user_id=?", userID).Order("window_start DESC,id DESC").Limit(1000).Find(&rows).Error
	return rows, e
}
func (s *Service) Capabilities(ctx context.Context) ([]Capability, error) {
	var rows []Capability
	e := s.db.WithContext(ctx).Table("system.cdnsell_api_capabilities").Order("id").Find(&rows).Error
	return rows, e
}
func (s *Service) AdminOverview(ctx context.Context) (map[string]any, error) {
	out := map[string]any{}
	var activeProducts, accounts, customers int64
	s.db.WithContext(ctx).Table(productsTable).Where("status=1").Count(&activeProducts)
	s.db.WithContext(ctx).Table(accountsTable).Where("status=1").Count(&accounts)
	s.db.WithContext(ctx).Table(batchesTable).Where("status='running'").Distinct("user_id").Count(&customers)
	var sold, remaining int64
	s.db.WithContext(ctx).Table(batchesTable).Select("COALESCE(SUM(original_bytes),0),COALESCE(SUM(remaining_bytes),0)").Row().Scan(&sold, &remaining)
	out["activeProducts"], out["activeAccounts"], out["customers"], out["soldBytes"], out["remainingBytes"] = activeProducts, accounts, customers, sold, remaining
	return out, nil
}
func (s *Service) Customers(ctx context.Context) ([]CustomerSummary, error) {
	var rows []CustomerSummary
	e := s.db.WithContext(ctx).Table("customer.customers c").Select("c.id user_id,c.username,COALESCE(SUM(b.remaining_bytes),0) remaining_bytes,COALESCE(SUM(b.consumed_bytes),0) used_bytes,SUM(CASE WHEN b.status='running' THEN 1 ELSE 0 END) active_batches").Joins("LEFT JOIN system.cdnsell_batches b ON b.user_id=c.id").Group("c.id,c.username").Having("COUNT(b.id)>0").Order("c.id DESC").Scan(&rows).Error
	return rows, e
}

func (s *Service) AdjustTraffic(ctx context.Context, userID, bytes int64, days int) error {
	if userID <= 0 || bytes == 0 {
		return errors.New("客户与调整流量不能为空")
	}
	if days <= 0 {
		days = 30
	}
	now := s.now()
	if bytes > 0 {
		return s.db.WithContext(ctx).Exec("INSERT INTO system.cdnsell_batches(id,user_id,product_id,order_id,original_bytes,consumed_bytes,remaining_bytes,effective_time,expire_time,status,created_at) VALUES(?,?,0,?,?,0,?,?,?,'running',?)", snowflake.GenID(), userID, snowflake.GenID(), bytes, bytes, now, now.AddDate(0, 0, days), now).Error
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []Batch
		if e := tx.Table(batchesTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=? AND status='running' AND expire_time>?", userID, now).Order("expire_time,id").Find(&rows).Error; e != nil {
			return e
		}
		over, updated := allocateFIFO(rows, -bytes)
		if over > 0 {
			return errors.New("客户剩余流量不足")
		}
		for _, b := range updated {
			if e := tx.Table(batchesTable).Where("id=?", b.ID).Updates(map[string]any{"consumed_bytes": b.ConsumedBytes, "remaining_bytes": b.RemainingBytes, "status": b.Status}).Error; e != nil {
				return e
			}
		}
		return nil
	})
}
func (s *Service) SetCustomerService(ctx context.Context, userID int64, enabled bool) error {
	var domains []DomainRecord
	if err := s.db.WithContext(ctx).Table(domainsTable).Where("user_id=? AND deleted=0 AND service_no<>''", userID).Find(&domains).Error; err != nil {
		return err
	}
	for _, d := range domains {
		if _, err := s.SetDomainState(ctx, userID, d.ID, enabled); err != nil {
			return err
		}
	}
	return nil
}

func allocateFIFO(batches []Batch, bytes int64) (int64, []Batch) {
	sort.Slice(batches, func(i, j int) bool {
		if batches[i].ExpireTime.Equal(batches[j].ExpireTime) {
			return batches[i].ID < batches[j].ID
		}
		return batches[i].ExpireTime.Before(batches[j].ExpireTime)
	})
	left := bytes
	for i := range batches {
		if left <= 0 {
			break
		}
		take := batches[i].RemainingBytes
		if take > left {
			take = left
		}
		batches[i].RemainingBytes -= take
		batches[i].ConsumedBytes += take
		left -= take
		if batches[i].RemainingBytes == 0 {
			batches[i].Status = "exhausted"
		}
	}
	return left, batches
}

func (s *Service) warnCustomer(ctx context.Context, userID int64) {
	var totals struct{ Original, Remaining int64 }
	now := s.now()
	if s.db.WithContext(ctx).Table(batchesTable).Where("user_id=? AND status IN ('running','exhausted') AND effective_time<=? AND expire_time>?", userID, now, now).Select("COALESCE(SUM(original_bytes),0) original,COALESCE(SUM(remaining_bytes),0) remaining").Scan(&totals).Error != nil || totals.Original <= 0 {
		return
	}
	var settings struct {
		Enabled    bool
		Thresholds json.RawMessage `gorm:"column:customer_warn_thresholds"`
		Channels   json.RawMessage `gorm:"column:notify_channels"`
		Cooldown   int             `gorm:"column:overage_notify_interval"`
	}
	if s.db.WithContext(ctx).Table(settingsTable).Where("id=1").Take(&settings).Error != nil || !settings.Enabled {
		return
	}
	var thresholds []struct {
		Value float64 `json:"value"`
	}
	if json.Unmarshal(settings.Thresholds, &thresholds) != nil {
		return
	}
	percent := float64(totals.Remaining) * 100 / float64(totals.Original)
	matched := false
	for _, v := range thresholds {
		if v.Value >= 0 && v.Value <= 100 && percent <= v.Value {
			matched = true
		}
	}
	if !matched {
		return
	}
	var channels map[string]bool
	if json.Unmarshal(settings.Channels, &channels) != nil {
		return
	}
	content := fmt.Sprintf("CDN 可用流量仅剩 %.2f%%（%d 字节），请及时购买流量包。", percent, totals.Remaining)
	_ = s.sendCDNWarning(ctx, userID, "customer_traffic", "cdnsell_usage", content, map[string]string{"remaining": strconv.FormatInt(totals.Remaining, 10), "percent": fmt.Sprintf("%.2f", percent)}, channels, settings.Cooldown)
}
func (s *Service) RunExpiry(ctx context.Context) error {
	enabled, err := s.Enabled(ctx)
	if err != nil || !enabled {
		return err
	}
	return s.db.WithContext(ctx).Table(batchesTable).Where("status='running' AND expire_time<=?", s.now()).Update("status", "expired").Error
}
func (s *Service) RunUsageSync(ctx context.Context) error {
	enabled, err := s.Enabled(ctx)
	if err != nil || !enabled {
		return err
	}
	var domains []struct {
		ID, UserID, AccountID   int64
		ServiceNo, BusinessType string
	}
	if err = s.db.WithContext(ctx).Table(domainsTable).Where("deleted=0 AND service_no<>''").Find(&domains).Error; err != nil {
		return err
	}
	// Leave a 15-minute settling delay, then revisit the last six hours for revisions.
	end := s.now().UTC().Add(-15 * time.Minute).Truncate(5 * time.Minute)
	var failures []error
	for _, d := range domains {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		start := end.Add(-6 * time.Hour)
		var cursor struct{ ConfirmedUntil time.Time }
		e := s.db.WithContext(ctx).Table("system.cdnsell_usage_cursors").Where("domain_id=?", d.ID).Take(&cursor).Error
		if e == nil && cursor.ConfirmedUntil.Before(start) {
			start = cursor.ConfirmedUntil
		}
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			failures = append(failures, e)
			continue
		}
		// Bounded catch-up; cursor advances only after every returned point commits.
		queryEnd := end
		if queryEnd.Sub(start) > 24*time.Hour {
			queryEnd = start.Add(24 * time.Hour)
		}
		_, account, e := s.accountSecret(ctx, d.AccountID)
		if e != nil {
			failures = append(failures, fmt.Errorf("CDN域名%d账号不可用", d.ID))
			continue
		}
		if e = s.waitCloudGap(ctx); e != nil {
			return e
		}
		points, e := s.cloud.GetUsage(ctx, account, provider.Domain{ServiceNo: d.ServiceNo, BusinessType: d.BusinessType}, start, queryEnd)
		if e != nil {
			failures = append(failures, fmt.Errorf("CDN域名%d用量查询失败: %w", d.ID, e))
			continue
		}
		if e = validateUsageSeries(points, start, queryEnd); e != nil {
			failures = append(failures, fmt.Errorf("CDN域名%d用量不完整: %w", d.ID, e))
			continue
		}
		complete := true
		for _, point := range points {
			if point.Start.Before(start) || point.End.After(queryEnd) {
				complete = false
				failures = append(failures, errors.New("供应商用量窗口越界"))
				break
			}
			if e = s.ApplyUsage(ctx, d.UserID, d.ID, point); e != nil {
				complete = false
				failures = append(failures, e)
				break
			}
		}
		if complete {
			if e = s.db.WithContext(ctx).Exec("INSERT INTO system.cdnsell_usage_cursors(domain_id,confirmed_until) VALUES(?,?) ON DUPLICATE KEY UPDATE confirmed_until=GREATEST(confirmed_until,VALUES(confirmed_until))", d.ID, queryEnd).Error; e != nil {
				failures = append(failures, e)
			}
		}
	}
	return errors.Join(failures...)
}

func (s *Service) Start(ctx context.Context) {
	s.startP2(ctx)
	go func() {
		usage := time.NewTicker(time.Minute)
		expiry := time.NewTicker(5 * time.Minute)
		defer usage.Stop()
		defer expiry.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-usage.C:
				if enabled, err := s.Enabled(ctx); err != nil || !enabled {
					continue
				}
				distsync.Task(ctx, "cdn_usage_sync", time.Minute, func(ctx context.Context) error {
					s.runTask(ctx, "usage_sync", s.RunUsageSync)
					s.runTask(ctx, "service_policy", s.RunServicePolicy)
					s.runTask(ctx, "overage_billing", s.RunOverageBilling)
					return nil
				})
			case <-expiry.C:
				if enabled, err := s.Enabled(ctx); err != nil || !enabled {
					continue
				}
				distsync.Task(ctx, "cdn_batch_expiry", time.Minute, func(ctx context.Context) error { s.runTask(ctx, "batch_expiry", s.RunExpiry); return nil })
			}
		}
	}()
}

func (s *Service) runTask(ctx context.Context, kind string, fn func(context.Context) error) {
	id := snowflake.GenID()
	no := "CT" + fmt.Sprintf("%d", id)
	now := s.now()
	_ = s.db.WithContext(ctx).Exec("INSERT INTO system.cdnsell_tasks(id,task_no,task_type,status,attempts,max_attempts,created_at) VALUES(?,?,?,'running',1,5,?)", id, no, kind, now).Error
	err := fn(ctx)
	if err == nil {
		_ = s.db.WithContext(ctx).Table("system.cdnsell_tasks").Where("id=?", id).Updates(map[string]any{"status": "success", "finished_at": s.now()}).Error
		return
	}
	_ = s.db.WithContext(ctx).Table("system.cdnsell_tasks").Where("id=?", id).Updates(map[string]any{"status": "failed", "error": err.Error(), "next_run_at": s.now().Add(time.Minute)}).Error
}

// Do not advance the billing cursor over a missing, duplicate, or malformed
// window. The provider must explicitly confirm zero traffic as well.
func validateUsageSeries(points []provider.UsagePoint, start, end time.Time) error {
	if !start.Equal(start.Truncate(5*time.Minute)) || !end.Equal(end.Truncate(5*time.Minute)) || !end.After(start) {
		return errors.New("查询窗口无效")
	}
	if len(points) != int(end.Sub(start)/(5*time.Minute)) {
		return errors.New("计量窗口缺失或重复")
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Start.Before(points[j].Start) })
	expected := start
	for _, p := range points {
		if p.Bytes < 0 || !p.Start.Equal(expected) || !p.End.Equal(expected.Add(5*time.Minute)) {
			return errors.New("计量窗口不连续或值无效")
		}
		expected = p.End
	}
	return nil
}
