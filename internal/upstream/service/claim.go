package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"ddyidc/internal/upstream/provider/contracts"
)

// InvoiceHostResult reports the upstream host created for a paid invoice.
// BoundInstanceID is set when another local instance already owns that host;
// the caller must never bind it twice.
type InvoiceHostResult struct {
	Match           *contracts.InvoiceHostMatch `json:"match"`
	BoundInstanceID string                      `json:"boundInstanceId,omitempty"`
}

// FindInvoiceHost reconciles a procurement whose payment result was lost.
// It is read-only: no upstream write, no local state change.
func (s *Store) FindInvoiceHost(ctx context.Context, supplierID, invoiceID string, upstreamProductID int, since time.Time) (*InvoiceHostResult, error) {
	supplierID = strings.TrimSpace(supplierID)
	invoice, err := strconv.ParseInt(strings.TrimSpace(invoiceID), 10, 64)
	if err != nil || invoice <= 0 {
		return nil, errors.New("上游账单 ID 无效")
	}
	if supplierID == "" {
		return nil, errors.New("供应商 ID 不能为空")
	}
	p, err := s.ProviderFor(supplierID)
	if err != nil {
		return nil, err
	}
	finder, ok := p.(interface {
		FindHostByInvoice(int64, int, time.Time) (*contracts.InvoiceHostMatch, error)
	})
	if !ok {
		return nil, errors.New("当前供应商不支持按账单核对")
	}
	match, err := finder.FindHostByInvoice(invoice, upstreamProductID, since)
	if err != nil {
		return nil, err
	}
	result := &InvoiceHostResult{Match: match}
	if match == nil || s.db == nil {
		return result, nil
	}
	var bound struct {
		HostID string `gorm:"column:host_id"`
	}
	if err := s.db.WithContext(ctx).Raw(
		"SELECT host_id FROM instance.upstream_hosts WHERE supplier_id=? AND BINARY upstream_host_id=BINARY ? LIMIT 1",
		supplierID, match.HostID,
	).Scan(&bound).Error; err == nil {
		result.BoundInstanceID = bound.HostID
	}
	return result, nil
}
