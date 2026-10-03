package customer

import (
	"context"
	"os"
	"strings"
	"testing"

	dom "ddyidc/internal/customer/domain"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/auth"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestV10PasswordUpgradeMySQL(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("isolated MySQL required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("unsafe socket")
	}
	database, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := database.DB()
	defer pool.Close()
	ctx := context.Background()
	if err := snowflake.Configure(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repo := &GormRepository{db: database}
	before := dom.Customer{ID: 8177, Username: "v10-upgrade-fixture", PasswordHash: "v10:###8892e4e2c6ed323c8369e0350bf43342", Status: 1, Level: 1}
	if err := database.Create(&before).Error; err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashMigratedPassword("Original!123")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Exec("CREATE TRIGGER system.reject_v10_audit BEFORE INSERT ON system.audit_logs FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='isolated audit failure'").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpgradeV10Password(ctx, &before, hash, "Original!123", "127.0.0.1"); err == nil {
		t.Fatal("audit failure accepted")
	}
	unchanged, _ := repo.FindByID(ctx, before.ID)
	if unchanged.PasswordHash != before.PasswordHash {
		t.Fatal("audit failure did not roll back")
	}
	if err := database.Exec("DROP TRIGGER system.reject_v10_audit").Error; err != nil {
		t.Fatal(err)
	}
	upgraded, err := repo.UpgradeV10Password(ctx, &before, hash, "Original!123", "127.0.0.1")
	if err != nil || upgraded.AuthVersion <= before.AuthVersion || !auth.PasswordMatches(upgraded.PasswordHash, "Original!123") {
		t.Fatal("upgrade/version failed", err)
	}
	if _, err := repo.UpgradeV10Password(ctx, &before, hash, "Original!123", "127.0.0.1"); err != nil {
		t.Fatal("concurrent winning upgrade rejected", err)
	}
	reset, _ := auth.HashPassword("Different!123")
	if err := database.Model(&dom.Customer{}).Where("id=?", before.ID).Update("password_hash", reset).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpgradeV10Password(ctx, &before, hash, "Original!123", "127.0.0.1"); err == nil {
		t.Fatal("stale upgrade overwrote reset")
	}
	var count int64
	if err := database.Table("system.audit_logs").Where("target_id=? AND action='customer.password_upgrade'", before.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("upgrade audit repeated", err)
	}
}
