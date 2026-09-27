package customer

import (
	"context"
	dom "ddyidc/internal/customer/domain"
	"ddyidc/internal/shared/snowflake"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"strings"
	"testing"
)

func TestIdentityMutationMySQL(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("isolated MySQL required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("unsafe socket")
	}
	db, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	defer pool.Close()
	ctx := context.Background()
	if err := snowflake.Configure(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repo := &GormRepository{db: db}
	customer := dom.Customer{ID: 177, Username: "identity-isolated", PasswordHash: "isolated-hash-before", Status: 1, Level: 1}
	if err := db.Create(&customer).Error; err != nil {
		t.Fatal(err)
	}
	before, err := repo.FindByID(ctx, 177)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TRIGGER system.reject_identity_audit BEFORE INSERT ON system.audit_logs FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='isolated audit failure'").Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateIdentity(ctx, before, "13900000177", "", "127.0.0.1"); err == nil {
		t.Fatal("audit failure accepted")
	}
	if err := db.Exec("DROP TRIGGER system.reject_identity_audit").Error; err != nil {
		t.Fatal(err)
	}
	unchanged, _ := repo.FindByID(ctx, 177)
	if unchanged.Phone != nil && *unchanged.Phone != "" {
		t.Fatal("audit failure left bound phone")
	}
	if err := repo.UpdateIdentity(ctx, before, "13900000177", "", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateIdentity(ctx, before, "13900000178", "", "127.0.0.1"); err == nil {
		t.Fatal("stale/rebind allowed")
	}
	bound, err := repo.FindUniqueActivePhone(ctx, "13900000177")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateIdentity(ctx, bound, "", "isolated-hash-after", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	reset, _ := repo.FindByID(ctx, 177)
	if reset.AuthVersion <= bound.AuthVersion {
		t.Fatal("password reset failed to revoke old account version")
	}
	if err := repo.UpdateIdentity(ctx, bound, "", "isolated-stale-hash", "127.0.0.1"); err == nil {
		t.Fatal("stale password write accepted")
	}
	other := dom.Customer{ID: 178, TenantID: 2, Username: "identity-other-tenant", Phone: reset.Phone, PasswordHash: "isolated-disabled", Status: 1, Level: 1}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FindUniqueActivePhone(ctx, "13900000177"); err == nil {
		t.Fatal("ambiguous tenant phone accepted")
	}
	var audits int64
	if err := db.Table("system.audit_logs").Where("target_type='customer' AND target_id=177 AND action IN ('customer.mobile_bind','customer.password_reset')").Count(&audits).Error; err != nil || audits != 2 {
		t.Fatal("identity audit count", err, audits)
	}
	t.Log("isolated identity: audit rollback, first binding only, stale writes refused, auth version bumped, ambiguous phone refused; no SMS sent")
}
