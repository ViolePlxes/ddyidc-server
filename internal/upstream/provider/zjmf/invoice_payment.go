package zjmf

import (
	"context"
	"ddyidc/internal/upstream/provider/contracts"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// PayInvoice pays an existing purchase invoice with supplier credit. It is used
// only when a previous payment was never sent: the invoice still exists and the
// order is already paid by the customer. Paying an already paid invoice is
// rejected locally after a read-only Paid-state check; unknown states fail closed.
func (p *Provider) PayInvoice(invoice string) error {
	invoice = strings.TrimSpace(invoice)
	if invoice == "" {
		return errors.New("上游账单 ID 为空")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	scoped := *p
	scoped.instanceContext = ctx
	scoped.procurementContext = true
	scoped.paymentContext = true
	raw, err := scoped.authorizedGet("/invoices/" + url.PathEscape(invoice))
	if err != nil {
		return err
	}
	paid, err := invoicePaymentState(raw, invoice)
	if err != nil {
		return err
	}
	if paid {
		return nil
	}
	body, err := scoped.authorizedFormPost("/apply_credit", url.Values{"invoiceid": {invoice}, "use_credit": {"1"}, "enough": {"0"}})
	if err != nil {
		return classifyInvoicePaymentFailure(err)
	}
	return requireProcurementStatus(body, 1001)
}

// Only an explicit pre-request failure or a completed pre-write transport phase
// proves that replay is safe. A missing WroteRequest callback is not proof.
func classifyInvoicePaymentFailure(err error) error {
	var f *contracts.ProcurementFailure
	if errors.As(err, &f) {
		if f.Code == "payment_not_sent" {
			return err
		}
		if !f.RequestWritten && !f.FirstByte {
			switch f.TransportPhase {
			case "connection_wait", "dns", "connect", "tls":
				return &contracts.ProcurementFailure{Stage: "payment", Code: "payment_not_sent", TransportPhase: f.TransportPhase, Cause: err}
			}
		}
	}
	return err
}
func invoicePaymentState(raw []byte, expected string) (bool, error) {
	var payload struct {
		Status int `json:"status"`
		Data   struct {
			Invoice struct {
				ID     json.RawMessage `json:"id"`
				Status string          `json:"status"`
			} `json:"invoices"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false, err
	}
	id := strings.Trim(string(payload.Data.Invoice.ID), "\"")
	if payload.Status != 200 || id != expected {
		return false, errors.New("账单付款状态未能核实，禁止补付")
	}
	switch strings.ToLower(payload.Data.Invoice.Status) {
	case "paid":
		return true, nil
	case "unpaid":
		return false, nil
	default:
		return false, fmt.Errorf("账单不处于可补付状态")
	}
}
