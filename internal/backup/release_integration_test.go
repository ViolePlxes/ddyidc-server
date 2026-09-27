package backup

import (
	"context"
	"ddyidc/internal/shared/testenv"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreReleaseAtomicMySQL(t *testing.T) {
	database := testenv.MySQL(t)
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	id := uuid.NewString()
	root := t.TempDir()
	base := filepath.Join(root, "restore", id)
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{"runtime-accounts.json": []RuntimeAccount{{User: "ddyidc_runtime", Host: "release-test.invalid", AccountLocked: "Y"}}, "reconciliation.json": RecoveryReview{ExternalChecked: true, Notes: "controlled empty database release test"}} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err = writeDurable(filepath.Join(base, name), raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeDurable(filepath.Join(base, "phase"), []byte("drained")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec("UPDATE system.operation_barrier SET mode='restore',reason=? WHERE id=1", "restore:"+id).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Create(&Job{ID: id, Kind: "restore", Status: "awaiting_review"}).Error; err != nil {
		t.Fatal(err)
	}
	fail := true
	const callback = "foundation_fail_release_history"
	if err := tx.Callback().Update().Before("gorm:update").Register(callback, func(call *gorm.DB) {
		if fail && call.Statement.Table == "backup_history" {
			call.AddError(errors.New("controlled history failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer tx.Callback().Update().Remove(callback)
	service := New(tx, nil, t.TempDir())
	service.privateRoot = root
	release := func() error {
		return service.ReleaseRestore(context.Background(), id, "已完成外部对账 "+id, "isolated-test", "no external transactions")
	}
	if release() == nil {
		t.Fatal("history failure accepted")
	}
	var mode string
	if err := tx.Raw("SELECT mode FROM system.operation_barrier WHERE id=1").Scan(&mode).Error; err != nil || mode != "restore" {
		t.Fatal("barrier opened despite failed history write", err)
	}
	fail = false
	if err := release(); err != nil {
		t.Fatal(err)
	}
	var job Job
	if err := tx.First(&job, "id=?", id).Error; err != nil || job.Status != "released" {
		t.Fatal("release state not persisted", err)
	}
	if err := tx.Raw("SELECT mode FROM system.operation_barrier WHERE id=1").Scan(&mode).Error; err != nil || mode != "running" {
		t.Fatal("barrier not released", err)
	}
	if err := release(); err != nil {
		t.Fatal("completed release not idempotent", err)
	}
	t.Log("isolated SQL: failed history update keeps maintenance closed; retry commits barrier and released history together; no account unlock, import or external request")
}
