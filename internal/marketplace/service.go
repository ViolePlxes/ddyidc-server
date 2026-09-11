package marketplace

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"ddyidc/internal/shared/snowflake"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

//go:embed schema.sql
var schemaSQL string

var (
	ErrNotFound    = errors.New("交易市场记录不存在")
	ErrForbidden   = errors.New("没有访问该交易记录的权限")
	ErrInvalid     = errors.New("当前状态不允许此操作")
	ErrMaintenance = errors.New("交易市场维护中")
)

type Notifier interface {
	NotifyCustomer(context.Context, string, int64, string, map[string]string) error
}

type Service struct {
	db       *gorm.DB
	redis    *redis.Client
	notifier Notifier
	now      func() time.Time
}

func NewService(database *gorm.DB, redisClient *redis.Client, notifier Notifier) *Service {
	return &Service{db: database, redis: redisClient, notifier: notifier, now: time.Now}
}

func (s *Service) EnsureSchema(ctx context.Context) error {
	if err := s.db.WithContext(ctx).Exec(schemaSQL).Error; err != nil {
		return fmt.Errorf("初始化交易市场表失败: %w", err)
	}
	var count int64
	if err := s.db.WithContext(ctx).Table(settingsTable).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		row := &Settings{ID: snowflake.GenID(), Enabled: false, AuditEnabled: true, RequireBackupConfirm: true,
			RefundEnabled: true, ForceSettleAfterWindow: true, WithdrawalAuditEnabled: true,
			Announcement: "交易前请确认产品配置、剩余周期与续费价格。",
			Agreement:    "买卖双方须遵守平台交易规则。订单支付后由系统完成本地资源归属转移，争议须在退款窗口内提交。",
			FeeType:      "ratio", FeeBearer: "seller", FeeRate: "5.00", FeeFixed: "0.00",
			WithdrawFeeType: "free", WithdrawFeeRate: "0.00", WithdrawFeeFixed: "0.00",
			MaxListingsPerUser: 20, ListingValidMinutes: 43200, OrderLockMinutes: 15,
			SettleFreezeDays: 3, RefundWindowDays: 3, AllowGroups: json.RawMessage("[]")}
		if err := s.db.WithContext(ctx).Table(settingsTable).Create(row).Error; err != nil {
			return err
		}
	}
	return s.ensureNotificationTemplates(ctx)
}

func (s *Service) ensureNotificationTemplates(ctx context.Context) error {
	rows := []struct{ scene, title, content string }{
		{"marketplace_listing_passed", "交易市场挂牌通过", "您的挂牌 {{listing_no}} 已审核通过。"},
		{"marketplace_trade_success", "交易市场订单支付成功", "交易订单 {{order_no}} 已支付，资源正在完成过户。"},
		{"marketplace_transfer_complete", "交易市场资源过户完成", "交易订单 {{order_no}} 的资源 {{product_name}} 已完成本地归属转移。"},
		{"marketplace_refund", "交易市场退款完成", "交易订单 {{order_no}} 已退款并回滚资源归属。"},
		{"marketplace_withdraw_result", "交易市场提现结果", "提现申请 {{withdraw_no}} 的处理结果为：{{status}}。"},
	}
	for _, row := range rows {
		if err := s.db.WithContext(ctx).Exec("INSERT IGNORE INTO system.notification_templates (id, scene, channel, title, content, enabled) VALUES (?, ?, 'both', ?, ?, 1)", snowflake.GenID(), row.scene, row.title, row.content).Error; err != nil {
			return err
		}
		if err := s.db.WithContext(ctx).Exec("INSERT IGNORE INTO system.system_configs (id, `key`, value, description) VALUES (?, ?, '', '交易市场短信模板编码')", snowflake.GenID(), "notify.sms.templates."+row.scene).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Enabled(ctx context.Context) (bool, error) {
	var row Settings
	err := s.db.WithContext(ctx).Table(settingsTable).Select("enabled").Order("id").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return row.Enabled, err
}

func (s *Service) Settings(ctx context.Context) (*Settings, error) {
	var row Settings
	if err := s.db.WithContext(ctx).Table(settingsTable).Order("id").First(&row).Error; err != nil {
		return nil, err
	}
	if len(row.AllowGroups) == 0 {
		row.AllowGroups = json.RawMessage("[]")
	}
	return &row, nil
}

func (s *Service) SaveSettings(ctx context.Context, row *Settings) error {
	current, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	row.ID = current.ID
	if err := validateSettings(row); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Table(settingsTable).Where("id=?", row.ID).Updates(map[string]any{
		"enabled": row.Enabled, "maintenance": row.Maintenance, "audit_enabled": row.AuditEnabled,
		"require_backup_confirm": row.RequireBackupConfirm, "require_realname_listing": row.RequireRealnameListing,
		"require_realname_purchase": row.RequireRealnamePurchase, "refund_enabled": row.RefundEnabled,
		"force_settle_after_window": row.ForceSettleAfterWindow, "withdrawal_audit_enabled": row.WithdrawalAuditEnabled,
		"announcement": strings.TrimSpace(row.Announcement), "agreement": strings.TrimSpace(row.Agreement),
		"maintenance_tip": strings.TrimSpace(row.MaintenanceTip), "fee_type": row.FeeType, "fee_bearer": row.FeeBearer,
		"fee_rate": row.FeeRate, "fee_fixed": row.FeeFixed, "withdraw_fee_type": row.WithdrawFeeType,
		"withdraw_fee_rate": row.WithdrawFeeRate, "withdraw_fee_fixed": row.WithdrawFeeFixed,
		"max_listings_per_user": row.MaxListingsPerUser, "new_user_limit_days": row.NewUserLimitDays,
		"listing_valid_minutes": row.ListingValidMinutes, "order_lock_minutes": row.OrderLockMinutes,
		"settle_freeze_days": row.SettleFreezeDays, "refund_window_days": row.RefundWindowDays,
		"allow_groups": row.AllowGroups, "updated_at": s.now(),
	}).Error
}

func validateSettings(v *Settings) error {
	if v.RequireRealnameListing || v.RequireRealnamePurchase { /* existing customer.real_name is the integration source */
	}
	if v.FeeType != "ratio" && v.FeeType != "fixed" {
		return errors.New("手续费类型无效")
	}
	if v.FeeBearer != "seller" && v.FeeBearer != "buyer" && v.FeeBearer != "platform" {
		return errors.New("手续费承担方无效")
	}
	if v.WithdrawFeeType != "ratio" && v.WithdrawFeeType != "fixed" && v.WithdrawFeeType != "free" {
		return errors.New("提现手续费类型无效")
	}
	if _, err := parseUnsignedMoney(v.FeeFixed); err != nil {
		return errors.New("固定手续费格式无效")
	}
	if _, err := parsePercent(v.FeeRate); err != nil {
		return errors.New("手续费率格式无效")
	}
	if _, err := parseUnsignedMoney(v.WithdrawFeeFixed); err != nil {
		return errors.New("提现固定手续费格式无效")
	}
	if _, err := parsePercent(v.WithdrawFeeRate); err != nil {
		return errors.New("提现手续费率格式无效")
	}
	if v.MaxListingsPerUser < 0 || v.NewUserLimitDays < 0 || v.ListingValidMinutes < 1 || v.OrderLockMinutes < 1 || v.SettleFreezeDays < 0 || v.RefundWindowDays < 0 {
		return errors.New("时效或风控配置无效")
	}
	var groups []string
	if len(v.AllowGroups) == 0 || json.Unmarshal(v.AllowGroups, &groups) != nil {
		return errors.New("可交易分组格式无效")
	}
	return nil
}

type ListingQuery struct {
	Keyword, Region, Sort, Quick, Status, AuditStatus string
	GroupID, ViewerID                                 int64
	Page, PageSize                                    int
	Public                                            bool
}

func (s *Service) ListListings(ctx context.Context, in ListingQuery) ([]Listing, int64, error) {
	in.Page, in.PageSize = normalizePage(in.Page, in.PageSize)
	q := s.db.WithContext(ctx).Table(listingsTable + " AS l")
	if in.Public {
		q = q.Where("l.status='on_sale' AND l.audit_status='passed' AND l.expires_at>?", s.now())
	}
	if in.Status != "" {
		q = q.Where("l.status=?", in.Status)
	}
	if in.AuditStatus != "" {
		q = q.Where("l.audit_status=?", in.AuditStatus)
	}
	if in.ViewerID > 0 && !in.Public {
		q = q.Where("l.seller_id=?", in.ViewerID)
	}
	if in.GroupID > 0 {
		q = q.Where("l.group_id=?", in.GroupID)
	}
	if strings.TrimSpace(in.Region) != "" {
		q = q.Where("l.region=?", strings.TrimSpace(in.Region))
	}
	switch in.Quick {
	case "below_renewal":
		q = q.Where("l.original_renewal_price>0 AND l.price<l.original_renewal_price")
	case "low_price":
		q = q.Where("l.price<=100")
	}
	if keyword := strings.TrimSpace(in.Keyword); keyword != "" {
		like := "%" + keyword + "%"
		q = q.Where("l.product_name LIKE ? OR l.region LIKE ? OR CAST(l.config_snapshot AS CHAR) LIKE ?", like, like, like)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	order := "l.is_recommended DESC,l.created_at DESC"
	switch in.Sort {
	case "price", "price_asc":
		order = "l.price ASC,l.created_at DESC"
	case "price_desc":
		order = "l.price DESC,l.created_at DESC"
	case "hot":
		order = "l.views DESC,l.favorite_count DESC"
	}
	var rows []Listing
	err := q.Select("l.*").Order(order).Offset((in.Page - 1) * in.PageSize).Limit(in.PageSize).Find(&rows).Error
	if in.Public {
		for i := range rows {
			rows[i].SellerID = 0
			rows[i].AuditorID = nil
			rows[i].AuditRemark = ""
		}
	}
	return rows, total, err
}

func (s *Service) Listing(ctx context.Context, id, viewerID int64, public bool) (*Listing, error) {
	var row Listing
	q := s.db.WithContext(ctx).Table(listingsTable).Where("id=?", id)
	if public {
		q = q.Where("status='on_sale' AND audit_status='passed' AND expires_at>?", s.now())
	}
	if err := q.First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if public {
		_ = s.db.WithContext(ctx).Table(listingsTable).Where("id=?", id).UpdateColumn("views", gorm.Expr("views+1")).Error
		row.Views++
		row.SellerID = 0
		row.AuditorID = nil
		row.AuditRemark = ""
	}
	if viewerID > 0 {
		var n int64
		_ = s.db.WithContext(ctx).Table(favoritesTable).Where("listing_id=? AND user_id=?", id, viewerID).Count(&n).Error
		row.Favorite = n > 0
	}
	_ = s.db.WithContext(ctx).Table(pricesTable).Where("listing_id=?", id).Order("created_at DESC").Limit(20).Find(&row.PriceHistory).Error
	return &row, nil
}

type ProductForListing struct {
	ID             int64           `json:"id,string"`
	ProductName    string          `json:"productName"`
	ProductType    string          `json:"productType"`
	GroupID        int64           `json:"groupId,string"`
	PrimaryIP      string          `json:"primaryIp"`
	ConfigSnapshot json.RawMessage `json:"configSnapshot"`
	ExpiresAt      *time.Time      `json:"expiresAt,omitempty"`
	RemainingDays  int             `json:"remainingDays"`
}

func (s *Service) ListingProducts(ctx context.Context, userID int64, keyword string) ([]ProductForListing, error) {
	settings, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	allowed := allowedGroups(settings.AllowGroups)
	q := s.db.WithContext(ctx).Table("instance.instances AS i").Select("i.id,i.product_name,i.module_type AS product_type,p.group_id,i.primary_ip,i.config_snapshot,i.expires_at").Joins("LEFT JOIN product.products p ON p.id=i.product_id").Where("i.customer_id=? AND i.deleted_at IS NULL AND i.status<>6", userID).Where("NOT EXISTS (SELECT 1 FROM system.marketplace_listings ml WHERE ml.product_id=i.id AND ml.status IN ('pending','on_sale','locked'))")
	if k := strings.TrimSpace(keyword); k != "" {
		like := "%" + k + "%"
		q = q.Where("i.product_name LIKE ? OR i.primary_ip LIKE ? OR CAST(i.config_snapshot AS CHAR) LIKE ?", like, like, like)
	}
	if len(allowed) > 0 {
		q = q.Where("p.group_id IN ?", allowed)
	}
	var rows []ProductForListing
	if err = q.Order("i.created_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	now := s.now()
	for i := range rows {
		if rows[i].ExpiresAt != nil && rows[i].ExpiresAt.After(now) {
			rows[i].RemainingDays = int(rows[i].ExpiresAt.Sub(now).Hours() / 24)
		}
	}
	return rows, nil
}

type CreateListingInput struct {
	ProductID                                              int64
	Title, Region, Price, OriginalRenewalPrice, SellerNote string
	BackupConfirmed                                        bool
}

func (s *Service) CreateListing(ctx context.Context, userID int64, in CreateListingInput) (*Listing, error) {
	settings, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	if settings.Maintenance {
		return nil, ErrMaintenance
	}
	price, err := parsePositiveMoney(in.Price)
	if err != nil {
		return nil, errors.New("挂牌价格必须是大于0且最多两位小数的金额")
	}
	renewal, err := parseUnsignedMoney(in.OriginalRenewalPrice)
	if err != nil {
		return nil, errors.New("续费价格格式无效")
	}
	if settings.RequireBackupConfirm && !in.BackupConfirmed {
		return nil, errors.New("请确认已完成数据备份")
	}
	if err = s.checkUserRisk(ctx, userID, settings, true); err != nil {
		return nil, err
	}
	var host struct {
		ID, CustomerID, SourceProductID, GroupID int64
		ProductName, ModuleType, PrimaryIP       string
		ConfigSnapshot                           json.RawMessage
		ExpiresAt                                *time.Time
	}
	err = s.db.WithContext(ctx).Table("instance.instances AS i").Select("i.id,i.customer_id,i.product_id AS source_product_id,p.group_id,i.product_name,i.module_type,i.primary_ip,i.config_snapshot,i.expires_at").Joins("LEFT JOIN product.products p ON p.id=i.product_id").Where("i.id=? AND i.deleted_at IS NULL", in.ProductID).First(&host).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if host.CustomerID != userID {
		return nil, ErrForbidden
	}
	if !groupAllowed(settings.AllowGroups, host.GroupID) {
		return nil, errors.New("该产品分组不在可交易范围内")
	}
	var active int64
	s.db.WithContext(ctx).Table(listingsTable).Where("product_id=? AND status IN ('pending','on_sale','locked')", host.ID).Count(&active)
	if active > 0 {
		return nil, errors.New("该产品已在挂牌或交易中")
	}
	config := mergeSnapshot(host.ConfigSnapshot, host.PrimaryIP)
	remaining := 0
	if host.ExpiresAt != nil && host.ExpiresAt.After(s.now()) {
		remaining = int(host.ExpiresAt.Sub(s.now()).Hours() / 24)
	}
	name := strings.TrimSpace(in.Title)
	if name == "" {
		name = host.ProductName
	}
	status, audit := "pending", "pending"
	if !settings.AuditEnabled {
		status, audit = "on_sale", "passed"
	}
	row := &Listing{ListingNo: s.newNo("TM"), SellerID: userID, ProductID: host.ID, SourceProductID: host.SourceProductID, GroupID: host.GroupID, ProductName: name, ProductType: host.ModuleType, Region: strings.TrimSpace(in.Region), ConfigSnapshot: config, Price: formatMoney(price), OriginalRenewalPrice: formatMoney(renewal), RemainingDays: remaining, Status: status, AuditStatus: audit, SellerNote: strings.TrimSpace(in.SellerNote), ExpiresAt: s.now().Add(time.Duration(settings.ListingValidMinutes) * time.Minute)}
	if err = s.db.WithContext(ctx).Table(listingsTable).Create(row).Error; err != nil {
		return nil, err
	}
	return row, nil
}

func (s *Service) OffListing(ctx context.Context, userID, id int64, admin bool) error {
	q := s.db.WithContext(ctx).Table(listingsTable).Where("id=? AND status IN ('pending','on_sale')", id)
	if !admin {
		q = q.Where("seller_id=?", userID)
	}
	res := q.Updates(map[string]any{"status": "off_shelf", "updated_at": s.now()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrInvalid
	}
	return nil
}
func (s *Service) Reprice(ctx context.Context, userID, id int64, priceText string) error {
	cents, err := parsePositiveMoney(priceText)
	if err != nil {
		return errors.New("价格格式无效")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row Listing
		if err := tx.Table(listingsTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND seller_id=?", id, userID).First(&row).Error; err != nil {
			return ErrNotFound
		}
		if row.Status != "on_sale" {
			return ErrInvalid
		}
		history := &ListingPrice{ListingID: id, OldPrice: row.Price, NewPrice: formatMoney(cents)}
		if err := tx.Table(pricesTable).Create(history).Error; err != nil {
			return err
		}
		return tx.Table(listingsTable).Where("id=?", id).Updates(map[string]any{"price": history.NewPrice, "updated_at": s.now()}).Error
	})
}

func (s *Service) Favorite(ctx context.Context, userID, listingID int64, on bool) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var n int64
		if err := tx.Table(listingsTable).Where("id=? AND status='on_sale'", listingID).Count(&n).Error; err != nil || n == 0 {
			return ErrNotFound
		}
		if on {
			res := tx.Exec("INSERT IGNORE INTO "+favoritesTable+" (id,listing_id,user_id,created_at) VALUES (?,?,?,?)", snowflake.GenID(), listingID, userID, s.now())
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected > 0 {
				return tx.Table(listingsTable).Where("id=?", listingID).UpdateColumn("favorite_count", gorm.Expr("favorite_count+1")).Error
			}
			return nil
		}
		res := tx.Where("listing_id=? AND user_id=?", listingID, userID).Delete(&struct{ ID int64 }{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected > 0 {
			return tx.Table(listingsTable).Where("id=?", listingID).UpdateColumn("favorite_count", gorm.Expr("GREATEST(favorite_count-1,0)")).Error
		}
		return nil
	})
}

func (s *Service) Groups(ctx context.Context) ([]map[string]any, error) {
	settings, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	q := s.db.WithContext(ctx).Table("product.product_groups").Select("id,name").Where("status=1")
	if groups := allowedGroups(settings.AllowGroups); len(groups) > 0 {
		q = q.Where("id IN ?", groups)
	}
	var rows []struct {
		ID   int64
		Name string
	}
	if err = q.Order("sort,id").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{"id": strconv.FormatInt(r.ID, 10), "name": r.Name})
	}
	return out, nil
}

func (s *Service) Announcement(ctx context.Context) (map[string]any, error) {
	v, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"announcement": v.Announcement, "maintenance": v.Maintenance, "maintenanceTip": v.MaintenanceTip}, nil
}
func (s *Service) Agreement(ctx context.Context) (string, error) {
	v, err := s.Settings(ctx)
	if err != nil {
		return "", err
	}
	return v.Agreement, nil
}

func (s *Service) CreateWant(ctx context.Context, userID int64, row *Want) (*Want, error) {
	min, err := parsePositiveMoney(row.BudgetMin)
	if err != nil {
		return nil, errors.New("最低预算格式无效")
	}
	max, err := parsePositiveMoney(row.BudgetMax)
	if err != nil || max < min {
		return nil, errors.New("最高预算不得低于最低预算")
	}
	row.ID = 0
	row.UserID = userID
	row.WantNo = s.newNo("TW")
	row.Title = strings.TrimSpace(row.Title)
	if row.Title == "" || len([]rune(row.Title)) > 128 {
		return nil, errors.New("求购标题不能为空且不超过128字")
	}
	row.BudgetMin = formatMoney(min)
	row.BudgetMax = formatMoney(max)
	row.Region = strings.TrimSpace(row.Region)
	row.Description = strings.TrimSpace(row.Description)
	row.Status = "open"
	if len(row.Tags) == 0 {
		row.Tags = json.RawMessage("[]")
	}
	if err = s.db.WithContext(ctx).Table(wantsTable).Create(row).Error; err != nil {
		return nil, err
	}
	return row, nil
}
func (s *Service) ListWants(ctx context.Context, userID int64, mine bool, page, pageSize int) ([]Want, int64, error) {
	page, pageSize = normalizePage(page, pageSize)
	q := s.db.WithContext(ctx).Table(wantsTable)
	if mine {
		q = q.Where("user_id=?", userID)
	} else {
		q = q.Where("status='open'")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Want
	err := q.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error
	if !mine {
		for i := range rows {
			rows[i].UserID = 0
		}
	}
	return rows, total, err
}
func (s *Service) OfferWant(ctx context.Context, userID, wantID int64, row *WantOffer) (*WantOffer, error) {
	price, err := parsePositiveMoney(row.Price)
	if err != nil {
		return nil, errors.New("报价格式无效")
	}
	var want Want
	if err = s.db.WithContext(ctx).Table(wantsTable).Where("id=? AND status='open'", wantID).First(&want).Error; err != nil {
		return nil, ErrNotFound
	}
	if want.UserID == userID {
		return nil, errors.New("不能对自己的求购报价")
	}
	if row.ListingID != nil {
		var n int64
		s.db.WithContext(ctx).Table(listingsTable).Where("id=? AND seller_id=? AND status='on_sale'", *row.ListingID, userID).Count(&n)
		if n == 0 {
			return nil, errors.New("关联挂牌不可用")
		}
	}
	row.ID = 0
	row.WantID = wantID
	row.SellerID = userID
	row.Price = formatMoney(price)
	row.Message = strings.TrimSpace(row.Message)
	if err = s.db.WithContext(ctx).Table(offersTable).Create(row).Error; err != nil {
		return nil, err
	}
	return row, nil
}

func (s *Service) checkUserRisk(ctx context.Context, userID int64, settings *Settings, listing bool) error {
	var user struct {
		CreatedAt time.Time
		RealName  string
	}
	if err := s.db.WithContext(ctx).Table("customer.customers").Select("created_at,real_name").Where("id=? AND deleted_at IS NULL AND status=1", userID).First(&user).Error; err != nil {
		return ErrForbidden
	}
	if listing && settings.RequireRealnameListing && strings.TrimSpace(user.RealName) == "" {
		return errors.New("挂牌前请先完成实名认证")
	}
	if !listing && settings.RequireRealnamePurchase && strings.TrimSpace(user.RealName) == "" {
		return errors.New("购买前请先完成实名认证")
	}
	if listing && settings.NewUserLimitDays > 0 && user.CreatedAt.AddDate(0, 0, settings.NewUserLimitDays).After(s.now()) {
		return errors.New("账号注册时间未达到市场挂牌要求")
	}
	if listing && settings.MaxListingsPerUser > 0 {
		var n int64
		s.db.WithContext(ctx).Table(listingsTable).Where("seller_id=? AND status IN ('pending','on_sale','locked')", userID).Count(&n)
		if n >= int64(settings.MaxListingsPerUser) {
			return errors.New("已达到最大挂牌数量")
		}
	}
	return nil
}

func allowedGroups(raw json.RawMessage) []int64 {
	var values []string
	if json.Unmarshal(raw, &values) != nil {
		return nil
	}
	out := make([]int64, 0, len(values))
	for _, v := range values {
		if id, e := strconv.ParseInt(v, 10, 64); e == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out
}
func groupAllowed(raw json.RawMessage, id int64) bool {
	groups := allowedGroups(raw)
	if len(groups) == 0 {
		return true
	}
	for _, v := range groups {
		if v == id {
			return true
		}
	}
	return false
}
func mergeSnapshot(raw json.RawMessage, ip string) json.RawMessage {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		value = map[string]any{}
	}
	if strings.TrimSpace(ip) != "" {
		value["primaryIp"] = ip
	}
	out, _ := json.Marshal(value)
	return out
}
func normalizePage(page, size int) (int, int) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	return page, size
}
func (s *Service) newNo(prefix string) string {
	return prefix + s.nowFormat() + fmt.Sprintf("%06d", snowflake.GenID()%1000000)
}
func (s *Service) nowFormat() string { return s.now().Format("20060102150405") }

func parsePositiveMoney(v string) (int64, error) {
	n, e := parseMoney(v)
	if e != nil || n <= 0 {
		return 0, errors.New("金额必须大于0")
	}
	return n, nil
}
func parseUnsignedMoney(v string) (int64, error) {
	if strings.TrimSpace(v) == "" {
		return 0, nil
	}
	n, e := parseMoney(v)
	if e != nil || n < 0 {
		return 0, errors.New("金额无效")
	}
	return n, nil
}
func parsePercent(v string) (int64, error) {
	n, e := parseMoney(v)
	if e != nil || n < 0 || n > 10000 {
		return 0, errors.New("比例无效")
	}
	return n, nil
}
func parseMoney(v string) (int64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, errors.New("金额为空")
	}
	if strings.HasPrefix(v, "-") {
		return 0, errors.New("金额不能为负")
	}
	parts := strings.Split(v, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, errors.New("金额格式错误")
	}
	whole, e := strconv.ParseInt(parts[0], 10, 64)
	if e != nil {
		return 0, e
	}
	frac := "00"
	if len(parts) == 2 {
		if len(parts[1]) > 2 {
			return 0, errors.New("金额最多两位小数")
		}
		frac = parts[1] + strings.Repeat("0", 2-len(parts[1]))
	}
	f, e := strconv.ParseInt(frac, 10, 64)
	if e != nil {
		return 0, e
	}
	if whole > 99999999 {
		return 0, errors.New("金额超出范围")
	}
	return whole*100 + f, nil
}
func formatMoney(cents int64) string { return fmt.Sprintf("%d.%02d", cents/100, cents%100) }
func calculateFee(amount int64, settings *Settings) (fee, buyerPay, sellerIncome int64, err error) {
	if settings.FeeType == "fixed" {
		fee, err = parseUnsignedMoney(settings.FeeFixed)
	} else {
		var rate int64
		rate, err = parsePercent(settings.FeeRate)
		fee = amount * rate / 10000
	}
	if err != nil {
		return
	}
	if fee > amount && settings.FeeBearer == "seller" {
		err = errors.New("手续费不能高于交易金额")
		return
	}
	buyerPay, sellerIncome = amount, amount
	switch settings.FeeBearer {
	case "buyer":
		buyerPay += fee
	case "seller":
		sellerIncome -= fee
	case "platform":
	}
	return
}
