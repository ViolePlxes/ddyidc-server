// Package historyarchive selects the verified read-only archive after offline retirement.
// No imports, payments, tasks, or DDL run in the application process.
package historyarchive

import "gorm.io/gorm"

func Ready(db *gorm.DB) (bool, error) {
	var n int64
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA='system' AND TABLE_NAME='history_archive_batches'").Scan(&n).Error; err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	if err := db.Table("system.history_archive_batches").Where("name=? AND state=?", "retire-legacy-identifiers-v1", "complete").Count(&n).Error; err != nil {
		return false, err
	}
	return n == 1, nil
}

// Table accepts only a closed allowlist; the ready marker is committed with all archive rows.
func Table(db *gorm.DB, kind string) (string, bool, error) {
	tables := map[string][2]string{
		"orders":    {"system.v10_historical_orders", "system.history_orders"},
		"items":     {"system.v10_historical_order_items", "system.history_order_items"},
		"records":   {"system.v10_history_records", "system.history_records"},
		"instances": {"system.v10_instance_source", "system.instance_migration_metadata"},
	}
	pair, ok := tables[kind]
	if !ok {
		return "", false, gorm.ErrInvalidData
	}
	ready, err := Ready(db)
	if err != nil {
		return "", false, err
	}
	if ready {
		return pair[1], true, nil
	}
	return pair[0], false, nil
}
