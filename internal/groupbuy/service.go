package groupbuy

import (
	"context"
	"crypto/rand"
	balance "ddyidc/internal/billing/balance"
	"ddyidc/internal/certification"
	"ddyidc/internal/shared/distsync"
	schemamode "ddyidc/internal/shared/schema"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	orderservice "ddyidc/internal/order/service"
	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrNotFound = errors.New("拼团活动或记录不存在")
	ErrClosed   = errors.New("拼团活动已结束或停用")
)

//go:embed schema.sql
var schemaSQL string

type OrderService interface {
	CreateOrder(context.Context, int64, *orderservice.CreateOrderInput) (*orderservice.OrderView, error)
	ProvisionAfterPayment(context.Context, int64, int64, string) error
}

type Notifier interface {
	NotifyCustomer(context.Context, string, int64, string, map[string]string) error
}

type Service struct {
	db         *gorm.DB
	orders     OrderService
	notifier   Notifier
	now        func() time.Time
	completion sync.Map
}

func NewService(database *gorm.DB, orders OrderService, notifier Notifier) *Service {
	return &Service{db: database, orders: orders, notifier: notifier, now: time.Now}
}

func (s *Service) EnsureSchema(ctx context.Context) error {
	if !schemamode.MigrationsEnabled() {
		return nil
	}
	if err := s.db.WithContext(ctx).Exec(schemaSQL).Error; err != nil {
		return fmt.Errorf("初始化拼团表失败: %w", err)
	}
	if err := s.db.WithContext(ctx).Exec("INSERT IGNORE INTO system.system_configs (id, `key`, value, description) VALUES (?, 'groupbuy.enabled', '0', '拼团模块总开关')", snowflake.GenID()).Error; err != nil {
		return err
	}
	var count int64
	if err := s.db.WithContext(ctx).Table(settingsTable).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		if err := s.db.WithContext(ctx).Table(settingsTable).Create(&Settings{ID: snowflake.GenID(), TimeoutRemindHours: 24, ShowMemberName: true, NewUserGroupLimit: 1}).Error; err != nil {
			return err
		}
	}
	return s.ensureNotificationTemplates(ctx)
}

func (s *Service) ensureNotificationTemplates(ctx context.Context) error {
	rows := []struct{ scene, channel, title, content string }{
		{"groupbuy_success", "both", "拼团成功", "您参与的 {{product_name}} 拼团已成功，订单 {{order_no}} 已进入开通流程。"},
		{"groupbuy_refund", "both", "拼团退款", "{{product_name}} 拼团未能完成，{{amount}} 已退回余额。"},
		{"groupbuy_remind", "email", "拼团即将超时", "您发起的 {{product_name}} 拼团还差 {{remaining}} 人，将于 {{timeout_at}} 超时。"},
	}
	for _, row := range rows {
		if err := s.db.WithContext(ctx).Exec("INSERT IGNORE INTO system.notification_templates (id, scene, channel, title, content, enabled) VALUES (?, ?, ?, ?, ?, 1)", snowflake.GenID(), row.scene, row.channel, row.title, row.content).Error; err != nil {
			return err
		}
		if err := s.db.WithContext(ctx).Exec("INSERT IGNORE INTO system.system_configs (id, `key`, value, description) VALUES (?, ?, '', '拼团短信模板编码')", snowflake.GenID(), "notify.sms.templates."+row.scene).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Enabled(ctx context.Context) (bool, error) {
	var row struct{ Value string }
	err := s.db.WithContext(ctx).Table("system.system_configs").Select("value").Where("`key` = 'groupbuy.enabled'").Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return row.Value == "1" || strings.EqualFold(row.Value, "true"), err
}

func (s *Service) validateActivity(ctx context.Context, activity *Activity) error {
	if activity.ProductID <= 0 {
		return errors.New("请选择商品")
	}
	if activity.PriceMode != 1 && activity.PriceMode != 2 {
		return errors.New("价格模式无效")
	}
	if activity.TimeoutDays < 1 || activity.TimeoutDays > 30 {
		return errors.New("拼团超时需为1-30天")
	}
	if activity.EndTime.Before(s.now()) {
		return errors.New("活动截止时间必须晚于当前时间")
	}

	cycleNames := map[string]string{"monthly": "月付", "quarterly": "季付", "semiannually": "半年付", "annually": "年付"}
	if cycleNames[activity.BillingCycle] == "" {
		return errors.New("计费周期仅支持月/季/半年/年")
	}
	activity.BillingCycleName = cycleNames[activity.BillingCycle]
	if len(activity.ConfigOptions) == 0 || !json.Valid(activity.ConfigOptions) {
		return errors.New("配置快照不是有效JSON")
	}
	var product struct {
		ID, GroupID int64
		Name        string
		Price       float64
		GroupName   string
	}
	err := s.db.WithContext(ctx).Table("product.products AS p").Select("p.id,p.group_id,p.name,COALESCE(up.price,0) AS price,COALESCE(g.name,'') AS group_name").Joins("LEFT JOIN product.upstream_products up ON up.product_id=p.id").Joins("LEFT JOIN product.product_groups g ON g.id=p.group_id").Where("p.id=? AND p.status=1 AND p.hidden=0", activity.ProductID).Take(&product).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errors.New("商品不存在或已下架")
	}
	if err != nil {
		return err
	}
	activity.ProductName, activity.GroupID, activity.GroupName = product.Name, product.GroupID, product.GroupName
	if activity.OriginalPrice <= 0 {
		activity.OriginalPrice = int64(product.Price*100 + .5)
	}
	if activity.OriginalPrice <= 0 {
		return errors.New("商品原价异常")
	}
	for _, size := range []string{"two", "three", "five"} {
		memberPrice, err := activityPrice(activity, size, false)
		if err != nil {
			return err
		}
		leaderPrice, err := activityPrice(activity, size, true)
		if err != nil {
			return err
		}
		if leaderPrice > memberPrice {
			return fmt.Errorf("%s团长价格不能高于团员价格", size)
		}
	}
	if activity.RenewType != 1 && activity.RenewType != 2 {
		return errors.New("续费方式无效")
	}
	if activity.MemberRestrictMode > 2 {
		return errors.New("人员限制模式无效")
	}
	return nil
}

func activityPrice(a *Activity, groupType string, leader bool) (int64, error) {
	var price, leaderPrice *int64
	var discount, leaderDiscount *int
	switch groupType {
	case "two":
		price, discount, leaderPrice, leaderDiscount = a.TwoPrice, a.TwoDiscount, a.TwoLeaderPrice, a.TwoLeaderDiscount
	case "three":
		price, discount, leaderPrice, leaderDiscount = a.ThreePrice, a.ThreeDiscount, a.ThreeLeaderPrice, a.ThreeLeaderDiscount
	case "five":
		price, discount, leaderPrice, leaderDiscount = a.FivePrice, a.FiveDiscount, a.FiveLeaderPrice, a.FiveLeaderDiscount
	default:
		return 0, errors.New("团型仅支持2/3/5人团")
	}
	if leader && (leaderPrice != nil || leaderDiscount != nil) {
		price, discount = leaderPrice, leaderDiscount
	}
	if a.PriceMode == 1 {
		if price == nil || *price <= 0 {
			return 0, fmt.Errorf("%s价格未配置", groupType)
		}
		return *price, nil
	}
	if discount == nil || *discount < 0 || *discount >= 100 {
		return 0, fmt.Errorf("%s折扣需为0-99", groupType)
	}
	value := a.OriginalPrice * int64(100-*discount) / 100
	if value <= 0 {
		return 0, errors.New("拼团价格异常")
	}
	return value, nil
}

func (s *Service) ListActivities(ctx context.Context, keyword string, status *uint8, page, pageSize int, publicOnly bool) ([]ActivityView, int64, error) {
	page, pageSize = normalizePage(page, pageSize)
	q := s.db.WithContext(ctx).Table(activityTable + " AS a")
	if publicOnly {
		q = q.Where("a.status=1 AND a.end_time>?", s.now())
	}
	if status != nil {
		q = q.Where("a.status=?", *status)
	}
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		q = q.Where("a.product_name LIKE ? OR a.slogan LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []ActivityView
	err := q.Select("a.*, (SELECT COUNT(*) FROM system.groupbuy_records r WHERE r.activity_id=a.id AND r.is_leader=1 AND r.status=1) AS active_groups, (SELECT COUNT(*) FROM system.groupbuy_records r WHERE r.activity_id=a.id AND r.status IN (1,2)) AS joined_members").Order("a.sort ASC,a.id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Scan(&rows).Error
	return rows, total, err
}

func (s *Service) Activity(ctx context.Context, id int64, publicOnly bool) (*ActivityView, error) {
	var row ActivityView
	q := s.db.WithContext(ctx).Table(activityTable+" AS a").Select("a.*, (SELECT COUNT(*) FROM system.groupbuy_records r WHERE r.activity_id=a.id AND r.is_leader=1 AND r.status=1) AS active_groups, (SELECT COUNT(*) FROM system.groupbuy_records r WHERE r.activity_id=a.id AND r.status IN (1,2)) AS joined_members").Where("a.id=?", id)
	if publicOnly {
		q = q.Where("a.status=1 AND a.end_time>?", s.now())
	}
	err := q.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &row, err
}

func (s *Service) SaveActivity(ctx context.Context, activity *Activity) error {
	if err := s.validateActivity(ctx, activity); err != nil {
		return err
	}
	if activity.ID == 0 {
		return s.db.WithContext(ctx).Table(activityTable).Create(activity).Error
	}
	result := s.db.WithContext(ctx).Table(activityTable).Where("id=?", activity.ID).Select("*").Omit("id", "created_at").Updates(activity)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) DeleteActivity(ctx context.Context, id int64) error {
	var count int64
	if err := s.db.WithContext(ctx).Table(recordTable).Where("activity_id=?", id).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return errors.New("活动已有拼团记录，不能删除，可改为停用")
	}
	result := s.db.WithContext(ctx).Table(activityTable).Where("id=?", id).Delete(&Activity{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) SetActivityStatus(ctx context.Context, id int64, status uint8) error {
	if status > 1 {
		return errors.New("活动状态无效")
	}
	result := s.db.WithContext(ctx).Table(activityTable).Where("id=?", id).Update("status", status)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) SortActivities(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return errors.New("排序列表不能为空")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for index, id := range ids {
			if id <= 0 {
				return errors.New("活动ID无效")
			}
			if result := tx.Table(activityTable).Where("id=?", id).Update("sort", index); result.Error != nil || result.RowsAffected != 1 {
				if result.Error != nil {
					return result.Error
				}
				return ErrNotFound
			}
		}
		return nil
	})
}

type StartInput struct {
	ActivityID        int64  `json:"-"`
	GroupType         string `json:"groupType"`
	IsPublic          bool   `json:"isPublic"`
	RecommendIdentify string `json:"recommendIdentify"`
	RecommendClientID *int64 `json:"recommendClientId,string"`
}

func (s *Service) StartGroup(ctx context.Context, clientID int64, input StartInput) (*Record, error) {
	var created Record
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockCustomer(tx, clientID); err != nil {
			return err
		}
		activity, err := lockedActivity(tx, input.ActivityID, s.now())
		if err != nil {
			return err
		}
		if err := s.validateMember(tx, activity, clientID, true); err != nil {
			return err
		}
		price, discount, err := s.memberPrice(tx, activity, clientID, input.GroupType, true)
		if err != nil {
			return err
		}
		id := snowflake.GenID()
		invite, err := newInviteCode()
		if err != nil {
			return err
		}
		created = Record{ID: id, ActivityID: activity.ID, ClientID: clientID, GroupType: input.GroupType, IsLeader: true, LeaderID: id, CurrentNum: 1, NeedNum: groupSize(input.GroupType), PayAmount: price - discount, GroupPrice: price, LevelDiscountAmount: discount, InviteCode: invite, IsPublic: input.IsPublic && activity.IsPublicGroupEnabled, Status: 1, TimeoutTime: minTime(s.now().AddDate(0, 0, activity.TimeoutDays), activity.EndTime), RecommendIdentify: strings.TrimSpace(input.RecommendIdentify), RecommendClientID: input.RecommendClientID}
		if activity.ForcePublic {
			created.IsPublic = true
		}
		if created.NeedNum == 0 {
			return errors.New("团型仅支持2/3/5人团")
		}
		if err := tx.Table(recordTable).Create(&created).Error; err != nil {
			return err
		}
		return s.changeBalance(tx, clientID, -created.PayAmount, "groupbuy", id, "发起拼团扣款")
	})
	if err != nil {
		return nil, err
	}
	return &created, nil
}

func (s *Service) JoinGroup(ctx context.Context, clientID, leaderID int64, recommend string, recommendID *int64) (*Record, error) {
	var created Record
	full := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var leader Record
		if err := tx.Table(recordTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND is_leader=1", leaderID).Take(&leader).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if leader.Status != 1 || !leader.TimeoutTime.After(s.now()) || leader.CurrentNum >= leader.NeedNum {
			return errors.New("该拼团已满员、结束或超时")
		}
		activity, err := lockedActivity(tx, leader.ActivityID, s.now())
		if err != nil {
			return err
		}
		if err := s.validateMember(tx, activity, clientID, false); err != nil {
			return err
		}
		var duplicate int64
		if err := tx.Table(recordTable).Where("leader_id=? AND client_id=? AND status IN (1,2)", leaderID, clientID).Count(&duplicate).Error; err != nil {
			return err
		}
		if duplicate > 0 {
			return errors.New("您已参加该拼团")
		}
		if err := lockCustomer(tx, clientID); err != nil {
			return err
		}
		price, discount, err := s.memberPrice(tx, activity, clientID, leader.GroupType, false)
		if err != nil {
			return err
		}
		invite, err := newInviteCode()
		if err != nil {
			return err
		}
		created = Record{ID: snowflake.GenID(), ActivityID: activity.ID, ClientID: clientID, GroupType: leader.GroupType, LeaderID: leader.ID, CurrentNum: 0, NeedNum: leader.NeedNum, PayAmount: price - discount, GroupPrice: price, LevelDiscountAmount: discount, InviteCode: invite, Status: 1, TimeoutTime: leader.TimeoutTime, RecommendIdentify: strings.TrimSpace(recommend), RecommendClientID: recommendID}
		if err := tx.Table(recordTable).Create(&created).Error; err != nil {
			return err
		}
		if err := s.changeBalance(tx, clientID, -created.PayAmount, "groupbuy", created.ID, "参与拼团扣款"); err != nil {
			return err
		}
		leader.CurrentNum++
		full = leader.CurrentNum >= leader.NeedNum
		return tx.Table(recordTable).Where("id=?", leader.ID).Update("current_num", leader.CurrentNum).Error
	})
	if err != nil {
		return nil, err
	}
	if full {
		if err := s.completeGroup(ctx, leaderID); err != nil {
			return nil, err
		}
	}
	return &created, nil
}

func lockedActivity(tx *gorm.DB, id int64, now time.Time) (*Activity, error) {
	var activity Activity
	err := tx.Table(activityTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&activity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if activity.Status != 1 || !activity.EndTime.After(now) {
		return nil, ErrClosed
	}
	return &activity, nil
}

func lockCustomer(tx *gorm.DB, id int64) error {
	var row struct{ ID int64 }
	err := tx.Table("customer.customers").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id=? AND status=1 AND deleted_at IS NULL", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errors.New("客户不存在或已停用")
	}
	return err
}

func (s *Service) validateMember(tx *gorm.DB, activity *Activity, clientID int64, leader bool) error {
	if activity.RequireCertification {
		if err := certification.RequireApproved(tx, clientID); err != nil {
			return err
		}
	}
	if activity.PurchaseLimit > 0 {
		var n int64
		if err := tx.Table(recordTable).Where("activity_id=? AND client_id=? AND status IN (1,2)", activity.ID, clientID).Count(&n).Error; err != nil {
			return err
		}
		if n >= int64(activity.PurchaseLimit) {
			return errors.New("已达到该活动限购数量")
		}
	}
	var orders int64
	if err := tx.Table("order.orders").Where("customer_id=? AND status IN (2,6)", clientID).Count(&orders).Error; err != nil {
		return err
	}
	isNew := orders == 0
	if activity.HideForOldUser && !isNew {
		return errors.New("该活动仅对新用户开放")
	}
	if activity.MemberRestrictMode == 1 && !isNew {
		return errors.New("纯新人团仅限新用户")
	}
	if activity.MemberRestrictMode == 2 && ((leader && isNew) || (!leader && !isNew)) {
		return errors.New("老带新拼团要求团长为老用户、团员为新用户")
	}
	settings, err := s.Settings(tx.Statement.Context)
	if err != nil {
		return err
	}
	if isNew && settings.NewUserGroupLimit > 0 {
		var n int64
		if err := tx.Table(recordTable).Where("client_id=? AND status IN (1,2)", clientID).Count(&n).Error; err != nil {
			return err
		}
		if n >= int64(settings.NewUserGroupLimit) {
			return errors.New("已达到新人拼团次数限制")
		}
	}
	return nil
}

func (s *Service) memberPrice(tx *gorm.DB, activity *Activity, clientID int64, groupType string, leader bool) (int64, int64, error) {
	price, err := activityPrice(activity, groupType, leader)
	if err != nil {
		return 0, 0, err
	}
	settings, err := s.Settings(tx.Statement.Context)
	if err != nil {
		return 0, 0, err
	}
	if !settings.ClientLevelDiscountEnable {
		return price, 0, nil
	}
	var customer struct{ Level int }
	if err := tx.Table("customer.customers").Select("level").Where("id=?", clientID).Take(&customer).Error; err != nil {
		return 0, 0, err
	}
	var cfg struct{ Value string }
	_ = tx.Table("system.system_configs").Select("value").Where("`key`=?", fmt.Sprintf("groupbuy.level_discount.%d", customer.Level)).Take(&cfg).Error
	percent, _ := strconv.Atoi(cfg.Value)
	if percent < 0 || percent > 50 {
		percent = 0
	}
	discount := price * int64(percent) / 100
	return price, discount, nil
}

func (s *Service) changeBalance(tx *gorm.DB, clientID, delta int64, related string, relatedID int64, remark string) error {
	if delta == 0 {
		return nil
	}
	typ := "order_pay"
	if delta > 0 {
		typ = "refund"
	}
	_, err := balance.New(tx).Change(tx.Statement.Context, balance.ChangeRequest{
		CustomerID: clientID, Type: typ, Amount: balance.Money(delta).String(),
		RefType: related, RefID: relatedID, Channel: "groupbuy",
		RequestID: fmt.Sprintf("GB:%s:%d", typ, relatedID), Notes: remark,
	})
	return err
}
func (s *Service) completeGroup(ctx context.Context, leaderID int64) error {
	lockValue, _ := s.completion.LoadOrStore(leaderID, &sync.Mutex{})
	mu := lockValue.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	defer s.completion.Delete(leaderID)
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	lockName := fmt.Sprintf("ddyidc:groupbuy:%d", leaderID)
	var acquired int
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 10)", lockName).Scan(&acquired); err != nil {
		return err
	}
	if acquired != 1 {
		return errors.New("拼团正在结算，请稍后查看")
	}
	defer conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK(?)", lockName)
	detail, err := s.Group(ctx, leaderID, 0, true)
	if err != nil {
		return err
	}
	if detail.Leader.Status != 1 {
		return nil
	}
	if len(detail.Members) < detail.Leader.NeedNum {
		return nil
	}
	if err := s.reserveStock(ctx, detail.Activity.ProductID, len(detail.Members)); err != nil {
		_ = s.refundGroup(ctx, leaderID)
		return fmt.Errorf("库存不足，已整团退款: %w", err)
	}
	reserved := true
	releaseStock := func() {
		if reserved {
			_ = s.releaseStock(ctx, detail.Activity.ProductID, len(detail.Members))
			reserved = false
		}
	}
	created := make([]struct{ recordID, clientID, orderID int64 }, 0, len(detail.Members))
	for _, member := range detail.Members {
		input, err := orderInput(detail.Activity.ConfigOptions, detail.Activity, member.ID)
		if err != nil {
			releaseStock()
			_ = s.refundGroup(ctx, leaderID)
			return err
		}
		order, err := s.orders.CreateOrder(ctx, member.ClientID, input)
		if err != nil {
			releaseStock()
			_ = s.refundGroup(ctx, leaderID)
			return fmt.Errorf("成团创建订单失败，已整团退款: %w", err)
		}
		orderID, _ := strconv.ParseInt(order.ID, 10, 64)
		now := s.now()
		payType := uint8(3)
		if err = s.db.WithContext(ctx).Table("order.orders").Where("id=? AND status=1", orderID).Updates(map[string]any{"total_amount": member.GroupPrice, "discount_amount": member.LevelDiscountAmount, "pay_amount": member.PayAmount, "balance_amount": member.PayAmount, "status": 2, "pay_type": payType, "paid_at": now, "admin_remark": "groupbuy:" + strconv.FormatInt(member.ID, 10)}).Error; err != nil {
			releaseStock()
			_ = s.refundGroup(ctx, leaderID)
			return err
		}
		if err = s.db.WithContext(ctx).Exec("UPDATE `order`.order_items SET config_snapshot=JSON_SET(config_snapshot, '$.orderType', 'groupbuy', '$.groupbuyRecordId', ?) WHERE order_id=?", strconv.FormatInt(member.ID, 10), orderID).Error; err != nil {
			releaseStock()
			_ = s.refundGroup(ctx, leaderID)
			return err
		}
		created = append(created, struct{ recordID, clientID, orderID int64 }{member.ID, member.ClientID, orderID})
	}
	for _, item := range created {
		if err := s.orders.ProvisionAfterPayment(ctx, item.clientID, item.orderID, "balance"); err != nil {
			releaseStock()
			_ = s.markOrdersRefunded(ctx, created)
			_ = s.refundGroup(ctx, leaderID)
			return fmt.Errorf("成团开通失败，已整团退款: %w", err)
		}
	}
	now := s.now()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, item := range created {
			var orderItem struct{ InstanceID *int64 }
			_ = tx.Table("order.order_items").Select("instance_id").Where("order_id=?", item.orderID).Take(&orderItem).Error
			if err := tx.Table(recordTable).Where("id=? AND status=1", item.recordID).Updates(map[string]any{"status": 2, "order_id": item.orderID, "host_id": orderItem.InstanceID, "success_time": now}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		releaseStock()
		return err
	}
	reserved = false
	for _, m := range detail.Members {
		s.notify("groupbuy_success", m.ClientID, m.ID, map[string]string{"product_name": detail.Activity.ProductName, "order_no": "已生成"})
	}
	return nil
}

func (s *Service) reserveStock(ctx context.Context, productID int64, quantity int) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var product struct {
			Stock     *int
			SoldCount int
		}
		if err := tx.Table("product.products").Clauses(clause.Locking{Strength: "UPDATE"}).Select("stock,sold_count").Where("id=?", productID).Take(&product).Error; err != nil {
			return err
		}
		if product.Stock != nil && product.SoldCount+quantity > *product.Stock {
			return errors.New("剩余库存不足以完成整团开通")
		}
		return tx.Table("product.products").Where("id=?", productID).Update("sold_count", gorm.Expr("sold_count + ?", quantity)).Error
	})
}

func (s *Service) releaseStock(ctx context.Context, productID int64, quantity int) error {
	return s.db.WithContext(ctx).Table("product.products").Where("id=?", productID).Update("sold_count", gorm.Expr("GREATEST(sold_count - ?, 0)", quantity)).Error
}

func orderInput(raw json.RawMessage, activity Activity, recordID int64) (*orderservice.CreateOrderInput, error) {
	var input orderservice.CreateOrderInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, errors.New("活动配置快照无法下单")
	}
	input.ProductID = strconv.FormatInt(activity.ProductID, 10)
	input.Quantity = 1
	input.BillingCycle = activity.BillingCycle
	input.UserCouponID = ""
	if input.DurationMonth == 0 {
		input.DurationMonth = map[string]int{"monthly": 1, "quarterly": 3, "semiannually": 6, "annually": 12}[activity.BillingCycle]
	}
	if input.Hostname == "" {
		input.Hostname = fmt.Sprintf("gb-%d", recordID)
	}
	if input.Password == "" {
		input.Password = fmt.Sprintf("Aa!%09d", recordID%1_000_000_000)
	}
	return &input, nil
}

func (s *Service) markOrdersRefunded(ctx context.Context, items []struct{ recordID, clientID, orderID int64 }) error {
	ids := make([]int64, 0, len(items))
	for _, i := range items {
		ids = append(ids, i.orderID)
	}
	return s.db.WithContext(ctx).Table("order.orders").Where("id IN ?", ids).Updates(map[string]any{"status": 4, "cancel_reason": "拼团开通失败退款"}).Error
}

func (s *Service) refundGroup(ctx context.Context, leaderID int64) error {
	return s.refund(ctx, leaderID, true)
}
func (s *Service) Refund(ctx context.Context, recordID int64, whole bool) error {
	return s.refund(ctx, recordID, whole)
}
func (s *Service) refund(ctx context.Context, recordID int64, whole bool) error {
	var notices []Record
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var selected Record
		if err := tx.Table(recordTable).Where("id=?", recordID).Take(&selected).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		leaderID := selected.LeaderID
		var leader Record
		if err := tx.Table(recordTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", leaderID).Take(&leader).Error; err != nil {
			return err
		}
		if selected.Status != 1 {
			return errors.New("仅拼团中记录可退款")
		}
		q := tx.Table(recordTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status=1", selected.ID)
		if whole || selected.IsLeader {
			q = tx.Table(recordTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("leader_id=? AND status=1", leaderID)
		}
		if err := q.Find(&notices).Error; err != nil {
			return err
		}
		for _, r := range notices {
			if err := lockCustomer(tx, r.ClientID); err != nil {
				return err
			}
			if err := s.changeBalance(tx, r.ClientID, r.PayAmount, "groupbuy_refund", r.ID, "拼团退款"); err != nil {
				return err
			}
			if err := tx.Table(recordTable).Where("id=?", r.ID).Update("status", 3).Error; err != nil {
				return err
			}
		}
		if !(whole || selected.IsLeader) {
			return tx.Table(recordTable).Where("id=?", leaderID).Update("current_num", gorm.Expr("GREATEST(current_num-1,1)")).Error
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, r := range notices {
		s.notify("groupbuy_refund", r.ClientID, r.ID, map[string]string{"product_name": "拼团商品", "amount": fmt.Sprintf("¥%.2f", float64(r.PayAmount)/100)})
	}
	return nil
}

func (s *Service) Group(ctx context.Context, id, clientID int64, admin bool) (*GroupDetail, error) {
	var selected Record
	if err := s.db.WithContext(ctx).Table(recordTable).Where("id=?", id).Take(&selected).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	if !admin && selected.ClientID != clientID && selected.LeaderID != id {
		return nil, errors.New("没有查看该拼团的权限")
	}
	var leader Record
	if err := s.db.WithContext(ctx).Table(recordTable).Where("id=?", selected.LeaderID).Take(&leader).Error; err != nil {
		return nil, err
	}
	var activity Activity
	if err := s.db.WithContext(ctx).Table(activityTable).Where("id=?", leader.ActivityID).Take(&activity).Error; err != nil {
		return nil, err
	}
	var members []Record
	settings, _ := s.Settings(ctx)
	selects := "r.*"
	if settings.ShowMemberName {
		selects += ", COALESCE(c.nickname,c.username,'') AS member_name"
	}
	if settings.ShowMemberPhone {
		selects += ", COALESCE(c.phone,'') AS member_phone"
	}
	err := s.db.WithContext(ctx).Table(recordTable+" AS r").Select(selects).Joins("LEFT JOIN customer.customers c ON c.id=r.client_id").Where("r.leader_id=?", leader.ID).Order("r.is_leader DESC,r.created_at ASC").Scan(&members).Error
	return &GroupDetail{Leader: leader, Members: members, Activity: activity}, err
}

func (s *Service) MyGroups(ctx context.Context, clientID int64, status *uint8, page, pageSize int) ([]Record, int64, error) {
	page, pageSize = normalizePage(page, pageSize)
	q := s.db.WithContext(ctx).Table(recordTable+" AS r").Where("r.client_id=?", clientID)
	if status != nil {
		q = q.Where("r.status=?", *status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Record
	err := q.Preload("Activity").Order("r.created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error
	return rows, total, err
}

func (s *Service) PublicGroups(ctx context.Context, activityID *int64, page, pageSize int) ([]Record, int64, error) {
	page, pageSize = normalizePage(page, pageSize)
	q := s.db.WithContext(ctx).Table(recordTable+" AS r").Where("r.is_leader=1 AND r.status=1 AND r.is_public=1 AND r.force_close_public=0 AND r.timeout_time>?", s.now())
	if activityID != nil {
		q = q.Where("r.activity_id=?", *activityID)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Record
	err := q.Preload("Activity").Order("r.created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error
	return rows, total, err
}

func (s *Service) Share(ctx context.Context, code string) (*GroupDetail, error) {
	var row Record
	if err := s.db.WithContext(ctx).Table(recordTable).Where("invite_code=?", strings.TrimSpace(code)).Take(&row).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	return s.Group(ctx, row.ID, 0, true)
}

func (s *Service) TogglePublic(ctx context.Context, clientID, recordID int64, isPublic bool) error {
	var r Record
	if err := s.db.WithContext(ctx).Table(recordTable).Where("id=? AND client_id=?", recordID, clientID).Take(&r).Error; err != nil {
		return ErrNotFound
	}
	if !r.IsLeader || r.Status != 1 {
		return errors.New("仅拼团中的团长可修改公开状态")
	}
	var a Activity
	if err := s.db.WithContext(ctx).Table(activityTable).Where("id=?", r.ActivityID).Take(&a).Error; err != nil {
		return err
	}
	if a.ForcePublic && !isPublic {
		return errors.New("该活动强制公开")
	}
	if !a.IsPublicGroupEnabled && isPublic {
		return errors.New("该活动未开放公开拼团")
	}
	return s.db.WithContext(ctx).Table(recordTable).Where("id=?", recordID).Update("is_public", isPublic).Error
}

func (s *Service) ForceClosePublic(ctx context.Context, leaderID int64, closed bool) error {
	return s.db.WithContext(ctx).Table(recordTable).Where("id=? AND is_leader=1", leaderID).Update("force_close_public", closed).Error
}

func (s *Service) Settings(ctx context.Context) (*Settings, error) {
	var row Settings
	err := s.db.WithContext(ctx).Table(settingsTable).Order("id").Take(&row).Error
	return &row, err
}
func (s *Service) SaveSettings(ctx context.Context, in *Settings) error {
	if in.TimeoutRemindHours < 1 || in.TimeoutRemindHours > 168 {
		return errors.New("提醒时间需为1-168小时")
	}
	if in.NewUserGroupLimit < 0 || in.NewUserGroupLimit > 100 {
		return errors.New("新人限次无效")
	}
	current, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	in.ID = current.ID
	return s.db.WithContext(ctx).Table(settingsTable).Where("id=?", current.ID).Select("client_level_discount_enable", "timeout_remind_hours", "show_member_name", "show_member_phone", "new_user_group_limit").Updates(in).Error
}

func (s *Service) TabCounts(ctx context.Context, clientID int64) (map[string]int64, error) {
	out := map[string]int64{"pending": 0, "success": 0, "refunded": 0}
	var rows []struct {
		Status uint8
		Count  int64
	}
	if err := s.db.WithContext(ctx).Table(recordTable).Select("status,COUNT(*) AS count").Where("client_id=?", clientID).Group("status").Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.Status == 1 {
			out["pending"] = r.Count
		} else if r.Status == 2 {
			out["success"] = r.Count
		} else if r.Status == 3 {
			out["refunded"] = r.Count
		}
	}
	return out, nil
}

func (s *Service) ListLeaders(ctx context.Context, activityID *int64, status *uint8, keyword string, page, pageSize int) ([]Record, int64, error) {
	page, pageSize = normalizePage(page, pageSize)
	q := s.db.WithContext(ctx).Table(recordTable + " AS r").Joins("LEFT JOIN customer.customers c ON c.id=r.client_id").Where("r.is_leader=1")
	if activityID != nil {
		q = q.Where("r.activity_id=?", *activityID)
	}
	if status != nil {
		q = q.Where("r.status=?", *status)
	}
	if keyword != "" {
		q = q.Where("c.username LIKE ? OR r.invite_code LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Record
	err := q.Select("r.*,COALESCE(c.nickname,c.username,'') AS member_name").Preload("Activity").Order("r.created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error
	return rows, total, err
}

func (s *Service) RunMaintenance(ctx context.Context) error {
	enabled, err := s.Enabled(ctx)
	if err != nil || !enabled {
		return err
	}
	settings, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	var expired []Record
	if err = s.db.WithContext(ctx).Table(recordTable).Where("is_leader=1 AND status=1 AND timeout_time<=?", s.now()).Find(&expired).Error; err != nil {
		return err
	}
	for _, r := range expired {
		_ = s.refundGroup(ctx, r.ID)
	}
	deadline := s.now().Add(time.Duration(settings.TimeoutRemindHours) * time.Hour)
	var reminders []Record
	if err = s.db.WithContext(ctx).Table(recordTable).Where("is_leader=1 AND status=1 AND is_notice=0 AND timeout_time>? AND timeout_time<=?", s.now(), deadline).Find(&reminders).Error; err != nil {
		return err
	}
	for _, r := range reminders {
		var a Activity
		_ = s.db.WithContext(ctx).Table(activityTable).Where("id=?", r.ActivityID).Take(&a).Error
		s.notify("groupbuy_remind", r.ClientID, r.ID, map[string]string{"product_name": a.ProductName, "remaining": strconv.Itoa(r.NeedNum - r.CurrentNum), "timeout_at": r.TimeoutTime.Format("2006-01-02 15:04")})
		_ = s.db.WithContext(ctx).Table(recordTable).Where("id=?", r.ID).Update("is_notice", true).Error
	}
	return nil
}

func (s *Service) StartMaintenance(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				distsync.Task(ctx, "groupbuy_maintenance", time.Minute, s.RunMaintenance)
			}
		}
	}()
}

func (s *Service) notify(scene string, clientID, businessID int64, params map[string]string) {
	if s.notifier == nil {
		return
	}
	go func() {
		_ = s.notifier.NotifyCustomer(context.Background(), scene, clientID, strconv.FormatInt(businessID, 10), params)
	}()
}

func normalizePage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 10
	}
	return page, pageSize
}
func groupSize(v string) int { return map[string]int{"two": 2, "three": 3, "five": 5}[v] }
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func newInviteCode() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(b)), nil
}
