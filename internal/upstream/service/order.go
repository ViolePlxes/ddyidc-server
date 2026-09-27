package service

import (
	"context"
	"ddyidc/internal/upstream/provider/contracts"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"ddyidc/internal/upstream/model"
	"ddyidc/internal/upstream/provider"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ProvisionInput struct {
	OrderID, LocalProductID, CustomerID, InstanceID, UnitID string
	Quantity                                                int
	Cycle                                                   string
	ConfigOptions                                           map[string]string
	Hostname, Password                                      string
}

type ProvisionResult struct {
	ActivatedAt, ExpiresAt            *time.Time
	SupplierID                        string
	UpstreamProductID                 int
	InvoiceID, HostID, Status         string
	Cost, BalanceBefore, BalanceAfter float64
}

// ProvisionOrder performs the verified finance flow synchronously. The
// unique order.upstream_orders(order_id) row is created before network I/O,
// which makes double-clicks and repeated HTTP submissions non-chargeable.
func (s *Store) ProvisionOrder(ctx context.Context, in ProvisionInput) (*ProvisionResult, error) {
	product, err := s.findProductByLocalID(in.LocalProductID)
	if err != nil {
		return nil, fmt.Errorf("not an upstream product: %w", err)
	}

	link := model.UpstreamOrder{}
	table := link.TableName()
	if in.UnitID != "" {
		table = "order.unit_procurements"
	}
	if s.isMemory() {
		return nil, fmt.Errorf("real provisioning requires database storage")
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Table(table).Clauses(clause.Locking{Strength: "UPDATE"})
		if in.UnitID != "" {
			query = query.Where("id=? AND order_id=?", in.UnitID, in.OrderID)
		} else {
			query = query.Where("order_id=?", in.OrderID)
		}
		findErr := query.First(&link).Error
		if findErr == nil {
			if link.Status == "paid" || link.Status == "active" {
				return nil
			}
			return fmt.Errorf("采购已发起，结果待核实；禁止重复下单")
		}
		if findErr != gorm.ErrRecordNotFound {
			return findErr
		}
		if err := s.ValidateLossGuard(product.ID, product.Price); err != nil {
			return err
		}
		if !product.AutoSetup {
			return fmt.Errorf("商品未启用自动开通")
		}

		link = model.UpstreamOrder{ID: in.UnitID, OrderID: in.OrderID, SupplierID: product.SupplierID, UpstreamProductID: product.UpstreamProductID, UpstreamOrderID: "pending", Status: "processing", AttemptCount: 1}
		return tx.Table(table).Create(&link).Error
	})
	if err != nil {
		return nil, err
	}
	if link.Status == "paid" || link.Status == "active" {
		return s.provisionResultWithDates(ctx, link), nil
	}

	p, err := s.ProviderFor(product.SupplierID)
	if err != nil {
		s.failProvision(table, &link, err)
		return nil, err
	}
	contextProvider, ok := p.(interface {
		CreateOrderContext(context.Context, provider.OrderInfo) (*provider.UpstreamOrderResult, error)
	})
	if !ok {
		s.failProvision(table, &link, fmt.Errorf("供应商尚未支持有界采购"))
		return nil, fmt.Errorf("供应商尚未支持有界采购")
	}
	result, err := contextProvider.CreateOrderContext(ctx, provider.OrderInfo{
		OrderID: in.OrderID, ProductID: product.UpstreamProductID, Quantity: in.Quantity,
		Cycle: in.Cycle, Amount: product.UpstreamPrice, CustomerID: in.CustomerID,
		ConfigOptions: in.ConfigOptions, Hostname: in.Hostname, Password: in.Password,
		DownstreamURL:    strings.TrimSpace(os.Getenv("DDYIDC_UPSTREAM_CALLBACK_URL")),
		DownstreamToken:  strings.TrimSpace(os.Getenv("DDYIDC_UPSTREAM_CALLBACK_TOKEN")),
		DownstreamHostID: in.InstanceID,
		Checkpoint: func(stage, invoiceID, hostID string) error {
			values := map[string]any{"status": stage, "updated_at": now()}
			if invoiceID != "" {
				values["upstream_order_id"] = invoiceID
			}
			if hostID != "" {
				values["upstream_host_id"] = hostID
			}
			return s.db.WithContext(ctx).Table(table).Where("id=?", link.ID).Updates(values).Error
		},
	})
	if err != nil {
		s.failProvision(table, &link, err)
		return nil, err
	}
	link.UpstreamOrderID, link.UpstreamHostID, link.Status = result.OrderID, result.HostID, result.Status
	link.Cost, link.BalanceBefore, link.BalanceAfter, link.LastError = result.Cost, result.BalanceBefore, result.BalanceAfter, ""
	if err := s.db.WithContext(ctx).Table(table).Where("id = ?", link.ID).Updates(map[string]any{
		"upstream_order_id": link.UpstreamOrderID, "upstream_host_id": link.UpstreamHostID,
		"status": link.Status, "cost": link.Cost, "balance_before": link.BalanceBefore,
		"balance_after": link.BalanceAfter, "last_error": "", "updated_at": now(),
	}).Error; err != nil {
		return nil, fmt.Errorf("上游已扣款但本地关联保存失败: %w", err)
	}
	return s.provisionResultWithDates(ctx, link), nil
}

func (s *Store) findProductByLocalID(localProductID string) (model.UpstreamProduct, error) {
	var value model.UpstreamProduct
	err := s.db.Table(value.TableName()).Where("product_id = ?", localProductID).First(&value).Error
	if err == gorm.ErrRecordNotFound {
		return value, fmt.Errorf("upstream product not found")
	}
	return value, err
}

func (s *Store) failProvision(table string, link *model.UpstreamOrder, failure error) {
	// A timeout or invalid response is not evidence that the supplier did not charge.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.db.WithContext(ctx).Table(table).Where("id = ?", link.ID).Updates(map[string]any{"status": "unknown", "last_error": "采购结果未确认，需核对远端订单，禁止重复下单", "updated_at": now()}).Error
}

func provisionResult(link model.UpstreamOrder) *ProvisionResult {
	return &ProvisionResult{SupplierID: link.SupplierID, UpstreamProductID: link.UpstreamProductID, InvoiceID: link.UpstreamOrderID, HostID: link.UpstreamHostID, Status: link.Status, Cost: link.Cost, BalanceBefore: link.BalanceBefore, BalanceAfter: link.BalanceAfter}
}

// Dates are provider facts, never inferred from a local purchase duration.
// A read failure must not discard the persisted purchase or trigger another charge.
func (s *Store) provisionResultWithDates(ctx context.Context, link model.UpstreamOrder) *ProvisionResult {
	result := provisionResult(link)
	hostID, err := strconv.ParseInt(link.UpstreamHostID, 10, 64)
	if err != nil || hostID <= 0 {
		return result
	}
	p, err := s.ProviderFor(link.SupplierID)
	if err != nil {
		return result
	}
	scoped, ok := p.(interface {
		WithInstanceContext(context.Context) contracts.InstanceProvider
	})
	if !ok {
		return result
	}
	readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	detail, err := scoped.WithInstanceContext(readCtx).GetInstanceDetail(hostID)
	if err == nil {
		applyProvisionDates(result, detail)
	}
	return result
}
func applyProvisionDates(result *ProvisionResult, detail *contracts.InstanceDetail) {
	if detail == nil || detail.ID != result.HostID {
		return
	}
	if detail.ProductID != "" && detail.ProductID != strconv.Itoa(result.UpstreamProductID) {
		return
	}
	if detail.ActivatedAt != nil && !detail.ActivatedAt.IsZero() {
		v := *detail.ActivatedAt
		result.ActivatedAt = &v
	}
	if detail.ExpiresAt != nil && !detail.ExpiresAt.IsZero() {
		v := *detail.ExpiresAt
		result.ExpiresAt = &v
	}
}
