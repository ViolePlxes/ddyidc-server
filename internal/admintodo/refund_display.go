package admintodo

import (
	"context"
	workflow "ddyidc/internal/payment/repository/refundworkflow"
	"encoding/json"
	"gorm.io/gorm"
	"sort"
	"strconv"
	"time"
)

type RefundMachine struct {
	ID        string `json:"id"`
	IPAddress string `json:"ipAddress"`
	Hostname  string `json:"hostname"`
}

// Reads local evidence only. A snapshot with no machines is an unprovisioned
// request, not permission to guess another service belonging to the customer.
func enrichRefundMachines(ctx context.Context, db *gorm.DB, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	applications, orders, markets, payments := []string{}, []string{}, []string{}, []string{}
	for _, e := range entries {
		switch e.Source {
		case "application":
			applications = append(applications, e.ID)
		case "payment":
			payments = append(payments, e.ID)
		case "marketplace":
			markets = append(markets, e.ID)
		}
	}
	paymentOrders := map[string]string{}
	if len(payments) > 0 {
		var rows []struct {
			ID      string
			OrderID string
		}
		if err := db.WithContext(ctx).Table("payment.payments").Select("CAST(id AS CHAR) id,CAST(order_id AS CHAR) order_id").Where("id IN ? AND biz_type='order'", payments).Scan(&rows).Error; err != nil {
			return err
		}
		for _, r := range rows {
			paymentOrders[r.ID] = r.OrderID
			orders = append(orders, r.OrderID)
		}
	}
	requests := []workflow.Request{}
	if len(applications) > 0 {
		if err := db.WithContext(ctx).Where("application_id IN ?", applications).Find(&requests).Error; err != nil {
			return err
		}
	}
	type applyTime struct {
		ID        string
		AppliedAt time.Time
	}
	times := []applyTime{}
	if len(applications) > 0 {
		if err := db.WithContext(ctx).Table("payment.payment_refunds").Select("CAST(id AS CHAR) id,created_at applied_at").Where("id IN ?", applications).Scan(&times).Error; err != nil {
			return err
		}
	}
	applied := map[string]time.Time{}
	for _, row := range times {
		applied[row.ID] = row.AppliedAt
	}
	byApplication := map[string]workflow.Request{}
	ids := []int64{}
	snapshots := map[string]workflow.Snapshot{}
	for _, r := range requests {
		key := strconv.FormatInt(r.ApplicationID, 10)
		byApplication[key] = r
		var snapshot workflow.Snapshot
		if json.Unmarshal(r.Snapshot, &snapshot) == nil {
			snapshots[key] = snapshot
			for _, p := range snapshot.Periods {
				if p.InstanceID > 0 {
					ids = append(ids, p.InstanceID)
				}
			}
		}
	}
	// Legacy applications without a workflow snapshot retain their original order association.
	for _, e := range entries {
		if e.Source == "application" {
			if _, ok := byApplication[e.ID]; !ok {
				orders = append(orders, e.OrderID)
			}
		}
	}
	type marketHost struct {
		ID         string
		InstanceID int64
	}
	marketHosts := []marketHost{}
	if len(markets) > 0 {
		if err := db.WithContext(ctx).Table("system.marketplace_orders m").Select("CAST(m.id AS CHAR) id,l.product_id instance_id").Joins("JOIN system.marketplace_listings l ON l.id=m.listing_id").Where("m.id IN ?", markets).Scan(&marketHosts).Error; err != nil {
			return err
		}
		for _, m := range marketHosts {
			ids = append(ids, m.InstanceID)
		}
	}
	type host struct {
		ID             int64
		OrderID        string
		CustomerID     string
		PrimaryIP      string
		ConfigSnapshot string
	}
	hosts := []host{}
	if len(ids) > 0 || len(orders) > 0 {
		q := db.WithContext(ctx).Table("instance.instances").Select("id,CAST(order_id AS CHAR) order_id,CAST(customer_id AS CHAR) customer_id,primary_ip,config_snapshot")
		if len(ids) > 0 && len(orders) > 0 {
			q = q.Where("id IN ? OR order_id IN ?", ids, orders)
		} else if len(ids) > 0 {
			q = q.Where("id IN ?", ids)
		} else {
			q = q.Where("order_id IN ?", orders)
		}
		if err := q.Order("id").Scan(&hosts).Error; err != nil {
			return err
		}
	}
	for i := range entries {
		e := &entries[i]
		if e.Source == "application" && (e.Status == "application_pending" || e.Status == "upstream_refund_waiting" || e.Status == "refund_submission_pending" || e.Status == "upstream_refund_rejected" || e.Status == "0") {
			if request, ok := byApplication[e.ID]; ok && strconv.FormatInt(request.CustomerID, 10) == e.CustomerID {
				snapshot, periodErr := workflow.ResolvePurchasedPeriod(db.WithContext(ctx), request, snapshots[e.ID])
				if periodErr != nil {
					continue
				}
				fees := workflow.Fees{Setup: snapshot.MinimumSetup.String(), Handling: snapshot.MinimumHandling.String(), Installation: "0.00", Gift: "0.00", Traffic: "0.00", ValueAdded: "0.00", Evidence: "原始账单快照，按申请时间计算，不足一天按一天扣费"}
				if calc, err := workflow.Calculate(snapshot, applied[e.ID], fees); err == nil {
					value := calc.Refund.String()
					e.RefundableAmount = &value
				}
			}
		}

		matched := map[int64]string{}
		if r, ok := byApplication[e.ID]; e.Source == "application" && ok {
			if strconv.FormatInt(r.CustomerID, 10) == e.CustomerID {
				for _, p := range snapshots[e.ID].Periods {
					if p.InstanceID > 0 {
						matched[p.InstanceID] = ""
					}
				}
			}
		} else {
			for _, h := range hosts {
				if h.CustomerID != e.CustomerID {
					continue
				}
				if (e.Source == "application" || e.Source == "payment" && paymentOrders[e.ID] == e.OrderID) && h.OrderID == e.OrderID {
					matched[h.ID] = ""
				}
				if e.Source == "marketplace" {
					for _, m := range marketHosts {
						if m.ID == e.ID && m.InstanceID == h.ID {
							matched[h.ID] = ""
						}
					}
				}
			}
		}
		for _, h := range hosts {
			if _, ok := matched[h.ID]; ok && h.CustomerID == e.CustomerID {
				matched[h.ID] = h.PrimaryIP
			}
		}
		names := map[int64]string{}
		for _, h := range hosts {
			if _, ok := matched[h.ID]; ok && h.CustomerID == e.CustomerID {
				var snapshot struct {
					Hostname    string `json:"hostname"`
					ListDisplay *struct {
						Hostname string `json:"hostname"`
					} `json:"listDisplay"`
				}
				if json.Unmarshal([]byte(h.ConfigSnapshot), &snapshot) == nil {
					names[h.ID] = snapshot.Hostname
					if snapshot.ListDisplay != nil && snapshot.ListDisplay.Hostname != "" {
						names[h.ID] = snapshot.ListDisplay.Hostname
					}
				}
			}
		}
		keys := []int64{}
		for id := range matched {
			keys = append(keys, id)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		for _, id := range keys {
			e.Machines = append(e.Machines, RefundMachine{ID: strconv.FormatInt(id, 10), IPAddress: matched[id], Hostname: names[id]})
		}
	}
	return nil
}
