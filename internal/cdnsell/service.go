package cdnsell

import (
	"context"
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
	if v.Name == "" || v.CapacityBytes <= 0 || !v.ExpireTime.After(v.EffectiveTime) {
		return errors.New("资源包参数无效")
	}
	if v.ID == 0 {
		v.ID = snowflake.GenID()
		return s.db.WithContext(ctx).Table(packagesTable).Create(v).Error
	}
	return s.db.WithContext(ctx).Table(packagesTable).Where("id=?", v.ID).Updates(v).Error
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
	if v.Name == "" || v.CapacityBytes <= 0 || v.ValidDays <= 0 || v.Price < 0 || v.MinQty < 1 || v.MaxQty < v.MinQty {
		return errors.New("产品参数无效")
	}
	if len(v.TierPrices) == 0 {
		v.TierPrices = json.RawMessage(`{}`)
	}
	if !json.Valid(v.TierPrices) {
		return errors.New("会员价格 JSON 无效")
	}
	if v.ID == 0 {
		v.ID = snowflake.GenID()
		return s.db.WithContext(ctx).Table(productsTable).Create(v).Error
	}
	return s.db.WithContext(ctx).Table(productsTable).Where("id=?", v.ID).Updates(v).Error
}
func (s *Service) DeleteProduct(ctx context.Context, id int64) error {
	return s.db.WithContext(ctx).Table(productsTable).Where("id=?", id).Delete(nil).Error
}
func priceFor(p Product, level int) int64 {
	var tiers map[string]int64
	if json.Unmarshal(p.TierPrices, &tiers) == nil {
		if v, ok := tiers[strconv.Itoa(level)]; ok {
			return v
		}
	}
	return p.Price
}
func (s *Service) Purchase(ctx context.Context, userID, productID int64, qty int) (*PurchaseResult, error) {
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
	amount := unit * int64(qty)
	now := s.now()
	mainID, bizID := snowflake.GenID(), snowflake.GenID()
	no := "CDN" + now.Format("20060102") + fmt.Sprintf("%010d", bizID%10000000000)
	snap, _ := json.Marshal(map[string]any{"productName": p.Name, "capacityBytes": p.CapacityBytes, "unitPrice": unit, "customerLevel": customer.Level, "quantity": qty, "validDays": p.ValidDays})
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		order := orderdom.Order{ID: mainID, OrderNo: no, TenantID: customer.TenantID, CustomerID: userID, TotalAmount: amount, PayAmount: amount, Status: orderdom.OrderStatusPending, CreatedAt: now, UpdatedAt: now}
		if e := tx.Create(&order).Error; e != nil {
			return e
		}
		item := orderdom.OrderItem{OrderID: mainID, ProductID: p.ID, ProductName: p.Name, ModuleType: "cdnsell", ConfigSnapshot: string(snap), Qty: uint(qty), UnitPrice: unit, Subtotal: amount, DeliveryStatus: 0, CreatedAt: now, UpdatedAt: now}
		if e := tx.Create(&item).Error; e != nil {
			return e
		}
		return tx.Exec("INSERT INTO system.cdnsell_orders(id,order_no,user_id,order_type,ref_order_id,product_id,traffic_bytes,amount,snapshot,status,created_at) VALUES(?,?,?,'traffic_pack',?,?,?,?,?,'pending',?)", bizID, no, userID, mainID, p.ID, p.CapacityBytes*int64(qty), amount, string(snap), now).Error
	})
	if e != nil {
		return nil, e
	}
	return &PurchaseResult{OrderID: mainID, OrderNo: no, Amount: amount}, nil
}

// ProvisionPlatformOrder is the idempotent bridge called by the existing payment dispatcher.
func (s *Service) ProvisionPlatformOrder(ctx context.Context, userID, mainOrderID int64, method string) (bool, error) {
	matched := false
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row struct {
			ID, UserID, ProductID, TrafficBytes int64
			Status                              string
			BatchID                             *int64
			Snapshot                            json.RawMessage
		}
		e := tx.Table(ordersTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("ref_order_id=?", mainOrderID).Take(&row).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return nil
		}
		if e != nil {
			return e
		}
		matched = true
		if row.UserID != userID {
			return errors.New("CDN 订单归属不匹配")
		}
		if row.Status == "paid" && row.BatchID != nil {
			return nil
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
		return tx.Table("order.orders").Where("id=?", mainOrderID).Updates(map[string]any{"status": orderdom.OrderStatusCompleted, "updated_at": now}).Error
	})
	_ = method
	return matched, e
}

func (s *Service) Overview(ctx context.Context, userID int64) (*Overview, error) {
	var v Overview
	s.db.WithContext(ctx).Table(batchesTable).Where("user_id=? AND status='running' AND expire_time>?", userID, s.now()).Select("COALESCE(SUM(remaining_bytes),0) remaining_bytes,COALESCE(SUM(consumed_bytes),0) used_bytes,COUNT(*) active_batches,MIN(expire_time) expiring_at").Scan(&v)
	s.db.WithContext(ctx).Table("system.cdnsell_domains").Where("user_id=? AND enabled=1 AND deleted=0", userID).Count(&v.ActiveDomains)
	day := s.now().Truncate(24 * time.Hour)
	s.db.WithContext(ctx).Table(meterTable).Where("user_id=? AND status='confirmed' AND window_start>=?", userID, day).Select("COALESCE(SUM(confirmed_bytes),0)").Scan(&v.TodayBytes)
	trend := make([]TrendPoint, 0)
	s.db.WithContext(ctx).Table(meterTable).Where("user_id=? AND status='confirmed'", userID).Select("window_start time,SUM(confirmed_bytes) bytes").Group("window_start").Order("window_start DESC").Limit(288).Scan(&trend)
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
	return s.db.WithContext(ctx).Table("system.cdnsell_domains").Where("user_id=? AND deleted=0", userID).Update("enabled", enabled).Error
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
func (s *Service) ApplyUsage(ctx context.Context, userID, domainID int64, p provider.UsagePoint) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var old MeterRecord
		e := tx.Table(meterTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=? AND domain_id=? AND window_start=? AND status='confirmed'", userID, domainID, p.Start).Order("id DESC").Take(&old).Error
		delta := p.Bytes
		if e == nil {
			if old.ConfirmedBytes == p.Bytes {
				return nil
			}
			delta = p.Bytes - old.ConfirmedBytes
			if e := tx.Table(meterTable).Where("id=?", old.ID).Updates(map[string]any{"status": "revised", "revised_bytes": p.Bytes}).Error; e != nil {
				return e
			}
		} else if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		if e := tx.Exec("INSERT INTO system.cdnsell_meter_records(id,user_id,domain_id,window_start,window_end,confirmed_bytes,revised_bytes,status,source) VALUES(?,?,?,?,?,?,0,'confirmed','usage_sync')", snowflake.GenID(), userID, domainID, p.Start, p.End, p.Bytes).Error; e != nil {
			return e
		}
		if delta <= 0 {
			return nil
		}
		var batches []Batch
		if e := tx.Table(batchesTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=? AND status='running' AND effective_time<=? AND expire_time>?", userID, p.End, p.Start).Order("expire_time,id").Find(&batches).Error; e != nil {
			return e
		}
		_, updated := allocateFIFO(batches, delta)
		for _, b := range updated {
			if e := tx.Table(batchesTable).Where("id=?", b.ID).Updates(map[string]any{"remaining_bytes": b.RemainingBytes, "consumed_bytes": b.ConsumedBytes, "status": b.Status}).Error; e != nil {
				return e
			}
		}
		return nil
	})
	if err == nil {
		s.warnCustomer(ctx, userID)
	}
	return err
}

func (s *Service) warnCustomer(ctx context.Context, userID int64) {
	var totals struct{ Original, Remaining int64 }
	if s.db.WithContext(ctx).Table(batchesTable).Where("user_id=? AND status='running'", userID).Select("COALESCE(SUM(original_bytes),0) original,COALESCE(SUM(remaining_bytes),0) remaining").Scan(&totals).Error != nil || totals.Original <= 0 {
		return
	}
	var settings struct {
		Thresholds json.RawMessage `gorm:"column:customer_warn_thresholds"`
		Channels   json.RawMessage `gorm:"column:notify_channels"`
		Cooldown   int             `gorm:"column:overage_notify_interval"`
	}
	if s.db.WithContext(ctx).Table(settingsTable).Where("id=1").Take(&settings).Error != nil {
		return
	}
	var thresholds []struct {
		Value float64 `json:"value"`
	}
	_ = json.Unmarshal(settings.Thresholds, &thresholds)
	percent := float64(totals.Remaining) * 100 / float64(totals.Original)
	threshold := -1.0
	for _, v := range thresholds {
		if percent <= v.Value && (threshold < 0 || v.Value < threshold) {
			threshold = v.Value
		}
	}
	if threshold < 0 {
		return
	}
	event := fmt.Sprintf("customer_traffic_%.0f", threshold)
	cool := settings.Cooldown
	if cool <= 0 {
		cool = 86400
	}
	var count int64
	s.db.WithContext(ctx).Table("system.cdnsell_notify_records").Where("user_id=? AND event=? AND created_at>?", userID, event, s.now().Add(-time.Duration(cool)*time.Second)).Count(&count)
	if count > 0 {
		return
	}
	content := fmt.Sprintf("CDN 可用流量仅剩 %.2f%%（%s 字节），请及时购买流量包。", percent, strconv.FormatInt(totals.Remaining, 10))
	now := s.now()
	if s.db.WithContext(ctx).Exec("INSERT INTO common.notifications(id,customer_id,channel,template_code,params,subject,content,status,sent_at,created_at,updated_at) VALUES(?,?,'inapp','cdnsell_usage',JSON_OBJECT('percent',?),'CDN 流量预警',?,2,?,?,?)", snowflake.GenID(), userID, percent, content, now, now, now).Error == nil {
		_ = s.db.WithContext(ctx).Exec("INSERT INTO system.cdnsell_notify_records(id,user_id,event,channel,template,content,status,created_at) VALUES(?,?,?,'site','cdnsell_usage',?,'sent',?)", snowflake.GenID(), userID, event, content, now).Error
	}
	var channels map[string]bool
	_ = json.Unmarshal(settings.Channels, &channels)
	if s.notifier != nil && (channels["email"] || channels["sms"]) {
		notifyErr := s.notifier.NotifyCustomer(ctx, "cdnsell_usage", userID, strconv.FormatInt(snowflake.GenID(), 10), map[string]string{"remaining": strconv.FormatInt(totals.Remaining, 10), "percent": fmt.Sprintf("%.2f", percent)})
		status, errText := "sent", ""
		if notifyErr != nil {
			status = "failed"
			errText = notifyErr.Error()
		}
		for _, channel := range []string{"email", "sms"} {
			if !channels[channel] {
				continue
			}
			_ = s.db.WithContext(ctx).Exec("INSERT INTO system.cdnsell_notify_records(id,user_id,event,channel,template,content,status,error,created_at) VALUES(?,?,?,?, 'cdnsell_usage',?,?,?,?)", snowflake.GenID(), userID, event, channel, content, status, errText, now).Error
		}
	}
}
func (s *Service) RunExpiry(ctx context.Context) error {
	return s.db.WithContext(ctx).Table(batchesTable).Where("status='running' AND expire_time<=?", s.now()).Update("status", "expired").Error
}
func (s *Service) RunUsageSync(ctx context.Context) error {
	var domains []struct {
		ID, UserID, AccountID   int64
		ServiceNo, BusinessType string
	}
	if e := s.db.WithContext(ctx).Table("system.cdnsell_domains").Where("enabled=1 AND deleted=0 AND upstream_status='active' AND service_no<>''").Find(&domains).Error; e != nil {
		return e
	}
	end := s.now().Truncate(5 * time.Minute)
	start := end.Add(-10 * time.Minute)
	for i, d := range domains {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
		_, account, e := s.accountSecret(ctx, d.AccountID)
		if e != nil {
			continue
		}
		calledAt := time.Now()
		if e = s.waitCloudGap(ctx); e != nil {
			return e
		}
		points, e := s.cloud.GetUsage(ctx, account, provider.Domain{ServiceNo: d.ServiceNo, BusinessType: d.BusinessType}, start, end)
		errText := ""
		if e != nil {
			errText = e.Error()
		}
		_ = s.db.WithContext(ctx).Exec("INSERT INTO system.cdnsell_api_logs(id,account_id,api_name,request,response,status_code,cost_ms,success,error) VALUES(?,?,'GetUsage',JSON_OBJECT('serviceNo',?,'start',?,'end',?),JSON_OBJECT('points',?),?,?,?,?)", snowflake.GenID(), d.AccountID, d.ServiceNo, start, end, len(points), map[bool]int{true: 200, false: 0}[e == nil], time.Since(calledAt).Milliseconds(), e == nil, errText).Error
		if e != nil {
			continue
		}
		for _, p := range points {
			_ = s.ApplyUsage(ctx, d.UserID, d.ID, p)
		}
	}
	return nil
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
				s.runTask(ctx, "usage_sync", s.RunUsageSync)
			case <-expiry.C:
				s.runTask(ctx, "batch_expiry", s.RunExpiry)
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
