package update

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"ddyidc/internal/shared/migrations"
	"errors"
	"fmt"
)

var cdnBootstrapTables = []string{"cdnsell_upstream_accounts", "cdnsell_upstream_packages", "cdnsell_products", "cdnsell_batches", "cdnsell_domains", "cdnsell_domain_configs", "cdnsell_config_tasks", "cdnsell_ssl_certs", "cdnsell_meter_records", "cdnsell_overage_bills", "cdnsell_orders", "cdnsell_tasks", "cdnsell_notify_records", "cdnsell_reconcile_records", "cdnsell_refresh_tasks", "cdnsell_ownership_challenges", "cdnsell_usage_windows", "cdnsell_usage_allocations", "cdnsell_usage_cursors"}

// The only legacy exception is an empty, disabled module, protected against
// concurrent inserts and enabling by the 045 migration while the update runs.
func VerifyPristineCDNBootstrap(ctx context.Context, database *sql.DB) error {
	var enabled, running int
	if err := database.QueryRowContext(ctx, "SELECT enabled FROM system.cdnsell_settings WHERE id=1").Scan(&enabled); err != nil || enabled != 0 {
		return errors.New("CDN必须保持关闭，不能使用空模块升级例外")
	}
	if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM system.update_tasks WHERE target='backend' AND status='running'").Scan(&running); err != nil || running != 1 {
		return errors.New("空模块升级必须由唯一正在运行的在线更新任务执行")
	}
	for _, table := range cdnBootstrapTables {
		var exists int
		if err := database.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM system."+table+" LIMIT 1)").Scan(&exists); err != nil || exists != 0 {
			return fmt.Errorf("CDN表%s并非空模块，旧版回退不兼容", table)
		}

	}
	steps, err := migrations.Steps()
	if err != nil {
		return err
	}
	for _, step := range steps {
		if step.Name == "045_cdn_bootstrap_guard.sql" {
			var hash, state string
			if err = database.QueryRowContext(ctx, "SELECT checksum,state FROM system.schema_migrations WHERE name=?", step.Name).Scan(&hash, &state); err != nil || state != "complete" || hash != fmt.Sprintf("%x", sha256.Sum256([]byte(step.SQL))) {
				return errors.New("CDN首次升级保护迁移未验证")
			}
			return nil
		}
	}
	return errors.New("CDN首次升级保护迁移缺失")
}
