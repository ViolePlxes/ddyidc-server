// Package testpolicy contains deployment-only restrictions for isolated labs.
// It does not change supplier protocol capabilities or authorize a purchase.
package testpolicy

import (
	billing "ddyidc/internal/billing/balance"
	"errors"
	"net/url"
	"strings"
	"sync/atomic"
)

type Rule struct {
	Host           string
	ProductIDs     []int
	BlockCatalogue bool
	MaxPayment     string `json:",optional"`
}

type Config struct {
	Enabled bool
	Rules   []Rule
}

type restriction struct {
	products       map[int]bool
	blockCatalogue bool
	maxPayment     billing.Money
}

type policy struct{ rules map[string]restriction }

var active atomic.Pointer[policy]

// Configure is called once before serving requests. A local policy must never
// silently carry into a production deployment. Failed validation preserves the
// previous restrictions rather than partially publishing a new configuration.
func Configure(environment string, cfg Config) error {
	if environment == "" {
		environment = "production"
	}
	if environment != "production" && environment != "local-vm" {
		return errors.New("invalid DeploymentEnvironment")
	}
	if !cfg.Enabled && len(cfg.Rules) != 0 {
		return errors.New("disabled upstream test policy contains rules")
	}
	if cfg.Enabled && environment != "local-vm" {
		return errors.New("upstream test policy is forbidden in production")
	}
	if cfg.Enabled && len(cfg.Rules) == 0 {
		return errors.New("enabled upstream test policy requires explicit rules")
	}
	next := &policy{rules: make(map[string]restriction)}
	for _, rule := range cfg.Rules {
		host := strings.ToLower(strings.TrimSpace(rule.Host))
		parsed, err := url.Parse("https://" + host)
		if err != nil || host == "" || parsed.Hostname() != host || parsed.Host != host || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil || strings.HasSuffix(host, ".") {
			return errors.New("upstream test policy requires an exact hostname")
		}
		if _, exists := next.rules[host]; exists {
			return errors.New("duplicate upstream test policy hostname")
		}
		r := restriction{products: make(map[int]bool), blockCatalogue: rule.BlockCatalogue}
		if rule.MaxPayment != "" {
			amount, err := billing.ParseMoney(rule.MaxPayment)
			if err != nil || amount <= 0 {
				return errors.New("invalid upstream test payment ceiling")
			}
			r.maxPayment = amount
		}
		for _, id := range rule.ProductIDs {
			if id <= 0 || r.products[id] {
				return errors.New("invalid upstream test policy product IDs")
			}
			r.products[id] = true
		}
		next.rules[host] = r
	}
	active.Store(next)
	return nil
}

func forURL(raw string) (restriction, bool) {
	p := active.Load()
	if p == nil {
		return restriction{}, false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return restriction{}, false
	}
	r, ok := p.rules[strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")]
	return r, ok
}

func CheckProduct(raw string, id int) error {
	if r, ok := forURL(raw); ok && !r.products[id] {
		return errors.New("local test policy does not permit this product")
	}
	return nil
}

func CheckCatalogue(raw string) error {
	if r, ok := forURL(raw); ok && r.blockCatalogue {
		return errors.New("local test policy forbids full catalogue sync")
	}
	return nil
}

// A per-payment ceiling is a lab restriction, never a production price rule.
func CheckPayment(raw string, amount billing.Money) error {
	if amount <= 0 {
		return errors.New("unverified upstream payment amount")
	}
	if r, ok := forURL(raw); ok && r.maxPayment > 0 && amount > r.maxPayment {
		return errors.New("供应商订单实际金额超过测试付款上限，未付款")
	}
	return nil
}
