package instance

import (
	"context"
	domain "ddyidc/internal/instance/domain"
	repo "ddyidc/internal/instance/repository"
	"ddyidc/internal/shared/testenv"
	"gorm.io/gorm"
	"testing"
)

// TestDestroySettingsMySQL proves the mailbox proof of a destroy application is
// an admin switch with a safe default: missing or removed configuration still
// requires the code, and only an explicit off value lets the reason alone
// through.
func TestDestroySettingsMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	ctx := context.Background()
	s := NewService(repo.NewRepository(db), nil)
	s.db = db
	if err := db.Exec("DELETE FROM system.system_configs WHERE `key`=?", destroySettingsKey).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM system.system_configs WHERE `key`=?", destroySettingsKey)
		db.Exec("DELETE FROM system.audit_logs WHERE action='instance.destroy_settings' AND operator_id=?", int64(9911001))
	})

	settings, err := LoadDestroySettings(db)
	if err != nil || !settings.RequireEmailVerification {
		t.Fatalf("default settings = %+v (%v)", settings, err)
	}
	check := func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			return s.destroySubmitCheck(ctx, int64(9911002), DestroyEmailInput{})(tx, domain.Host{ID: 9911003})
		})
	}
	if err := check(); err == nil {
		t.Fatal("missing code accepted while verification is on")
	}
	if _, err := s.SaveDestroySettings(ctx, DestroySettings{RequireEmailVerification: false}, 9911001, "198.51.100.7"); err != nil {
		t.Fatal(err)
	}
	settings, err = LoadDestroySettings(db)
	if err != nil || settings.RequireEmailVerification {
		t.Fatalf("stored settings = %+v (%v)", settings, err)
	}
	if err := check(); err != nil {
		t.Fatalf("verification disabled but submission still refused: %v", err)
	}
	var audits int64
	if err := db.Table("system.audit_logs").Where("action='instance.destroy_settings' AND operator_id=?", int64(9911001)).Count(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("settings change audit = %d", audits)
	}
	// Restoring the switch restores the credential requirement immediately.
	if _, err := s.SaveDestroySettings(ctx, DestroySettings{RequireEmailVerification: true}, 9911001, "198.51.100.7"); err != nil {
		t.Fatal(err)
	}
	if err := check(); err == nil {
		t.Fatal("missing code accepted after verification was re-enabled")
	}
	// A corrupt row must not silently disable the proof.
	if err := db.Exec("UPDATE system.system_configs SET value='{broken' WHERE `key`=?", destroySettingsKey).Error; err != nil {
		t.Fatal(err)
	}
	if settings, err := LoadDestroySettings(db); err == nil || !settings.RequireEmailVerification {
		t.Fatalf("corrupt settings accepted: %+v (%v)", settings, err)
	}
}
