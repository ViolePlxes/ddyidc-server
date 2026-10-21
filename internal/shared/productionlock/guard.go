// Package productionlock is the common server-side guard for instance mutations.
package productionlock

import (
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrLocked = errors.New("服务器已开启生产锁，请先解除")

// Check must also run after acquiring the instance row lock before a write.
func Check(db *gorm.DB, id int64) error {
	if db == nil {
		return nil
	}
	var row struct{ Enabled bool }
	// A locking read sees the current row even if an earlier payment query has
	// already established a REPEATABLE READ snapshot in this transaction.
	err := db.Table("instance.instance_production_locks").Clauses(clause.Locking{Strength: "UPDATE"}).Select("enabled").Where("instance_id=?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if row.Enabled {
		return ErrLocked
	}
	return nil
}
