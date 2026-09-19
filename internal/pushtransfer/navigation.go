package pushtransfer

import (
	"ddyidc/internal/shared/snowflake"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// One-time append only: never replace an administrator's configured navigation.
func SeedNavigation(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		result := tx.Exec("INSERT IGNORE INTO system.system_configs (id,`key`,value,description) VALUES (?,'push_transfer.navigation_seeded','true','017 导航迁移标记')", snowflake.GenID())
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		for _, scope := range []string{"console", "admin"} {
			var revision struct {
				Scope    string
				Revision int64
			}
			if err := tx.Table("system.navigation_scopes").Clauses(clause.Locking{Strength: "UPDATE"}).Where("scope=?", scope).Take(&revision).Error; err != nil {
				return err
			}
			var group struct{ ID int64 }
			if err := tx.Table("system.navigation_nodes").Select("id").Where("scope=? AND parent_id=0", scope).Order("sort,id").Take(&group).Error; err != nil {
				return err
			}
			var sort int
			if err := tx.Table("system.navigation_nodes").Where("scope=? AND parent_id=?", scope, group.ID).Select("COALESCE(MAX(sort),0)+1").Scan(&sort).Error; err != nil {
				return err
			}
			label, gate := "实例接收中心", "/console/transfers"
			if scope == "admin" {
				label = "实例转让记录"
				gate = ""
			}
			if err := tx.Exec("INSERT INTO system.navigation_nodes (id,scope,parent_id,name,href,target,icon,gate,enabled,sort,created_at,updated_at) VALUES (?,?,?,?,?,'self','ArrowsClockwiseIcon',?,true,?,?,?)", snowflake.GenID(), scope, group.ID, label, fmt.Sprintf("/%s/transfers", scope), gate, sort, time.Now(), time.Now()).Error; err != nil {
				return err
			}
			if err := tx.Table("system.navigation_scopes").Where("scope=?", scope).Update("revision", gorm.Expr("revision+1")).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
