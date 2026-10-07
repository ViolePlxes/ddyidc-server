package promocoupon

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"time"
)

type Permission struct {
	AdminID   int64     `json:"adminId,string"`
	CanView   bool      `json:"canView"`
	CanReview bool      `json:"canReview"`
	CanClose  bool      `json:"canClose"`
	CanRetry  bool      `json:"canRetry"`
	CanConfig bool      `json:"canConfig"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (Permission) TableName() string { return "marketing.promo_coupon_permissions" }
func (s *Service) Allowed(ctx context.Context, admin int64, operation string) (bool, error) {
	var owner struct {
		IsSuper bool
		Status  int
	}
	if e := s.db.WithContext(ctx).Table("system.admins").Select("is_super,status").Where("id=?", admin).Take(&owner).Error; e != nil {
		return false, e
	}
	if owner.Status != 1 {
		return false, nil
	}
	if owner.IsSuper {
		return true, nil
	}
	if operation == "permissions" {
		return false, nil
	}
	var p Permission
	e := s.db.WithContext(ctx).Take(&p, "admin_id=?", admin).Error
	if e == gorm.ErrRecordNotFound {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	switch operation {
	case "view":
		return p.CanView, nil
	case "review":
		return p.CanReview, nil
	case "close":
		return p.CanClose, nil
	case "retry":
		return p.CanRetry, nil
	case "config":
		return p.CanConfig, nil
	}
	return false, nil
}
func (s *Service) SavePermission(ctx context.Context, a Actor, p Permission) error {
	ok, e := s.Allowed(ctx, a.ID, "permissions")
	if e != nil {
		return e
	}
	if !ok || a.Notes == "" {
		return ErrForbidden
	}
	var n int64
	if e = s.db.WithContext(ctx).Table("system.admins").Where("id=? AND status=1", p.AdminID).Count(&n).Error; e != nil {
		return e
	}
	if n != 1 {
		return errors.New("管理员不存在或已停用")
	}
	p.UpdatedAt = s.now()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before Permission
		e := tx.Take(&before, "admin_id=?", p.AdminID).Error
		if e != nil && e != gorm.ErrRecordNotFound {
			return e
		}
		if e = tx.Save(&p).Error; e != nil {
			return e
		}
		return s.auditDetails(tx, a, "permission_updated", fmt.Sprintf("permission:%d:%d", p.AdminID, snowflake.GenID()), before, p)
	})
}
