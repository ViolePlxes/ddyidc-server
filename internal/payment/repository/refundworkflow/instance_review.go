package refundworkflow

import (
	invoice "ddyidc/internal/billing/api"
	balance "ddyidc/internal/billing/balance"
	instance "ddyidc/internal/instance/domain"
	order "ddyidc/internal/order/domain"
	orderstate "ddyidc/internal/order/repository"
	"encoding/json"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

func loadMachineSnapshot(tx *gorm.DB, applicationID int64) (MachineSnapshot, error) {
	var out MachineSnapshot
	var req Request
	if err := tx.Where("application_id=?", applicationID).Take(&req).Error; err != nil {
		return out, ErrCalculation
	}
	if req.Source != "instance" {
		return out, ErrConflict
	}
	if err := json.Unmarshal(req.Snapshot, &out); err != nil {
		return out, ErrCalculation
	}
	return out, nil
}

// lockInstanceSource locks the machine and then every order that the machine
// refund touches, in ascending id order so concurrent reviews cannot deadlock.
func lockInstanceSource(tx *gorm.DB, r Request) error {
	var h instance.Host
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=?", r.ObjectID, r.CustomerID).Take(&h).Error; err != nil {
		return ErrConflict
	}
	snapshot, err := loadMachineSnapshot(tx, r.ApplicationID)
	if err != nil {
		return err
	}
	for _, allocation := range snapshot.Allocations {
		var id int64
		if err := tx.Table("`order`.orders").Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=?", allocation.OrderID, r.CustomerID).Scan(&id).Error; err != nil {
			return err
		}
	}
	return nil
}

// verifyInstanceEntitlement refuses to refund a machine that is still being
// provisioned, already destroyed, or has another refund in flight.
func verifyInstanceEntitlement(tx *gorm.DB, r Request, _ Snapshot) error {
	var h instance.Host
	if err := tx.Where("id=? AND customer_id=?", r.ObjectID, r.CustomerID).Take(&h).Error; err != nil {
		return ErrEntitlement
	}
	// 1 待开通 / 2 开通中 / 5 异常 / 6 已删除 are not refundable here.
	if h.Status == 1 || h.Status == 2 || h.Status == 5 || h.Status == 6 {
		return ErrEntitlement
	}
	var active int64
	if err := tx.Table("payment.payment_refunds a").Joins("JOIN payment.todo_refund_requests q ON q.application_id=a.id").Where("q.source='instance' AND q.object_id=? AND a.status<>2 AND a.id<>?", r.ObjectID, r.ApplicationID).Count(&active).Error; err != nil {
		return err
	}
	if active != 0 {
		return ErrEntitlement
	}
	snapshot, err := loadMachineSnapshot(tx, r.ApplicationID)
	if err != nil {
		return err
	}
	for _, allocation := range snapshot.Allocations {
		if allocation.OrderID <= 0 || allocation.Paid <= 0 {
			return ErrCalculation
		}
		var row struct{ Status uint8 }
		if err := tx.Table("`order`.orders").Select("status").Where("id=? AND customer_id=?", allocation.OrderID, r.CustomerID).Take(&row).Error; err != nil {
			return ErrEntitlement
		}
		if row.Status != order.OrderStatusPaid && row.Status != order.OrderStatusCompleted && row.Status != order.OrderStatusPartialRefund {
			return ErrEntitlement
		}
	}
	return nil
}

// finalizeInstance splits the reviewed refund over the orders that supplied the
// machine periods, settles each invoice with its own share and destroys the
// machine entitlement.
func finalizeInstance(tx *gorm.DB, r Request, calc Calculation, now time.Time) error {
	snapshot, err := loadMachineSnapshot(tx, r.ApplicationID)
	if err != nil {
		return err
	}
	if len(calc.Periods) != len(snapshot.Allocations) {
		return ErrCalculation
	}
	shares := map[int64]balance.Money{}
	orderIDs := []int64{}
	for index, quote := range calc.Periods {
		orderID := snapshot.Allocations[index].OrderID
		if _, seen := shares[orderID]; !seen {
			orderIDs = append(orderIDs, orderID)
		}
		shares[orderID] += quote.Refund
	}
	allocations := make([]invoice.RefundAllocation, 0, len(orderIDs))
	statuses := map[int64]uint8{}
	for _, orderID := range orderIDs {
		amount := shares[orderID]
		var row struct{ PayAmount int64 }
		if err := tx.Table("`order`.orders").Select("pay_amount").Where("id=? AND customer_id=?", orderID, r.CustomerID).Take(&row).Error; err != nil {
			return ErrConflict
		}
		if amount < 0 || amount > balance.Money(row.PayAmount) {
			return ErrConflict
		}
		if amount == 0 {
			// Nothing of this order is refunded (fully used period): keep the
			// order and its invoice untouched.
			delete(shares, orderID)
			continue
		}
		next := order.OrderStatusPartialRefund
		if amount == balance.Money(row.PayAmount) {
			next = order.OrderStatusRefunded
		}
		statuses[orderID] = next
		allocations = append(allocations, invoice.RefundAllocation{OrderID: orderID, Amount: amount, Paid: balance.Money(row.PayAmount)})
	}
	refundedOrders := make([]int64, 0, len(statuses))
	for _, orderID := range orderIDs {
		if _, ok := statuses[orderID]; ok {
			refundedOrders = append(refundedOrders, orderID)
		}
	}
	// Settle the invoices while the orders are still in a paid state: the invoice
	// reader derives its paid amount and status from the order.
	if err := invoice.RecordRefundAllocationsTx(tx, "order", r.ApplicationID, allocations, now); err != nil {
		return err
	}
	for _, orderID := range refundedOrders {
		settled, err := orderstate.SettleReviewedMachineRefundTx(tx, orderID, r.CustomerID, statuses[orderID], now)
		if err != nil || !settled {
			return ErrConflict
		}
		if err := tx.Table("`order`.delivery_units").Where("order_id=? AND status='pending'", orderID).Updates(map[string]any{"status": "cancelled", "updated_at": now}).Error; err != nil {
			return err
		}
	}
	destroyed := tx.Table("instance.instances").Where("id=? AND customer_id=? AND status IN (3,4,6)", r.ObjectID, r.CustomerID).Updates(map[string]any{"status": 6, "expires_at": now, "updated_at": now})
	if destroyed.Error != nil || destroyed.RowsAffected != 1 {
		return ErrConflict
	}
	if err := tx.Table("instance.instance_renewals").Where("instance_id=? AND state IN ('unpaid','invoiced_verified')", r.ObjectID).Updates(map[string]any{"state": "cancelled", "updated_at": now}).Error; err != nil {
		return err
	}
	return tx.Exec("UPDATE system.durable_tasks SET status='cancelled',updated_at=? WHERE status='pending' AND JSON_UNQUOTE(JSON_EXTRACT(payload,'$.businessType'))='order' AND JSON_UNQUOTE(JSON_EXTRACT(payload,'$.businessId')) IN ?", now, orderIDs).Error
}
