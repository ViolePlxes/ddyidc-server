package adminread

import (
	"context"
	"ddyidc/internal/shared/testenv"
	"gorm.io/gorm"
	"testing"
)

func TestBalancePermissionsMySQL(t *testing.T) {
	database := testenv.MySQL(t)
	if e := database.Transaction(func(tx *gorm.DB) error {
		for _, sql := range []string{
			"INSERT INTO system.admins(id,username,password_hash,real_name,status,is_super) VALUES(993701,'permission-fixture','unused','fixture',1,0),(993702,'permission-super','unused','fixture',1,1)",
			"INSERT INTO system.roles(id,name,status) VALUES(993703,'isolated-finance-role',1)",
			"INSERT INTO system.permissions(id,code,name,module) VALUES(993704,'balance:adjust','fixture','balance') ON DUPLICATE KEY UPDATE id=id",
		} {
			if e := tx.Exec(sql).Error; e != nil {
				return e
			}
		}
		ctx := context.Background()
		if hasBalancePermission(ctx, tx, 993701, "adjust") {
			t.Fatal("admin without grant changed balance")
		}
		if !hasBalancePermission(ctx, tx, 993702, "adjust") {
			t.Fatal("active super-admin refused")
		}
		if e := tx.Exec("INSERT INTO system.admin_roles(admin_id,role_id) VALUES(993701,993703)").Error; e != nil {
			return e
		}
		if e := tx.Exec("INSERT INTO system.role_permissions(role_id,permission_id) SELECT 993703,id FROM system.permissions WHERE code='balance:adjust'").Error; e != nil {
			return e
		}
		if !hasBalancePermission(ctx, tx, 993701, "adjust") || hasBalancePermission(ctx, tx, 993701, "recharge") {
			t.Fatal("action grant not isolated")
		}
		if e := tx.Exec("UPDATE system.roles SET status=0 WHERE id=993703").Error; e != nil {
			return e
		}
		if hasBalancePermission(ctx, tx, 993701, "adjust") {
			t.Fatal("disabled role allowed mutation")
		}
		if e := tx.Exec("UPDATE system.admins SET status=0 WHERE id=993702").Error; e != nil {
			return e
		}
		if hasBalancePermission(ctx, tx, 993702, "adjust") || hasBalancePermission(ctx, tx, 993701, "unknown") {
			t.Fatal("inactive admin or unknown action allowed")
		}
		return gorm.ErrInvalidTransaction
	}); e != gorm.ErrInvalidTransaction {
		t.Fatal(e)
	}
}
