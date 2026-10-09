package withdrawal

import (
	"context"
	"gorm.io/gorm"
)

func HasPermission(ctx context.Context, db *gorm.DB, admin int64, action string) bool {
	if action != "view" && action != "review" && action != "account" && action != "remit" {
		return false
	}
	var row struct {
		IsSuper bool
		Status  int
	}
	if db.WithContext(ctx).Table("system.admins").Select("is_super,status").Where("id=?", admin).Take(&row).Error != nil || row.Status != 1 {
		return false
	}
	if row.IsSuper {
		return true
	}
	var n int64
	return db.WithContext(ctx).Table("system.permissions AS p").Joins("JOIN system.role_permissions rp ON rp.permission_id=p.id").Joins("JOIN system.roles r ON r.id=rp.role_id AND r.status=1").Joins("JOIN system.admin_roles ar ON ar.role_id=r.id").Where("ar.admin_id=? AND p.code=?", admin, "withdrawal:"+action).Count(&n).Error == nil && n > 0
}
