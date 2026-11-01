package instance

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
)

type DeleteInput struct {
	Reason  string `json:"reason"`
	Confirm string `json:"confirm"`
	Force   bool   `json:"force"`
}

// Delete removes a customer instance from the platform (soft delete).
// It never cancels, refunds or terminates anything at the supplier: an instance
// that still owns an upstream host requires force plus the exact instance
// number, so the administrator explicitly acknowledges that the upstream
// resource is left as-is. Purchase orders, delivery tasks and balance ledgers
// stay untouched for audit.
func (s *Service) Delete(ctx context.Context, customerID, id int64, ip string, in DeleteInput) error {
	actor, admin := actionActor(ctx, customerID)
	if actor != 2 || admin <= 0 {
		return ErrForbidden
	}
	reason := strings.TrimSpace(in.Reason)
	if n := utf8.RuneCountInString(reason); n < 2 || n > 100 {
		return errors.New("请填写 2-100 字的删除原因")
	}
	if s.db == nil {
		return ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	h, e := s.owned(ctx, customerID, id)
	if e != nil {
		return e
	}
	if e := s.productionMutationAllowed(ctx, id); e != nil {
		return e
	}
	if locked, e := s.repo.IsMarketplaceLocked(ctx, id); e != nil {
		return e
	} else if locked {
		return ErrTradingLocked
	}
	if h.UpstreamInstanceID != "" && (!in.Force || strings.TrimSpace(in.Confirm) != h.InstanceNo) {
		return errors.New("该实例仍绑定上游主机，需勾选强制删除并输入实例编号确认")
	}
	before, _ := json.Marshal(map[string]any{
		"instanceNo": h.InstanceNo, "status": h.Status,
		"upstreamInstanceId": h.UpstreamInstanceID, "orderId": strconv.FormatInt(h.OrderID, 10),
	})
	after, _ := json.Marshal(map[string]any{
		"deleted": true, "reason": reason, "upstreamHostKept": h.UpstreamInstanceID != "",
	})
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Exec("UPDATE instance.instances SET deleted_at=NOW(3), updated_at=NOW(3) WHERE id=? AND customer_id=? AND deleted_at IS NULL", id, customerID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("实例状态已变化，未删除")
		}
		if e := tx.Exec("UPDATE instance.instance_renewal_settings SET enabled=0 WHERE instance_id=? AND customer_id=?", id, customerID).Error; e != nil {
			return e
		}
		// The local binding is removed with the instance; the supplier host is
		// intentionally untouched and can be imported again if needed.
		if e := tx.Exec("DELETE FROM instance.upstream_hosts WHERE host_id=?", strconv.FormatInt(id, 10)).Error; e != nil {
			return e
		}
		return tx.Exec("INSERT INTO instance.admin_instance_audits(id,admin_id,operator_ip,instance_id,customer_id,task_id,action,before_value,after_value,result,created_at) VALUES(?,?,?,?,?,0,'delete',?,?,'success',NOW(3))",
			snowflake.GenID(), admin, ip, id, customerID, string(before), string(after)).Error
	})
}
