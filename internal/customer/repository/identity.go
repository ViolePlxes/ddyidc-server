package customer

import (
	"context"
	dom "ddyidc/internal/customer/domain"
	"ddyidc/internal/shared/snowflake"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net"
)

var ErrIdentityChanged = errors.New("账户信息已变化，请重新验证")

func (r *GormRepository) FindUniqueActivePhone(ctx context.Context, phone string) (*dom.Customer, error) {
	var rows []dom.Customer
	if err := r.db.WithContext(ctx).Where("phone=? AND status=1", phone).Limit(2).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, ErrIdentityChanged
	}
	return &rows[0], nil
}

// UpdateIdentity changes only the verified field; it never saves a stale full
// customer snapshot. Password changes invalidate sessions via auth_version.
func (r *GormRepository) UpdateIdentity(ctx context.Context, before *dom.Customer, mobile, passwordHash, ip string) error {
	if before == nil || before.ID <= 0 || net.ParseIP(ip) == nil || (mobile == "") == (passwordHash == "") {
		return ErrIdentityChanged
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current dom.Customer
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", before.ID).Take(&current).Error; err != nil {
			return err
		}
		if current.Status != 1 || current.AuthVersion != before.AuthVersion || current.PasswordHash != before.PasswordHash {
			return ErrIdentityChanged
		}
		phone := func(v *string) string {
			if v == nil {
				return ""
			}
			return *v
		}
		if phone(current.Phone) != phone(before.Phone) {
			return ErrIdentityChanged
		}
		changes := map[string]any{}
		action := "customer.password_reset"
		if mobile != "" {
			if phone(current.Phone) != "" {
				return ErrIdentityChanged
			}
			changes["phone"] = mobile
			action = "customer.mobile_bind"
		} else {
			if phone(current.Phone) == "" {
				return ErrIdentityChanged
			}
			changes["password_hash"] = passwordHash
		}
		if err := tx.Model(&current).Updates(changes).Error; err != nil {
			return err
		}
		return tx.Table("system.audit_logs").Create(map[string]any{"id": snowflake.GenID(), "operator_type": 2, "operator_id": current.ID, "action": action, "target_type": "customer", "target_id": current.ID, "ip": ip, "detail": "{\"verification\":\"sms_once\"}"}).Error
	})
}
