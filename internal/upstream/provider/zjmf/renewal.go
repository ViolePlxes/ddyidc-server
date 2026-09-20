package zjmf

import (
	"ddyidc/internal/billing/balance"
	"ddyidc/internal/upstream/provider/contracts"
	"fmt"
	"net/url"
	"strconv"
)

func (p *Provider) RenewalQuotes(id int64) ([]contracts.RenewalQuote, error) {
	b, e := p.authorizedGet("/host/renewpage?hostid=" + strconv.FormatInt(id, 10))
	if e != nil {
		return nil, e
	}
	d, e := businessData(b, "renewal quote")
	if e != nil {
		return nil, e
	}
	rows, _ := d["cycle"].([]any)
	out := []contracts.RenewalQuote{}
	for _, raw := range rows {
		r, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		cycle := firstMapString(r, "billingcycle")
		amount := firstMapString(r, "amount")
		money, e := billing.ParseMoney(amount)
		duration, _ := strconv.ParseInt(firstMapString(r, "duration"), 10, 64)
		if cycle == "" || e != nil || money <= 0 || duration <= 0 {
			continue
		}
		out = append(out, contracts.RenewalQuote{Cycle: cycle, Label: firstMapString(r, "billingcycle_zh"), Amount: money.String(), Duration: duration})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("暂无可用续费周期")
	}
	return out, nil
}
func (p *Provider) CreateRenewalInvoice(id int64, cycle string, duration int64) (string, error) {
	b, e := p.authorizedFormPost("/host/renew", url.Values{"hostid": {strconv.FormatInt(id, 10)}, "billingcycles": {cycle}, "duration": {strconv.FormatInt(duration, 10)}})
	if e != nil {
		return "", fmt.Errorf("续费订单结果待核对，禁止重复创建")
	}
	d, e := businessData(b, "renewal")
	if e != nil {
		return "", e
	}
	invoice := firstMapString(d, "invoiceid", "invoice_id")
	if invoice == "" || invoice == "0" {
		return "", fmt.Errorf("续费订单编号未确认")
	}
	return invoice, nil
}
func (p *Provider) PayRenewalInvoice(invoice string) error {
	b, e := p.authorizedFormPost("/apply_credit", url.Values{"invoiceid": {invoice}, "use_credit": {"1"}})
	if e != nil {
		return fmt.Errorf("续费支付结果待核对")
	}
	return requireUpstreamStatus(b, "renewal payment", 1001)
}
