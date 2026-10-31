package zjmf

import (
	"ddyidc/internal/billing/balance"
	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/internal/upstream/testpolicy"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func (p *Provider) RenewalQuotes(id int64) ([]contracts.RenewalQuote, error) {
	read := func(basis string) (map[string]any, error) {
		b, err := p.authorizedGet("/host/renewpage?hostid=" + strconv.FormatInt(id, 10) + "&price_basis=" + basis)
		if err != nil {
			return nil, err
		}
		return businessData(b, "renewal quote")
	}
	agent, e := read("agent")
	if e != nil {
		return nil, e
	}
	standard, e := read("standard")
	if e != nil {
		return nil, e
	}
	rows, _ := agent["cycle"].([]any)
	retailRows, _ := standard["cycle"].([]any)
	host := nestedMap(standard, "host")
	out := []contracts.RenewalQuote{}
	for _, raw := range rows {
		r, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		cycle := firstMapString(r, "billingcycle")
		money, err := billing.ParseMoney(firstMapString(r, "amount"))
		duration, _ := strconv.ParseInt(firstMapString(r, "duration"), 10, 64)
		if cycle == "" || err != nil || money <= 0 || duration <= 0 {
			continue
		}
		var original billing.Money
		matches := 0
		for _, value := range retailRows {
			row, ok := value.(map[string]any)
			if !ok || firstMapString(row, "billingcycle") != cycle {
				continue
			}
			matches++
			original, err = billing.ParseMoney(firstMapString(row, "amount"))
			if err != nil || original <= 0 {
				return nil, fmt.Errorf("供应商完整续费原价未确认，停止报价")
			}
		}
		if matches != 1 {
			return nil, fmt.Errorf("供应商续费周期原价未唯一确认")
		}
		// Fixed-contract current-cycle amounts can remain discounted even in standard mode.
		// Use only the exact fixed catalogue offer, never a discount from another cycle.
		if firstMapString(host, "flag") == "1" && cycle == firstMapString(host, "billingcycle") {
			pid, err := strconv.Atoi(firstMapString(host, "productid"))
			if err != nil || pid <= 0 {
				return nil, fmt.Errorf("供应商续费商品身份未确认")
			}
			detail, err := p.renewalOriginalProduct(pid)
			if err != nil || detail == nil || detail.ID != pid || detail.Cycle != cycle || detail.HasConfigOptions || detail.Type == "mf_cloud" || detail.Type == "mf_dcim" {
				return nil, fmt.Errorf("固定合约完整续费原价未确认，请人工设置售价")
			}
			original, err = billing.ParseMoney(strconv.FormatFloat(detail.Price, 'f', 2, 64))
			if err != nil || original <= 0 {
				return nil, fmt.Errorf("供应商续费原价无效")
			}
		}
		if original < money {
			return nil, fmt.Errorf("供应商原价低于采购成本，请人工核对")
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
		return fmt.Errorf("续费支付请求未收到有效响应（网络或超时），是否扣款未知")
	}
	var payload struct {
		Status  int    `json:"status"`
		Message string `json:"msg"`
	}
	if decodeJSON(b, &payload) == nil && payload.Status != 1001 && insufficientUpstreamBalance(payload.Message) {
		return fmt.Errorf("上游账户余额不足，无法支付续费账单")
	}
	return requireUpstreamStatus(b, "renewal payment", 1001)
}

// The finance API rejects a credit payment when the reseller account balance is
// too low. Keep that as a precise, non-generic reason for the manual queue.
func insufficientUpstreamBalance(message string) bool {
	if strings.TrimSpace(message) == "" {
		return false
	}
	for _, token := range []string{"余额不足", "余额不够", "余额不足够", "insufficient", "not enough balance", "balance is not enough", "credit_not_enough", "insufficient_balance"} {
		if strings.Contains(strings.ToLower(message), strings.ToLower(token)) {
			return true
		}
	}
	return false
}

// Payment verification must not reuse the ten-minute catalogue display cache.
func (p *Provider) renewalOriginalProduct(id int) (*contracts.UpstreamProductDetail, error) {
	if err := p.enforceTestProductPolicy(id); err != nil {
		return nil, err
	}
	body, err := p.authorizedGet(fmt.Sprintf("/api/product/%d?price_basis=standard", id))
	if err != nil {
		return nil, err
	}
	var response struct {
		Status int `json:"status"`
		Data   struct {
			Product financeProduct `json:"product"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &response) != nil || response.Status != 200 || response.Data.Product.ID != id {
		return nil, fmt.Errorf("完整商品原价未确认")
	}
	return response.Data.Product.toContract(id), nil
}
