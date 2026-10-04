package service

import (
	"ddyidc/internal/upstream/model"
	"ddyidc/internal/upstream/provider/contracts"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"strconv"
)

// CalculateRetailPrice rounds decimal supplier amounts once, after rate and margin.
func CalculateRetailPrice(value, rate float64, profitType int, profit float64) float64 {
	decimal := func(v float64) *big.Rat {
		r, ok := new(big.Rat).SetString(strconv.FormatFloat(v, 'f', -1, 64))
		if !ok {
			return nil
		}
		return r
	}
	v, r, p := decimal(value), decimal(rate), decimal(profit)
	if v == nil || r == nil || p == nil {
		return math.NaN()
	}
	v.Mul(v, r)
	if profitType == 1 {
		v.Add(v, p)
	} else {
		p.Add(p, big.NewRat(100, 1))
		p.Quo(p, big.NewRat(100, 1))
		v.Mul(v, p)
	}
	v.Mul(v, big.NewRat(100, 1))
	n := new(big.Int).Abs(v.Num())
	q, remainder := new(big.Int), new(big.Int)
	q.QuoRem(n, v.Denom(), remainder)
	if remainder.Lsh(remainder, 1).Cmp(v.Denom()) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if v.Sign() < 0 {
		q.Neg(q)
	}
	result, _ := new(big.Rat).SetFrac(q, big.NewInt(100)).Float64()
	return result
}

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
		return CalculateRetailPrice(value, rate, 0, 0)
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
