// Package zjmf implements the HTTP protocol shared by IDC Smart suppliers.
// Finance suppliers use the legacy "魔方财务" endpoints; see
// docs/upstream-zjmf-protocol.md for the verified protocol.
package zjmf

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ddyidc/internal/upstream/metrics"
	"ddyidc/internal/upstream/provider/contracts"
)

const (
	requestTimeout = 30 * time.Second
	cacheSafety    = 5 * time.Minute
	browserUA      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36"
)

var (
	client = &http.Client{Timeout: requestTimeout}
	tokens = struct {
		sync.Mutex
		values map[string]cachedJWT
	}{values: make(map[string]cachedJWT)}
	financeCache = struct {
		sync.Mutex
		products map[string]cachedFinanceProduct
		pages    map[string]cachedOrderPage
	}{products: make(map[string]cachedFinanceProduct), pages: make(map[string]cachedOrderPage)}
	requestGates = struct {
		sync.Mutex
		last map[string]time.Time
	}{last: make(map[string]time.Time)}
	auditMu sync.Mutex
)

type cachedJWT struct {
	token     string
	expiresAt time.Time
}

type cachedFinanceProduct struct {
	value     financeProduct
	expiresAt time.Time
}

type cachedOrderPage struct {
	value     contracts.OrderPage
	expiresAt time.Time
}

// Provider is stateless apart from an in-process JWT cache. Supplier
// credentials are supplied by service.Store only after AES-GCM decryption.
type Provider struct{ Supplier contracts.Supplier }

func New(s contracts.Supplier) *Provider { return &Provider{Supplier: s} }

func (p *Provider) baseURL() (string, error) {
	base := strings.TrimRight(strings.TrimSpace(p.Supplier.URL), "/")
	if base == "" || p.Supplier.Username == "" || p.Supplier.Token == "" {
		return "", errors.New("supplier URL, username and token are required")
	}
	parsed, err := url.ParseRequestURI(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("invalid supplier URL")
	}
	return base, nil
}

func (p *Provider) tokenKey() string {
	sum := sha256.Sum256([]byte(p.Supplier.URL + "\x00" + p.Supplier.Username + "\x00" + p.Supplier.Token))
	return hex.EncodeToString(sum[:])
}

func (p *Provider) isQKIDC() bool {
	parsed, err := url.Parse(strings.TrimSpace(p.Supplier.URL))
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "qkidc.cn" || strings.HasSuffix(host, ".qkidc.cn")
}

func (p *Provider) enforceQKIDCProduct(id int) error {
	if p.isQKIDC() && id != 489 && id != 353 {
		return fmt.Errorf("qkidc safety policy only permits products 353 and 489")
	}
	return nil
}

func (p *Provider) cachedToken() string {
	tokens.Lock()
	defer tokens.Unlock()
	value, ok := tokens.values[p.tokenKey()]
	if !ok || value.token == "" || time.Now().Add(cacheSafety).After(value.expiresAt) {
		return ""
	}
	return value.token
}

func (p *Provider) clearToken() {
	tokens.Lock()
	delete(tokens.values, p.tokenKey())
	tokens.Unlock()
}

// Auth signs in to the verified legacy finance API. JWT is intentionally kept
// only in process memory and is refreshed shortly before its exp claim.
func (p *Provider) Auth() error {
	if p.cachedToken() != "" {
		return nil
	}
	base, err := p.baseURL()
	if err != nil {
		return err
	}
	form := url.Values{"username": {p.Supplier.Username}, "password": {p.Supplier.Token}}
	response, status, err := p.request(http.MethodPost, base+"/zjmf_api_login", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", "")
	if err != nil {
		return fmt.Errorf("upstream login request: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("upstream login HTTP status %d", status)
	}
	var payload struct {
		Status  int    `json:"status"`
		Message string `json:"msg"`
		JWT     string `json:"jwt"`
	}
	if err := json.Unmarshal(response, &payload); err != nil {
		return fmt.Errorf("decode upstream login response: %w", err)
	}
	if payload.Status != http.StatusOK || payload.JWT == "" {
		if payload.Message == "" {
			payload.Message = "JWT missing from upstream login response"
		}
		return fmt.Errorf("upstream login rejected: %s", payload.Message)
	}
	tokens.Lock()
	tokens.values[p.tokenKey()] = cachedJWT{token: payload.JWT, expiresAt: jwtExpiry(payload.JWT)}
	tokens.Unlock()
	return nil
}

func jwtExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) == 3 {
		if raw, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
			var claims struct {
				Exp int64 `json:"exp"`
			}
			if json.Unmarshal(raw, &claims) == nil && claims.Exp > time.Now().Unix() {
				return time.Unix(claims.Exp, 0)
			}
		}
	}
	// The verified finance token is valid for about three days. Keep a shorter
	// fallback lifetime if a non-JWT implementation omits exp.
	return time.Now().Add(48 * time.Hour)
}

// GetProductList calls the public finance catalogue endpoint. Product listing
// intentionally does not authenticate, matching the upstream protocol.
func (p *Provider) GetProductList() ([]contracts.UpstreamProduct, error) {
	if p.isQKIDC() {
		return nil, errors.New("qkidc safety policy forbids full catalogue sync")
	}
	base, err := p.baseURL()
	if err != nil {
		return nil, err
	}
	body, status, err := p.request(http.MethodGet, base+"/api/product/list", nil, "", "")
	if err != nil {
		return nil, fmt.Errorf("request upstream product list: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("upstream product list HTTP status %d", status)
	}
	var payload struct {
		Status  int    `json:"status"`
		Message string `json:"msg"`
		Data    struct {
			CurrencyCode string `json:"currency_code"`
			List         []struct {
				ID          json.Number `json:"id"`
				Type        string      `json:"type"`
				GroupID     json.Number `json:"gid"`
				Name        string      `json:"name"`
				Description string      `json:"description"`
				PayMethod   string      `json:"pay_method"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := decodeJSON(body, &payload); err != nil {
		return nil, fmt.Errorf("decode upstream product list: %w", err)
	}
	if payload.Status != http.StatusOK {
		return nil, upstreamError("product list", payload.Message)
	}
	products := make([]contracts.UpstreamProduct, 0, len(payload.Data.List))
	for _, item := range payload.Data.List {
		id, err := item.ID.Int64()
		if err != nil || id <= 0 {
			continue
		}
		groupID, _ := item.GroupID.Int64()
		products = append(products, contracts.UpstreamProduct{
			ID:           int(id),
			Name:         item.Name,
			Description:  htmlUnescape(item.Description),
			Type:         item.Type,
			GroupID:      int(groupID),
			PayType:      item.PayMethod,
			CurrencyCode: payload.Data.CurrencyCode,
		})
	}
	return products, nil
}

// GetProductDetail requests the supplier-specific agent price. A 401/405
// response invalidates the in-memory JWT and retries once after re-login.
func (p *Provider) GetProductDetail(id int) (*contracts.UpstreamProductDetail, error) {
	if id <= 0 {
		return nil, errors.New("upstream product ID must be positive")
	}
	if err := p.enforceQKIDCProduct(id); err != nil {
		return nil, err
	}
	product, err := p.getFinanceProduct(id)
	if err != nil {
		return nil, err
	}
	return product.toContract(id), nil
}

func (p *Provider) InvalidateProduct(id int) {
	financeCache.Lock()
	defer financeCache.Unlock()
	delete(financeCache.products, p.tokenKey()+":product:"+strconv.Itoa(id))
	delete(financeCache.pages, p.tokenKey()+":page:"+strconv.Itoa(id))
}
func (p *Provider) getFinanceProduct(id int) (financeProduct, error) {
	cacheKey := p.tokenKey() + ":product:" + strconv.Itoa(id)
	financeCache.Lock()
	if cached, ok := financeCache.products[cacheKey]; ok && time.Now().Before(cached.expiresAt) {
		financeCache.Unlock()
		return cached.value, nil
	}
	financeCache.Unlock()
	body, err := p.authorizedGet(fmt.Sprintf("/api/product/%d?price_basis=agent", id))
	if err != nil {
		return financeProduct{}, err
	}
	var payload struct {
		Status  int    `json:"status"`
		Message string `json:"msg"`
		Data    struct {
			Product financeProduct `json:"product"`
		} `json:"data"`
	}
	if err := decodeJSON(body, &payload); err != nil {
		return financeProduct{}, fmt.Errorf("decode upstream product detail: %w", err)
	}
	if payload.Status != http.StatusOK {
		if payload.Message == "商品不存在" || payload.Message == "产品不存在" || payload.Message == "该商品不可代理" || payload.Message == "商品已下架" {
			return financeProduct{}, contracts.ErrProductUnavailable
		}
		return financeProduct{}, upstreamError("product detail", payload.Message)
	}
	financeCache.Lock()
	financeCache.products[cacheKey] = cachedFinanceProduct{value: payload.Data.Product, expiresAt: time.Now().Add(10 * time.Minute)}
	financeCache.Unlock()
	return payload.Data.Product, nil
}

// GetBalance reads the finance supplier account credit using the cached JWT.
func (p *Provider) GetBalance() (float64, error) {
	body, err := p.authorizedGet("/user_info")
	if err != nil {
		return 0, err
	}
	var payload struct {
		Status  int    `json:"status"`
		Message string `json:"msg"`
		User    struct {
			Credit json.RawMessage `json:"credit"`
		} `json:"user"`
	}
	if err := decodeJSON(body, &payload); err != nil {
		return 0, fmt.Errorf("decode upstream balance: %w", err)
	}
	if payload.Status != http.StatusOK {
		return 0, upstreamError("balance", payload.Message)
	}
	return number(payload.User.Credit), nil
}

// GetOrderPage obtains real provider-side purchase configuration. Finance
// cloud products first try the recursive V10 endpoint; older finance panels
// commonly reject that endpoint even with a valid finance JWT, so an invalid
// business response is deliberately followed by the verified cart/set_config
// fallback instead of being surfaced as a fictitious cloud configuration.
func (p *Provider) GetOrderPage(productID int) (*contracts.OrderPage, error) {
	if productID <= 0 {
		return nil, errors.New("upstream product ID must be positive")
	}
	if err := p.enforceQKIDCProduct(productID); err != nil {
		return nil, err
	}
	cacheKey := p.tokenKey() + ":page:" + strconv.Itoa(productID)
	financeCache.Lock()
	if cached, ok := financeCache.pages[cacheKey]; ok && time.Now().Before(cached.expiresAt) {
		page := cached.value
		financeCache.Unlock()
		return &page, nil
	}
	financeCache.Unlock()
	product, err := p.getFinanceProduct(productID)
	if err != nil {
		return nil, err
	}
	if product.APIType == "zjmf_api" && number(product.UpstreamVersion) > 0 && (product.Type == "dcimcloud" || product.Type == "dcim") {
		if page, cloudErr := p.getCloudOrderPage(productID, product); cloudErr == nil {
			p.cacheOrderPage(cacheKey, page)
			return page, nil
		}
	}
	page, err := p.getLegacyOrderPage(productID, product)
	if err == nil {
		p.cacheOrderPage(cacheKey, page)
	}
	return page, err
}

func (p *Provider) cacheOrderPage(key string, page *contracts.OrderPage) {
	if page == nil {
		return
	}
	financeCache.Lock()
	financeCache.pages[key] = cachedOrderPage{value: *page, expiresAt: time.Now().Add(10 * time.Minute)}
	financeCache.Unlock()
}

func (p *Provider) getCloudOrderPage(productID int, product financeProduct) (*contracts.OrderPage, error) {
	body, err := p.authorizedGet(fmt.Sprintf("/v10/host/product/%d/mf_cloud/order_page?price_basis=agent&is_downstream=1", productID))
	if err != nil {
		return nil, err
	}
	var payload struct {
		Status  int             `json:"status"`
		Message string          `json:"msg"`
		Data    json.RawMessage `json:"data"`
	}
	if err := decodeJSON(body, &payload); err != nil {
		return nil, fmt.Errorf("decode cloud order page: %w", err)
	}
	if payload.Status != http.StatusOK || len(payload.Data) == 0 {
		return nil, upstreamError("cloud order page", payload.Message)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(payload.Data, &data); err != nil {
		return nil, fmt.Errorf("decode cloud order page data: %w", err)
	}
	dataCenters := configValues(data["data_center"])
	if len(dataCenters) == 0 {
		return nil, errors.New("cloud order page has no data_center definition")
	}
	page := &contracts.OrderPage{Product: product.toContract(productID).UpstreamProduct, DataCenters: dataCenters}
	page.Images = configValues(data["image"])
	page.NetworkTypes = configValues(data["network"])
	for _, field := range []string{"cpu", "memory", "disk", "bandwidth", "ip_num", "network"} {
		if raw := data[field]; len(raw) > 0 {
			page.ConfigOptions = append(page.ConfigOptions, cloudConfigOption(field, raw))
		}
	}
	page.Cycles = cloudCycles(data["cycle"])
	return page, nil
}

func (p *Provider) getLegacyOrderPage(productID int, detail financeProduct) (*contracts.OrderPage, error) {
	query := url.Values{"pid": {strconv.Itoa(productID)}, "billingcycle": {detail.BillingCycle}}
	body, err := p.authorizedGet("/cart/set_config?" + query.Encode())
	if err != nil {
		return nil, err
	}
	var payload legacyOrderPage
	if err := decodeJSON(body, &payload); err != nil {
		return nil, fmt.Errorf("decode finance set_config: %w", err)
	}
	if payload.Status != http.StatusOK {
		return nil, upstreamError("finance set_config", payload.Message)
	}
	page := &contracts.OrderPage{
		Product:         detail.toContract(productID).UpstreamProduct,
		Cycles:          legacyCycles(payload.Product.Cycle),
		DefaultPassword: payload.Product.Password.Password,
	}
	if page.Product.Name == "" {
		page.Product.Name = payload.Product.Name
		page.Product.Description = htmlUnescape(payload.Product.Description)
	}
	for _, option := range payload.Option {
		contract := option.toContract()
		page.ConfigOptions = append(page.ConfigOptions, contract)
		switch contract.Field {
		case "data_center":
			page.DataCenters = contract.Options
		case "image":
			page.Images = contract.Options
			page.ImageGroups = imageGroups(option.Sub)
		case "network":
			page.NetworkTypes = contract.Options
		}
	}
	return page, nil
}

type financeProduct struct {
	ID                 int             `json:"id"`
	Name               string          `json:"name"`
	Type               string          `json:"type"`
	GroupID            int             `json:"gid"`
	Description        string          `json:"description"`
	PayMethod          string          `json:"pay_method"`
	PayType            string          `json:"pay_type"`
	APIType            string          `json:"api_type"`
	UpstreamVersion    json.RawMessage `json:"upstream_version"`
	UpstreamPriceType  string          `json:"upstream_price_type"`
	UpstreamPriceValue json.RawMessage `json:"upstream_price_value"`
	ProductPrice       json.RawMessage `json:"product_price"`
	SetupFee           json.RawMessage `json:"setup_fee"`
	BillingCycle       string          `json:"billingcycle"`
	BillingCycleZH     string          `json:"billingcycle_zh"`
	Quantity           json.RawMessage `json:"qty"`
	StockControl       json.RawMessage `json:"stock_control"`
	OnTrial            json.RawMessage `json:"ontrial"`
	OnTrialPrice       json.RawMessage `json:"ontrial_price"`
	CustomFields       json.RawMessage `json:"customfields"`
	ConfigOptions      json.RawMessage `json:"configoptions"`
}

type legacyOrderPage struct {
	Status  int                  `json:"status"`
	Message string               `json:"msg"`
	Product legacyOrderProduct   `json:"product"`
	Option  []legacyConfigOption `json:"option"`
}

type legacyOrderProduct struct {
	ID          int            `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Password    legacyPassword `json:"password"`
	Cycle       []legacyCycle  `json:"cycle"`
}

type legacyPassword struct {
	Password string `json:"password"`
}

type legacyCycle struct {
	Billingcycle string          `json:"billingcycle"`
	Label        string          `json:"billingcycle_zh"`
	Price        json.RawMessage `json:"product_price"`
	SetupFee     json.RawMessage `json:"setup_fee"`
}

type legacyConfigOption struct {
	ID   int             `json:"id"`
	Name string          `json:"option_name"`
	Type int             `json:"option_type"`
	Min  json.RawMessage `json:"qty_minimum"`
	Max  json.RawMessage `json:"qty_maximum"`
	Step json.RawMessage `json:"qty_stage"`
	Unit string          `json:"unit"`
	Sub  json.RawMessage `json:"sub"`
}

func (v legacyConfigOption) toContract() contracts.ConfigOption {
	optionType := "select"
	if isQuantityOption(v.Type) {
		optionType = "qty"
	}
	values := configValues(v.Sub)
	result := contracts.ConfigOption{
		ID:         v.ID,
		Name:       v.Name,
		Field:      configField(v.Name),
		OptionType: optionType,
		Required:   true,
		Options:    values,
		Min:        number(v.Min),
		Max:        number(v.Max),
		Step:       number(v.Step),
		Unit:       v.Unit,
	}
	if result.Step == 0 {
		result.Step = 1
	}
	if optionType == "qty" {
		result.Default = strconv.FormatFloat(result.Min, 'f', -1, 64)
	} else if len(values) > 0 {
		result.Default = strconv.Itoa(values[0].ID)
	}
	return result
}

func isQuantityOption(kind int) bool {
	switch kind {
	case 4, 7, 9, 11, 14, 15, 16, 17, 18, 19:
		return true
	default:
		return false
	}
}

func configField(name string) string {
	switch {
	case strings.Contains(name, "数据中心") || strings.Contains(strings.ToLower(name), "data center"):
		return "data_center"
	case strings.Contains(name, "操作系统") || strings.Contains(strings.ToLower(name), "image"):
		return "image"
	case strings.EqualFold(name, "CPU"):
		return "cpu"
	case strings.Contains(name, "内存") || strings.Contains(strings.ToLower(name), "memory"):
		return "memory"
	case strings.Contains(name, "系统盘"):
		return "system_disk"
	case strings.Contains(name, "数据盘"):
		return "data_disk"
	case strings.Contains(name, "带宽") || strings.Contains(strings.ToLower(name), "bandwidth"):
		return "bandwidth"
	case strings.Contains(name, "IP"):
		return "ip_num"
	case strings.Contains(name, "网络") || strings.Contains(strings.ToLower(name), "network"):
		return "network"
	default:
		return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), " ", "_"))
	}
}

// configValues accepts finance's heterogeneous sub field: normal arrays,
// data-center arrays with area IDs, and image-category maps with child arrays.
func configValues(raw json.RawMessage) []contracts.ConfigOptionValue {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var nodes []json.RawMessage
	if err := json.Unmarshal(raw, &nodes); err != nil {
		var groups map[string]json.RawMessage
		if json.Unmarshal(raw, &groups) != nil {
			return nil
		}
		keys := make([]string, 0, len(groups))
		for key := range groups {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			group := groups[key]
			var wrapped struct {
				Child []json.RawMessage `json:"child"`
			}
			if json.Unmarshal(group, &wrapped) == nil {
				nodes = append(nodes, wrapped.Child...)
			}
		}
	}
	values := make([]contracts.ConfigOptionValue, 0, len(nodes))
	for _, node := range nodes {
		var item struct {
			ID      int             `json:"id"`
			Name    string          `json:"name"`
			Option  string          `json:"option_name"`
			Version string          `json:"version"`
			Display string          `json:"show_pricing"`
			Price   json.RawMessage `json:"pricing"`
			Min     json.RawMessage `json:"qty_minimum"`
			Max     json.RawMessage `json:"qty_maximum"`
			Step    json.RawMessage `json:"qty_stage"`
			Area    []struct {
				ID int `json:"id"`
			} `json:"area"`
		}
		if json.Unmarshal(node, &item) != nil {
			continue
		}
		if item.ID == 0 && len(item.Area) > 0 {
			item.ID = item.Area[0].ID
		}
		name := firstNonEmpty(item.Display, item.Version, item.Option, item.Name)
		if item.ID != 0 && name != "" {
			values = append(values, contracts.ConfigOptionValue{
				ID: item.ID, Name: name, Price: number(item.Price),
				Min: number(item.Min), Max: number(item.Max), Step: number(item.Step),
			})
		}
	}
	return values
}

func imageGroups(raw json.RawMessage) []contracts.ImageGroup {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var source map[string]json.RawMessage
	if json.Unmarshal(raw, &source) != nil {
		return nil
	}
	names := make([]string, 0, len(source))
	for name := range source {
		names = append(names, name)
	}
	sort.Strings(names)
	groups := make([]contracts.ImageGroup, 0, len(names))
	for _, name := range names {
		var wrapped struct {
			Child json.RawMessage `json:"child"`
		}
		if json.Unmarshal(source[name], &wrapped) != nil {
			continue
		}
		children := configValues(wrapped.Child)
		if len(children) > 0 {
			groups = append(groups, contracts.ImageGroup{Name: name, Child: children})
		}
	}
	return groups
}

func legacyCycles(cycles []legacyCycle) []contracts.CycleItem {
	result := make([]contracts.CycleItem, 0, len(cycles))
	for _, cycle := range cycles {
		if cycle.Billingcycle == "" || cycle.Billingcycle == "ontrial" {
			continue
		}
		result = append(result, contracts.CycleItem{Billingcycle: cycle.Billingcycle, Label: cycle.Label, Price: number(cycle.Price), SetupFee: number(cycle.SetupFee)})
	}
	return result
}

func cloudConfigOption(field string, raw json.RawMessage) contracts.ConfigOption {
	values := configValues(raw)
	result := contracts.ConfigOption{Field: field, Name: field, OptionType: "select", Required: true, Options: values}
	if len(values) > 0 {
		result.Default = strconv.Itoa(values[0].ID)
	}
	return result
}

func cloudCycles(raw json.RawMessage) []contracts.CycleItem {
	var cycles []legacyCycle
	if json.Unmarshal(raw, &cycles) != nil {
		return nil
	}
	return legacyCycles(cycles)
}

func (p financeProduct) toContract(requestedID int) *contracts.UpstreamProductDetail {
	config := map[string]string{
		"upstream_price_type":  p.UpstreamPriceType,
		"upstream_price_value": rawString(p.UpstreamPriceValue),
		"setup_fee":            rawString(p.SetupFee),
		"billingcycle_zh":      p.BillingCycleZH,
		"stock_control":        rawString(p.StockControl),
		"ontrial":              rawString(p.OnTrial),
		"ontrial_price":        rawString(p.OnTrialPrice),
		"customfields":         rawString(p.CustomFields),
	}
	for key, value := range config {
		if value == "" || value == "null" {
			delete(config, key)
		}
	}
	id := p.ID
	if id == 0 {
		id = requestedID
	}
	stock := int(number(p.Quantity))
	if _, err := strconv.ParseFloat(rawString(p.Quantity), 64); err != nil {
		stock = -1
	}
	if control := strings.TrimSpace(rawString(p.StockControl)); control == "" || control == "0" || strings.EqualFold(control, "false") {
		stock = -1
	}
	return &contracts.UpstreamProductDetail{
		UpstreamProduct: contracts.UpstreamProduct{
			ID:                 id,
			Name:               p.Name,
			Description:        htmlUnescape(p.Description),
			Price:              number(p.ProductPrice),
			Cycle:              p.BillingCycle,
			PayType:            firstNonEmpty(p.PayType, p.PayMethod),
			Type:               p.Type,
			GroupID:            p.GroupID,
			SetupFee:           number(p.SetupFee),
			UpstreamPriceType:  p.UpstreamPriceType,
			UpstreamPriceValue: number(p.UpstreamPriceValue),
			Stock:              stock,
			StockValue:         p.Quantity,
			TrialEnabled:       number(p.OnTrial) != 0,
			TrialPrice:         number(p.OnTrialPrice),
		},
		Config:           config,
		ConfigOptions:    productConfigOptions(p.ConfigOptions),
		HasConfigOptions: hasProductConfigOptions(p.ConfigOptions),
	}
}

func hasProductConfigOptions(raw json.RawMessage) bool {
	value := strings.TrimSpace(string(raw))
	switch value {
	case "", "null", "[]", "{}", "0", `""`, `"0"`:
		return false
	default:
		return true
	}
}

// productConfigOptions decodes the agent product-detail configoptions field.
// Unlike cart/set_config, this is attached to the upstream SKU itself and is
// therefore the authoritative editable-configuration signal.
func productConfigOptions(raw json.RawMessage) []contracts.ConfigOption {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "[]" {
		return nil
	}
	var list []legacyConfigOption
	if err := json.Unmarshal(raw, &list); err == nil {
		out := make([]contracts.ConfigOption, 0, len(list))
		for _, option := range list {
			out = append(out, option.toContract())
		}
		return out
	}
	// Some finance versions wrap the same list in an object. Keep the decoder
	// permissive without treating arbitrary cart data as editable options.
	var wrapped struct {
		Options []legacyConfigOption `json:"options"`
		List    []legacyConfigOption `json:"list"`
	}
	if json.Unmarshal(raw, &wrapped) != nil {
		return nil
	}
	list = wrapped.Options
	if len(list) == 0 {
		list = wrapped.List
	}
	out := make([]contracts.ConfigOption, 0, len(list))
	for _, option := range list {
		out = append(out, option.toContract())
	}
	return out
}

func (p *Provider) authorizedGet(path string) ([]byte, error) {
	base, err := p.baseURL()
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := p.Auth(); err != nil {
			return nil, err
		}
		jwt := p.cachedToken()
		body, status, requestErr := p.request(http.MethodGet, base+path, nil, "", jwt)
		if requestErr != nil {
			return nil, requestErr
		}
		if status == http.StatusUnauthorized || status == http.StatusMethodNotAllowed {
			p.clearToken()
			continue
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("upstream request HTTP status %d", status)
		}
		return body, nil
	}
	return nil, errors.New("upstream authorization was rejected after JWT refresh")
}

func (p *Provider) authorizedFormPost(path string, form url.Values) ([]byte, error) {
	base, err := p.baseURL()
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := p.Auth(); err != nil {
			return nil, err
		}
		body, status, requestErr := p.request(http.MethodPost, base+path, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", p.cachedToken())
		if requestErr != nil {
			return nil, requestErr
		}
		if status == http.StatusUnauthorized || status == http.StatusMethodNotAllowed {
			p.clearToken()
			continue
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("upstream request HTTP status %d", status)
		}
		return body, nil
	}
	return nil, errors.New("upstream authorization was rejected after JWT refresh")
}

// request enforces qkidc's audit/rate/retreat discipline centrally. This is
// intentionally below Auth/GetProduct/CreateOrder so every request, including
// login and read-only traffic, is covered by the same guard.
func (p *Provider) request(method, endpoint string, body io.Reader, contentType, jwt string) ([]byte, int, error) {
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = io.ReadAll(body)
		if err != nil {
			return nil, 0, err
		}
	}
	parsed, parseErr := url.Parse(endpoint)
	if parseErr != nil {
		return nil, 0, parseErr
	}
	isQKIDC := parsed.Hostname() == "qkidc.cn" || strings.HasSuffix(parsed.Hostname(), ".qkidc.cn")
	remoteAudit := isRemoteAudit(bodyBytes)
	var lastErr error
	attempts := 1
	if method == http.MethodGet {
		attempts = 2
	}
	for attempt := 0; attempt < attempts; attempt++ {
		if isQKIDC {
			waitForRequestSlot(parsed.Hostname(), 2*time.Second)
		}
		req, err := http.NewRequest(method, endpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("User-Agent", browserUA)
		req.Header.Set("Accept", "application/json, text/plain, */*")
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if jwt != "" {
			req.Header.Set("Authorization", "Bearer "+jwt)
		}
		observed := *client
		observed.Transport = metrics.Transport{Base: client.Transport, SupplierID: p.Supplier.ID}
		response, err := observed.Do(req)
		if err != nil {
			writeAudit(endpoint, method, attempt+1, bodyBytes, 0, nil, err, remoteAudit)
			lastErr = err
			if attempt+1 < attempts {
				if isQKIDC {
					time.Sleep(30 * time.Second)
				}
				continue
			}
			break
		}
		payload, readErr := io.ReadAll(io.LimitReader(response.Body, 20<<20))
		response.Body.Close()
		if readErr != nil {
			writeAudit(endpoint, method, attempt+1, bodyBytes, response.StatusCode, nil, readErr, remoteAudit)
			lastErr = readErr
			if attempt+1 < attempts {
				if isQKIDC {
					time.Sleep(30 * time.Second)
				}
				continue
			}
			break
		}
		writeAudit(endpoint, method, attempt+1, bodyBytes, response.StatusCode, payload, nil, remoteAudit)
		retryable := response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
		if retryable && attempt+1 < attempts {
			lastErr = fmt.Errorf("upstream HTTP status %d", response.StatusCode)
			if isQKIDC {
				time.Sleep(30 * time.Second)
			}
			continue
		}
		if retryable {
			return payload, response.StatusCode, fmt.Errorf("upstream HTTP status %d after one retry", response.StatusCode)
		}
		return payload, response.StatusCode, nil
	}
	return nil, 0, fmt.Errorf("upstream request failed after retry: %w", lastErr)
}

func waitForRequestSlot(host string, minimum time.Duration) {
	requestGates.Lock()
	defer requestGates.Unlock()
	if wait := requestGates.last[host].Add(minimum).Sub(time.Now()); wait > 0 {
		time.Sleep(wait)
	}
	requestGates.last[host] = time.Now()
}

func writeAudit(endpoint, method string, attempt int, requestBody []byte, status int, responseBody []byte, requestErr error, remoteAction bool) {
	path := strings.TrimSpace(os.Getenv("DDYIDC_UPSTREAM_AUDIT_LOG"))
	if path == "" {
		return
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return
	}
	record := map[string]any{
		"timestamp": time.Now().Format(time.RFC3339Nano), "host": "[UPSTREAM]",
		"method": method, "path": parsed.EscapedPath(), "query": redactPayload([]byte(parsed.RawQuery)),
		"attempt": attempt, "request": redactPayload(requestBody), "status": status,
		"response": redactPayload(responseBody),
	}
	if remoteAction {
		record["response"] = "[REDACTED_REMOTE_CONSOLE]"
	}
	if requestErr != nil {
		record["error"] = "upstream request failed"
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return
	}
	auditMu.Lock()
	defer auditMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(encoded, '\n'))
}

func isRemoteAudit(requestBody []byte) bool {
	values, err := url.ParseQuery(string(requestBody))
	return err == nil && strings.EqualFold(values.Get("func"), "vnc")
}

func redactPayload(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	var decoded any
	if json.Unmarshal(raw, &decoded) == nil {
		return redactValue(decoded)
	}
	if values, err := url.ParseQuery(string(raw)); err == nil && len(values) > 0 {
		out := make(map[string]any, len(values))
		for key, value := range values {
			if sensitiveKey(key) {
				out[key] = "[REDACTED]"
			} else {
				out[key] = value
			}
		}
		return out
	}
	return string(raw)
}

func redactValue(value any) any {
	switch current := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(current))
		for key, item := range current {
			if sensitiveKey(key) {
				out[key] = "[REDACTED]"
			} else {
				out[key] = redactValue(item)
			}
		}
		return out
	case []any:
		out := make([]any, len(current))
		for i := range current {
			out[i] = redactValue(current[i])
		}
		return out
	default:
		return current
	}
}

func sensitiveKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "[", "_"), "]", ""))
	sensitiveParts := []string{
		"password", "token", "secret", "authorization", "username", "email",
		"phone", "address", "postcode", "card", "wechat", "signature", "company", "pass", "url",
	}
	for _, part := range sensitiveParts {
		if strings.Contains(key, part) {
			return true
		}
	}
	return key == "jwt" || key == "qq"
}

func decodeJSON(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	return decoder.Decode(target)
}

func number(raw json.RawMessage) float64 {
	value, _ := strconv.ParseFloat(strings.Trim(strings.TrimSpace(string(raw)), "\""), 64)
	return value
}

func rawString(raw json.RawMessage) string { return strings.Trim(string(raw), "\"") }

func htmlUnescape(value string) string {
	replacer := strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&", "&quot;", "\"", "&#039;", "'")
	return replacer.Replace(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func upstreamError(operation, message string) error {
	if message == "" {
		message = "upstream returned a non-success status"
	}
	return fmt.Errorf("upstream %s: %s", operation, message)
}

func unsupported(operation string) error {
	return fmt.Errorf("finance upstream %s is not implemented because its write protocol has not been verified", operation)
}

func (p *Provider) GetProductResource(int) (*contracts.ProductResource, error) {
	return nil, unsupported("resource lookup")
}
func (p *Provider) CreateOrder(order contracts.OrderInfo) (*contracts.UpstreamOrderResult, error) {
	if order.ProductID <= 0 || order.Quantity <= 0 || order.Cycle == "" {
		return nil, errors.New("product ID, quantity and billing cycle are required")
	}
	if err := p.enforceQKIDCProduct(order.ProductID); err != nil {
		return nil, err
	}
	// Amount is loaded by Store from the persisted supplier mapping, never from
	// the customer request. Reusing that verified value avoids extra catalogue
	// traffic at payment time (especially important for rate-limited qkidc).
	requiredAmount := order.Amount
	if requiredAmount <= 0 {
		return nil, errors.New("upstream order price is unavailable")
	}
	// Avoid placing an unpaid cart/order upstream. This is also the precise,
	// safe outcome for the supplied moebun account, whose credit is 0.00.
	balance, err := p.GetBalance()
	if err != nil {
		return nil, err
	}
	if balance+0.000001 < requiredAmount {
		return nil, fmt.Errorf("upstream balance insufficient: available %.2f, required %.2f", balance, requiredAmount)
	}
	form := orderForm(order)
	addBody, err := p.authorizedFormPost("/cart/add_to_shop", form)
	if err != nil {
		return nil, fmt.Errorf("add upstream cart: %w", err)
	}
	if err := requireUpstreamSuccess(addBody, "cart add"); err != nil {
		return nil, err
	}
	cartPosition := firstJSONValue(addBody, "i")
	settleBody, err := p.authorizedFormPost("/cart/settle", settlementForm(order, cartPosition))
	if err != nil {
		return nil, fmt.Errorf("settle upstream cart: %w", err)
	}
	var settle struct {
		Status  int    `json:"status"`
		Message string `json:"msg"`
		Data    struct {
			InvoiceID interface{} `json:"invoiceid"`
			ID        interface{} `json:"id"`
		} `json:"data"`
	}
	if err := decodeJSON(settleBody, &settle); err != nil {
		return nil, fmt.Errorf("decode upstream cart settlement: %w", err)
	}
	if settle.Status != http.StatusOK {
		p.clearDirtyCart(order)
		return nil, upstreamError("cart settlement", settle.Message)
	}
	invoiceID := fmt.Sprint(settle.Data.InvoiceID)
	if invoiceID == "<nil>" || invoiceID == "" {
		invoiceID = fmt.Sprint(settle.Data.ID)
	}
	if invoiceID == "<nil>" || invoiceID == "" {
		p.clearDirtyCart(order)
		return nil, errors.New("upstream settlement did not return an invoice ID")
	}
	payBody, err := p.authorizedFormPost("/apply_credit", applyCreditForm(order, invoiceID, true))
	if err != nil {
		return nil, fmt.Errorf("pay upstream invoice: %w", err)
	}
	if err := requireUpstreamStatus(payBody, "upstream balance payment", 1001); err != nil {
		// The verified finance implementation releases the pending credit hold by
		// posting use_credit=0. This is not a payment retry and cannot create a
		// second order; it only prevents a dirty unpaid invoice.
		_, _ = p.authorizedFormPost("/apply_credit", applyCreditForm(order, invoiceID, false))
		return nil, err
	}
	hostID := firstJSONValue(settleBody, "hostid", "host_id", "serviceid", "service_id")
	if hostID == "" {
		hostID = firstJSONValue(payBody, "hostid", "host_id", "serviceid", "service_id")
	}
	status := "paid"
	if hostID != "" {
		status = "active"
	}
	balanceAfter := balance - requiredAmount
	if value := firstJSONNumber(payBody, "credit", "balance", "remaining_credit"); value >= 0 {
		balanceAfter = value
	}
	return &contracts.UpstreamOrderResult{OrderID: invoiceID, Status: status, HostID: hostID, Cost: requiredAmount, BalanceBefore: balance, BalanceAfter: balanceAfter}, nil
}

func (p *Provider) clearDirtyCart(order contracts.OrderInfo) {
	// Cleanup is best effort and is only reached after add_to_shop succeeded but
	// settlement produced a normal HTTP response that cannot be used. Transport
	// and risk-control failures return earlier and never trigger extra traffic.
	_, _ = p.authorizedFormPost("/cart/clear", downstreamForm(order))
}

// orderForm serializes the legacy finance cart format without performing I/O.
// Keeping it separate makes the exact payload independently testable.
func orderForm(order contracts.OrderInfo) url.Values {
	form := url.Values{
		"pid":                     {strconv.Itoa(order.ProductID)},
		"billingcycle":            {order.Cycle},
		"qty":                     {strconv.Itoa(order.Quantity)},
		"currencyid":              {"1"},
		"checkout":                {"1"},
		"upstream_product[cycle]": {order.Cycle},
		"upstream_product[qty]":   {strconv.Itoa(order.Quantity)},
	}
	for optionID, value := range order.ConfigOptions {
		form.Set("configoption["+optionID+"]", value)
		form.Set("upstream_product[config_options]["+optionID+"]", value)
	}
	for fieldID, value := range order.CustomFields {
		form.Set("customfield["+fieldID+"]", value)
		form.Set("upstream_product[self_defined_field]["+fieldID+"]", value)
	}
	if order.Hostname != "" {
		form.Set("host", order.Hostname)
	}
	if order.Password != "" {
		form.Set("password", order.Password)
	}
	if order.DataCenterID > 0 {
		form.Set("data_center_id", strconv.Itoa(order.DataCenterID))
	}
	if order.ImageID > 0 {
		form.Set("image_id", strconv.Itoa(order.ImageID))
	}
	if order.NetworkType != "" {
		form.Set("network_type", order.NetworkType)
	}
	return form
}

func downstreamForm(order contracts.OrderInfo) url.Values {
	form := url.Values{}
	if order.DownstreamURL != "" {
		form.Set("downstream_url", order.DownstreamURL)
	}
	if order.DownstreamToken != "" {
		form.Set("downstream_token", order.DownstreamToken)
	}
	if order.DownstreamHostID != "" {
		form.Set("downstream_id", order.DownstreamHostID)
	}
	form.Set("downstream_system_type", "v10")
	return form
}

func settlementForm(order contracts.OrderInfo, cartPosition string) url.Values {
	form := downstreamForm(order)
	if cartPosition != "" {
		form.Add("pos[]", cartPosition)
	}
	return form
}

func applyCreditForm(order contracts.OrderInfo, invoiceID string, useCredit bool) url.Values {
	form := downstreamForm(order)
	form.Set("invoiceid", invoiceID)
	if useCredit {
		form.Set("use_credit", "1")
	} else {
		form.Set("use_credit", "0")
	}
	form.Set("enough", "0")
	return form
}

func firstJSONValue(body []byte, keys ...string) string {
	var value any
	if json.Unmarshal(body, &value) != nil {
		return ""
	}
	set := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		set[strings.ToLower(key)] = struct{}{}
	}
	var visit func(any) string
	visit = func(node any) string {
		switch current := node.(type) {
		case map[string]any:
			for key, item := range current {
				if _, ok := set[strings.ToLower(key)]; ok {
					if result := scalarJSONValue(item); result != "" {
						return result
					}
				}
			}
			for _, item := range current {
				if result := visit(item); result != "" {
					return result
				}
			}
		case []any:
			for _, item := range current {
				if result := visit(item); result != "" {
					return result
				}
			}
		}
		return ""
	}
	return visit(value)
}

func scalarJSONValue(value any) string {
	switch current := value.(type) {
	case []any:
		for _, item := range current {
			if result := scalarJSONValue(item); result != "" {
				return result
			}
		}
		return ""
	case map[string]any:
		for _, item := range current {
			if result := scalarJSONValue(item); result != "" {
				return result
			}
		}
		return ""
	default:
		result := fmt.Sprint(current)
		if result == "" || result == "<nil>" {
			return ""
		}
		return result
	}
}

func firstJSONNumber(body []byte, keys ...string) float64 {
	raw := firstJSONValue(body, keys...)
	if raw == "" {
		return -1
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return -1
	}
	return value
}

func requireUpstreamSuccess(body []byte, operation string) error {
	return requireUpstreamStatus(body, operation, http.StatusOK, 1001)
}

func requireUpstreamStatus(body []byte, operation string, allowed ...int) error {
	var payload struct {
		Status  int    `json:"status"`
		Message string `json:"msg"`
	}
	if err := decodeJSON(body, &payload); err != nil {
		return fmt.Errorf("decode upstream %s response: %w", operation, err)
	}
	for _, status := range allowed {
		if payload.Status == status {
			return nil
		}
	}
	return upstreamError(operation, payload.Message)
}

func (p *Provider) GetInstanceDetail(hostID int64) (*contracts.InstanceDetail, error) {
	if hostID <= 0 {
		return nil, errors.New("upstream host ID must be positive")
	}
	query := url.Values{"host_id": {strconv.FormatInt(hostID, 10)}}
	body, err := p.authorizedGet("/host/header?" + query.Encode())
	if err != nil {
		return nil, fmt.Errorf("read upstream instance: %w", err)
	}
	data, err := businessData(body, "instance detail")
	if err != nil {
		return nil, err
	}
	hostData := nestedMap(data, "host_data")
	if len(hostData) == 0 {
		hostData = data
	}
	primaryIP := firstMapString(hostData, "dedicatedip", "dedicated_ip", "mainip", "main_ip", "ip")
	ips := parseInstanceIPs(primaryIP, hostData["assignedips"], hostData["assigned_ips"])
	result := &contracts.InstanceDetail{
		Hostname:        firstMapString(hostData, "domain", "hostname", "name"),
		PrimaryIP:       primaryIP,
		SubnetMask:      firstMapString(hostData, "subnet_mask", "netmask"),
		Gateway:         firstMapString(hostData, "gateway"),
		OperatingSystem: firstMapString(hostData, "os", "os_name", "operating_system", "system"),
		Status:          firstMapString(hostData, "domainstatus", "domain_status", "status"),
		PowerStatus:     firstMapString(hostData, "power_status", "powerstatus"),
		IPs:             ips,
		Capabilities: contracts.InstanceCapabilities{
			Start: true, Stop: true, Reboot: true, Reinstall: true,
			ResetPassword: true, Traffic: true, Remote: true, Destroy: false,
		},
	}
	if err := parseInstanceSnapshot(data, hostData, hostID, result); err != nil {
		return nil, err
	}
	if result.PowerStatus == "" {
		result.PowerStatus = normalizePowerStatus(result.Status)
	}
	return result, nil
}

func (p *Provider) GetInstanceStatus(hostID int64) (string, error) {
	form := url.Values{
		"id": {strconv.FormatInt(hostID, 10)}, "func": {"status"}, "is_api": {"true"},
	}
	body, err := p.authorizedFormPost("/provision/default", form)
	if err != nil {
		return "", fmt.Errorf("read upstream instance status: %w", err)
	}
	data, err := businessData(body, "instance status")
	if err != nil {
		return "", err
	}
	return normalizePowerStatus(firstMapString(data, "status", "power_status")), nil
}

func (p *Provider) ExecuteInstanceAction(hostID int64, input contracts.InstanceActionInput) (*contracts.InstanceActionResult, error) {
	if hostID <= 0 {
		return nil, errors.New("upstream host ID must be positive")
	}
	upstreamFunc, ok := map[string]string{
		"start": "on", "stop": "off", "reboot": "reboot", "reset_password": "crack_pass",
		"reinstall": "reinstall", "remote": "vnc",
	}[input.Action]
	if !ok {
		return nil, fmt.Errorf("finance upstream instance action %s is not supported", input.Action)
	}
	form := instanceActionForm(hostID, upstreamFunc, input)
	body, err := p.authorizedFormPost("/provision/default", form)
	if err != nil {
		if input.Action == "remote" {
			return nil, errors.New("remote console upstream unavailable")
		}
		return nil, fmt.Errorf("execute upstream instance action: %w", err)
	}
	data, err := businessData(body, "instance action")
	if err != nil {
		if input.Action == "remote" {
			return nil, errors.New("remote console upstream unavailable")
		}
		return nil, err
	}
	message := firstJSONValue(body, "msg", "message")
	if message == "" {
		message = "上游已接受实例操作"
	}
	return &contracts.InstanceActionResult{
		Status:         normalizePowerStatus(firstMapString(data, "status", "power_status")),
		Message:        message,
		RemoteEndpoint: firstMapString(data, "url", "remote_url", "vnc_url"),
		RemotePassword: firstMapString(data, "vnc_pass", "vnc_password", "password", "pass"),
	}, nil
}

func instanceActionForm(hostID int64, upstreamFunc string, input contracts.InstanceActionInput) url.Values {
	form := url.Values{
		"id": {strconv.FormatInt(hostID, 10)}, "func": {upstreamFunc}, "is_api": {"true"},
	}
	if input.Password != "" {
		form.Set("password", input.Password)
	}
	if input.ImageID != "" {
		form.Set("os", input.ImageID)
	}
	if input.Port != "" {
		form.Set("port", input.Port)
	}
	if input.FormatDataDisk {
		form.Set("format_data_disk", "1")
	} else if input.Action == "reinstall" {
		form.Set("format_data_disk", "0")
	}
	return form
}

func (p *Provider) GetInstanceMonitor(hostID int64, start, end time.Time) (*contracts.InstanceMonitor, error) {
	if hostID <= 0 {
		return nil, errors.New("upstream host ID must be positive")
	}
	result := &contracts.InstanceMonitor{}
	failed := 0
	cpu, err := p.monitorData(hostID, "cpu", start, end)
	if err != nil {
		failed++
	} else {
		series := extractMonitorSeries(cpu)
		if len(series) > 0 {
			result.CPU = series[0]
		}
	}
	memory, err := p.monitorData(hostID, "memory", start, end)
	if err != nil {
		failed++
	} else {
		series := extractMonitorSeries(memory)
		if len(series) >= 2 {
			result.Memory = percentageSeries(series[0], series[1])
		} else if len(series) == 1 {
			result.Memory = series[0]
		}
	}
	disk, err := p.monitorData(hostID, "disk", start, end)
	if err != nil {
		failed++
	} else {
		series := extractMonitorSeries(disk)
		if len(series) > 0 {
			result.DiskRead = series[0]
		}
		if len(series) > 1 {
			result.DiskWrite = series[1]
		}
		result.DiskIO = mergeMonitorSeries(result.DiskRead, result.DiskWrite)
	}
	flow, err := p.monitorData(hostID, "flow", start, end)
	if err != nil {
		failed++
	} else {
		series := extractMonitorSeries(flow)
		if len(series) > 0 {
			result.NetworkIn = series[0]
		}
		if len(series) > 1 {
			result.NetworkOut = series[1]
		}
	}
	result.Available = len(result.CPU)+len(result.Memory)+len(result.DiskIO)+len(result.NetworkIn)+len(result.NetworkOut) > 0
	if !result.Available {
		if failed == 4 {
			result.Message = "上游未开放监控接口"
		} else {
			result.Message = "暂无监控数据"
		}
	}
	return result, nil
}

func extractMonitorSeries(node any) [][]contracts.MonitorPoint {
	var list any
	var find func(any)
	find = func(current any) {
		if list != nil {
			return
		}
		switch value := current.(type) {
		case map[string]any:
			if candidate, ok := value["list"]; ok {
				list = candidate
				return
			}
			for _, item := range value {
				find(item)
			}
		case []any:
			for _, item := range value {
				find(item)
			}
		}
	}
	find(node)
	rawSeries, _ := list.([]any)
	series := make([][]contracts.MonitorPoint, 0, len(rawSeries))
	for _, raw := range rawSeries {
		points := extractMonitorPoints(raw)
		if len(points) > 0 {
			series = append(series, points)
		}
	}
	if len(series) == 0 {
		points := extractMonitorPoints(node)
		if len(points) > 0 {
			series = append(series, points)
		}
	}
	return series
}

func percentageSeries(total, used []contracts.MonitorPoint) []contracts.MonitorPoint {
	length := len(total)
	if len(used) < length {
		length = len(used)
	}
	result := make([]contracts.MonitorPoint, 0, length)
	for index := 0; index < length; index++ {
		if total[index].Value <= 0 {
			continue
		}
		result = append(result, contracts.MonitorPoint{Timestamp: total[index].Timestamp, Value: used[index].Value / total[index].Value * 100})
	}
	return result
}

func mergeMonitorSeries(series ...[]contracts.MonitorPoint) []contracts.MonitorPoint {
	values := map[int64]float64{}
	for _, current := range series {
		for _, point := range current {
			values[point.Timestamp] += point.Value
		}
	}
	timestamps := make([]int64, 0, len(values))
	for timestamp := range values {
		timestamps = append(timestamps, timestamp)
	}
	sort.Slice(timestamps, func(i, j int) bool { return timestamps[i] < timestamps[j] })
	result := make([]contracts.MonitorPoint, 0, len(timestamps))
	for _, timestamp := range timestamps {
		result = append(result, contracts.MonitorPoint{Timestamp: timestamp, Value: values[timestamp]})
	}
	return result
}

func (p *Provider) monitorData(hostID int64, kind string, start, end time.Time) (any, error) {
	query := url.Values{"type": {kind}, "is_api": {"true"}}
	if !start.IsZero() {
		query.Set("start", strconv.FormatInt(start.UnixMilli(), 10))
	}
	if !end.IsZero() {
		query.Set("end", strconv.FormatInt(end.UnixMilli(), 10))
	}
	body, err := p.authorizedGet("/provision/chart/" + strconv.FormatInt(hostID, 10) + "?" + query.Encode())
	if err != nil {
		return nil, err
	}
	return businessDataAny(body, "instance monitor")
}

func businessData(body []byte, operation string) (map[string]any, error) {
	data, err := businessDataAny(body, operation)
	if err != nil {
		return nil, err
	}
	result, _ := data.(map[string]any)
	if result == nil {
		result = map[string]any{}
	}
	return result, nil
}

func businessDataAny(body []byte, operation string) (any, error) {
	var payload struct {
		Status  int    `json:"status"`
		Message string `json:"msg"`
		Data    any    `json:"data"`
	}
	if err := decodeJSON(body, &payload); err != nil {
		return nil, fmt.Errorf("decode upstream %s response: %w", operation, err)
	}
	if payload.Status != http.StatusOK {
		return nil, upstreamError(operation, payload.Message)
	}
	return payload.Data, nil
}

func nestedMap(data map[string]any, key string) map[string]any {
	if value, ok := data[key].(map[string]any); ok {
		return value
	}
	return nil
}

func firstMapString(data map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := data[key]; ok {
			if result := scalarJSONValue(value); result != "" {
				return result
			}
		}
	}
	return ""
}

func parseInstanceIPs(primary string, values ...any) []contracts.InstanceIP {
	seen := map[string]struct{}{}
	out := make([]contracts.InstanceIP, 0)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if net.ParseIP(value) == nil {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		out = append(out, contracts.InstanceIP{Address: value})
	}
	add(primary)
	for _, value := range values {
		switch current := value.(type) {
		case string:
			for _, item := range strings.FieldsFunc(current, func(r rune) bool { return r == ',' || r == ';' || r == '\n' || r == '\r' || r == ' ' }) {
				add(item)
			}
		case []any:
			for _, item := range current {
				add(scalarJSONValue(item))
			}
		}
	}
	return out
}

func normalizePowerStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on", "active", "running", "waiting":
		return "on"
	case "off", "stopped", "suspended":
		return "off"
	case "task", "process", "operating", "cold_migrate", "hot_migrate":
		return "operating"
	case "pending", "provisioning":
		return "pending"
	case "":
		return ""
	default:
		return "fault"
	}
}

func findNestedValue(node any, keys ...string) any {
	set := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		set[strings.ToLower(key)] = struct{}{}
	}
	var visit func(any) any
	visit = func(current any) any {
		switch value := current.(type) {
		case map[string]any:
			for key, item := range value {
				if _, ok := set[strings.ToLower(key)]; ok {
					return item
				}
			}
			for _, item := range value {
				if found := visit(item); found != nil {
					return found
				}
			}
		case []any:
			for _, item := range value {
				if found := visit(item); found != nil {
					return found
				}
			}
		}
		return nil
	}
	return visit(node)
}

func extractMonitorPoints(node any) []contracts.MonitorPoint {
	points := make([]contracts.MonitorPoint, 0)
	var visit func(any)
	visit = func(current any) {
		switch value := current.(type) {
		case []any:
			if len(value) >= 2 {
				if timestamp, ok := monitorTimestamp(value[0]); ok {
					if metric, ok := monitorNumber(value[1]); ok {
						points = append(points, contracts.MonitorPoint{Timestamp: timestamp, Value: metric})
						return
					}
				}
			}
			for _, item := range value {
				visit(item)
			}
		case map[string]any:
			var timestamp int64
			var metric float64
			timeOK, valueOK := false, false
			for _, key := range []string{"timestamp", "time", "date", "x"} {
				if item, exists := value[key]; exists {
					timestamp, timeOK = monitorTimestamp(item)
					if timeOK {
						break
					}
				}
			}
			for _, key := range []string{"value", "usage", "percent", "y", "read", "rx"} {
				if item, exists := value[key]; exists {
					metric, valueOK = monitorNumber(item)
					if valueOK {
						break
					}
				}
			}
			if timeOK && valueOK {
				points = append(points, contracts.MonitorPoint{Timestamp: timestamp, Value: metric})
				return
			}
			for _, item := range value {
				visit(item)
			}
		}
	}
	visit(node)
	return points
}

func monitorTimestamp(value any) (int64, bool) {
	raw := scalarJSONValue(value)
	if raw == "" {
		return 0, false
	}
	if number, err := strconv.ParseInt(strings.Split(raw, ".")[0], 10, 64); err == nil {
		if number < 10_000_000_000 {
			number *= 1000
		}
		return number, true
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if parsed, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return parsed.UnixMilli(), true
		}
	}
	return 0, false
}

func monitorNumber(value any) (float64, bool) {
	raw := strings.TrimSuffix(strings.TrimSpace(scalarJSONValue(value)), "%")
	number, err := strconv.ParseFloat(raw, 64)
	return number, err == nil
}

func (p *Provider) CreateHost(int64) error          { return unsupported("host creation") }
func (p *Provider) SuspendHost(int64, string) error { return unsupported("host suspension") }
func (p *Provider) UnsuspendHost(int64) error       { return unsupported("host unsuspension") }
func (p *Provider) TerminateHost(int64) error       { return unsupported("host termination") }
func (p *Provider) RenewHost(int64, string) (*contracts.RenewResult, error) {
	return nil, unsupported("host renewal")
}
func (p *Provider) UpgradeHost(int64, contracts.UpgradeConfig) (*contracts.UpgradeResult, error) {
	return nil, unsupported("host upgrade")
}
func (p *Provider) RebootHost(hostID int64) error {
	_, err := p.ExecuteInstanceAction(hostID, contracts.InstanceActionInput{Action: "reboot"})
	return err
}
func (p *Provider) ReinstallHost(hostID int64, imageID string) error {
	_, err := p.ExecuteInstanceAction(hostID, contracts.InstanceActionInput{Action: "reinstall", ImageID: imageID})
	return err
}

func (p *Provider) VerifyWebhook(sign string, data []byte) bool {
	if p.Supplier.Secret == "" || sign == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(p.Supplier.Secret))
	_, _ = mac.Write(data)
	return hmac.Equal([]byte(strings.ToLower(sign)), []byte(hex.EncodeToString(mac.Sum(nil))))
}

func (p *Provider) ParseWebhook(data []byte) (*contracts.WebhookEvent, error) {
	var event contracts.WebhookEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return nil, err
	}
	return &event, nil
}
