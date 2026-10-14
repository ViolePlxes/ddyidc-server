package service

import (
	"ddyidc/internal/upstream/model"
	"ddyidc/internal/upstream/provider/contracts"
	"strconv"
)

type ComparisonCatalog struct {
	Items      []model.CatalogProduct      `json:"items"`
	Categories []contracts.CatalogCategory `json:"categories"`
	Warning    string                      `json:"warning"`
	FetchedAt  int64                       `json:"fetchedAt"`
}

func (s *Store) ComparisonCatalog(supplierID string) (ComparisonCatalog, error) {
	p, e := s.catalogueProvider(supplierID)
	if e != nil {
		return ComparisonCatalog{}, e
	}
	rows, e := p.GetProductList()
	if e != nil {
		return ComparisonCatalog{}, e
	}
	out := ComparisonCatalog{Items: []model.CatalogProduct{}, Categories: []contracts.CatalogCategory{}, FetchedAt: now()}
	seen := map[int]bool{}
	for _, v := range rows {
		if v.ID <= 0 || seen[v.ID] {
			continue
		}
		seen[v.ID] = true
		out.Items = append(out.Items, model.CatalogProduct{ID: strconv.Itoa(v.ID), SupplierID: supplierID, UpstreamProductID: v.ID, Name: v.Name, Description: v.Description, Type: v.Type, GroupID: v.GroupID, PayType: v.PayType, Price: v.Price, Cycle: v.Cycle, CurrencyCode: v.CurrencyCode, Status: "active", SyncedAt: out.FetchedAt})
	}
	if cp, ok := p.(contracts.CatalogCategoryProvider); ok {
		if groups, e := cp.GetProductCategories(); e == nil {
			out.Categories = groups
		} else {
			out.Warning = "供应商分类信息暂不可用，未识别分类的商品仍完整展示。"
		}
	} else {
		out.Warning = "此供应商未提供分类层级，商品完整展示在未分组中。"
	}
	return out, nil
}
