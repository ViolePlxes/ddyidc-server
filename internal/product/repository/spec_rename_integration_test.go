package product

import (
	"context"
	dom "ddyidc/internal/product/domain"
	"ddyidc/internal/shared/testenv"
	"testing"
)

// The admin edits one visible name field, so saving the specification must also
// rename the product and the instances that already exist for it.
func TestReplaceSpecsRenamesProductAndInstancesMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	repo := &GormRepository{db: tx}
	ctx := context.Background()
	const productID = 951400
	if err := tx.Exec("INSERT INTO product.products(id,group_id,name,module_type,status,hidden,created_at,updated_at) VALUES(?,1,'西部数码弹性云 · 西南枢纽','cloud',1,0,NOW(),NOW())", productID).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec("INSERT INTO instance.instances(id,customer_id,product_id,product_name,module_type,config_snapshot,instance_no,created_at,updated_at) VALUES(951401,99,?,'西部数码弹性云 · 西南枢纽','cloud','{}','HOST-951401',NOW(),NOW())", productID).Error; err != nil {
		t.Fatal(err)
	}
	spec := &dom.ProductSpec{Name: "四川", ProductID: productID}
	if err := repo.ReplaceSpecs(ctx, productID, []*dom.ProductSpec{spec}); err != nil {
		t.Fatal(err)
	}
	var productName, instanceName, specName string
	if err := tx.Raw("SELECT name FROM product.products WHERE id=?", productID).Scan(&productName).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Raw("SELECT product_name FROM instance.instances WHERE id=951401").Scan(&instanceName).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Raw("SELECT name FROM product.product_specs WHERE product_id=?", productID).Scan(&specName).Error; err != nil {
		t.Fatal(err)
	}
	if productName != "四川" || instanceName != "四川" || specName != "四川" {
		t.Fatalf("product=%q instance=%q spec=%q", productName, instanceName, specName)
	}
}
