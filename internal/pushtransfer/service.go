// Package pushtransfer is the only writer of an existing instance's ownership.
package pushtransfer

import (
	"context"
	"ddyidc/internal/shared/productionlock"
	schemamode "ddyidc/internal/shared/schema"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"ddyidc/internal/certification"
	"ddyidc/internal/remoteconsole"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/auth"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

const Table = "system.push_transfers"

//go:embed schema.sql
var schema string
var ErrDenied = errors.New("当前用户无权操作该转让")
var ErrBusy = errors.New("实例正在转让或交易中")
var ErrState = errors.New("实例归属或转让状态已变化，请刷新后重试")

type Record struct {
	ID             int64     `json:"id,string" gorm:"primaryKey;autoIncrement:false"`
	InstanceID     int64     `json:"instanceId,string"`
	FromCustomerID int64     `json:"fromCustomerId,string"`
	ToCustomerID   int64     `json:"toCustomerId,string"`
	SourceOrderID  int64     `json:"sourceOrderId,string"`
	MarketOrderID  int64     `json:"marketOrderId,string"`
	RequestKey     string    `json:"-"`
	Source         string    `json:"source"`
	Status         string    `json:"status"`
	OperatorIP     string    `json:"operatorIp"`
	ReceiverIP     string    `json:"receiverIp"`
	RiskFlag       string    `json:"riskFlag"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

func (Record) TableName() string { return Table }

type Host struct {
	ID, CustomerID, TenantID, OrderID, OwnershipVersion int64
	Status                                              uint8
}
type Service struct {
	db    *gorm.DB
	redis redis.UniversalClient
}

func New(db *gorm.DB, r redis.UniversalClient) *Service { return &Service{db: db, redis: r} }

func EnsureSchema(db *gorm.DB) error {
	if !schemamode.MigrationsEnabled() {
		return nil
	}
	if err := db.Exec(schema).Error; err != nil {
		return err
	}
	var n int64
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='instance' AND table_name='instances' AND column_name='ownership_version'").Scan(&n).Error; err != nil {
		return err
	}
	if n == 0 {
		if err := db.Exec("ALTER TABLE instance.instances ADD COLUMN ownership_version BIGINT NOT NULL DEFAULT 0").Error; err != nil {
			return err
		}
	}
	return db.Exec("INSERT IGNORE INTO system.system_configs (id,`key`,value,description) VALUES (?,'push_transfer.enabled','true','免费实例转让开关')", snowflake.GenID()).Error
}
func Enabled(db *gorm.DB, key string) (bool, error) {
	var row struct{ Value string }
	err := db.Table("system.system_configs").Select("value").Where("`key`=?", key).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return row.Value == "true" || row.Value == "1", err
}
func LockHost(tx *gorm.DB, id int64) (*Host, error) {
	var h Host
	err := tx.Table("instance.instances").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id,customer_id,tenant_id,order_id,status,ownership_version").Where("id=? AND deleted_at IS NULL", id).Take(&h).Error
	if err != nil {
		return nil, err
	}
	if h.Status == 6 {
		return nil, ErrState
	}
	return &h, nil
}

// AvailableTx requires the instance row lock. Every reservation writer takes it.
func AvailableTx(tx *gorm.DB, instanceID, exceptTransfer, exceptListing int64) error {
	if err := productionlock.Check(tx, instanceID); err != nil {
		return err
	}
	var upgradeCount int64
	if err := tx.Table("instance.instance_upgrades").Where("instance_id=? AND state NOT IN ('completed','cancelled') AND NOT (state='unpaid' AND order_id IN (SELECT id FROM `order`.orders WHERE status=3)) AND NOT (state='quoted' AND expires_at<NOW(3))", instanceID).Count(&upgradeCount).Error; err != nil {
		return err
	}
	if upgradeCount > 0 {
		return errors.New("实例存在未结束的升降配订单，暂不能转让或挂牌")
	}
	var refundCount int64
	if err := tx.Table("payment.machine_refund_legs l").Joins("JOIN payment.machine_refund_actions a ON a.application_id=l.application_id").Where("l.instance_id=? AND (a.state<>'rejected' OR l.state NOT IN ('prepared','rejected'))", instanceID).Count(&refundCount).Error; err != nil {
		return err
	}
	if refundCount > 0 {
		return errors.New("实例存在人工退款处理，暂不能转让或挂牌")
	}

	{
		var count int64
		if err := tx.Table("instance.instance_renewals r").Joins("JOIN `order`.orders o ON o.id=r.order_id").Where("r.instance_id=? AND r.state NOT IN ('completed','cancelled') AND o.status IN (1,2)", instanceID).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return errors.New("实例存在未结束的续费订单，暂不能转让或挂牌")
		}
	}

	var pending []Record
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("instance_id=? AND status='pending' AND id<>?", instanceID, exceptTransfer).Find(&pending).Error; err != nil {
		return err
	}
	if len(pending) > 0 {
		return ErrBusy
	}
	var rows []struct{ ID int64 }
	if err := tx.Table("system.marketplace_listings").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("product_id=? AND status IN ('pending','on_sale','locked') AND id<>?", instanceID, exceptListing).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) > 0 {
		return ErrBusy
	}
	return nil
}
func parties(tx *gorm.DB, h *Host, from, to int64) error {
	if from <= 0 || to <= 0 || from == to || h.CustomerID != from {
		return ErrState
	}
	var customers []struct{ ID, TenantID int64 }
	if err := tx.Table("customer.customers").Select("id,tenant_id").Where("id IN ? AND status=1 AND deleted_at IS NULL", []int64{from, to}).Find(&customers).Error; err != nil {
		return err
	}
	if len(customers) != 2 {
		return ErrDenied
	}
	for _, c := range customers {
		if c.TenantID != h.TenantID {
			return ErrDenied
		}
		if err := certification.RequireApproved(tx, c.ID); err != nil {
			return errors.New("转让双方均须完成实名认证")
		}
	}
	return nil
}
func ValidatePartiesTx(tx *gorm.DB, instanceID, from, to int64) error {
	h, err := LockHost(tx, instanceID)
	if err != nil {
		return err
	}
	return parties(tx, h, from, to)
}
func risk(tx *gorm.DB, h *Host, from, to int64) (string, error) {
	var n int64
	err := tx.Model(&Record{}).Where("status='completed' AND created_at>? AND (instance_id=? OR (from_customer_id=? AND to_customer_id=?))", time.Now().Add(-24*time.Hour), h.ID, to, from).Count(&n).Error
	if n > 0 {
		return "rapid_resale_or_reciprocal_transfer", err
	}
	return "", err
}

type Change struct {
	InstanceID, From, To, MarketOrderID, ListingID, TransferID int64
	Source, Key, IP                                            string
}

// ChangeOwnerTx must be called inside the business transaction, including refunds.
// Original financial orders remain with their payer; SourceOrderID is the immutable transfer mark.
func ChangeOwnerTx(tx *gorm.DB, in Change) (*Record, error) {
	if in.Key == "" || in.IP == "" || (in.Source != "direct" && in.Source != "marketplace" && in.Source != "marketplace_refund") {
		return nil, ErrState
	}
	h, err := LockHost(tx, in.InstanceID)
	if err != nil {
		return nil, err
	}
	var existing Record
	err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_key=?", in.Key).Take(&existing).Error
	if err == nil && existing.Status == "completed" {
		if existing.InstanceID != in.InstanceID || existing.FromCustomerID != in.From || existing.ToCustomerID != in.To {
			return nil, ErrState
		}
		return &existing, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if err = parties(tx, h, in.From, in.To); err != nil {
		return nil, err
	}
	if err = AvailableTx(tx, h.ID, in.TransferID, in.ListingID); err != nil {
		return nil, err
	}
	flag, err := risk(tx, h, in.From, in.To)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	row := Record{ID: snowflake.GenID(), InstanceID: h.ID, FromCustomerID: in.From, ToCustomerID: in.To, SourceOrderID: h.OrderID, MarketOrderID: in.MarketOrderID, RequestKey: in.Key, Source: in.Source, Status: "completed", OperatorIP: in.IP, RiskFlag: flag, CreatedAt: now, UpdatedAt: now}
	if in.TransferID > 0 {
		if existing.ID != in.TransferID || existing.Status != "pending" || existing.ToCustomerID != in.To || existing.FromCustomerID != in.From {
			return nil, ErrState
		}
		row = existing
		row.Status = "completed"
		row.ReceiverIP = in.IP
		row.RiskFlag = flag
		row.UpdatedAt = now
		if err = tx.Save(&row).Error; err != nil {
			return nil, err
		}
	} else if err = tx.Create(&row).Error; err != nil {
		return nil, err
	}
	result := tx.Table("instance.instances").Where("id=? AND customer_id=? AND ownership_version=?", h.ID, in.From, h.OwnershipVersion).Updates(map[string]any{"customer_id": in.To, "ownership_version": h.OwnershipVersion + 1, "updated_at": now})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, ErrState
	}
	if err = tx.Exec("INSERT INTO instance.instance_events (id,instance_id,event_type,from_status,to_status,operator_type,operator_id,status,upstream_request,upstream_response,error_msg,created_at,processed_at,updated_at) VALUES (?,?,'push_transfer',?,?,1,?,2,JSON_OBJECT('transferId',?,'marketOrderId',?,'sourceOrderId',?),JSON_OBJECT('localOwnershipChanged',true),'',?,?,?)", snowflake.GenID(), h.ID, h.Status, h.Status, in.To, row.ID, in.MarketOrderID, h.OrderID, now, now, now).Error; err != nil {
		return nil, err
	}
	// Conservative revocation is safe even if the surrounding transaction later rolls back.
	remoteconsole.RevokeInstance(h.ID)
	return &row, nil
}
func (s *Service) Initiate(ctx context.Context, from, instanceID int64, account, password, ip string) (*Record, error) {
	enabled, err := Enabled(s.db.WithContext(ctx), "push_transfer.enabled")
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, errors.New("功能未开放")
	}
	if len(account) > 254 || strings.TrimSpace(account) == "" || len(password) > 256 || password == "" {
		return nil, errors.New("请填写接收账号与登录密码")
	}
	if s.redis == nil {
		return nil, errors.New("安全频控暂不可用，请稍后重试")
	}
	for _, key := range []string{fmt.Sprintf("push:auth:user:%d", from), "push:auth:ip:" + ip} {
		n, e := s.redis.Eval(ctx, "local n=redis.call('INCR',KEYS[1]); if n==1 then redis.call('EXPIRE',KEYS[1],60) end; return n", []string{key}).Int()
		if e != nil {
			return nil, errors.New("安全频控暂不可用")
		}
		if n > 5 {
			return nil, errors.New("请求过于频繁，请一分钟后重试")
		}
	}
	var user struct{ PasswordHash string }
	// Never include hashes or submitted passwords in ORM logs.
	if err = s.db.WithContext(ctx).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Table("customer.customers").Select("password_hash").Where("id=? AND status=1 AND deleted_at IS NULL", from).Take(&user).Error; err != nil || !auth.PasswordMatches(user.PasswordHash, password) {
		return nil, errors.New("登录密码不正确")
	}
	var targets []struct{ ID int64 }
	if err = s.db.WithContext(ctx).Table("customer.customers").Select("id").Where("(username=? OR email=? OR phone=?) AND status=1 AND deleted_at IS NULL", strings.TrimSpace(account), strings.TrimSpace(account), strings.TrimSpace(account)).Limit(2).Find(&targets).Error; err != nil {
		return nil, err
	}
	if len(targets) != 1 {
		return nil, errors.New("接收账号不存在或无法唯一识别")
	}
	var row Record
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		h, e := LockHost(tx, instanceID)
		if e != nil {
			return e
		}
		if e = parties(tx, h, from, targets[0].ID); e != nil {
			return e
		}
		if e = AvailableTx(tx, instanceID, 0, 0); e != nil {
			return e
		}
		flag, e := risk(tx, h, from, targets[0].ID)
		if e != nil {
			return e
		}
		id := snowflake.GenID()
		row = Record{ID: id, InstanceID: instanceID, FromCustomerID: from, ToCustomerID: targets[0].ID, SourceOrderID: h.OrderID, RequestKey: fmt.Sprintf("direct:%d", id), Source: "direct", Status: "pending", OperatorIP: ip, RiskFlag: flag, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		return tx.Create(&row).Error
	})
	return &row, err
}
func (s *Service) Decide(ctx context.Context, user, id int64, action, ip string) (*Record, error) {
	var row Record
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id=?", id).Take(&row).Error; err != nil {
			return err
		}
		if row.Source != "direct" {
			return ErrDenied
		}
		if action == "cancel" {
			if row.FromCustomerID != user {
				return ErrDenied
			}
		} else if row.ToCustomerID != user {
			return ErrDenied
		}
		var host Host
		if err := tx.Table("instance.instances").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id=?", row.InstanceID).Take(&host).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&row).Error; err != nil {
			return err
		}
		if action == "accept" && row.Status == "completed" {
			return nil
		}
		if row.Status != "pending" {
			return ErrState
		}
		if action == "accept" {
			changed, err := ChangeOwnerTx(tx, Change{InstanceID: row.InstanceID, From: row.FromCustomerID, To: user, TransferID: row.ID, Source: "direct", Key: row.RequestKey, IP: ip})
			if err == nil {
				row = *changed
			}
			return err
		}
		if action != "reject" && action != "cancel" {
			return ErrState
		}
		row.Status = map[string]string{"reject": "rejected", "cancel": "cancelled"}[action]
		row.ReceiverIP = ip
		row.UpdatedAt = time.Now()
		return tx.Save(&row).Error
	})
	return &row, err
}
func (s *Service) List(ctx context.Context, user int64, admin bool, page int) ([]Record, int64, error) {
	if page < 1 {
		page = 1
	}
	q := s.db.WithContext(ctx).Model(&Record{})
	if !admin {
		q = q.Where("from_customer_id=? OR to_customer_id=?", user, user)
	}
	var rows []Record
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := q.Order("created_at DESC,id DESC").Offset((page - 1) * 30).Limit(30).Find(&rows).Error
	return rows, total, err
}
