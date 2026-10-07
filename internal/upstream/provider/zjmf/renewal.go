package zjmf

import (
	"ddyidc/internal/billing/balance"
	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/internal/upstream/testpolicy"
	"fmt"
	"math/big"
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
	current := nestedMap(d, "host")
	out := []contracts.RenewalQuote{}
	for _, raw := range rows {
		r, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		cycle := firstMapString(r, "billingcycle")
		amount := firstMapString(r, "amount")
		original, originalErr := renewalOriginalAmount(r, rows, current)
		if originalErr != nil {
			return nil, originalErr
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

// price is only the base product fee. amount includes the selected configuration,
// and saleproducts is the supplier discount. Fixed current-cycle contracts omit
// that discount; accept a percentage only when multiple other cycles confirm it.
func renewalOriginalAmount(row map[string]any, rows []any, host map[string]any) (billing.Money, error) {
	invalid := fmt.Errorf("供应商完整续费原价未确认，停止报价")
	cost, err := billing.ParseMoney(firstMapString(row, "amount"))
	if err != nil || cost <= 0 {
		return 0, invalid
	}
	discount, err := billing.ParseMoney(firstMapString(row, "saleproducts"))
	if err != nil || discount < 0 {
		return 0, invalid
	}
	if discount > 0 || firstMapString(host, "flag") != "1" || firstMapString(row, "billingcycle") != firstMapString(host, "billingcycle") {
		if discount > billing.Money(99999999999999)-cost {
			return 0, invalid
		}
		return cost + discount, nil
	}
	var rate *big.Rat
	confirmed := 0
	for _, raw := range rows {
		r, ok := raw.(map[string]any)
		if !ok || firstMapString(r, "billingcycle") == firstMapString(host, "billingcycle") {
			continue
		}
		amount, e := billing.ParseMoney(firstMapString(r, "amount"))
		off, e2 := billing.ParseMoney(firstMapString(r, "saleproducts"))
		if e != nil || e2 != nil || amount <= 0 || off < 0 || off > billing.Money(99999999999999)-amount {
			return 0, invalid
		}
		next := big.NewRat(int64(amount), int64(amount+off))
		if rate != nil && rate.Cmp(next) != 0 {
			return 0, invalid
		}
		rate = next
		confirmed++
	}
	if rate == nil || confirmed < 2 {
		return 0, invalid
	}
	value := new(big.Rat).Quo(new(big.Rat).SetInt64(int64(cost)), rate)
	whole, rem := new(big.Int), new(big.Int)
	whole.QuoRem(value.Num(), value.Denom(), rem)
	if rem.Sign() > 0 {
		whole.Add(whole, big.NewInt(1))
	}
	if !whole.IsInt64() || whole.Int64() > 99999999999999 {
		return 0, invalid
	}
	return billing.Money(whole.Int64()), nil
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
