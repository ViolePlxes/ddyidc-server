package service

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	productdomain "ddyidc/internal/product/domain"
	"ddyidc/internal/product/specification"
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
	GroupID            string  `json:"groupId"`
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
		in.ProductGroupID = in.GroupID
	}
	if in.ProductGroupID == "" {
		in.ProductGroupID = "0"
	}
	if err := s.validateLocalProductGroup(in.ProductGroupID); err != nil {
		return model.UpstreamProduct{}, err
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
	var page *contracts.OrderPage
	if optional, ok := p.(contracts.OptionalProvider); ok {
		page, _ = optional.GetOrderPage(in.UpstreamProductID)
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
			if e := ensurePublishedLocalProduct(tx, &v, detail, page); e != nil {
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
		in.ProductGroupID = in.GroupID
	}
	if in.ProductGroupID == "" {
		in.ProductGroupID = "0"
	}
	if err = s.validateLocalProductGroup(in.ProductGroupID); err != nil {
		return v, err
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
	if err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Table(v.TableName()).Where("id = ?", key).Save(&v).Error; err != nil {
			return err
		}
		if v.ProductID != "" && v.ProductID != "0" {
			return tx.Table((productdomain.Product{}).TableName()).Where("id = ?", v.ProductID).Update("group_id", v.ProductGroupID).Error
		}
		return nil
	}); err != nil {
		return v, err
	}
	return v, nil
}

func (s *Store) ConnectProducts(supplierID, groupID string, items []ProductInput) ([]model.UpstreamProduct, error) {
	if strings.TrimSpace(supplierID) == "" {
		return nil, fmt.Errorf("请选择供应商")
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("请选择要对接的上游商品")
	}
	if len(items) > 20 {
		return nil, fmt.Errorf("单次最多对接 20 个商品")
	}
	if groupID == "" {
		groupID = "0"
	}
	if err := s.validateLocalProductGroup(groupID); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.connectors[supplierID] {
		s.mu.Unlock()
		return nil, fmt.Errorf("该供应商正在执行商品对接，请稍候")
	}
	s.connectors[supplierID] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.connectors, supplierID)
		s.mu.Unlock()
	}()

	results := make([]model.UpstreamProduct, 0, len(items))
	for index, input := range items {
		if index > 0 {
			time.Sleep(2 * time.Second)
		}
		input.SupplierID = supplierID
		input.ProductGroupID = groupID
		input.Mode = "sync"
		product, err := s.AddProduct(input)
		if err != nil {
			return results, fmt.Errorf("对接上游商品 %d 失败：%w", input.UpstreamProductID, err)
		}
		results = append(results, product)
	}
	return results, nil
}

func (s *Store) validateLocalProductGroup(groupID string) error {
	if groupID == "" || groupID == "0" || s.isMemory() {
		return nil
	}
	id, err := strconv.ParseInt(groupID, 10, 64)
	if err != nil || id <= 0 {
		return fmt.Errorf("本地商品分组无效")
	}
	var group productdomain.ProductGroup
	if err := s.db.Table(group.TableName()).Where("id = ?", id).First(&group).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("本地商品分组不存在")
	} else if err != nil {
		return err
	}
	if group.ParentID == 0 {
		return fmt.Errorf("请选择二级商品分组")
	}
	return nil
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
	var page *contracts.OrderPage
	if optional, ok := p.(contracts.OptionalProvider); ok {
		page, _ = optional.GetOrderPage(v.UpstreamProductID)
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
			if e := ensurePublishedLocalProduct(tx, &v, d, page); e != nil {
				return e
			}
		}
		return tx.Table(v.TableName()).Where("id = ?", key).Save(&v).Error
	})
}

func ensurePublishedLocalProduct(tx *gorm.DB, mapping *model.UpstreamProduct, detail *contracts.UpstreamProductDetail, pages ...*contracts.OrderPage) error {
	var page *contracts.OrderPage
	if len(pages) > 0 {
		page = pages[0]
	}
	saveSnapshot := func() error {
		localID, _ := strconv.ParseInt(mapping.ProductID, 10, 64)
		return specification.Save(tx, localID, specification.Capture(detail, page, mapping.Name, mapping.Price))
	}
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
			if err := tx.Table((productdomain.Product{}).TableName()).Where("id = ?", localID).Updates(values).Error; err != nil {
				return err
			}
			return saveSnapshot()
		}
	}
	local := productdomain.Product{
		GroupID: groupID, Name: name, ModuleType: detail.Type, PayType: 1,
		Stock: stock, Status: 1, Hidden: mapping.Hidden,
		Summary: string(summaryRunes), Description: description,
	}
	var maxSort *int
	if e := tx.Table(local.TableName()).Where("group_id = ?", groupID).Select("MAX(sort)").Scan(&maxSort).Error; e != nil {
		return e
	}
	if maxSort != nil {
		local.Sort = *maxSort + 1
	}
	if e := tx.Table(local.TableName()).Create(&local).Error; e != nil {
		return e
	}
	mapping.ProductID = strconv.FormatInt(local.ID, 10)
	return saveSnapshot()
}
