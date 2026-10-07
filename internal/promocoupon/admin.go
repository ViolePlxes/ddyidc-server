package promocoupon

import (
	"context"
	"database/sql"
	"ddyidc/internal/shared/snowflake"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func validateConfig(c Config) error {
	var ids []string
	if c.PointsPerYuan < 1 || c.PointsPerYuan > 1000000 || c.FaceMax < 100 || c.FaceMax > 100000 || c.BatchMax < 1 || c.BatchMax > 100 || c.DailyCount < 1 || c.DailyCount > 1000 || c.DailyBudget < 100 || c.DailyBudget > 5000000 || c.ValidDaysMax < 1 || c.ValidDaysMax > 365 || c.MaintenanceSeconds < 30 || c.MaintenanceSeconds > 3600 || json.Unmarshal(c.ProductIDs, &ids) != nil || len(ids) > 1000 {
		return errors.New("发券配置超出允许范围")
	}
	if c.Enabled && len(ids) == 0 {
		return errors.New("开启前请配置适用商品")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		var n int64
		if _, e := fmt.Sscan(id, &n); e != nil || n <= 0 || fmt.Sprint(n) != id || seen[id] {
			return errors.New("适用商品ID无效或重复")
		}
		seen[id] = true
	}
	return nil
}
func (s *Service) SaveConfig(ctx context.Context, a Actor, c Config) error {
	if a.Role != "admin" || a.ID <= 0 || a.Notes == "" {
		return ErrForbidden
	}
	if e := validateConfig(c); e != nil {
		return e
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before Config
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&before, "id=1").Error; e != nil {
			return e
		}
		c.ID = 1
		c.Version = before.Version + 1
		c.UpdatedAt = s.now()
		if e := tx.Save(&c).Error; e != nil {
			return e
		}
		return s.auditDetails(tx, a, "config_updated", fmt.Sprintf("config:%d", c.Version), before, c)

	})
}
func (s *Service) SaveTemplate(ctx context.Context, a Actor, t Template) error {
	if a.Role != "admin" || a.Notes == "" {
		return ErrForbidden
	}
	if t.Name == "" || len([]rune(t.Name)) > 100 || t.Face < 1 || t.Minimum <= t.Face || t.ValidDays < 1 || t.ValidDays > 365 {
		return errors.New("模板规则无效")
	}
	if t.ID == 0 {
		t.ID = snowflake.GenID()
	}
	t.UpdatedAt = s.now()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before Template
		e := tx.Take(&before, "id=?", t.ID).Error
		if e != nil && e != gorm.ErrRecordNotFound {
			return e
		}
		if e = tx.Save(&t).Error; e != nil {
			return e
		}
		return s.auditDetails(tx, a, "template_updated", fmt.Sprintf("template:%d:%d", t.ID, snowflake.GenID()), before, t)
	})
}
func (s *Service) Batches(ctx context.Context, user int64, admin bool, page int) ([]Batch, int64, error) {
	if page < 1 {
		page = 1
	}
	q := s.db.WithContext(ctx).Model(&Batch{})
	if !admin {
		q = q.Where("owner_id=?", user)
	}
	var total int64
	if e := q.Count(&total).Error; e != nil {
		return nil, 0, e
	}
	rows := []Batch{}
	e := q.Order("created_at DESC,id DESC").Limit(20).Offset((page - 1) * 20).Find(&rows).Error
	return rows, total, e
}
func (s *Service) Detail(ctx context.Context, user, id int64, admin bool) (map[string]any, error) {
	var b Batch
	q := s.db.WithContext(ctx)
	if !admin {
		q = q.Where("owner_id=?", user)
	}
	if e := q.Take(&b, "id=?", id).Error; e != nil {
		return nil, ErrForbidden
	}
	units := []Unit{}
	if e := s.db.WithContext(ctx).Where("batch_id=?", id).Order("id").Find(&units).Error; e != nil {
		return nil, e
	}
	events := []Event{}
	if e := s.db.WithContext(ctx).Where("batch_id=?", id).Order("created_at,id").Find(&events).Error; e != nil {
		return nil, e
	}
	// Administrative notes may contain personal data. Customer view exposes only
	// lifecycle and asset fields, never free-form operator notes/identity.
	if !admin {
		for i := range events {
			events[i].Notes = ""
			events[i].ActorID = 0
			events[i].Details = nil
		}
	}
	return map[string]any{"batch": b, "units": units, "events": events}, nil
}
func (s *Service) Customers(ctx context.Context, owner int64, page int, keyword string) ([]map[string]string, int64, error) {
	if page < 1 {
		page = 1
	}
	q := s.db.WithContext(ctx).Table("marketing.invite_relations r").Joins("JOIN customer.customers c ON c.id=r.invitee_id").Where("r.inviter_id=? AND r.level=1 AND r.status=1 AND c.status=1 AND c.deleted_at IS NULL", owner)
	if keyword != "" {
		q = q.Where("CAST(r.invitee_id AS CHAR) LIKE ?", "%"+keyword+"%")
	}
	var total int64
	if e := q.Count(&total).Error; e != nil {
		return nil, 0, e
	}
	var ids []int64
	if e := q.Order("r.created_at DESC,r.id DESC").Limit(20).Offset((page-1)*20).Pluck("r.invitee_id", &ids).Error; e != nil {
		return nil, 0, e
	}
	rows := []map[string]string{}
	for _, id := range ids {
		rows = append(rows, map[string]string{"userId": fmt.Sprint(id)})
	}
	return rows, total, nil
}
func (s *Service) Offers(ctx context.Context, user int64, page int) ([]Batch, int64, error) {
	c, e := s.Config(ctx)
	if e != nil {
		return nil, 0, e
	}
	if !c.Enabled || !c.OfferEnabled {
		return []Batch{}, 0, nil
	}
	if page < 1 {
		page = 1
	}
	q := s.db.WithContext(ctx).Model(&Batch{}).Where("mode='offer' AND status='active' AND expires_at>? AND owner_id IN (SELECT inviter_id FROM marketing.invite_relations WHERE invitee_id=? AND level=1 AND status=1) AND owner_id IN (SELECT id FROM customer.customers WHERE status=1 AND deleted_at IS NULL)", s.now(), user)
	var total int64
	if e = q.Count(&total).Error; e != nil {
		return nil, 0, e
	}
	rows := []Batch{}
	e = q.Order("created_at DESC,id DESC").Limit(20).Offset((page - 1) * 20).Find(&rows).Error
	for i := range rows {
		rows[i].OwnerID = 0
		rows[i].OrderID = 0
		rows[i].PointCost = 0
		rows[i].BalanceCost = 0
		rows[i].RefundedBalance = 0
		rows[i].RefundedPoints = 0
	}
	return rows, total, e
}
func (s *Service) Tasks(ctx context.Context, page int) ([]Task, int64, error) {
	if page < 1 {
		page = 1
	}
	q := s.db.WithContext(ctx).Model(&Task{})
	var total int64
	if e := q.Count(&total).Error; e != nil {
		return nil, 0, e
	}
	rows := []Task{}
	e := q.Order("FIELD(status,'failed','pending','completed'),updated_at DESC,id DESC").Limit(20).Offset((page - 1) * 20).Find(&rows).Error
	return rows, total, e
}

func (s *Service) Retry(ctx context.Context, a Actor, id int64) error {
	if a.Role != "admin" || a.Notes == "" {
		return ErrForbidden
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var t Task
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&t, "id=? AND status='failed'", id).Error; e != nil {
			return errors.New("只有失败任务可重试")
		}
		if e := s.event(tx, t.BatchID, 0, a, "task_retry", fmt.Sprintf("retry:%d:%d", id, snowflake.GenID()), 0, 0); e != nil {
			return e
		}
		return tx.Model(&t).Updates(map[string]any{"status": "pending", "attempts": 0, "next_at": s.now(), "updated_at": s.now()}).Error
	})
}
func ApplyRuntimePrivileges(ctx context.Context, db *sql.DB, user, host string) error {
	if user != "ddyidc_runtime" || (host != "127.0.0.1" && host != "::1") {
		return errors.New("unexpected promo coupon runtime identity")
	}
	for table, rights := range map[string]string{"permissions": "SELECT,INSERT,UPDATE", "settings": "SELECT,UPDATE", "templates": "SELECT,INSERT,UPDATE", "batches": "SELECT,INSERT,UPDATE", "units": "SELECT,INSERT,UPDATE", "events": "SELECT,INSERT", "tasks": "SELECT,INSERT,UPDATE", "notifications": "SELECT,INSERT,UPDATE", "reconciliations": "SELECT,INSERT"} {
		if _, e := db.ExecContext(ctx, fmt.Sprintf("GRANT %s ON marketing.promo_coupon_%s TO '%s'@'%s'", rights, table, user, host)); e != nil {
			return errors.New("promo coupon runtime grants failed")
		}
	}
	return nil
}

// Schema stays in the offline migration runner. No runtime DDL or DROP exists.

func (s *Service) auditDetails(tx *gorm.DB, a Actor, event, key string, before, after any) error {
	if len([]rune(a.Notes)) > 500 || len(a.IP) > 64 {
		return errors.New("审计说明过长")
	}
	details, e := json.Marshal(map[string]any{"before": before, "after": after})
	if e != nil {
		return e
	}
	return tx.Create(&Event{ID: snowflake.GenID(), ActorID: a.ID, ActorRole: a.Role, OperatorIP: a.IP, Event: event, RequestID: key, Notes: a.Notes, Details: details, CreatedAt: s.now()}).Error
}
