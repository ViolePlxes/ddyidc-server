package db

import (
	"fmt"

	"ddyidc/internal/shared/snowflake"
	"golang.org/x/crypto/bcrypt"
)

// SeedDevelopmentData is idempotent and only writes accounts/configuration once
// the migration-created tables exist.
func SeedDevelopmentData() error {
	if G == nil {
		return fmt.Errorf("database is not initialized")
	}
	// GORM's Migrator.HasTable treats a dotted name as one identifier on MySQL.
	// Check schema and table separately so cross-schema seed tables are detected
	// reliably.
	for _, table := range []struct {
		schema string
		name   string
	}{
		{schema: "customer", name: "customers"},
		{schema: "system", name: "admins"},
		{schema: "system", name: "system_configs"},
	} {
		var count int64
		if err := G.Raw(
			"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ? AND table_name = ?",
			table.schema,
			table.name,
		).Scan(&count).Error; err != nil || count != 1 {
			return fmt.Errorf("database table %s.%s is missing; run with -migrate or start mysql with the sql volume", table.schema, table.name)
		}
	}
	adminHash, err := bcrypt.GenerateFromPassword([]byte("Admin1234"), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash admin password: %w", err)
	}
	demoHash, err := bcrypt.GenerateFromPassword([]byte("Demo1234"), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash demo password: %w", err)
	}
	if err := G.Exec("INSERT IGNORE INTO system.admins (id, username, password_hash, real_name, status, is_super) VALUES (?, ?, ?, ?, ?, ?)", snowflake.GenID(), "admin", string(adminHash), "系统管理员", 1, 1).Error; err != nil {
		return fmt.Errorf("seed admin: %w", err)
	}
	if err := G.Exec("INSERT IGNORE INTO customer.customers (id, tenant_id, username, email, password_hash, status, level) VALUES (?, ?, ?, ?, ?, ?, ?)", snowflake.GenID(), 0, "demo_user", "demo@ddyidc.local", string(demoHash), 1, 1).Error; err != nil {
		return fmt.Errorf("seed demo user: %w", err)
	}
	for _, item := range []struct{ key, value, desc string }{{"site_name", "DDYIDC", "站点名称"}, {"currency_code", "CNY", "默认货币代码"}} {
		if err := G.Exec("INSERT IGNORE INTO system.system_configs (id, `key`, value, description) VALUES (?, ?, ?, ?)", snowflake.GenID(), item.key, item.value, item.desc).Error; err != nil {
			return fmt.Errorf("seed config %s: %w", item.key, err)
		}
	}
	return nil
}
