package instance

import (
	"context"
	orderapi "ddyidc/internal/order/api"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"ddyidc/internal/shared/distsync"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/upstream/provider/contracts"
	upstream "ddyidc/internal/upstream/service"
	"gorm.io/gorm"
)

// StartUnpaidPaymentRecovery retries payments that were never sent to the
// supplier. It is part of the single cron list in main.go. A payment that never
// left the process is a definite failure: the invoice already exists, the order
// is already paid by the customer, and paying it again cannot double charge
// because the supplier rejects an already paid invoice.
func (s *Service) StartUnpaidPaymentRecovery(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = distsync.Work(ctx, func(call context.Context) error {
				call, cancel := context.WithTimeout(call, 3*time.Minute)
				defer cancel()
				if s.db != nil {
					s.recoverUnpaidPayments(call)
				}
				return nil
			})
		}
	}
}

type unpaidPayment struct {
	UnitID     int64
	OrderID    int64
	ItemID     int64
	InstanceID int64
	SupplierID string
	Invoice    string
	ProductID  string
	CreatedAt  int64
}

func (s *Service) recoverUnpaidPayments(ctx context.Context) {
	if upstream.Default == nil {
		return
	}
	var rows []unpaidPayment
	query := "SELECT d.id unit_id,d.order_id,d.order_item_id item_id,d.instance_id,p.supplier_id,p.upstream_order_id invoice,CAST(p.upstream_product_id AS CHAR) product_id,p.created_at" +
		" FROM `order`.delivery_units d" +
		" JOIN `order`.unit_procurements p ON p.id=d.id" +
		" JOIN instance.instances i ON i.id=d.instance_id AND i.deleted_at IS NULL" +
		" WHERE d.status IN ('unknown','failed') AND COALESCE(i.upstream_instance_id,'')=''" +
		" AND p.upstream_order_id<>'' AND p.attempt_count<5" +
		" AND p.last_error LIKE '%payment_not_sent%'" +
		" AND p.updated_at<UNIX_TIMESTAMP(NOW() - INTERVAL 2 MINUTE)" +
		" ORDER BY d.id LIMIT 10"
	if err := s.db.WithContext(ctx).Raw(query).Scan(&rows).Error; err != nil {
		log.Printf("PAYMENT_RECOVERY scan failed: %v", err)
		return
	}
	for _, row := range rows {
		s.recoverUnpaidPayment(ctx, row)
	}
}

func (s *Service) recoverUnpaidPayment(ctx context.Context, row unpaidPayment) {
	since := time.Now().Add(-6 * time.Hour)
	if row.CreatedAt > 0 {
		since = time.Unix(row.CreatedAt, 0).Add(-10 * time.Minute)
	}
	// A host may already exist: the original attempt reached the supplier even
	// though the reply was lost. Bind it without paying again.
	if s.claimInvoiceHost(ctx, row, since) {
		return
	}
	provider, err := upstream.Default.ProviderFor(row.SupplierID)
	if err != nil {
		s.markPaymentRetry(row.UnitID, err)
		return
	}
	payer, ok := provider.(interface{ PayInvoice(string) error })
	if !ok {
		return
	}
	if err := payer.PayInvoice(row.Invoice); err != nil {
		s.markPaymentRetry(row.UnitID, err)
		return
	}
	time.Sleep(3 * time.Second)
	if s.claimInvoiceHost(ctx, row, since) {
		return
	}
	s.markPaymentRetry(row.UnitID, fmt.Errorf("补付已提交但上游主机尚未出现"))
}

func (s *Service) claimInvoiceHost(ctx context.Context, row unpaidPayment, since time.Time) bool {
	product, _ := strconv.Atoi(row.ProductID)
	found, err := upstream.Default.FindInvoiceHost(ctx, row.SupplierID, row.Invoice, product, since)
	if err != nil || found == nil || found.Match == nil || found.BoundInstanceID != "" {
		return false
	}
	match := found.Match
	if !strings.EqualFold(strings.TrimSpace(match.Status), "active") || strings.TrimSpace(match.ExpiresAt) == "" {
		return false
	}
	expires, err := time.Parse(time.RFC3339, match.ExpiresAt)
	if err != nil {
		return false
	}
	if err := s.bindInvoiceHost(ctx, row, match, expires); err != nil {
		log.Printf("PAYMENT_RECOVERY bind unit=%d failed: %v", row.UnitID, err)
		return false
	}
	log.Printf("PAYMENT_RECOVERY unit=%d invoice=%s host=%s bound", row.UnitID, row.Invoice, match.HostID)
	return true
}

func (s *Service) bindInvoiceHost(ctx context.Context, row unpaidPayment, match *contracts.InvoiceHostMatch, expires time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var other int64
		if err := tx.Raw("SELECT COUNT(*) FROM instance.instances WHERE upstream_instance_id=? AND id<>? AND deleted_at IS NULL", match.HostID, row.InstanceID).Row().Scan(&other); err != nil {
			return err
		}
		if other > 0 {
			return fmt.Errorf("主机 %s 已被其他实例占用", match.HostID)
		}
		res := tx.Exec("UPDATE instance.instances SET status=3, upstream_provider_id=?, upstream_instance_id=?, primary_ip=?, expires_at=?, activated_at=NOW(3), updated_at=NOW(3) WHERE id=? AND status=2 AND COALESCE(upstream_instance_id,'')=''",
			row.SupplierID, match.HostID, match.PrimaryIP, expires.Format("2006-01-02 15:04:05"), row.InstanceID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return fmt.Errorf("实例状态已变化")
		}
		var bound int64
		if err := tx.Raw("SELECT COUNT(*) FROM instance.upstream_hosts WHERE supplier_id=? AND upstream_host_id=?", row.SupplierID, match.HostID).Row().Scan(&bound); err != nil {
			return err
		}
		if bound == 0 {
			if err := tx.Exec("INSERT INTO instance.upstream_hosts(id,host_id,supplier_id,upstream_host_id,status,due_time,created_at,updated_at) VALUES(?,?,?,?, 'active', UNIX_TIMESTAMP(?), NOW(3), NOW(3))",
				snowflake.GenID(), fmt.Sprint(row.InstanceID), row.SupplierID, match.HostID, expires.Format("2006-01-02 15:04:05")).Error; err != nil {
				return err
			}
		}
		if err := tx.Exec("UPDATE `order`.unit_procurements SET status='active', upstream_host_id=?, last_error='', attempt_count=attempt_count+1, updated_at=UNIX_TIMESTAMP(NOW()) WHERE id=?", match.HostID, row.UnitID).Error; err != nil {
			return err
		}
		if err := tx.Exec("UPDATE `order`.delivery_units SET status='complete', updated_at=NOW(3) WHERE id=?", row.UnitID).Error; err != nil {
			return err
		}
		if row.ItemID > 0 {
			if err := tx.Exec("UPDATE `order`.order_items SET delivery_status=2, updated_at=NOW(3) WHERE id=? AND order_id=?", row.ItemID, row.OrderID).Error; err != nil {
				return err
			}
		}
		if err := orderapi.CompleteDeliveredOrder(tx, row.OrderID); err != nil {
			return err
		}
		var customerID int64
		if err := tx.Raw("SELECT customer_id FROM instance.instances WHERE id=?", row.InstanceID).Row().Scan(&customerID); err != nil {
			return err
		}
		reason := fmt.Sprintf("支付未发出自动补付：invoice=%s host=%s ip=%s", row.Invoice, match.HostID, match.PrimaryIP)
		return tx.Exec("INSERT INTO instance.admin_instance_audits(id,admin_id,operator_ip,instance_id,customer_id,task_id,action,before_value,after_value,result,created_at) VALUES(?,0,'payment-recovery',?,?,0,'auto_payment_recovery',?,?,'success',NOW(3))",
			snowflake.GenID(), row.InstanceID, customerID, fmt.Sprintf(`{"invoice":%q,"unit":%d}`, row.Invoice, row.UnitID), fmt.Sprintf(`{"host":%q,"reason":%q}`, match.HostID, reason)).Error
	})
}

func (s *Service) markPaymentRetry(unitID int64, cause error) {
	message := "补付未成功"
	if cause != nil {
		message += "：" + cause.Error()
	}
	if runes := []rune(message); len(runes) > 180 {
		message = string(runes[:180])
	}
	if err := s.db.WithContext(context.Background()).Exec("UPDATE `order`.unit_procurements SET attempt_count=attempt_count+1, last_error=?, updated_at=UNIX_TIMESTAMP(NOW()) WHERE id=?", message, unitID).Error; err != nil {
		log.Printf("PAYMENT_RECOVERY mark unit=%d failed: %v", unitID, err)
	}
}
