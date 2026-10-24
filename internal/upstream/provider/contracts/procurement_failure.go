package contracts

import (
	"context"
	"errors"
	"fmt"
	"net"
)

// ProcurementFailure is safe to persist/log. Cause may contain transport or
// supplier secrets and must never be logged. Keep the public text categorical.
type ProcurementFailure struct {
	Stage          string
	Code           string
	HTTPStatus     int
	SupplierStatus int
	Cause          error `json:"-"`
}

func (e *ProcurementFailure) Error() string {
	safe := sanitizeProcurementFailure(*e)
	return fmt.Sprintf("采购失败 [stage=%s code=%s http=%d supplier=%d]，需人工核对，禁止重复下单", safe.Stage, safe.Code, safe.HTTPStatus, safe.SupplierStatus)
}

func (e *ProcurementFailure) Unwrap() error { return e.Cause }

func ClassifyProcurementFailure(stage string, err error) *ProcurementFailure {
	var existing *ProcurementFailure
	if errors.As(err, &existing) {
		copy := *existing
		if copy.Stage == "" {
			copy.Stage = stage
		}
		copy = sanitizeProcurementFailure(copy)
		return &copy
	}
	code := "unclassified"
	var network net.Error
	switch {
	case errors.Is(err, context.Canceled):
		code = "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		code = "timeout"
	case errors.As(err, &network):
		code = "network"
		if network.Timeout() {
			code = "timeout"
		}
	}
	failure := sanitizeProcurementFailure(ProcurementFailure{Stage: stage, Code: code, Cause: err})
	return &failure
}

func sanitizeProcurementFailure(value ProcurementFailure) ProcurementFailure {
	switch value.Stage {
	case "validation", "provider", "quote", "cart_add", "cart_settle", "invoice_created", "balance_read", "payment", "paid", "active":
	default:
		value.Stage = "provider"
	}
	switch value.Code {
	case "unclassified", "cancelled", "timeout", "network", "checkpoint_failed", "http_rejected", "authentication_rejected", "budget_exceeded", "invalid_cart_position", "unexpected_invoice", "invalid_response", "invoice_missing", "supplier_rejected", "quote_invalid":
	default:
		value.Code = "unclassified"
	}
	return value
}

// SafePreflightRetry is emitted only after durable proof that no procurement
// write has begun. Never infer it from a message or a historical unknown row.
type SafePreflightRetry struct{ Cause error }

func (e *SafePreflightRetry) Error() string {
	return "采购前报价暂时不可用，尚未提交开通，将限次重试"
}
func (e *SafePreflightRetry) Unwrap() error     { return e.Cause }
func (e *SafePreflightRetry) SafeToRetry() bool { return true }
