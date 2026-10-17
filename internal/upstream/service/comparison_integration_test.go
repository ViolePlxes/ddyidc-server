package service

import (
	product "ddyidc/internal/product/domain"
	"ddyidc/internal/shared/testenv"
	"ddyidc/internal/upstream/model"
	"ddyidc/internal/upstream/provider"
	"ddyidc/internal/upstream/provider/contracts"
	"fmt"
	"strconv"
	"testing"
)

type comparisonProviderFixture struct{ contracts.Provider }

func (comparisonProviderFixture) GetProductList() ([]contracts.UpstreamProduct, error) {
	out := []contracts.UpstreamProduct{}
	for i := 1; i <= 137; i++ {
		out = append(out, contracts.UpstreamProduct{ID: i, Name: fmt.Sprintf("fixture %d", i), GroupID: 17})
	}
	return out, nil
}
func (comparisonProviderFixture) GetProductCategories() ([]contracts.CatalogCategory, error) {
	return []contracts.CatalogCategory{{ID: "first:8", ParentID: "0", Name: "海外"}, {ID: "17", ParentID: "first:8", Name: "香港"}}, nil
}
func (comparisonProviderFixture) GetProductDetail(id int) (*contracts.UpstreamProductDetail, error) {
	return &contracts.UpstreamProductDetail{UpstreamProduct: contracts.UpstreamProduct{ID: id, Name: fmt.Sprintf("fixture %d", id), Type: "cloud", Price: 9, Cycle: "monthly", Stock: -1}}, nil
}
func TestComparisonCatalogAndConnectMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	if _, registered := provider.ModuleForVariant("comparison-fixture"); !registered {
		if err := provider.RegisterModule(provider.ModuleRegistration{Manifest: provider.Module{ID: "comparison-fixture", Name: "隔离模块", Version: "1", ContractVersion: 1, Capabilities: map[string]string{"purchase": "implemented"}}, Constructors: map[string]provider.Constructor{"comparison-fixture": func(contracts.Supplier) contracts.Provider { return comparisonProviderFixture{} }}}); err != nil {
			t.Fatal(err)
		}
	}
	store := NewStore(tx, []byte("12345678901234567890123456789012"))
	cipher, e := store.encrypt("isolated")
	if e != nil {
		t.Fatal(e)
	}
	supplier := model.Supplier{Name: "comparison fixture", Type: "comparison-fixture", URL: "https://fixture.invalid", Username: "isolated", Token: cipher, Secret: cipher, Rate: 1, Status: 1}
	if e = tx.Create(&supplier).Error; e != nil {
		t.Fatal(e)
	}
	root := product.ProductGroup{Name: "一级", Status: 1}
	if e = tx.Create(&root).Error; e != nil {
		t.Fatal(e)
	}
	child := product.ProductGroup{Name: "二级", ParentID: root.ID, Status: 1}
	if e = tx.Create(&child).Error; e != nil {
		t.Fatal(e)
	}
	cat, e := store.ComparisonCatalog(supplier.ID)
	if e != nil || len(cat.Items) != 137 || len(cat.Categories) != 2 {
		t.Fatal("catalog truncated", len(cat.Items), e)
	}
	input := ProductInput{SupplierID: supplier.ID, UpstreamProductID: 137, ProductGroupID: strconv.FormatInt(child.ID, 10), Mode: "sync", PriceBasis: "upstream", AutoSetup: true}
	m, e := store.AddProduct(input)
	if e != nil {
		t.Fatal(e)
	}
	if m.ProductID == "" || m.ProductID == "0" {
		t.Fatal("local SKU missing")
	}
	again, e := store.AddProduct(input)
	if e != nil || again.ID != m.ID || again.ProductID != m.ProductID {
		t.Fatal("duplicate connection created SKU", e)
	}
	var count int64
	tx.Table("product.products").Where("id=? AND group_id=?", m.ProductID, child.ID).Count(&count)
	if count != 1 {
		t.Fatal("wrong local category")
	}
	if e = store.SetHidden(m.ID, true); e != nil {
		t.Fatal(e)
	}
	if e = store.DeleteProducts([]string{m.ID}); e != nil {
		t.Fatal(e)
	}
	tx.Table("product.products").Where("id=?", m.ProductID).Count(&count)
	if count != 0 {
		t.Fatal("SKU retained after valid disconnect")
	}
	// Reads and catalogue mapping do not create orders, machines or financial rows.
	var events int64
	tx.Table("order.order_items").Where("product_id=?", m.ProductID).Count(&events)
	if events != 0 {
		t.Fatal("mapping placed an order")
	}
	t.Log("137-item real SQL catalogue/mapping workflow: connect, repeat, target category, hide and disconnect; provider fixture creates no orders")
}
