package contracts

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
)

// ProcurementFailure is safe to persist/log. Cause may contain transport or
// supplier secrets and must never be logged. SupplierMessage is the only
// supplier-authored text that may be persisted, and only after sanitizeSupplierMessage
// proves it carries no credentials, URLs, or opaque blobs.
type ProcurementFailure struct {
	Stage           string
	Code            string
	HTTPStatus      int
	SupplierStatus  int
	TransportPhase  string
	RequestWritten  bool
	FirstByte       bool
	ElapsedMS       int64
	SupplierMessage string
	Cause           error `json:"-"`
}

func (e *ProcurementFailure) Error() string {
	safe := sanitizeProcurementFailure(*e)
	transport := ""
	if safe.TransportPhase != "" {
		transport = fmt.Sprintf(" transport=%s written=%t first_byte=%t elapsed_ms=%d", safe.TransportPhase, safe.RequestWritten, safe.FirstByte, safe.ElapsedMS)
	}
	message := ""
	if safe.SupplierMessage != "" {
		message = "：上游返回 " + safe.SupplierMessage
	}
	return fmt.Sprintf("采购失败 [stage=%s code=%s http=%d supplier=%d%s]%s，需人工核对，禁止重复下单", safe.Stage, safe.Code, safe.HTTPStatus, safe.SupplierStatus, transport, message)
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
	if existing == nil {
		failure.SupplierMessage = sanitizeSupplierMessage(err)
	}
	return &failure
}

var (
	supplierSecretPattern = regexp.MustCompile(`(?i)(password|passwd|pwd|secret|token|api[_-]?key|authorization|cookie|session|private[_-]?key)`)
	supplierURLPattern    = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://\S+`)
	supplierEmailPattern  = regexp.MustCompile(`[\w.+-]+@[\w-]+\.[\w.-]+`)
	supplierBlobPattern   = regexp.MustCompile(`\b[0-9a-fA-F]{32,}\b|\b[A-Za-z0-9+/]{40,}={0,2}\b`)
	supplierSpacePattern  = regexp.MustCompile(`\s+`)
)

// sanitizeSupplierMessage returns the supplier's own failure text only when it
// is provably free of credentials, URLs, addresses and opaque blobs; otherwise
// the administrator keeps the categorical Evidence text alone.
func sanitizeSupplierMessage(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if text == "" {
		return ""
	}
	for _, pattern := range []*regexp.Regexp{supplierSecretPattern, supplierURLPattern, supplierEmailPattern, supplierBlobPattern} {
		if pattern.MatchString(text) {
			return ""
		}
	}
	text = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || (r < 0x20) {
			return ' '
		}
		return r
	}, text)
	text = strings.TrimSpace(supplierSpacePattern.ReplaceAllString(text, " "))
	if runes := []rune(text); len(runes) > 160 {
		text = string(runes[:160]) + "…"
	}
	return text
}

func sanitizeProcurementFailure(value ProcurementFailure) ProcurementFailure {
	switch value.TransportPhase {
	case "", "connection_wait", "dns", "connect", "tls", "request_write", "response_headers", "response_body":
	default:
		value.TransportPhase = "connection_wait"
	}
	if value.ElapsedMS < 0 {
		value.ElapsedMS = 0
	}
	if value.ElapsedMS > 3600000 {
		value.ElapsedMS = 3600000
	}
	switch value.Stage {
	case "validation", "provider", "quote", "cart_add", "cart_settle", "invoice_created", "balance_read", "payment", "paid", "active":
	default:
		value.Stage = "provider"
	}
	switch value.Code {
	case "unclassified", "cancelled", "timeout", "network", "checkpoint_failed", "http_rejected", "authentication_rejected", "budget_exceeded", "invalid_cart_position", "unexpected_invoice", "invalid_response", "invoice_missing", "supplier_rejected", "quote_invalid", "payment_not_sent":
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
