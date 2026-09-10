package service

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	productdomain "ddyidc/internal/product/domain"
	"ddyidc/internal/upstream/model"
	"ddyidc/internal/upstream/provider/contracts"
	"gorm.io/gorm"
)

type ProductInput struct {
	SupplierID         string  `json:"supplierId"`
	UpstreamProductID  int     `json:"upstreamProductId"`
	Name               string  `json:"name"`
	Description        string  `json:"description"`
	ProductGroupID     string  `json:"productGroupId"`
	ProfitType         int     `json:"profitType"`
	ProfitValue        float64 `json:"profitValue"`
	RenewProfitType    int     `json:"renewProfitType"`
	RenewProfitValue   float64 `json:"renewProfitValue"`
	UpgradeProfitType  int     `json:"upgradeProfitType"`
	UpgradeProfitValue float64 `json:"upgradeProfitValue"`
	PriceBasis         string  `json:"priceBasis"`
	Mode               string  `json:"mode"`
	AutoSetup          bool    `json:"autoSetup"`
}

func CalculatePrice(upstream, rate float64, profitType int, profit float64) float64 {
	cost := upstream * rate
	if profitType == 1 {
		return cost + profit
	}
	return cost * (1 + profit/100)
}
func (s *Store) PullProducts(supplierID string) (interface{}, error) {
	p, e := s.ProviderFor(supplierID)
	if e != nil {
		return nil, e
	}
	return p.GetProductList()
}

// GetProductDetail returns the supplier's current agent view for a mapped
// product. Callers should retain locally persisted prices as their fallback.
func (s *Store) GetProductDetail(supplierID string, upstreamProductID int) (*contracts.UpstreamProductDetail, error) {
	p, err := s.ProviderFor(supplierID)
	if err != nil {
		return nil, err
	}
	return p.GetProductDetail(upstreamProductID)
}

// GetOrderPage exposes optional provider purchase configuration without
// coupling the service layer to a concrete finance/cloud implementation.
func (s *Store) GetOrderPage(supplierID string, upstreamProductID int) (*contracts.OrderPage, error) {
	p, err := s.ProviderFor(supplierID)
	if err != nil {
		return nil, err
	}
	configurable, ok := p.(contracts.OptionalProvider)
	if !ok {
		return nil, fmt.Errorf("upstream provider %T does not support order-page configuration", p)
	}
	return configurable.GetOrderPage(upstreamProductID)
}

// GetOrderPageByLocalProduct resolves the published local SKU to its supplier
// mapping before reusing the provider order-page cache. It never performs a
// catalogue pull and therefore also respects qkidc's single-product policy.
func (s *Store) GetOrderPageByLocalProduct(localProductID string) (*contracts.OrderPage, error) {
	mapping, err := s.findProductByLocalID(localProductID)
	if err != nil {
		return nil, err
	}
	return s.GetOrderPage(mapping.SupplierID, mapping.UpstreamProductID)
}
func (s *Store) AddProduct(in ProductInput) (model.UpstreamProduct, error) {
	// product.upstream_products.product_group_id is bigint unsigned. The
	// upstream catalogue does not require a local group, so use its explicit
	// sentinel value instead of passing an empty string to MySQL.
	if in.ProductGroupID == "" {
		in.ProductGroupID = "0"
	}
	supplier, e := s.GetSupplier(in.SupplierID)
	if e != nil {
		return model.UpstreamProduct{}, e
	}
	p, e := s.ProviderFor(in.SupplierID)
	if e != nil {
		return model.UpstreamProduct{}, e
	}
	detail, e := p.GetProductDetail(in.UpstreamProductID)
	if e != nil {
		return model.UpstreamProduct{}, e
	}
	if in.Name == "" {
		in.Name = detail.Name
	}
	if in.Description == "" {
		in.Description = detail.Description
	}
	if in.Mode == "" {
		in.Mode = "only_api"
	}
	if in.PriceBasis == "" {
		in.PriceBasis = "upstream"
	}
	v := model.UpstreamProduct{SupplierID: in.SupplierID, UpstreamProductID: in.UpstreamProductID, Name: in.Name, Description: in.Description, ProductGroupID: in.ProductGroupID, ProfitType: in.ProfitType, ProfitValue: in.ProfitValue, RenewProfitType: in.RenewProfitType, RenewProfitValue: in.RenewProfitValue, UpgradeProfitType: in.UpgradeProfitType, UpgradeProfitValue: in.UpgradeProfitValue, AutoSetup: in.AutoSetup, Mode: in.Mode, PriceBasis: in.PriceBasis, UpstreamPrice: detail.Price, Price: CalculatePrice(detail.Price, supplier.Rate, in.ProfitType, in.ProfitValue), Cycle: detail.Cycle, PayType: detail.PayType}
	if s.isMemory() {
		s.mu.Lock()
		v.ID = id()
		v.CreatedAt = now()
		v.UpdatedAt = v.CreatedAt
		s.products[v.ID] = v
		s.mu.Unlock()
	} else if in.Mode == "sync" {
		if e := s.db.Transaction(func(tx *gorm.DB) error {
			var existing model.UpstreamProduct
			findErr := tx.Table(v.TableName()).Where("supplier_id = ? AND upstream_product_id = ?", in.SupplierID, in.UpstreamProductID).First(&existing).Error
			if findErr == nil {
				v.ID = existing.ID
				v.ProductID = existing.ProductID
				v.CreatedAt = existing.CreatedAt
			} else if !errors.Is(findErr, gorm.ErrRecordNotFound) {
				return findErr
			}
			if e := ensurePublishedLocalProduct(tx, &v, detail); e != nil {
				return e
			}
			if findErr == nil {
				return tx.Table(v.TableName()).Where("id = ?", v.ID).Save(&v).Error
			}
			return tx.Table(v.TableName()).Create(&v).Error
		}); e != nil {
			return model.UpstreamProduct{}, e
		}
	} else if e := s.db.Table(v.TableName()).Create(&v).Error; e != nil {
		return model.UpstreamProduct{}, e
	}
	return v, nil
}

// UpdateProduct preserves the mapped local SKU while allowing the complete
// ZJMF pricing policy to round-trip through the administration dialog.
func (s *Store) UpdateProduct(key string, in ProductInput) (model.UpstreamProduct, error) {
	v, err := s.findProduct(key)
	if err != nil {
		return v, err
	}
	if in.ProductGroupID == "" {
		in.ProductGroupID = "0"
	}
	if in.PriceBasis == "" {
		in.PriceBasis = v.PriceBasis
	}
	if in.Name != "" {
		v.Name = in.Name
	}
	v.Description = in.Description
	v.ProductGroupID = in.ProductGroupID
	v.ProfitType = in.ProfitType
	v.ProfitValue = in.ProfitValue
	v.RenewProfitType = in.RenewProfitType
	v.RenewProfitValue = in.RenewProfitValue
	v.UpgradeProfitType = in.UpgradeProfitType
	v.UpgradeProfitValue = in.UpgradeProfitValue
	v.PriceBasis = in.PriceBasis
	v.AutoSetup = in.AutoSetup
	supplier, err := s.GetSupplier(v.SupplierID)
	if err != nil {
		return v, err
	}
	v.Price = CalculatePrice(v.UpstreamPrice, supplier.Rate, v.ProfitType, v.ProfitValue)
	if s.isMemory() {
		s.mu.Lock()
		v.UpdatedAt = now()
		s.products[key] = v
		s.mu.Unlock()
		return v, nil
	}
	if err = s.db.Table(v.TableName()).Where("id = ?", key).Save(&v).Error; err != nil {
		return v, err
	}
	return v, nil
}
func (s *Store) ListProducts() (out []model.UpstreamProduct) {
	if s.isMemory() {
		s.mu.RLock()
		for _, v := range s.products {
			out = append(out, v)
		}
		s.mu.RUnlock()
		sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
		return
	}
	if s.db.Table((model.UpstreamProduct{}).TableName()).Order("created_at DESC").Find(&out).Error != nil {
		return nil
	}
	return
}
func (s *Store) findProduct(key string) (model.UpstreamProduct, error) {
	if s.isMemory() {
		s.mu.RLock()
		v, ok := s.products[key]
		s.mu.RUnlock()
		if !ok {
			return model.UpstreamProduct{}, fmt.Errorf("upstream product not found")
		}
		return v, nil
	}
	var v model.UpstreamProduct
	e := s.db.Table((model.UpstreamProduct{}).TableName()).Where("id = ?", key).First(&v).Error
	if e == gorm.ErrRecordNotFound {
		return model.UpstreamProduct{}, fmt.Errorf("upstream product not found")
	}
	return v, e
}
func (s *Store) SyncProduct(key string) error {
	v, e := s.findProduct(key)
	if e != nil {
		return e
	}
	p, e := s.ProviderFor(v.SupplierID)
	if e != nil {
		return e
	}
	d, e := p.GetProductDetail(v.UpstreamProductID)
	if e != nil {
		return e
	}
	supplier, e := s.GetSupplier(v.SupplierID)
	if e != nil {
		return e
	}
	v.UpstreamPrice = d.Price
	v.Price = CalculatePrice(d.Price, supplier.Rate, v.ProfitType, v.ProfitValue)
	v.Cycle = d.Cycle
	v.PayType = d.PayType
	if s.isMemory() {
		s.mu.Lock()
		v.UpdatedAt = now()
		s.products[key] = v
		s.mu.Unlock()
		return nil
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if v.Mode == "sync" {
			if e := ensurePublishedLocalProduct(tx, &v, d); e != nil {
				return e
			}
		}
		return tx.Table(v.TableName()).Where("id = ?", key).Save(&v).Error
	})
}

func ensurePublishedLocalProduct(tx *gorm.DB, mapping *model.UpstreamProduct, detail *contracts.UpstreamProductDetail) error {
	groupID, _ := strconv.ParseInt(mapping.ProductGroupID, 10, 64)
	var stock *int
	if detail.Stock >= 0 {
		stockValue := detail.Stock
		stock = &stockValue
	}
	description := mapping.Description
	if description == "" {
		description = detail.Description
	}
	name := strings.TrimSpace(mapping.Name)
	if name == "" {
		name = detail.Name
	}
	summaryRunes := []rune(strings.Join(strings.Fields(description), " "))
	if len(summaryRunes) > 255 {
		summaryRunes = summaryRunes[:255]
	}
	values := map[string]any{
		"group_id": groupID, "name": name, "module_type": detail.Type,
		"pay_type": 1, "stock": stock, "status": 1, "hidden": mapping.Hidden,
		"summary": string(summaryRunes), "description": description,
	}
	if localID, err := strconv.ParseInt(mapping.ProductID, 10, 64); err == nil && localID > 0 {
		var count int64
		if e := tx.Table((productdomain.Product{}).TableName()).Where("id = ?", localID).Count(&count).Error; e != nil {
			return e
		}
		if count > 0 {
			return tx.Table((productdomain.Product{}).TableName()).Where("id = ?", localID).Updates(values).Error
		}
	}
	local := productdomain.Product{
		GroupID: groupID, Name: name, ModuleType: detail.Type, PayType: 1,
		Stock: stock, Status: 1, Hidden: mapping.Hidden,
		Summary: string(summaryRunes), Description: description,
	}
	if e := tx.Table(local.TableName()).Create(&local).Error; e != nil {
		return e
	}
	mapping.ProductID = strconv.FormatInt(local.ID, 10)
	return nil
}
