package refundworkflow

import (
	balance "ddyidc/internal/billing/balance"
	instance "ddyidc/internal/instance/domain"
	order "ddyidc/internal/order/domain"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// A renewal's period comes from its original base expiry and the completion
// evidence for that exact order, never from a later renewal's current expiry.
func renewalPeriod(tx *gorm.DB, o order.Order, item order.OrderItem, paid balance.Money) (Period, []string, error) {
	var r struct {
		ID, InstanceID, CustomerID, OwnershipVersion int64
		Amount                                       balance.Money
		BaseExpiry                                   time.Time
		State                                        string
	}
	if err := tx.Table("instance.instance_renewals").Where("order_id=? AND customer_id=?", o.ID, o.CustomerID).Take(&r).Error; err != nil {
		return Period{}, nil, ErrCalculation
	}
	if item.InstanceID == nil || *item.InstanceID != r.InstanceID || item.Qty != 1 || r.Amount != paid {
		return Period{}, nil, ErrConflict
	}
	var h instance.Host
	if err := tx.Unscoped().Clauses(clause.Locking{Strength: "SHARE"}).First(&h, r.InstanceID).Error; err != nil {
		return Period{}, nil, ErrConflict
	}
	p := Period{InstanceID: h.ID, OwnershipVersion: h.OwnershipVersion, RenewalID: r.ID, Paid: paid, Start: &r.BaseExpiry, EntitlementStart: h.ActivatedAt, EntitlementEnd: h.ExpiresAt}
	warnings := []string{}
	if r.State != "completed" || r.BaseExpiry.IsZero() {
		warnings = append(warnings, "本次续费尚未核实交付结果，不能估算退款服务周期")
	}
	if h.CustomerID != o.CustomerID || r.OwnershipVersion != h.OwnershipVersion {
		warnings = append(warnings, "续费机器归属已变更，需核实退款权益")
	}
	var rows []struct{ UpstreamResponse string }
	if err := tx.Table("instance.instance_events").Select("upstream_response").Where("instance_id=? AND event_type='renew' AND status=2 AND JSON_UNQUOTE(JSON_EXTRACT(upstream_response,'$.orderId'))=?", h.ID, fmt.Sprint(o.ID)).Find(&rows).Error; err != nil {
		return p, nil, err
	}
	for _, row := range rows {
		var proof struct {
			ExpiresAt string `json:"expiresAt"`
		}
		if json.Unmarshal([]byte(row.UpstreamResponse), &proof) != nil {
			continue
		}
		end, err := time.Parse(time.RFC3339, proof.ExpiresAt)
		if err != nil || !end.After(r.BaseExpiry) {
			continue
		}
		if p.End != nil && !p.End.Equal(end) {
			return p, nil, ErrCalculation
		}
		p.End = &end
	}
	if p.End == nil {
		warnings = append(warnings, "本次续费缺少对应订单的完成时间证据，请核查原始账单")
	}
	return p, warnings, nil
}

func originalPeriodEnd(tx *gorm.DB, instanceID int64, current *time.Time) (*time.Time, error) {
	var first struct{ BaseExpiry time.Time }
	err := tx.Table("instance.instance_renewals").Select("base_expiry").Where("instance_id=? AND state='completed'", instanceID).Order("created_at ASC,id ASC").Take(&first).Error
	if err == gorm.ErrRecordNotFound {
		return current, nil
	}
	if err != nil {
		return nil, err
	}
	if first.BaseExpiry.IsZero() {
		return nil, ErrCalculation
	}
	return &first.BaseExpiry, nil
}
