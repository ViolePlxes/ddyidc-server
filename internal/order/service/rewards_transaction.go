package order

import (
	repository "ddyidc/internal/order/repository"
	"gorm.io/gorm"
)

// WithTransaction keeps reward redemption, coupon use, stock reservation and
// the existing order/payment intention in the caller's transaction.
func (s *Service) WithTransaction(tx *gorm.DB) *Service {
	worker := NewService(repository.NewRepository(tx), s.hooks, s.snapshotKey)
	worker.notifier = s.notifier
	worker.renewalBalance = s.renewalBalance
	return worker
}

func (p *PreparedOrder) TotalAmount() int64 { return p.order.TotalAmount }
