package zjmf

import (
	"ddyidc/internal/billing/balance"
	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/internal/upstream/testpolicy"
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
		original, originalErr := billing.ParseMoney(firstMapString(r, "price"))
		if originalErr != nil || original <= 0 {
			return nil, fmt.Errorf("供应商未返回有效续费原价，停止报价")
		}
		money, e := billing.ParseMoney(amount)
		duration, _ := strconv.ParseInt(firstMapString(r, "duration"), 10, 64)
		if cycle == "" || e != nil || money <= 0 || duration <= 0 {
			continue
		}
		out = append(out, contracts.RenewalQuote{Cycle: cycle, Label: firstMapString(r, "billingcycle_zh"), Amount: money.String(), OriginalAmount: original.String(), Duration: duration})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("暂无可用续费周期")
	}
	return out, nil
}
func (p *Provider) CreateRenewalInvoice(id int64, cycle string, duration int64) (string, string, error) {
	quotes, err := p.RenewalQuotes(id)
	if err != nil {
		return "", "", err
	}
	var amount billing.Money
	for _, quote := range quotes {
		if quote.Cycle == cycle && quote.Duration == duration {
			amount, err = billing.ParseMoney(quote.Amount)
			break
		}
	}
	if err != nil || amount <= 0 {
		return "", "", fmt.Errorf("供应商未返回匹配周期的续费报价，未创建订单")
	}
	if err := testpolicy.CheckPayment(p.Supplier.URL, amount); err != nil {
		return "", "", err
	}
	b, e := p.authorizedFormPost("/host/renew", url.Values{"hostid": {strconv.FormatInt(id, 10)}, "billingcycles": {cycle}, "duration": {strconv.FormatInt(duration, 10)}})
	if e != nil {
		return "", "", fmt.Errorf("续费订单结果待核对，禁止重复创建")
	}
	d, e := businessData(b, "renewal")
	if e != nil {
		return "", "", e
	}
	invoice := firstMapString(d, "invoiceid", "invoice_id")
	if invoice == "" || invoice == "0" {
		return "", "", fmt.Errorf("续费订单编号未确认")
	}
	return invoice, amount.String(), nil
}
func (p *Provider) PayRenewalInvoice(invoice string) error {
	b, e := p.authorizedFormPost("/apply_credit", url.Values{"invoiceid": {invoice}, "use_credit": {"1"}})
	if e != nil {
		return fmt.Errorf("续费支付结果待核对")
	}
	return requireUpstreamStatus(b, "renewal payment", 1001)
}
