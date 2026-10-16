package instance

import (
	dom "ddyidc/internal/instance/domain"
	product "ddyidc/internal/product/domain"
	"ddyidc/internal/product/specification"
	"ddyidc/internal/upstream/model"
	"ddyidc/internal/upstream/provider/contracts"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strconv"
	"strings"
)

func sameSupplier(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// The caller holds the supplier row lock: competing edits/imports cannot create
// duplicate mappings. This creates only a hidden, non-saleable local reference.
func resolveEditedProduct(tx *gorm.DB, host dom.Host, detail *contracts.UpstreamProductDetail) (*product.Product, error) {
	if host.UpstreamProviderID == nil || detail == nil || detail.ID <= 0 || strings.TrimSpace(detail.Name) == "" {
		return nil, errors.New("上游商品信息不完整，未修改绑定")
	}
	var mappings []model.UpstreamProduct
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("supplier_id=? AND upstream_product_id=?", *host.UpstreamProviderID, detail.ID).Limit(2).Find(&mappings).Error; err != nil {
		return nil, err
	}
	if len(mappings) > 1 {
		return nil, errors.New("该上游商品存在多个本地关联，请先修正对接关系")
	}
	if len(mappings) == 1 {
		id, _ := strconv.ParseInt(mappings[0].ProductID, 10, 64)
		if id > 0 {
			var local product.Product
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&local, "id=?", id).Error
			if err == nil {
				if local.TenantID != 0 && local.TenantID != host.TenantID {
					return nil, errors.New("商品不属于当前租户，未修改绑定")
				}
				var references int64
				if err := tx.Model(&model.UpstreamProduct{}).Where("product_id=?", local.ID).Count(&references).Error; err != nil {
					return nil, err
				}
				if references != 1 {
					return nil, errors.New("本地商品存在多个供应商关联，请先修正对接关系")
				}
				// Refresh this instance's service type without rewriting the
				// shared catalogue entry or the original order snapshot.
				if strings.TrimSpace(detail.Type) != "" {
					local.ModuleType = detail.Type
				}
				return &local, nil
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, err
			}
		}
	}
	if strings.TrimSpace(detail.Type) == "" {
		return nil, errors.New("上游商品未返回服务类型，未修改绑定")
	}
	var group product.ProductGroup
	groupName := fmt.Sprintf("实例专用商品-%d", *host.UpstreamProviderID)
	err := tx.Where("tenant_id=0 AND name=? AND status=2", groupName).First(&group).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		group = product.ProductGroup{TenantID: 0, Name: groupName, Status: 2}
		err = tx.Create(&group).Error
	}
	if err != nil {
		return nil, err
	}
	local := product.Product{TenantID: 0, GroupID: group.ID, Name: detail.Name, ModuleType: detail.Type, PayType: 1, Status: 2, Hidden: true}
	if err = tx.Create(&local).Error; err != nil {
		return nil, err
	}
	mapping := model.UpstreamProduct{}
	if len(mappings) == 1 {
		mapping = mappings[0]
	}
	mapping.SupplierID = strconv.FormatInt(*host.UpstreamProviderID, 10)
	mapping.UpstreamProductID = detail.ID
	mapping.ProductID = strconv.FormatInt(local.ID, 10)
	mapping.ProductGroupID = strconv.FormatInt(group.ID, 10)
	mapping.Name = detail.Name
	mapping.Description = detail.Description
	mapping.Hidden = true
	mapping.Mode = "only_api"
	mapping.PriceBasis = "upstream"
	mapping.Price = detail.Price
	mapping.UpstreamPrice = detail.Price
	mapping.Cycle = detail.Cycle
	mapping.PayType = detail.PayType
	if mapping.ID == "" {
		err = tx.Create(&mapping).Error
	} else {
		err = tx.Save(&mapping).Error
	}
	if err != nil {
		return nil, err
	}
	if err = specification.Save(tx, local.ID, specification.Capture(detail, nil, detail.Name, detail.Price)); err != nil {
		return nil, err
	}
	return &local, nil
}
