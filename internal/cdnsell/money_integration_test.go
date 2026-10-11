package cdnsell

import (
	"context"
	orderdom "ddyidc/internal/order/domain"
	"ddyidc/internal/shared/testenv"
	"encoding/json"
	"testing"
	"time"
)

func TestYuanPurchaseMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	ctx := context.Background()
	now := time.Now()
	s := NewService(db, nil, nil)
	cdnFixture(t, db, 893001, 893101, now)
	db.Exec("UPDATE customer.customers SET level=2 WHERE id=893001")
	db.Exec("UPDATE system.cdnsell_settings SET enabled=1 WHERE id=1")
	defer db.Exec("UPDATE system.cdnsell_settings SET enabled=0 WHERE id=1")
	readiness, err := s.Readiness(ctx)
	if err != nil || !readiness.Enabled || readiness.Phase != "P2" || readiness.InventoryMode != "manual" {
		t.Fatal("readiness lost module state during settings scan", readiness, err)
	}
	p := Product{Name: "isolated yuan product", CapacityBytes: 1024, ValidDays: 30, Price: 1999, TierPrices: json.RawMessage(`{"2":"9.00"}`), MinQty: 1, MaxQty: 10, Description: "isolated", Status: true}
	if e := s.SaveProduct(ctx, &p); e != nil {
		t.Fatal(e)
	}
	var stored string
	if e := db.Table(productsTable).Select("price").Where("id=?", p.ID).Scan(&stored).Error; e != nil || stored != "19.99" {
		t.Fatal(stored, e)
	}
	order, e := s.Purchase(ctx, 893001, p.ID, 2)
	if e != nil || order.Amount != 1800 {
		t.Fatal(order, e)
	}
	var main orderdom.Order
	if e = db.Where("id=?", order.OrderID).Take(&main).Error; e != nil || main.PayAmount != 1800 {
		t.Fatal(main, e)
	}
	if e = db.Table(ordersTable).Select("amount").Where("ref_order_id=?", order.OrderID).Scan(&stored).Error; e != nil || stored != "18.00" {
		t.Fatal(stored, e)
	}
	var converted string
	if e = db.Raw(`SELECT CAST(JSON_OBJECTAGG(k.tier,CAST(JSON_UNQUOTE(JSON_EXTRACT(p.prices,CONCAT('$.',JSON_QUOTE(k.tier)))) AS DECIMAL(14,2))/100) AS CHAR) FROM (SELECT CAST('{"2":900}' AS JSON) prices) p JOIN JSON_TABLE(JSON_KEYS(p.prices),'$[*]' COLUMNS(tier VARCHAR(64) PATH '$')) k`).Scan(&converted).Error; e != nil {
		t.Fatal(e)
	}
	var tiers map[string]Yuan
	if e = json.Unmarshal([]byte(converted), &tiers); e != nil || tiers["2"] != 900 {
		t.Fatal("legacy tier conversion", converted, e)
	}
	t.Log("CDN 19.99 yuan persisted exactly; membership 9.00 x2 creates 18.00 CDN order and 1800 internal platform cents; old tier migration parsed exactly")
}
