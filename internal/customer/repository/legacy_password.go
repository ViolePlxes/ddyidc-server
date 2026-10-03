package customer

import (
	"context"
	"errors"
	"net"

	dom "ddyidc/internal/customer/domain"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/auth"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UpgradeV10Password serializes transparent upgrades with password reset,
// disablement, and concurrent login. A stale credential can never overwrite a
// newer password. The caller receives the post-trigger auth_version.
func (r *GormRepository) UpgradeV10Password(ctx context.Context, before *dom.Customer, newHash, password, ip string) (*dom.Customer, error) {
	if before == nil || !auth.IsV10Password(before.PasswordHash) || net.ParseIP(ip) == nil || !auth.PasswordMatches(newHash, password) {
		return nil, ErrIdentityChanged
	}
	var current dom.Customer
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", before.ID).Take(&current).Error; err != nil {
			return err
		}
		if current.Status != 1 {
			return ErrIdentityChanged
		}
		if current.PasswordHash != before.PasswordHash || current.AuthVersion != before.AuthVersion {
			// Another transparent upgrade may have won. Verify its final hash;
			// a concurrent reset to a different password must fail this login.
			if !auth.IsV10Password(current.PasswordHash) && auth.PasswordMatches(current.PasswordHash, password) {
				return nil
			}
			return ErrIdentityChanged
		}
		if err := tx.Model(&current).Update("password_hash", newHash).Error; err != nil {
			return err
		}
		if err := tx.Table("system.audit_logs").Create(map[string]any{
			"id": snowflake.GenID(), "operator_type": 2, "operator_id": current.ID,
			"action": "customer.password_upgrade", "target_type": "customer", "target_id": current.ID,
			"ip": ip, "detail": "{\"source\":\"v10\",\"verification\":\"original_password\"}",
		}).Error; err != nil {
			return err
		}
		return tx.Where("id=?", current.ID).Take(&current).Error
	})
	if err != nil {
		if errors.Is(err, ErrIdentityChanged) {
			return nil, ErrIdentityChanged
		}
		return nil, err
	}
	return &current, nil
}
