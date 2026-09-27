package zjmf

import (
	billing "ddyidc/internal/billing/balance"
	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/internal/upstream/testpolicy"
	"encoding/json"
	"errors"
)

// Finance suppliers use get_total, not the v10 common-module calculate route.
// Submit the same configuration, cycle and quantity that will be purchased.
func (p *Provider) purchaseQuote(order contracts.OrderInfo) (billing.Money, error) {
	form := orderForm(order)
	form.Del("password")
	form.Del("host")
	form.Del("checkout")
	form.Set("price_basis", "agent")
	body, err := p.authorizedFormPost("/cart/get_total", form)
	if err != nil {
		return 0, err
	}
	if err := requireUpstreamSuccess(body, "purchase quote"); err != nil {
		return 0, err
	}
	amount, err := purchaseQuoteAmount(body, order.Cycle)
	if err != nil {
		return 0, err
	}
	if err := testpolicy.CheckPayment(p.Supplier.URL, amount); err != nil {
		return 0, err
	}
	return amount, nil
}

func purchaseQuoteAmount(body []byte, cycle string) (billing.Money, error) {
	var r struct {
		Products struct {
			Cycle string          `json:"billingcycle"`
			Total json.RawMessage `json:"sale_total"`
			Type  struct {
				Type     int             `json:"type"`
				Discount json.RawMessage `json:"bates"`
			} `json:"type"`
		} `json:"products"`
	}
	invalid := errors.New("供应商配置报价不完整或无效，未创建订单")
	if json.Unmarshal(body, &r) != nil || r.Products.Cycle != cycle {
		return 0, invalid
	}
	parse := func(raw json.RawMessage) (billing.Money, error) {
		value := string(raw)
		if len(value) > 0 && value[0] == '"' {
			if json.Unmarshal(raw, &value) != nil {
				return 0, invalid
			}
		}
		return billing.ParseMoney(value)
	}
	total, err := parse(r.Products.Total)
	if err != nil || total <= 0 {
		return 0, invalid
	}
	// Match v10 finance pricing: type 1 already contains the agent discount;
	// otherwise bates is the amount to subtract, never a catalogue fallback.
	if r.Products.Type.Type != 1 {
		discount, err := parse(r.Products.Type.Discount)
		if err != nil || discount < 0 || discount >= total {
			return 0, invalid
		}
		total -= discount
	}
	return total, nil
}
