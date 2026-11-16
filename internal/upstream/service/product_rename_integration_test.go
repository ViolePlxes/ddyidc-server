package service

import (
	"testing"

	"ddyidc/internal/shared/testenv"
	"ddyidc/internal/upstream/model"
)

// Renaming a mapped product must reach the customer, because the product list
// reads product.products and the instance list reads the instance snapshot.
func TestProductRenamePropagatesToProductAndInstancesMySQL(t *testing.T) {
	database := testenv.MySQL(t)
	store := NewStore(database, nil)
	supplier := model.Supplier{Name: "rename fixture", Type: "west", Status: 1, URL: "https://fixture.invalid", Username: "fixture", Token: "cipher", Secret: "cipher", Rate: 1}
	if err := database.Create(&supplier).Error; err != nil {
		t.Fatal(err)
	}
	const productID, mappingID, instanceID = 951300, "951303", 951302
	t.Cleanup(func() {
		database.Exec("DELETE FROM instance.instances WHERE id=?", instanceID)
		database.Exec("DELETE FROM product.upstream_products WHERE id=?", mappingID)
		database.Exec("DELETE FROM product.products WHERE id=?", productID)
		database.Delete(&supplier)
	})
	if err := database.Exec("INSERT INTO product.products(id,group_id,name,module_type,status,hidden,created_at,updated_at) VALUES(?,1,'西部数码弹性云 · 西南枢纽','cloud',1,0,NOW(),NOW())", productID).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec("INSERT INTO instance.instances(id,customer_id,product_id,product_name,module_type,config_snapshot,instance_no,created_at,updated_at) VALUES(?,99,?,'西部数码弹性云 · 西南枢纽','cloud','{}','HOST-951302',NOW(),NOW())", instanceID, productID).Error; err != nil {
		t.Fatal(err)
	}
	mapping := model.UpstreamProduct{
		ID: mappingID, SupplierID: supplier.ID, UpstreamProductID: 37, ProductID: "951300",
		Name: "西部数码弹性云 · 西南枢纽", Mode: "sync", PriceBasis: "upstream",
		ProfitType: 2, RenewProfitType: 2, UpgradeProfitType: 2, AutoSetup: true,
		UpstreamPrice: 181, Price: 181, Cycle: "monthly",
	}
	if err := database.Create(&mapping).Error; err != nil {
		t.Fatal(err)
	}
	updated, err := store.UpdateProduct(mappingID, ProductInput{
		Name: "四川", Description: "四川机房", ProductGroupID: "0",
		ProfitType: 2, RenewProfitType: 2, UpgradeProfitType: 2,
		PriceBasis: "upstream", Mode: "sync", AutoSetup: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "四川" {
		t.Fatalf("mapping name = %q", updated.Name)
	}
	var productName, instanceName string
	if err := database.Raw("SELECT name FROM product.products WHERE id=?", productID).Scan(&productName).Error; err != nil {
		t.Fatal(err)
	}
	if productName != "四川" {
		t.Fatalf("customer product list still shows %q", productName)
	}
	if err := database.Raw("SELECT product_name FROM instance.instances WHERE id=?", instanceID).Scan(&instanceName).Error; err != nil {
		t.Fatal(err)
	}
	if instanceName != "四川" {
		t.Fatalf("instance list still shows %q", instanceName)
	}
}
