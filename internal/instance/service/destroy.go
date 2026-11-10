package instance

import (
	"context"
	instance "ddyidc/internal/instance/domain"
	workflow "ddyidc/internal/payment/repository/refundworkflow"
	payment "ddyidc/internal/payment/domain"
	"time"
)

// DestroyView is the customer-facing preview of a machine destruction: what the
// machine has paid so far and what the refund policy would return today.
type DestroyView struct {
	InstanceID  int64                `json:"instanceId,string"`
	InstanceNo  string               `json:"instanceNo"`
	ProductName string               `json:"productName"`
	Paid        string               `json:"paidAmount"`
	Refundable  string               `json:"refundableAmount"`
	Periods     []workflow.PeriodQuote `json:"periods"`
	Requests    []DestroyRequest     `json:"requests"`
}

type DestroyRequest struct {
	ID           int64      `json:"id,string"`
	RefundNo     string     `json:"refundNo"`
	Amount       string     `json:"amount"`
	Reason       string     `json:"reason"`
	Status       uint8      `json:"status"`
	CreatedAt    time.Time  `json:"createdAt"`
	RefundedAt   *time.Time `json:"refundedAt,omitempty"`
	ReviewReason string     `json:"reviewReason,omitempty"`
}

// DestroyQuote previews one machine. It never writes: the customer needs to know
// the amount before submitting the destruction request.
func (s *Service) DestroyQuote(ctx context.Context, customer, instanceID int64) (DestroyView, error) {
	var out DestroyView
	if s.db == nil {
		return out, ErrUnsupported
	}
	var h instance.Host
	if err := s.db.WithContext(ctx).Where("id=? AND customer_id=?", instanceID, customer).Take(&h).Error; err != nil {
		return out, ErrNotFound
	}
	snapshot, calc, _, err := workflow.InstancePreview(ctx, s.db, customer, instanceID)
	if err != nil {
		return out, err
	}
	out = DestroyView{InstanceID: h.ID, InstanceNo: h.InstanceNo, ProductName: h.ProductName, Paid: snapshot.Paid.String(), Refundable: calc.Refund.String(), Periods: calc.Periods}
	requests, err := s.destroyRequests(ctx, customer, instanceID)
	if err != nil {
		return out, err
	}
	out.Requests = requests
	return out, nil
}

func (s *Service) SubmitDestroy(ctx context.Context, customer, instanceID int64, reason, key string) (payment.RefundApplication, error) {
	return workflow.SubmitInstance(ctx, s.db, customer, instanceID, reason, key)
}

func (s *Service) DestroyRequests(ctx context.Context, customer int64) ([]DestroyRequest, error) {
	return s.destroyRequests(ctx, customer, 0)
}

func (s *Service) destroyRequests(ctx context.Context, customer, instanceID int64) ([]DestroyRequest, error) {
	query := s.db.WithContext(ctx).Table("payment.payment_refunds a").
		Select("a.id, a.refund_no, a.refund_amount amount, a.reason, a.status, a.created_at, a.refunded_at, a.audit_remark review_reason").
		Joins("JOIN payment.todo_refund_requests q ON q.application_id=a.id").
		Where("q.source='instance' AND a.user_id=?", customer)
	if instanceID > 0 {
		query = query.Where("q.object_id=?", instanceID)
	}
	var rows []DestroyRequest
	if err := query.Order("a.created_at DESC").Limit(50).Scan(&rows).Error; err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []DestroyRequest{}
	}
	return rows, nil
}
