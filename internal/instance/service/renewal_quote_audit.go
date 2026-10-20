package instance

import (
	"context"
	billing "ddyidc/internal/billing/balance"
	"ddyidc/internal/instance/readmodel"
	"fmt"
	"time"
)

type RenewalQuoteAuditItem struct {
	OrderID            string `json:"orderId"`
	Supplier           string `json:"supplier"`
	Cycle              string `json:"cycle"`
	Gross              string `json:"grossAmount"`
	Payable            string `json:"payableAmount"`
	State              string `json:"state"`
	OrderStatus        uint8  `json:"orderStatus"`
	SnapshotAmount     string `json:"snapshotAmount,omitempty"`
	SnapshotVerifiedAt string `json:"snapshotVerifiedAt,omitempty"`
	Finding            string `json:"finding"`
	HasEvidence        bool   `json:"hasEvidence"`
	CreatedAt          string `json:"createdAt"`
}
type RenewalQuoteAudit struct {
	Items     []RenewalQuoteAuditItem `json:"items"`
	Total     int64                   `json:"total"`
	Page      int                     `json:"page"`
	PageSize  int                     `json:"pageSize"`
	ReadOnly  bool                    `json:"readOnly"`
	LiveQuote bool                    `json:"liveQuote"`
}

// AuditRenewalQuotes is an inventory, not a payment authorization or a correction.
// Local snapshots only flag cases for individual live verification; no HTTP/write occurs.
func (s *Service) AuditRenewalQuotes(ctx context.Context,access AdminAccess, page int) (RenewalQuoteAudit, error) {
	result := RenewalQuoteAudit{Items: []RenewalQuoteAuditItem{}, Page: page, PageSize: 20, ReadOnly: true, LiveQuote: false}
	if page < 1 || page > 100000 {
		return result, fmt.Errorf("分页参数无效")
	}
	query := s.db.WithContext(ctx).Table("instance.instance_renewals r").Joins("JOIN `order`.orders o ON o.id=r.order_id").Joins("JOIN instance.instances i ON i.id=r.instance_id").Joins("LEFT JOIN common.suppliers s ON s.id=r.supplier_id").Where("r.state NOT IN ('completed','cancelled')")
 if len(access.CustomerIDs)>0 {query=query.Where("r.customer_id IN ?",access.CustomerIDs)}
	if e := query.Count(&result.Total).Error; e != nil {
		return result, e
	}
	var rows []struct {
		OrderID, CustomerID, OwnershipVersion, SupplierID int64
		HostID, Cycle, State, ConfigSnapshot, Supplier    string
		TotalAmount, PayAmount                            int64
		Status                                            uint8
		CreatedAt                                         time.Time
		HasEvidence                                       bool
	}
	if e := query.Select("r.order_id,r.customer_id,r.ownership_version,r.supplier_id,r.host_id,r.cycle,r.state,i.config_snapshot,s.name supplier,o.total_amount,o.pay_amount,o.status,r.created_at,EXISTS(SELECT 1 FROM instance.instance_renewal_quote_evidence e WHERE e.order_id=r.order_id) has_evidence").Order("r.created_at DESC,r.id DESC").Offset((page - 1) * 20).Limit(20).Scan(&rows).Error; e != nil {
		return result, e
	}
	for _, row := range rows {
		item := RenewalQuoteAuditItem{OrderID: fmt.Sprint(row.OrderID), Supplier: row.Supplier, Cycle: row.Cycle, Gross: billing.Money(row.TotalAmount).String(), Payable: billing.Money(row.PayAmount).String(), State: row.State, OrderStatus: row.Status, HasEvidence: row.HasEvidence, Finding: "live_verification_required", CreatedAt: row.CreatedAt.Format(time.RFC3339)}
		if !row.HasEvidence {
			item.Finding = "quote_evidence_missing"
		}
		snap, ok := readmodel.Decode(row.ConfigSnapshot)
		if ok && readmodel.ValidPrice(snap, row.CustomerID, row.OwnershipVersion, row.SupplierID, row.HostID, row.Cycle) {
			item.SnapshotAmount = snap.Amount
			item.SnapshotVerifiedAt = time.Unix(snap.VerifiedAt, 0).Format(time.RFC3339)
			amount, e := billing.ParseMoney(snap.Amount)
			if e == nil && int64(amount) != row.TotalAmount {
				item.Finding = "snapshot_price_differs"
			}
			if snap.Stale(time.Now()) {
				item.Finding = "snapshot_stale_live_verification_required"
			}
		}
		if row.Status != 1 {
			item.Finding = "paid_execution_review_required"
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}
