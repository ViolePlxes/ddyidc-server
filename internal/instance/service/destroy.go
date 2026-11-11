package instance

import (
	"context"
	instance "ddyidc/internal/instance/domain"
	payment "ddyidc/internal/payment/domain"
	workflow "ddyidc/internal/payment/repository/refundworkflow"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"strings"
	"time"
)

// DestroyView is the customer-facing preview of a machine destruction: what the
// machine has paid so far and what the refund policy would return today.
type DestroyView struct {
	InstanceID  int64                  `json:"instanceId,string"`
	InstanceNo  string                 `json:"instanceNo"`
	ProductName string                 `json:"productName"`
	Paid        string                 `json:"paidAmount"`
	Refundable  string                 `json:"refundableAmount"`
	Periods     []workflow.PeriodQuote `json:"periods"`
	Requests    []DestroyRequest       `json:"requests"`
}

type DestroyRequest struct {
	ID           int64      `json:"id,string"`
	RefundNo     string     `json:"refundNo"`
	InstanceID   int64      `json:"instanceId,string"`
	ProductName  string     `json:"productName"`
	InstanceNo   string     `json:"instanceNo"`
	IP           string     `json:"ip"`
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

// SubmitDestroy refuses before the refund workflow writes anything: the machine
// must not be production-locked or auto-renewing, must not already have a
// destruction request in review, and the customer must prove the bound mailbox
// through the same one-time code the production lock uses.
func (s *Service) SubmitDestroy(ctx context.Context, customer, instanceID int64, reason, key string, email DestroyEmailInput) (payment.RefundApplication, error) {
	if s.db == nil {
		return payment.RefundApplication{}, ErrUnsupported
	}
	return workflow.SubmitInstance(ctx, s.db, customer, instanceID, reason, key, s.destroySubmitCheck(ctx, customer, email))
}

func (s *Service) destroySubmitCheck(ctx context.Context, customer int64, in DestroyEmailInput) workflow.SubmitCheck {
	return func(tx *gorm.DB, h instance.Host) error {
		var lock ProductionLock
		switch err := tx.Where("instance_id=? AND enabled=1", h.ID).Take(&lock).Error; {
		case err == nil:
			return errors.New("已开启生产锁，请先关闭")
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return err
		}
		var settings RenewalSettings
		switch err := tx.Where("instance_id=? AND customer_id=? AND enabled=1", h.ID, customer).Take(&settings).Error; {
		case err == nil:
			return errors.New("已开启自动续费，请先关闭")
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return err
		}
		var pending int64
		if err := tx.Table("payment.payment_refunds a").
			Joins("JOIN payment.todo_refund_requests q ON q.application_id=a.id").
			Where("q.source='instance' AND q.object_id=? AND a.status IN (0,1)", h.ID).
			Count(&pending).Error; err != nil {
			return err
		}
		if pending > 0 {
			return errors.New("已有销毁申请在审核中")
		}
		if strings.TrimSpace(in.Code) == "" {
			return errors.New("请先完成邮箱验证码验证")
		}
		return s.verifyDestroyEmailTx(ctx, tx, h, in)
	}
}

// DestroyOption is one destroyable machine with its current refundable amount,
// so the console list can show the estimate without a request per row.
type DestroyOption struct {
	InstanceID       int64      `json:"instanceId,string"`
	InstanceNo       string     `json:"instanceNo"`
	ProductName      string     `json:"productName"`
	IP               string     `json:"ip"`
	ExpiresAt        *time.Time `json:"expiresAt,omitempty"`
	Remaining        string     `json:"remaining"`
	Paid             string     `json:"paidAmount"`
	Refundable       string     `json:"refundableAmount"`
	Eligible         bool       `json:"eligible"`
	Reason           string     `json:"reason,omitempty"`
	LastRejectReason string     `json:"lastRejectReason,omitempty"`
}

func (s *Service) DestroyOptions(ctx context.Context, customer int64) ([]DestroyOption, error) {
	if s.db == nil {
		return nil, ErrUnsupported
	}
	var hosts []instance.Host
	if err := s.db.WithContext(ctx).Where("customer_id=? AND status IN (3,4)", customer).Order("expires_at ASC,id ASC").Limit(100).Find(&hosts).Error; err != nil {
		return nil, err
	}
	if len(hosts) == 0 {
		return []DestroyOption{}, nil
	}
	ids := make([]int64, 0, len(hosts))
	for _, h := range hosts {
		ids = append(ids, h.ID)
	}
	pending := map[int64]bool{}
	var pendingIDs []int64
	if err := s.db.WithContext(ctx).Table("payment.payment_refunds a").
		Joins("JOIN payment.todo_refund_requests q ON q.application_id=a.id").
		Where("q.source='instance' AND q.customer_id=? AND a.status IN (0,1) AND q.object_id IN ?", customer, ids).
		Pluck("q.object_id", &pendingIDs).Error; err != nil {
		return nil, err
	}
	for _, id := range pendingIDs {
		pending[id] = true
	}
	locked := map[int64]bool{}
	var lockIDs []int64
	if err := s.db.WithContext(ctx).Table("instance.instance_production_locks").
		Where("instance_id IN ? AND enabled=1", ids).Pluck("instance_id", &lockIDs).Error; err != nil {
		return nil, err
	}
	for _, id := range lockIDs {
		locked[id] = true
	}
	autoRenewing := map[int64]bool{}
	var autoIDs []int64
	if err := s.db.WithContext(ctx).Table("instance.instance_renewal_settings").
		Where("instance_id IN ? AND customer_id=? AND enabled=1", ids, customer).Pluck("instance_id", &autoIDs).Error; err != nil {
		return nil, err
	}
	for _, id := range autoIDs {
		autoRenewing[id] = true
	}
	type rejection struct {
		ObjectID    int64
		AuditRemark string
	}
	var rejections []rejection
	if err := s.db.WithContext(ctx).Table("payment.payment_refunds a").
		Select("q.object_id object_id, COALESCE(a.audit_remark,'') audit_remark").
		Joins("JOIN payment.todo_refund_requests q ON q.application_id=a.id").
		Where("q.source='instance' AND q.customer_id=? AND a.status=2 AND q.object_id IN ?", customer, ids).
		Order("a.created_at DESC,a.id DESC").Scan(&rejections).Error; err != nil {
		return nil, err
	}
	lastReject := map[int64]string{}
	for _, row := range rejections {
		if _, seen := lastReject[row.ObjectID]; seen {
			continue
		}
		lastReject[row.ObjectID] = strings.TrimSpace(row.AuditRemark)
	}
	out := make([]DestroyOption, 0, len(hosts))
	for _, h := range hosts {
		if pending[h.ID] {
			continue
		}
		option := DestroyOption{InstanceID: h.ID, InstanceNo: h.InstanceNo, ProductName: h.ProductName, IP: h.PrimaryIP, ExpiresAt: h.ExpiresAt, LastRejectReason: lastReject[h.ID]}
		if h.ExpiresAt != nil {
			option.Remaining = remainingLabel(h.ExpiresAt.Sub(time.Now().UTC()))
		}
		if locked[h.ID] {
			option.Reason = "已开启生产锁，请先关闭生产锁"
			out = append(out, option)
			continue
		}
		if autoRenewing[h.ID] {
			option.Reason = "已开启自动续费，请先关闭自动续费"
			out = append(out, option)
			continue
		}
		snapshot, calc, _, err := workflow.InstancePreview(ctx, s.db, customer, h.ID)
		if err != nil {
			option.Eligible = false
			option.Reason = destroyOptionReason(err)
			out = append(out, option)
			continue
		}
		option.Eligible = true
		option.Paid = snapshot.Paid.String()
		option.Refundable = calc.Refund.String()
		out = append(out, option)
	}
	return out, nil
}

// remainingLabel renders the time left with the same whole-day rounding the
// refund policy uses: any started day counts as a full day.
func remainingLabel(left time.Duration) string {
	if left <= 0 {
		return "已到期"
	}
	days := int64(left / (24 * time.Hour))
	if left%(24*time.Hour) != 0 {
		days++
	}
	if days >= 1 {
		return fmt.Sprintf("剩余 %d 天", days)
	}
	return "剩余不足 1 天"
}

func destroyOptionReason(err error) string {
	switch {
	case errors.Is(err, workflow.ErrCalculation):
		return "计费周期或费用明细不完整，暂不能核算"
	case errors.Is(err, workflow.ErrConflict):
		return "实例状态已变化，请刷新"
	default:
		return "暂不能核算可退金额"
	}
}

func (s *Service) DestroyRequests(ctx context.Context, customer int64) ([]DestroyRequest, error) {
	return s.destroyRequests(ctx, customer, 0)
}

func (s *Service) destroyRequests(ctx context.Context, customer, instanceID int64) ([]DestroyRequest, error) {
	query := s.db.WithContext(ctx).Table("payment.payment_refunds a").
		Select("a.id, a.refund_no, q.object_id instance_id, a.refund_amount amount, a.reason, a.status, a.created_at, a.refunded_at, COALESCE(a.audit_remark,'') review_reason, COALESCE(i.product_name,'') product_name, COALESCE(i.instance_no,'') instance_no, COALESCE(i.primary_ip,'') ip").
		Joins("JOIN payment.todo_refund_requests q ON q.application_id=a.id").
		Joins("LEFT JOIN instance.instances i ON i.id=q.object_id").
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
