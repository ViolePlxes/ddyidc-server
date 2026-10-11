package cdnsell

import (
	"context"
	"ddyidc/internal/shared/testenv"
	"encoding/json"
	"testing"
)

func TestReadinessSnapshotMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	var before bool
	if err := db.Table(settingsTable).Select("enabled").Where("id=1").Scan(&before).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE system.cdnsell_settings SET enabled=1 WHERE id=1").Error; err != nil {
		t.Fatal(err)
	}
	defer db.Exec("UPDATE system.cdnsell_settings SET enabled=? WHERE id=1", before)
	s := NewService(db, nil, nil)
	value, err := s.Readiness(context.Background())
	if err != nil || !value.Enabled || value.Phase != "P2" || value.InventoryMode != "manual" {
		t.Fatal("readiness metadata cleared by partial settings scan", value, err)
	}
	p := Product{ID: 939999, Name: "does not exist", CapacityBytes: 1024, ValidDays: 30, Price: 100, TierPrices: json.RawMessage(`{}`), MinQty: 1, MaxQty: 10, Description: "isolated", Status: true}
	if err = s.SaveProduct(context.Background(), &p); err != ErrNotFound {
		t.Fatal("nonexistent product update accepted", err)
	}
}
