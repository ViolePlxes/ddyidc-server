package zjmf

import (
	billing "ddyidc/internal/billing/balance"
	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/internal/upstream/testpolicy"
	"fmt"
	"math/big"
	"net/url"
	"strconv"
)

func (p *Provider) UpgradePlans(id int64) ([]contracts.UpgradePlan, error) {
	if id <= 0 {
		return nil, fmt.Errorf("invalid upgrade host")
	}
	b, e := p.authorizedGet("/upgrade/upgrade_product/" + strconv.FormatInt(id, 10))
	if e != nil {
		return nil, e
	}
	d, e := businessData(b, "upgrade plans")
	if e != nil {
		return nil, e
	}
	old := nestedMap(d, "old_host")
	if old == nil {
		return nil, fmt.Errorf("产品升级关联未确认")
	}
	rows, ok := d["host"].([]any)
	if !ok {
		return nil, fmt.Errorf("产品升级列表未确认")
	}
	out := []contracts.UpgradePlan{}
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("升级列表格式无效")
		}
		pid := firstMapString(row, "id", "pid")
		n, e := strconv.ParseInt(pid, 10, 64)
		if e != nil || n <= 0 {
			continue
		}
		plan := contracts.UpgradePlan{ProductID: pid, Name: firstMapString(row, "name", "host"), Cycles: []contracts.UpgradeCycle{}}
		cycles, _ := row["cycle"].([]any)
		for _, rawCycle := range cycles {
			cycle, ok := rawCycle.(map[string]any)
			if !ok {
				continue
			}
			key := firstMapString(cycle, "billingcycle")
			if key != "" {
				plan.Cycles = append(plan.Cycles, contracts.UpgradeCycle{ID: key, Label: firstMapString(cycle, "billingcycle_zh")})
			}
		}
		if plan.Name != "" && len(plan.Cycles) > 0 {
			out = append(out, plan)
		}
	}
	return out, nil
}
func undiscountedUpgrade(cost billing.Money, bates string) (billing.Money, error) {
	rate, ok := new(big.Rat).SetString(bates)
	if !ok || rate.Sign() <= 0 || rate.Cmp(big.NewRat(10, 1)) > 0 {
		return 0, fmt.Errorf("产品升级折扣依据无效")
	}
	value := new(big.Rat).Quo(new(big.Rat).Mul(new(big.Rat).SetInt64(int64(cost)), big.NewRat(10, 1)), rate)
	amount := new(big.Int).Quo(value.Num(), value.Denom())
	if new(big.Int).Mod(value.Num(), value.Denom()).Sign() > 0 {
		amount.Add(amount, big.NewInt(1))
	}
	if !amount.IsInt64() || amount.Sign() <= 0 || amount.Int64() > int64(billing.MaxMoney) {
		return 0, fmt.Errorf("产品升级原价无效")
	}
	return billing.Money(amount.Int64()), nil
}
func (p *Provider) QuoteUpgrade(id int64, pid, cycle string) (contracts.UpgradeQuote, error) {
	var out contracts.UpgradeQuote
	plans, e := p.UpgradePlans(id)
	if e != nil {
		return out, e
	}
	allowed := false
	for _, plan := range plans {
		if plan.ProductID != pid {
			continue
		}
		for _, c := range plan.Cycles {
			if c.ID == cycle {
				allowed = true
			}
		}
	}
	if !allowed {
		return out, fmt.Errorf("当前实例没有该升级方案或周期")
	}
	// This is the quote confirmation endpoint used by the finance client page.
	// It is separate from invoice checkout and cannot pay or execute a module.
	q := *p
	q.procurementContext = true
	_, e = q.authorizedFormPost("/servicedetail?id="+strconv.FormatInt(id, 10)+"&action=upgrade", url.Values{"hid": {strconv.FormatInt(id, 10)}, "pid": {pid}, "billingcycle": {cycle}})
	if e != nil {
		return out, e
	}
	b, e := q.authorizedGet("/upgrade/upgrade_product_page?hid=" + strconv.FormatInt(id, 10))
	if e != nil {
		return out, e
	}
	d, e := businessData(b, "upgrade price")
	if e != nil {
		return out, e
	}
	old := nestedMap(d, "old_host")
	currency := nestedMap(d, "currency")
	discount := nestedMap(d, "type")
	if firstMapString(old, "id") != strconv.FormatInt(id, 10) || firstMapString(d, "new_pid") != pid || firstMapString(d, "billingcycle") != cycle || firstMapString(currency, "code") != "CNY" || firstMapString(discount, "type") != "1" {
		return out, fmt.Errorf("升级报价的实例、产品、周期或货币未确认")
	}
	cost, e := billing.ParseMoney(firstMapString(d, "amount_total"))
	if e != nil || cost <= 0 {
		return out, fmt.Errorf("当前方案无有效正数差价；涉及返款的降配请人工核查")
	}
	original, e := undiscountedUpgrade(cost, firstMapString(discount, "bates"))
	if e != nil {
		return out, e
	}
	return contracts.UpgradeQuote{HostID: strconv.FormatInt(id, 10), ProductID: pid, Cycle: cycle, Cost: cost.String(), OriginalAmount: original.String()}, nil
}
func (p *Provider) CreateUpgradeInvoice(id int64, pid, cycle, expected string) (string, string, error) {
	quote, e := p.QuoteUpgrade(id, pid, cycle)
	if e != nil {
		return "", "", e
	}
	quoted, parseErr := billing.ParseMoney(quote.Cost)
	ceiling, ceilingErr := billing.ParseMoney(expected)
	if parseErr != nil || ceilingErr != nil || quoted <= 0 || quoted > ceiling {
		return "", "", fmt.Errorf("升级差价已变化，未创建供应商账单")
	}
	cost, e := billing.ParseMoney(quote.Cost)
	if e != nil {
		return "", "", e
	}
	if e = testpolicy.CheckPayment(p.Supplier.URL, cost); e != nil {
		return "", "", e
	}
	q := *p
	q.procurementContext = true
	b, e := q.authorizedFormPost("/upgrade/checkout_upgrade_product", url.Values{"hid": {strconv.FormatInt(id, 10)}, "payment": {"credit"}})
	if e != nil {
		return "", "", fmt.Errorf("升级账单结果待核对，禁止重复结算")
	}
	d, e := businessData(b, "upgrade invoice")
	if e != nil {
		return "", "", e
	}
	invoice := firstMapString(d, "invoice_id", "invoiceid")
	n, parseErr := strconv.ParseInt(invoice, 10, 64)
	if parseErr != nil || n <= 0 {
		return "", "", fmt.Errorf("升级账单编号未确认")
	}
	amount := firstMapString(d, "amount_total")
	actual, e := billing.ParseMoney(amount)
	if e != nil || actual != cost {
		return invoice, "", fmt.Errorf("升级账单金额未确认，禁止支付")
	}
	if _, e = q.verifyUpgradeInvoice(invoice, strconv.FormatInt(id, 10), cost); e != nil {
		return invoice, "", e
	}
	return invoice, actual.String(), nil
}
func (p *Provider) verifyUpgradeInvoice(invoice, host string, cost billing.Money) (string, error) {
	b, e := p.authorizedGet("/get_invoices_detail?id=" + url.QueryEscape(invoice))
	if e != nil {
		return "", e
	}
	d, e := businessData(b, "upgrade invoice verification")
	if e != nil {
		return "", e
	}
	detail := nestedMap(d, "detail")
	amount, e := billing.ParseMoney(firstMapString(detail, "subtotal"))
	if e != nil || amount != cost || firstMapString(detail, "id") != invoice {
		return "", fmt.Errorf("升级账单金额或编号未确认，禁止支付")
	}
	state := firstMapString(detail, "status")
	if state != "Unpaid" && state != "Paid" {
		return "", fmt.Errorf("升级账单状态未确认，禁止支付")
	}
	items, ok := d["invoice_items"].([]any)
	if !ok {
		return "", fmt.Errorf("升级账单关联未确认，禁止支付")
	}
	matched := false
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			return "", fmt.Errorf("升级账单项目无效")
		}
		kind := firstMapString(item, "type")
		if kind == "discount" || kind == "promo" {
			continue
		}
		if kind != "upgrade" && kind != "product_upgrade" {
			return "", fmt.Errorf("账单包含非升级项目，禁止支付")
		}
		if firstMapString(item, "hid", "host_id") != host {
			return "", fmt.Errorf("升级账单实例关联未确认，禁止支付")
		}
		matched = true
	}
	if !matched {
		return "", fmt.Errorf("升级账单缺少实例项目，禁止支付")
	}
	return state, nil
}
func (p *Provider) PayUpgradeInvoice(invoice string) error {
	// Credit checkout may have paid the invoice already. Inspect its canonical
	// state first instead of applying account credit for a second time.
	b, e := p.authorizedGet("/get_invoices_detail?id=" + url.QueryEscape(invoice))
	if e != nil {
		return e
	}
	d, e := businessData(b, "upgrade payment verification")
	if e != nil {
		return e
	}
	detail := nestedMap(d, "detail")
	if firstMapString(detail, "id") != invoice {
		return fmt.Errorf("升级账单编号未确认")
	}
	state := firstMapString(detail, "status")
	if state == "Paid" {
		return nil
	}
	if state != "Unpaid" {
		return fmt.Errorf("升级账单状态未确认")
	}
	return p.PayRenewalInvoice(invoice)
}

var _ contracts.UpgradeProvider = (*Provider)(nil)

func (p *Provider) ConfirmUpgradeInvoice(host int64, invoice, amount string) (bool, error) {
	cost, e := billing.ParseMoney(amount)
	if e != nil || cost <= 0 {
		return false, fmt.Errorf("升级核对金额无效")
	}
	state, e := p.verifyUpgradeInvoice(invoice, strconv.FormatInt(host, 10), cost)
	return state == "Paid", e
}
