package service

import (
	"ddyidc/internal/upstream/model"
	"ddyidc/internal/upstream/provider/contracts"
	"encoding/json"
	"errors"
	"math"
)

// The supplier's cached page contains procurement prices. Never mutate it:
// each published SKU applies its own currency rate and v10 sales margin.
func retailOrderPage(raw *contracts.OrderPage, mapping model.UpstreamProduct, rate float64) (*contracts.OrderPage, error) {
	if raw == nil || rate <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return nil, errors.New("商品价格配置无效")
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var page contracts.OrderPage
	if err = json.Unmarshal(data, &page); err != nil {
		return nil, err
	}
	price := func(value float64) float64 {
		if value <= 0 {
			return value
		}
		return math.Round(CalculatePrice(value, rate, mapping.ProfitType, mapping.ProfitValue)*100) / 100
	}
	page.Product.Price = price(page.Product.Price)
	page.Product.SetupFee = price(page.Product.SetupFee)
	for i := range page.Cycles {
		page.Cycles[i].Price = price(page.Cycles[i].Price)
		page.Cycles[i].SetupFee = price(page.Cycles[i].SetupFee)
	}
	for i := range page.ConfigOptions {
		for j := range page.ConfigOptions[i].Options {
			page.ConfigOptions[i].Options[j].Price = price(page.ConfigOptions[i].Options[j].Price)
		}
	}
	// Supplier-provided initial passwords must never reach public purchase APIs.
	page.DefaultPassword = ""
	return &page, nil
}
