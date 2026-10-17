package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	billing "ddyidc/internal/billing/balance"
	"ddyidc/internal/upstream/provider/contracts"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type QuoteInput struct {
	LocalProductID string            `json:"localProductId"`
	Cycle          string            `json:"cycle"`
	Quantity       int               `json:"quantity"`
	ConfigOptions  map[string]string `json:"configOptions"`
	CustomFields   map[string]string `json:"customFields,omitempty"`
}
type RetailQuote struct {
	Schema             string `json:"-"`
	ProcurementCeiling string `json:"-"`
	Token              string `json:"token"`
	Total              string `json:"total"`
	Unit               string `json:"unit"`
	Renewal            string `json:"renewal"`
	Setup              string `json:"setup"`
	Currency           string `json:"currency"`
	ExpiresAt          int64  `json:"expiresAt"`
	Cycle              string `json:"cycle"`
	Quantity           int    `json:"quantity"`
}
type quoteSeal struct {
	Schema      string `json:"schema,omitempty"`
	Fingerprint string `json:"fingerprint"`
	Total       string `json:"total"`
	Expires     int64  `json:"expires"`
}

func quoteFingerprint(in QuoteInput) string {
	raw, _ := json.Marshal(in)
	v := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(v[:])
}
func (s *Store) sealQuote(in QuoteInput, total string, expires int64, schema ...string) (string, error) {
	if len(s.aesKey) < 16 {
		return "", errors.New("报价签名未配置")
	}
	schemaHash := ""
	if len(schema) > 0 {
		schemaHash = schema[0]
	}
	raw, _ := json.Marshal(quoteSeal{Fingerprint: quoteFingerprint(in), Total: total, Expires: expires, Schema: schemaHash})
	mac := hmac.New(sha256.New, s.aesKey)
	mac.Write([]byte("adapter-quote-v1:"))
	mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
func (s *Store) verifyQuote(token string, in QuoteInput) (quoteSeal, error) {
	var seal quoteSeal
	bad := errors.New("报价已失效或配置已变更，请返回商品页面重新确认")
	parts := strings.Split(token, ".")
	if len(parts) != 2 || len(token) > 2048 {
		return seal, bad
	}
	raw, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return seal, bad
	}
	sig, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return seal, bad
	}
	mac := hmac.New(sha256.New, s.aesKey)
	mac.Write([]byte("adapter-quote-v1:"))
	mac.Write(raw)
	if len(s.aesKey) < 16 || !hmac.Equal(sig, mac.Sum(nil)) || json.Unmarshal(raw, &seal) != nil || seal.Expires <= time.Now().Unix() || seal.Fingerprint != quoteFingerprint(in) {
		return seal, bad
	}
	return seal, nil
}
func ConfigurationVisible(o contracts.ConfigOption, values map[string]string) bool {
	for _, c := range o.VisibleWhen {
		matched := false
		for _, want := range c.Values {
			if values[strconv.Itoa(c.OptionID)] == want {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
func NormalizeConfiguration(page *contracts.OrderPage, values map[string]string) (map[string]string, error) {
	if page == nil || len(values) > 64 {
		return nil, errors.New("商品配置无效")
	}
	allowed := map[string]bool{}
	out := map[string]string{}
	for _, o := range page.ConfigOptions {
		key := strconv.Itoa(o.ID)
		allowed[key] = true
		value, ok := values[key]
		if !ConfigurationVisible(o, values) {
			if ok && value != "" {
				return nil, fmt.Errorf("%s在当前配置下不可选", o.Name)
			}
			continue
		}
		if !ok || strings.TrimSpace(value) == "" {
			if o.Required {
				return nil, fmt.Errorf("请选择%s", o.Name)
			}
			continue
		}
		if len(value) > 2048 {
			return nil, errors.New("配置值过长")
		}
		switch o.OptionType {
		case "qty", "quantity", "quantity_range":
			n, e := strconv.ParseFloat(value, 64)
			step := o.Step
			if step <= 0 {
				step = 1
			}
			steps := (n - o.Min) / step
			if e != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < o.Min || n > o.Max || math.Abs(steps-math.Round(steps)) > 1e-7 {
				return nil, fmt.Errorf("%s超出范围或步长无效", o.Name)
			}
			value = strconv.FormatFloat(n, 'f', -1, 64)
		case "", "select", "radio", "yes_no", "area":
			found := false
			for _, c := range o.Options {
				if strconv.Itoa(c.ID) == value {
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("%s选项无效", o.Name)
			}
		case "multi_select", "cascade":
			var selected []string
			if json.Unmarshal([]byte(value), &selected) != nil || len(selected) > 32 || (o.Required && len(selected) == 0) {
				return nil, fmt.Errorf("%s配置值无效", o.Name)
			}
			seen := map[string]bool{}
			choices := o.Options
			for _, id := range selected {
				if seen[id] {
					return nil, fmt.Errorf("%s存在重复选项", o.Name)
				}
				seen[id] = true
				var found *contracts.ConfigOptionValue
				for j := range choices {
					if strconv.Itoa(choices[j].ID) == id {
						found = &choices[j]
						break
					}
				}
				if found == nil {
					return nil, fmt.Errorf("%s配置选项无效", o.Name)
				}
				if o.OptionType == "cascade" {
					choices = found.Children
				}
			}
			if o.OptionType == "cascade" && len(choices) > 0 {
				return nil, fmt.Errorf("请完整选择%s", o.Name)
			}
			if o.OptionType == "multi_select" {
				sort.Strings(selected)
			}
			raw, _ := json.Marshal(selected)
			value = string(raw)
		default:
			return nil, fmt.Errorf("商品配置类型%s暂未支持，请联系管理员", o.OptionType)
		}
		out[key] = value
	}
	for k := range values {
		if !allowed[k] {
			return nil, errors.New("包含未知配置项")
		}
	}
	return out, nil
}
func ValidateConfiguration(page *contracts.OrderPage, values map[string]string) error {
	_, err := NormalizeConfiguration(page, values)
	return err
}
func (s *Store) Quote(ctx context.Context, in QuoteInput) (*RetailQuote, error) {
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	if in.Cycle != "monthly" && in.Cycle != "quarterly" && in.Cycle != "semiannually" && in.Cycle != "annually" {
		return nil, errors.New("此商品计费模式尚未支持，不能按月付代替下单")
	}
	if in.Quantity < 1 || in.Quantity > 10 || len(in.ConfigOptions) > 64 || len(in.CustomFields) > 64 {
		return nil, errors.New("购买参数无效或自定义字段尚未支持")
	}
	mapping, e := s.findProductByLocalID(in.LocalProductID)
	if e != nil {
		return nil, e
	}
	if mapping.Hidden || mapping.Mode != "sync" {
		return nil, errors.New("商品已停售")
	}
	if e = s.CheckSupplierCapability(mapping.SupplierID, "sales"); e != nil {
		return nil, e
	}
	page, e := s.GetOrderPageContext(ctx, mapping.SupplierID, mapping.UpstreamProductID)
	if e != nil {
		return nil, e
	}
	found := false
	for _, cycle := range page.Cycles {
		if cycle.Billingcycle == in.Cycle {
			found = true
		}
	}
	if !found {
		return nil, errors.New("商品计费周期无效")
	}
	if e = ValidateCustomFields(page.CustomFields, in.CustomFields); e != nil {
		return nil, e
	}
	normalized, e := NormalizeConfiguration(page, in.ConfigOptions)
	if e != nil {
		return nil, e
	}
	in.ConfigOptions = normalized
	p, e := s.ProviderFor(mapping.SupplierID)
	if e != nil {
		return nil, e
	}
	qp, ok := p.(contracts.QuoteProvider)
	if !ok {
		return nil, errors.New("此对接能力尚未支持权威报价")
	}
	// Existing delivery creates one durable procurement per machine. Quote the same
	// single-machine configuration and multiply exactly once, never assume bulk discounts.
	flightInput := in
	flightInput.Quantity = 1
	result := s.quoteOnce(quoteFingerprint(flightInput), func() (*contracts.QuoteResult, error) {
		shared, cancel := context.WithTimeout(context.WithoutCancel(ctx), 35*time.Second)
		defer cancel()
		select {
		case s.quoteSlots <- struct{}{}:
			defer func() { <-s.quoteSlots }()
		case <-shared.Done():
			return nil, shared.Err()
		}
		return qp.Quote(shared, contracts.QuoteRequest{ProductID: mapping.UpstreamProductID, Cycle: in.Cycle, Quantity: 1, ConfigOptions: in.ConfigOptions, CustomFields: in.CustomFields})
	})
	var q *contracts.QuoteResult
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-result.Done:
		if result.Err != nil || result.Value == nil {
			return nil, fmt.Errorf("商品报价暂不可用，请重试")
		}
		q = result.Value
	}
	supplier, e := s.GetSupplier(mapping.SupplierID)
	if e != nil {
		return nil, e
	}
	retail := func(raw string) (billing.Money, error) {
		m, e := billing.ParseMoney(raw)
		if e != nil || m < 0 {
			return 0, errors.New("商品报价金额无效")
		}
		v := CalculateRetailPrice(float64(m)/100, supplier.Rate, 0, 0)
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, errors.New("商品汇率无效")
		}
		return billing.ParseMoney(strconv.FormatFloat(v, 'f', 2, 64))
	}
	unit, e := retail(q.Total)
	if e != nil || unit <= 0 {
		return nil, errors.New("商品总报价无效")
	}
	renew, e := retail(q.RenewalUnit)
	if e != nil {
		return nil, e
	}
	setup, e := retail(q.SetupUnit)
	if e != nil {
		return nil, e
	}
	total := unit * billing.Money(in.Quantity)
	if total > billing.MaxMoney {
		return nil, errors.New("商品总报价超过上限")
	}
	schemaData, _ := json.Marshal(struct {
		CustomFields   []contracts.CustomField
		Options        []contracts.ConfigOption
		Cycles         []contracts.CycleItem
		Rate           float64
		MappingID      string
		MappingVersion int64
	}{page.CustomFields, page.ConfigOptions, page.Cycles, supplier.Rate, mapping.ID, mapping.UpdatedAt})
	schemaSum := sha256.Sum256(schemaData)
	schemaHash := base64.RawURLEncoding.EncodeToString(schemaSum[:])
	expires := time.Now().Add(5 * time.Minute).Unix()
	token, e := s.sealQuote(in, total.String(), expires, schemaHash)
	if e != nil {
		return nil, e
	}
	rawTotal, e := billing.ParseMoney(q.Total)
	if e != nil || rawTotal <= 0 {
		return nil, errors.New("商品采购预算无效")
	}
	return &RetailQuote{Schema: schemaHash, ProcurementCeiling: rawTotal.String(), Token: token, Total: total.String(), Unit: (total / billing.Money(in.Quantity)).String(), Renewal: (renew * billing.Money(in.Quantity)).String(), Setup: (setup * billing.Money(in.Quantity)).String(), Currency: q.Currency, ExpiresAt: expires, Cycle: in.Cycle, Quantity: in.Quantity}, nil
}

// Reprice before any local order or balance mutation. Tokens are not payment authorization.
func (s *Store) VerifyRetailQuote(ctx context.Context, in QuoteInput, token string) (*RetailQuote, error) {
	seal, e := s.verifyQuote(token, in)
	if e != nil {
		return nil, e
	}
	q, e := s.Quote(ctx, in)
	if e != nil {
		return nil, e
	}
	if q.Total != seal.Total || seal.Schema == "" || seal.Schema != q.Schema {
		return nil, errors.New("商品价格已变化，请重新确认后下单")
	}
	return q, nil
}

// Share only concurrent read-only calculations. No quote history cache or credentials.
type quoteFlight struct {
	Done  chan struct{}
	Value *contracts.QuoteResult
	Err   error
}

func (s *Store) quoteOnce(key string, fn func() (*contracts.QuoteResult, error)) *quoteFlight {
	s.quoteFlightMu.Lock()
	defer s.quoteFlightMu.Unlock()
	if s.quoteFlights == nil {
		s.quoteFlights = map[string]*quoteFlight{}
	}
	if s.quoteSlots == nil {
		s.quoteSlots = make(chan struct{}, 4)
	}
	if f := s.quoteFlights[key]; f != nil {
		return f
	}
	f := &quoteFlight{Done: make(chan struct{})}
	if len(s.quoteFlights) >= 32 {
		f.Err = errors.New("报价请求繁忙，请稍后重试")
		close(f.Done)
		return f
	}
	s.quoteFlights[key] = f
	go func() {
		defer func() {
			if recover() != nil {
				f.Value = nil
				f.Err = errors.New("对接能力报价异常")
			}
			s.quoteFlightMu.Lock()
			delete(s.quoteFlights, key)
			close(f.Done)
			s.quoteFlightMu.Unlock()
		}()
		f.Value, f.Err = fn()
	}()
	return f
}

func ConfigurationDisplay(o contracts.ConfigOption, value string) string {
	if o.OptionType == "multi_select" || o.OptionType == "cascade" {
		var ids []string
		if json.Unmarshal([]byte(value), &ids) != nil {
			return value
		}
		labels := []string{}
		choices := o.Options
		for _, id := range ids {
			for _, c := range choices {
				if strconv.Itoa(c.ID) == id {
					labels = append(labels, c.Name)
					if o.OptionType == "cascade" {
						choices = c.Children
					}
					break
				}
			}
		}
		return strings.Join(labels, " / ")
	}
	if o.OptionType == "qty" || o.OptionType == "quantity" || o.OptionType == "quantity_range" {
		return strings.TrimSpace(value + " " + o.Unit)
	}
	for _, c := range o.Options {
		if strconv.Itoa(c.ID) == value {
			return c.Name
		}
	}
	return value
}

func ValidateCustomFields(fields []contracts.CustomField, values map[string]string) error {
	if len(values) > 64 {
		return errors.New("附加字段过多")
	}
	allowed := map[string]bool{}
	for _, f := range fields {
		key := strconv.Itoa(f.ID)
		allowed[key] = true
		v := values[key]
		if strings.TrimSpace(v) == "" {
			if f.Required {
				return fmt.Errorf("请填写%s", f.Name)
			}
			continue
		}
		if len(v) > 2048 {
			return fmt.Errorf("%s内容过长", f.Name)
		}
		switch f.Type {
		case "text", "password", "email":
		case "select":
			found := false
			for _, c := range f.Choices {
				if v == c {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("%s选项无效", f.Name)
			}
		default:
			return fmt.Errorf("%s字段类型暂未支持", f.Name)
		}
		if f.Validation != "" {
			if len(f.Validation) > 512 {
				return fmt.Errorf("%s验证规则无效", f.Name)
			}
			pattern := strings.Trim(f.Validation, "/")
			re, e := regexp.Compile(pattern)
			if e != nil || !re.MatchString(v) {
				return fmt.Errorf("%s格式不正确", f.Name)
			}
		}
	}
	for k := range values {
		if !allowed[k] {
			return errors.New("包含未知附加字段")
		}
	}
	return nil
}
