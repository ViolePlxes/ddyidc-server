package instance

import (
	"ddyidc/internal/finance/executionlog"
	"strconv"

	"gorm.io/gorm"
)

// recordRenewalExecution writes the finance renewal ledger row. Only finance-type
// suppliers are recorded; west and other adapters keep their own contracts.
func (s *Service) recordRenewalExecution(db *gorm.DB, r *Renewal, status, code, message string) {
	if s.db == nil || db == nil || r == nil {
		return
	}
	var supplierType string
	if err := db.Raw("SELECT type FROM common.suppliers WHERE id = ?", r.SupplierID).Scan(&supplierType).Error; err != nil {
		return
	}
	if supplierType != "finance" && supplierType != "default" {
		return
	}
	renewal := strconv.FormatInt(r.ID, 10)
	_, _ = executionlog.Record(db, executionlog.BeginInput{
		Module:         executionlog.ModuleRenewal,
		IdempotencyKey: "renewal:" + renewal,
		EntityType:     "instance_renewal",
		EntityUID:      renewal,
		Operation:      "renew",
	}, status, code, message)
}
