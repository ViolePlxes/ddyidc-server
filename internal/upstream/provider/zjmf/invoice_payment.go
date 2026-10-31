package zjmf

import (
	"context"
	"ddyidc/internal/upstream/provider/contracts"
	"errors"
	"net/url"
	"strings"
	"time"
)

// PayInvoice pays an existing purchase invoice with supplier credit. It is used
// only when a previous payment was never sent: the invoice still exists and the
// order is already paid by the customer. Paying an already paid invoice is
// rejected by the supplier, so a retry cannot charge twice.
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
	body, err := scoped.authorizedFormPost("/apply_credit", url.Values{"invoiceid": {invoice}, "use_credit": {"1"}, "enough": {"0"}})
	if err != nil {
		var failure *contracts.ProcurementFailure
		if errors.As(err, &failure) && failure.RequestWritten {
			return err
		}
		return &contracts.ProcurementFailure{Stage: "payment", Code: "payment_not_sent", RequestWritten: false, Cause: err}
	}
	return requireProcurementStatus(body, 1001)
}
