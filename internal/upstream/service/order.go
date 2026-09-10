package service

import (
	"fmt"
	"os"
	"strings"

	"ddyidc/internal/upstream/model"
	"ddyidc/internal/upstream/provider"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ProvisionInput struct {
	OrderID, LocalProductID, CustomerID, InstanceID string
	Quantity                                        int
	Cycle                                           string
	ConfigOptions                                   map[string]string
	Hostname, Password                              string
}

type ProvisionResult struct {
	SupplierID                        string
	UpstreamProductID                 int
	InvoiceID, HostID, Status         string
	Cost, BalanceBefore, BalanceAfter float64
}

// ProvisionOrder performs the verified finance flow synchronously. The
// unique order.upstream_orders(order_id) row is created before network I/O,
// which makes double-clicks and repeated HTTP submissions non-chargeable.
func (s *Store) ProvisionOrder(in ProvisionInput) (*ProvisionResult, error) {
	product, err := s.findProductByLocalID(in.LocalProductID)
	if err != nil {
		return nil, fmt.Errorf("not an upstream product: %w", err)
	}
	if err := s.ValidateLossGuard(product.ID, product.Price); err != nil {
		return nil, err
	}
	if !product.AutoSetup {
		return nil, fmt.Errorf("商品未启用自动开通")
	}

	link := model.UpstreamOrder{}
	if s.isMemory() {
		return nil, fmt.Errorf("real provisioning requires database storage")
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		findErr := tx.Table(link.TableName()).Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_id = ?", in.OrderID).First(&link).Error
		if findErr == nil {
			if link.Status == "paid" || link.Status == "active" {
				return nil
			}
			if link.Status == "processing" {
				return fmt.Errorf("订单正在开通，请勿重复支付")
			}
			if link.AttemptCount >= 2 && isQKIDCSupplierURL(s, product.SupplierID) {
				return fmt.Errorf("真实下单已达到最多2次限制")
			}
			link.Status, link.LastError = "processing", ""
			link.AttemptCount++
			return tx.Table(link.TableName()).Where("id = ?", link.ID).Updates(map[string]any{"status": link.Status, "last_error": "", "attempt_count": link.AttemptCount, "updated_at": now()}).Error
		}
		if findErr != gorm.ErrRecordNotFound {
			return findErr
		}
		link = model.UpstreamOrder{OrderID: in.OrderID, SupplierID: product.SupplierID, UpstreamProductID: product.UpstreamProductID, UpstreamOrderID: "pending", Status: "processing", AttemptCount: 1}
		return tx.Table(link.TableName()).Create(&link).Error
	})
	if err != nil {
		return nil, err
	}
	if link.Status == "paid" || link.Status == "active" {
		return provisionResult(link), nil
	}

	p, err := s.ProviderFor(product.SupplierID)
	if err != nil {
		s.failProvision(&link, err)
		return nil, err
	}
	result, err := p.CreateOrder(provider.OrderInfo{
		OrderID: in.OrderID, ProductID: product.UpstreamProductID, Quantity: in.Quantity,
		Cycle: in.Cycle, Amount: product.UpstreamPrice, CustomerID: in.CustomerID,
		ConfigOptions: in.ConfigOptions, Hostname: in.Hostname, Password: in.Password,
		DownstreamURL:    strings.TrimSpace(os.Getenv("DDYIDC_UPSTREAM_CALLBACK_URL")),
		DownstreamToken:  strings.TrimSpace(os.Getenv("DDYIDC_UPSTREAM_CALLBACK_TOKEN")),
		DownstreamHostID: in.InstanceID,
	})
	if err != nil {
		s.failProvision(&link, err)
		return nil, err
	}
	link.UpstreamOrderID, link.UpstreamHostID, link.Status = result.OrderID, result.HostID, result.Status
	link.Cost, link.BalanceBefore, link.BalanceAfter, link.LastError = result.Cost, result.BalanceBefore, result.BalanceAfter, ""
	if err := s.db.Table(link.TableName()).Where("id = ?", link.ID).Updates(map[string]any{
		"upstream_order_id": link.UpstreamOrderID, "upstream_host_id": link.UpstreamHostID,
		"status": link.Status, "cost": link.Cost, "balance_before": link.BalanceBefore,
		"balance_after": link.BalanceAfter, "last_error": "", "updated_at": now(),
	}).Error; err != nil {
		return nil, fmt.Errorf("上游已扣款但本地关联保存失败: %w", err)
	}
	return provisionResult(link), nil
}

func isQKIDCSupplierURL(s *Store, supplierID string) bool {
	supplier, err := s.GetSupplier(supplierID)
	if err != nil {
		return false
	}
	url := strings.ToLower(strings.TrimSpace(supplier.URL))
	return strings.Contains(url, "qkidc.cn")
}

func (s *Store) findProductByLocalID(localProductID string) (model.UpstreamProduct, error) {
	var value model.UpstreamProduct
	err := s.db.Table(value.TableName()).Where("product_id = ?", localProductID).First(&value).Error
	if err == gorm.ErrRecordNotFound {
		return value, fmt.Errorf("upstream product not found")
	}
	return value, err
}

func (s *Store) failProvision(link *model.UpstreamOrder, failure error) {
	message := failure.Error()
	if len(message) > 512 {
		message = message[:512]
	}
	_ = s.db.Table(link.TableName()).Where("id = ?", link.ID).Updates(map[string]any{"status": "failed", "last_error": message, "updated_at": now()}).Error
}

func provisionResult(link model.UpstreamOrder) *ProvisionResult {
	return &ProvisionResult{SupplierID: link.SupplierID, UpstreamProductID: link.UpstreamProductID, InvoiceID: link.UpstreamOrderID, HostID: link.UpstreamHostID, Status: link.Status, Cost: link.Cost, BalanceBefore: link.BalanceBefore, BalanceAfter: link.BalanceAfter}
}
