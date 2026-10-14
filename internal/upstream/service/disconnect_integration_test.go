package service

import (
	order "ddyidc/internal/order/domain"
	product "ddyidc/internal/product/domain"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"ddyidc/internal/upstream/model"
	"fmt"
	"strconv"
	"testing"
)

func TestProductDisconnectHistoryMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	for _, kind := range []string{"unused", "order_item", "cancelled_procurement", "refunded_procurement", "deleted_machine"} {
		t.Run(kind, func(t *testing.T) {
			tx := db.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			defer tx.Rollback()
			p := product.Product{Name: "isolated disconnect " + kind, Status: 1}
			if e := tx.Create(&p).Error; e != nil {
				t.Fatal(e)
			}
			mapping := model.UpstreamProduct{SupplierID: strconv.FormatInt(snowflake.GenID(), 10), UpstreamProductID: 7, ProductID: strconv.FormatInt(p.ID, 10), Name: p.Name, ProductGroupID: "0", Mode: "sync"}
			if e := tx.Create(&mapping).Error; e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "order_item":
				v := order.OrderItem{OrderID: snowflake.GenID(), ProductID: p.ID, ProductName: p.Name, ConfigSnapshot: "{}", Qty: 1}
				if e := tx.Create(&v).Error; e != nil {
					t.Fatal(e)
				}
			case "cancelled_procurement", "refunded_procurement":
				status := uint8(3)
				if kind == "refunded_procurement" {
					status = 4
				}
				o := order.Order{OrderNo: fmt.Sprintf("GUARD-%d", snowflake.GenID()), CustomerID: snowflake.GenID(), Status: status}
				if e := tx.Create(&o).Error; e != nil {
					t.Fatal(e)
				}
				u := model.UpstreamOrder{OrderID: strconv.FormatInt(o.ID, 10), SupplierID: mapping.SupplierID, UpstreamProductID: mapping.UpstreamProductID, Status: "failed", UpstreamOrderID: "history"}
				if e := tx.Create(&u).Error; e != nil {
					t.Fatal(e)
				}
			case "deleted_machine":
				iid := snowflake.GenID()
				e := tx.Exec("INSERT INTO instance.instances (id,instance_no,customer_id,order_id,order_item_id,product_id,product_name,module_type,config_snapshot,status,deleted_at) VALUES (?,?,?,?,?,?,?,'cloud','{}',6,NOW())", iid, fmt.Sprint(iid), snowflake.GenID(), snowflake.GenID(), snowflake.GenID(), p.ID, p.Name).Error
				if e != nil {
					t.Fatal(e)
				}
			}
			store := NewStore(tx, nil)
			e := store.DeleteProducts([]string{mapping.ID})
			if kind == "unused" {
				if e != nil {
					t.Fatal(e)
				}
				var n int64
				tx.Model(&product.Product{}).Where("id=?", p.ID).Count(&n)
				if n != 0 {
					t.Fatal("unused product retained")
				}
				return
			}
			if e == nil {
				t.Fatal("associated product deleted")
			}
			if e = store.SetHidden(mapping.ID, true); e != nil {
				t.Fatal(e)
			}
			var retained product.Product
			if e = tx.First(&retained, "id=?", p.ID).Error; e != nil || !retained.Hidden {
				t.Fatal("hidden product must be retained", e)
			}
			var saved model.UpstreamProduct
			if e = tx.First(&saved, "id=?", mapping.ID).Error; e != nil || !saved.Hidden {
				t.Fatal("mapping must be retained and hidden", e)
			}
		})
	}
	t.Log("isolated MySQL: unused pair deletion; all order items and cancelled/refunded procurement plus deleted machines protect deletion; hide retains pair")
}
