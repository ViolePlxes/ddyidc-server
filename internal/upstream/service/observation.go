package service

import (
	"ddyidc/internal/product/specification"
	"ddyidc/internal/upstream/metrics"
	"ddyidc/internal/upstream/model"
	"ddyidc/internal/upstream/provider/contracts"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"reflect"
	"time"
)

type SupplierMetric struct {
	SupplierID string `json:"supplierId"`
	Name       string `json:"name"`
	metrics.Summary
}

func (s *Store) RequestMetrics() []SupplierMetric {
	snapshot := metrics.Default.Snapshot()
	rows := []SupplierMetric{}
	for _, supplier := range s.ListSuppliers() {
		rows = append(rows, SupplierMetric{supplier.ID, supplier.Name, snapshot[supplier.ID]})
	}
	return rows
}

type ChangeSummary struct {
	SupplierID            string `json:"supplierId"`
	UpstreamProductID     int    `json:"upstreamProductId"`
	CheckedAt             int64  `json:"checkedAt"`
	Price                 int    `json:"price"`
	Stock                 int    `json:"stock"`
	Specification         int    `json:"specification"`
	Unavailable           int    `json:"unavailable"`
	Unchanged             int    `json:"unchanged"`
	StockCompared         bool   `json:"stockCompared"`
	SpecificationCompared bool   `json:"specificationCompared"`
}

func compareSnapshot(before, after *specification.View, result *ChangeSummary) {
	if before == nil || after == nil {
		return
	}
	result.StockCompared = before.UpstreamStock != nil && after.UpstreamStock != nil
	if result.StockCompared && !jsonEqual(before.UpstreamStock, after.UpstreamStock) {
		result.Stock = 1
	}
	// Price and stock have independent counters; compare the stored supplier
	// specification rather than administrator overrides or generated timestamps.
	a, b := before.Spec, after.Spec
	a.ID = 0
	b.ID = 0
	a.ProductID = 0
	b.ProductID = 0
	a.Price = 0
	b.Price = 0
	result.SpecificationCompared = true
	if !reflect.DeepEqual(a, b) || (before.Options != nil && after.Options != nil && !jsonEqual(before.Options, after.Options)) {
		result.Specification = 1
	}
}
func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
func (s *Store) SyncProductWithSummary(key string) (result ChangeSummary, retErr error) {
	v, err := s.findProduct(key)
	if err != nil {
		return ChangeSummary{}, err
	}
	result = ChangeSummary{SupplierID: v.SupplierID, UpstreamProductID: v.UpstreamProductID}
	p, err := s.ProviderFor(v.SupplierID)
	if err != nil {
		return result, err
	}
	if fresh, ok := p.(interface{ InvalidateProduct(int) error }); ok {
		if err := fresh.InvalidateProduct(v.UpstreamProductID); err != nil {
			return result, err
		}
		// In-flight reads started during this refresh must not publish current
		// cache entries after the refresh finishes, including failure paths.
		defer func() {
			if err := fresh.InvalidateProduct(v.UpstreamProductID); err != nil && retErr == nil {
				retErr = err
			}
		}()
	}
	detail, err := p.GetProductDetail(v.UpstreamProductID)
	if errors.Is(err, contracts.ErrProductUnavailable) {
		if err = s.SetHidden(key, true); err != nil {
			return result, err
		}
		result.Unavailable = 1
		result.CheckedAt = time.Now().Unix()
		return result, s.saveCheck(v, result.CheckedAt)
	}
	if err != nil {
		return result, err
	}
	supplier, err := s.GetSupplier(v.SupplierID)
	if err != nil {
		return result, err
	}
	var page *contracts.OrderPage
	if optional, ok := p.(contracts.OptionalProvider); ok {
		page, _ = optional.GetOrderPage(v.UpstreamProductID)
	}
	var before *specification.View
	if !s.isMemory() && v.ProductID != "" && v.ProductID != "0" {
		var row specification.Snapshot
		e := s.db.Where("product_id=?", v.ProductID).Take(&row).Error
		if e == nil {
			before = &specification.View{}
			if e = json.Unmarshal([]byte(row.Data), before); e != nil {
				return result, e
			}
		} else if !errors.Is(e, gorm.ErrRecordNotFound) {
			return result, e
		}
	}
	price := CalculatePrice(detail.Price, supplier.Rate, v.ProfitType, v.ProfitValue)
	if v.UpstreamPrice != detail.Price || v.Price != price {
		result.Price = 1
	}
	after := specification.Capture(detail, page, v.Name, price)
	compareSnapshot(before, &after, &result)
	if result.Price+result.Stock+result.Specification == 0 {
		result.Unchanged = 1
	}
	v.UpstreamPrice = detail.Price
	v.Price = price
	v.Cycle = detail.Cycle
	v.PayType = detail.PayType
	if s.isMemory() {
		s.mu.Lock()
		v.UpdatedAt = now()
		s.products[key] = v
		s.mu.Unlock()
		result.CheckedAt = now()
		return result, nil
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if v.Mode == "sync" {
			if e := ensurePublishedLocalProduct(tx, &v, detail, page); e != nil {
				return e
			}
		}
		return tx.Table(v.TableName()).Where("id=?", key).Save(&v).Error
	})
	if err != nil {
		return result, err
	}
	result.CheckedAt = now()
	return result, s.saveCheck(v, result.CheckedAt)
}

// Reuse one existing sync_tasks row per mapping. No request metrics, diff
// history, new columns or new tables are persisted.
func (s *Store) saveCheck(v model.UpstreamProduct, stamp int64) error {
	if s.isMemory() {
		return nil
	}
	task := model.SyncTask{ID: v.ID, SupplierID: v.SupplierID, Type: "product_check", Status: "success", StartedAt: stamp, FinishedAt: stamp, TriggerBy: "admin", Total: 1, Progress: 1}
	return s.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.AssignmentColumns([]string{"status", "started_at", "finished_at", "total", "progress"})}).Create(&task).Error
}
