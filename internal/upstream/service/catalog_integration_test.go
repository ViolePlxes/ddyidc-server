package service

import (
	"context"
	"ddyidc/internal/shared/testenv"
	"ddyidc/internal/upstream/model"
	"ddyidc/internal/upstream/provider/contracts"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCatalogReplacementMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	store := NewStore(tx, nil)
	task := model.SyncTask{ID: id(), SupplierID: "998001", Type: "catalog", Status: "running", TriggerBy: "manual"}
	if err := tx.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	original := []contracts.UpstreamProduct{{ID: 1, Name: "original"}, {ID: 2, Name: "retained"}}
	if err := store.reconcileCatalog(context.Background(), task, original); err != nil {
		t.Fatal(err)
	}
	replacement := []contracts.UpstreamProduct{{ID: 1, Name: "changed"}, {ID: 3, Name: strings.Repeat("x", 300)}}
	if err := store.reconcileCatalog(context.Background(), task, replacement); err == nil {
		t.Fatal("invalid replacement accepted")
	}
	var rows []model.CatalogProduct
	if err := tx.Where("supplier_id=? AND status='active'", task.SupplierID).Order("upstream_product_id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Name != "original" || rows[1].Name != "retained" {
		t.Fatal("failed refresh damaged previous catalogue")
	}
	replacement[1].Name = "new product"
	if err := store.reconcileCatalog(context.Background(), task, replacement); err != nil {
		t.Fatal(err)
	}
	rows = nil
	if err := tx.Where("supplier_id=? AND status='active'", task.SupplierID).Order("upstream_product_id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Name != "changed" || rows[1].UpstreamProductID != 3 {
		t.Fatal("complete replacement not published")
	}
	t.Log("real isolated MySQL: mid-refresh SQL failure rolls back invalidation and earlier rows; complete snapshot publishes atomically; no supplier requests")
}

func TestCatalogMultiNodeMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	supplier := model.Supplier{ID: id(), Name: "isolated catalog admission", Type: "zjmf", URL: "https://example.invalid", Username: "isolated", Token: "", Rate: 1, Status: 1}
	if err := db.Create(&supplier).Error; err != nil {
		t.Fatal(err)
	}
	defer db.Delete(&supplier)
	defer db.Where("supplier_id=?", supplier.ID).Delete(&model.SyncTask{})
	defer db.Where("supplier_id=?", supplier.ID).Delete(&model.CatalogProduct{})
	nodes := []*Store{NewStore(db, nil), NewStore(db, nil)}
	start := make(chan struct{})
	tasks := make(chan model.SyncTask, 2)
	var wg sync.WaitGroup
	for _, node := range nodes {
		wg.Add(1)
		go func(node *Store) {
			defer wg.Done()
			<-start
			task, err := node.admitCatalogSync(supplier.ID, "manual")
			if err == nil {
				tasks <- task
			}
		}(node)
	}
	close(start)
	wg.Wait()
	close(tasks)
	var admitted []model.SyncTask
	for task := range tasks {
		admitted = append(admitted, task)
	}
	if len(admitted) != 1 {
		t.Fatalf("admitted %d concurrent tasks", len(admitted))
	}
	old := admitted[0]
	if err := db.Model(&old).Update("status", "running").Error; err != nil {
		t.Fatal(err)
	}
	if err := nodes[1].CancelTask(old.ID); err != nil {
		t.Fatal(err)
	}
	if err := nodes[0].reconcileCatalog(context.Background(), old, []contracts.UpstreamProduct{{ID: 1, Name: "cancelled result"}}); err == nil {
		t.Fatal("cancelled node published stale result")
	}
	latest, err := nodes[1].admitCatalogSync(supplier.ID, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&latest).Updates(map[string]any{"status": "running", "started_at": time.Now().Unix() - 121}).Error; err != nil {
		t.Fatal(err)
	}
	replacement, err := nodes[0].admitCatalogSync(supplier.ID, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if err = nodes[1].reconcileCatalog(context.Background(), latest, []contracts.UpstreamProduct{{ID: 1, Name: "expired result"}}); err == nil {
		t.Fatal("expired node published stale result")
	}
	if err = db.Model(&replacement).Update("status", "running").Error; err != nil {
		t.Fatal(err)
	}
	if err = nodes[0].reconcileCatalog(context.Background(), replacement, []contracts.UpstreamProduct{{ID: 2, Name: "current result"}}); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err = db.Model(&model.CatalogProduct{}).Where("supplier_id=? AND status='active'", supplier.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("unexpected active catalogue", err, count)
	}
	t.Log("isolated MySQL, two independent stores: single admission, cross-node cancel, expired task replacement, stale publication rejection; no provider request")
}
