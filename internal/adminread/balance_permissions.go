package adminread

import (
	"context"
	"gorm.io/gorm"
)

// Financial mutations require an explicit active role grant. Query failure denies access.
func hasBalancePermission(ctx context.Context, database *gorm.DB, adminID int64, action string) bool {
	switch action {
	case "adjust", "recharge", "freeze", "unfreeze":
	default:
		return false
	}
	var admin struct {
		IsSuper bool
		Status  int
	}
	if database.WithContext(ctx).Table("system.admins").Select("is_super,status").Where("id=?", adminID).Take(&admin).Error != nil || admin.Status != 1 {
		return false
	}
	if admin.IsSuper {
		return true
	}
	var count int64
	return database.WithContext(ctx).Table("system.permissions AS p").Joins("JOIN system.role_permissions rp ON rp.permission_id=p.id").Joins("JOIN system.roles r ON r.id=rp.role_id AND r.status=1").Joins("JOIN system.admin_roles ar ON ar.role_id=r.id").Where("ar.admin_id=? AND p.code=?", adminID, "balance:"+action).Count(&count).Error == nil && count > 0
}
