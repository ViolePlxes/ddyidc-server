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
	var data struct {
		Products struct {
			Cycle           string               `json:"billingcycle"`
			Total           json.RawMessage      `json:"total"`
			SaleTotal       json.RawMessage      `json:"sale_total"`
			OriginalRenewal json.RawMessage      `json:"signal_price"`
			Setup           json.RawMessage      `json:"product_setup_fee"`
			SaleSetup       json.RawMessage      `json:"product_sale_setup_fee"`
			Type            financeQuoteDiscount `json:"type"`
		} `json:"products"`
	}
	if json.Unmarshal(body, &data) != nil || data.Products.Cycle != cycle {
		return nil, fmt.Errorf("报价格式或周期无效")
	}
	// Standard-mode no-discount responses may omit duplicate original-total/setup keys.
	// Discounted responses must carry explicit originals; never reuse the cost decoder.
	discount, de := decodeQuoteMoney(data.Products.Type.Discount)
	noDiscount := data.Products.Type.Type == 0 && de == nil && discount == 0
	totalRaw := data.Products.Total
	if len(totalRaw) == 0 && noDiscount {
		totalRaw = data.Products.SaleTotal
	}
	total, e := decodeQuoteMoney(totalRaw)
	if e != nil || total <= 0 {
		return nil, fmt.Errorf("商品完整原价未确认")
	}
	renewal, e := decodeQuoteMoney(data.Products.OriginalRenewal)
	if e != nil || renewal < 0 {
		return nil, fmt.Errorf("续费完整原价未确认")
	}
	setupRaw := data.Products.Setup
	if len(setupRaw) == 0 && noDiscount {
		setupRaw = data.Products.SaleSetup
	}
	setup, e := decodeQuoteMoney(setupRaw)
	if e != nil || setup < 0 {
		return nil, fmt.Errorf("安装费原价未确认")
	}
	return &contracts.QuoteResult{Total: total.String(), RenewalUnit: renewal.String(), SetupUnit: setup.String(), Currency: "CNY"}, nil
}
