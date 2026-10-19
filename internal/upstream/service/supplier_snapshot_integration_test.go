package service

import (
	"context"
	"ddyidc/internal/shared/testenv"
	"ddyidc/internal/shared/workgate"
	"ddyidc/internal/upstream/metrics"
	"ddyidc/internal/upstream/model"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"sync/atomic"
	"testing"
	"time"
)

type snapshotFixtureWorker struct {
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (w *snapshotFixtureWorker) SyncSupplierReadSnapshots(ctx context.Context, id, run string, report func(int, int, int) error) error {
	if !metrics.IsBackground(ctx) {
		return errors.New("missing background rate scope")
	}
	w.calls.Add(1)
	if e := report(2, 0, 0); e != nil {
		return e
	}
	close(w.entered)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.release:
	}
	return report(2, 2, 1)
}
func TestSupplierSnapshotConcurrencyMySQL(t *testing.T) {
	database := testenv.MySQL(t)
	pool, e := database.DB()
	if e != nil {
		t.Fatal(e)
	}
	workgate.Configure(pool)
	ctx := context.Background()
	supplier := model.Supplier{Name: "snapshot isolated fixture", Type: "default", Status: 1, URL: "https://fixture.invalid", Username: "fixture", Token: "cipher", Secret: "cipher", Rate: 1}
	if e := database.Create(&supplier).Error; e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		database.Table("system.system_configs").Where("`key`=?", snapshotKey(supplier.ID)).Delete(&configRow{})
		database.Delete(&supplier)
	})
	worker := &snapshotFixtureWorker{entered: make(chan struct{}), release: make(chan struct{})}
	first, second := NewStore(database, nil), NewStore(database, nil)
	first.SetSupplierSnapshotWorker(worker)
	second.SetSupplierSnapshotWorker(worker)
	if _, e := first.ConfigureSupplierSnapshot(ctx, supplier.ID, true, 120); e != nil {
		t.Fatal(e)
	}
	info, e := first.StartSupplierSnapshot(ctx, supplier.ID, true)
	if e != nil || info.State != "running" {
		t.Fatal(info, e)
	}
	select {
	case <-worker.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	again, e := second.StartSupplierSnapshot(ctx, supplier.ID, false)
	if e != nil || again.State != "running" || worker.calls.Load() != 1 {
		t.Fatal("concurrent schedule duplicated work", again, e)
	}
	if _, e = first.ConfigureSupplierSnapshot(ctx, supplier.ID, false, 60); e != nil {
		t.Fatal(e)
	}
	close(worker.release)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		info, e = first.SupplierSnapshotState(ctx, supplier.ID)
		if e != nil {
			t.Fatal(e)
		}
		if info.State != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if info.State != "partial" || info.LastSuccessAt != 0 || info.Completed != 2 || info.Failed != 1 || info.Enabled || info.IntervalMinutes != 60 {
		t.Fatal("partial failure/settings corrupt", info)
	}
	if _, e = first.StartSupplierSnapshot(ctx, supplier.ID, false); e != nil {
		t.Fatal(e)
	}
	if worker.calls.Load() != 1 {
		t.Fatal("disabled schedule ran")
	}
	automatic := &snapshotFixtureWorker{entered: make(chan struct{}), release: make(chan struct{})}
	close(automatic.release)
	second.SetSupplierSnapshotWorker(automatic)
	if _, e = second.ConfigureSupplierSnapshot(ctx, supplier.ID, true, 120); e != nil {
		t.Fatal(e)
	}
	if _, e = second.StartSupplierSnapshot(ctx, supplier.ID, false); e != nil {
		t.Fatal(e)
	}
	select {
	case <-automatic.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("automatic worker missing background scope or did not start")
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		info, e = second.SupplierSnapshotState(ctx, supplier.ID)
		if e != nil {
			t.Fatal(e)
		}
		if info.State != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if info.State != "partial" || automatic.calls.Load() != 1 {
		t.Fatal("automatic schedule result", info)
	}

	e = database.Transaction(func(tx *gorm.DB) error {
		row, v, e := snapshotStateTx(tx, supplier.ID)
		if e != nil {
			return e
		}
		v.State = "running"
		v.RunID = "new-fence"
		v.LeaseUntil = time.Now().Unix() + 90
		if e = saveSnapshotState(tx, row, v); e != nil {
			return e
		}
		if e = VerifySupplierSnapshotLeaseTx(ctx, tx, supplier.ID, "old-fence"); e == nil {
			return errors.New("old fence accepted")
		}
		v.LeaseUntil = time.Now().Unix() - 1
		if e = saveSnapshotState(tx, row, v); e != nil {
			return e
		}
		if e = VerifySupplierSnapshotLeaseTx(ctx, tx, supplier.ID, "new-fence"); e == nil {
			return errors.New("expired lease accepted")
		}
		raw, _ := json.Marshal(v)
		if len(raw) > 4096 {
			return errors.New("unbounded state")
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
}
