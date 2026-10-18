package order

import (
	"context"
	rep "ddyidc/internal/order/repository"
	upstream "ddyidc/internal/upstream/service"
	"errors"
	"gorm.io/gorm"
)

// Shared by normal/points orders and prepaid group commits. No remote I/O is
// performed inside the transaction; quote preparation has already completed.
func (s *Service) checkPreparedSupplierTx(ctx context.Context, tx *gorm.DB, p *PreparedOrder) error {
	binding, err := rep.FindOrderSupplierBindingTx(ctx, tx, p.item.ProductID)
	if err != nil {
		return err
	}
	normalize := func(v string) string {
		if v == "0" {
			return ""
		}
		return v
	}
	if normalize(binding.SupplierID) != normalize(p.supplierID) || binding.UpstreamProductID != p.upstreamProductID {
		return errors.New("商品关联已变更，请刷新后重新下单")
	}
	if normalize(binding.SupplierID) == "" {
		return nil
	}
	if upstream.Default == nil {
		return errors.New("商品服务暂不可用，请稍后重试")
	}
	return upstream.Default.CheckSupplierCapabilityTx(ctx, tx, binding.SupplierID, "sales")
}
