package zjmf

import (
	"context"
	billing "ddyidc/internal/billing/balance"
	"ddyidc/internal/upstream/provider/contracts"
	"encoding/json"
	"fmt"
)

// Quote uses only get_total. Never add/clear/settle a remote shopping cart here.
func (p *Provider) Quote(ctx context.Context, in contracts.QuoteRequest) (*contracts.QuoteResult, error) {
	if err := p.enforceTestProductPolicy(in.ProductID); err != nil {
		return nil, err
	}
	if in.Quantity < 1 || in.Quantity > 10 {
		return nil, fmt.Errorf("购买数量无效")
	}
	q := *p
	q.instanceContext = ctx
	q.procurementContext = true
	form := orderForm(contracts.OrderInfo{ProductID: in.ProductID, Cycle: in.Cycle, Quantity: in.Quantity, ConfigOptions: in.ConfigOptions, CustomFields: in.CustomFields})
	form.Del("checkout")
	form.Set("price_basis", "standard")
	body, err := q.authorizedFormPost("/cart/get_total", form)
	if err != nil {
		return nil, err
	}
	if err = requireUpstreamSuccess(body, "configuration quote"); err != nil {
		return nil, err
	}
	return decodePublicQuote(body, in.Cycle)
}
func decodeQuoteMoney(raw json.RawMessage) (billing.Money, error) {
	v := string(raw)
	if len(v) > 0 && v[0] == '"' {
		if json.Unmarshal(raw, &v) != nil {
			return 0, fmt.Errorf("报价金额无效")
		}
	}
	return billing.ParseMoney(v)
}
func decodePublicQuote(body []byte, cycle string) (*contracts.QuoteResult, error) {
	total, e := purchaseQuoteAmount(body, cycle)
	if e != nil {
		return nil, e
	}
	var data struct {
		Products struct {
			Renewal         json.RawMessage      `json:"sale_signal_price"`
			OriginalRenewal json.RawMessage      `json:"signal_price"`
			Setup           json.RawMessage      `json:"product_sale_setup_fee"`
			Type            financeQuoteDiscount `json:"type"`
		} `json:"products"`
	}
	if json.Unmarshal(body, &data) != nil {
		return nil, fmt.Errorf("报价格式无效")
	}
	renewalRaw := data.Products.Renewal
	if data.Products.Type.Type != 1 {
		renewalRaw = data.Products.OriginalRenewal
	}
	renewal, e := decodeQuoteMoney(renewalRaw)
	if e != nil || renewal < 0 {
		return nil, fmt.Errorf("续费报价未确认")
	}
	setup, e := decodeQuoteMoney(data.Products.Setup)
	if e != nil || setup < 0 {
		return nil, fmt.Errorf("安装费报价未确认")
	}
	return &contracts.QuoteResult{Total: total.String(), RenewalUnit: renewal.String(), SetupUnit: setup.String(), Currency: "CNY"}, nil
}
