package service

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"ddyidc/internal/upstream/model"
	"ddyidc/internal/upstream/provider"
	"gorm.io/gorm"
)

type SupplierInput struct {
	Name         string  `json:"name"`
	Type         string  `json:"type"`
	URL          string  `json:"url"`
	Username     string  `json:"username"`
	Token        string  `json:"token"`
	Secret       string  `json:"secret"`
	Rate         float64 `json:"rate"`
	CurrencyCode string  `json:"currencyCode"`
	Status       int     `json:"status"`
	Remark       string  `json:"remark"`
}

func sanitizeSupplier(v model.Supplier) model.Supplier { v.Token = ""; v.Secret = ""; return v }

func (s *Store) CreateSupplier(in SupplierInput) (model.Supplier, error) {
	in.URL = strings.TrimRight(strings.TrimSpace(in.URL), "/")
	if in.Name == "" || in.URL == "" {
		return model.Supplier{}, fmt.Errorf("供应商名称和接口地址为必填项")
	}
	if in.Type == "" {
		in.Type = "default"
	}
	module, ok := provider.ModuleForVariant(in.Type)
	if !ok {
		return model.Supplier{}, fmt.Errorf("请选择已注册的对接能力")
	}
	fields := map[string]string{"url": in.URL, "username": in.Username, "token": in.Token, "secret": in.Secret}
	for _, field := range module.ConfigFields {
		if field.Required && strings.TrimSpace(fields[field.Name]) == "" {
			return model.Supplier{}, fmt.Errorf("请填写%s", field.Label)
		}
	}
	state, e := s.moduleState(module.ID)
	if e != nil {
		return model.Supplier{}, e
	}
	if e = moduleAllows(state, "sales"); e != nil {
		return model.Supplier{}, e
	}
	if in.Rate == 0 {
		in.Rate = 1
	}
	if in.Status == 0 {
		in.Status = 1
	}
	token, e := s.encrypt(in.Token)
	if e != nil {
		return model.Supplier{}, e
	}
	secret, e := s.encrypt(in.Secret)
	if e != nil {
		return model.Supplier{}, e
	}
	v := model.Supplier{Name: in.Name, Type: in.Type, URL: in.URL, Username: in.Username, Token: token, Secret: secret, Rate: in.Rate, CurrencyCode: in.CurrencyCode, Status: in.Status, Remark: in.Remark}
	if s.isMemory() {
		s.mu.Lock()
		v.ID = id()
		v.CreatedAt = now()
		v.UpdatedAt = v.CreatedAt
		s.suppliers[v.ID] = v
		s.mu.Unlock()
	} else if e := s.db.Transaction(func(tx *gorm.DB) error {
		if e := tx.Table(v.TableName()).Create(&v).Error; e != nil {
			return e
		}
		return s.CheckSupplierCapabilityTx(context.Background(), tx, v.ID, "sales")
	}); e != nil {
		return model.Supplier{}, e
	}
	return sanitizeSupplier(v), nil
}
func (s *Store) ListSuppliers() (out []model.Supplier) {
	if s.isMemory() {
		s.mu.RLock()
		for _, v := range s.suppliers {
			out = append(out, sanitizeSupplier(v))
		}
		s.mu.RUnlock()
		sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
		s.attachSupplierSnapshots(out)
		return
	}
	if s.db.Table((model.Supplier{}).TableName()).Order("created_at DESC").Find(&out).Error != nil {
		return nil
	}
	for i := range out {
		out[i] = sanitizeSupplier(out[i])
	}
	s.attachSupplierSnapshots(out)
	return
}
func (s *Store) GetSupplier(key string) (model.Supplier, error) {
	if s.isMemory() {
		s.mu.RLock()
		v, ok := s.suppliers[key]
		s.mu.RUnlock()
		if !ok {
			return model.Supplier{}, fmt.Errorf("supplier not found")
		}
		return v, nil
	}
	var v model.Supplier
	e := s.db.Table((model.Supplier{}).TableName()).Where("id = ?", key).First(&v).Error
	if e == gorm.ErrRecordNotFound {
		return model.Supplier{}, fmt.Errorf("supplier not found")
	}
	return v, e
}
func (s *Store) UpdateSupplier(key string, in SupplierInput) (model.Supplier, error) {
	in.URL = strings.TrimRight(strings.TrimSpace(in.URL), "/")
	v, e := s.GetSupplier(key)
	if e != nil {
		return v, e
	}
	if in.Name != "" {
		v.Name = in.Name
	}
	if in.URL != "" {
		v.URL = in.URL
	}
	if in.Username != "" {
		v.Username = in.Username
	}
	if in.Type != "" && in.Type != v.Type {
		return v, fmt.Errorf("供应商协议不可直接切换，请新增独立供应商，避免影响已有商品和机器")
	}
	if in.Rate > 0 {
		v.Rate = in.Rate
	}
	if in.CurrencyCode != "" {
		v.CurrencyCode = in.CurrencyCode
	}
	if in.Status != 0 {
		v.Status = in.Status
	}
	v.Remark = in.Remark
	if in.Token != "" {
		v.Token, e = s.encrypt(in.Token)
		if e != nil {
			return v, e
		}
	}
	if in.Secret != "" {
		v.Secret, e = s.encrypt(in.Secret)
		if e != nil {
			return v, e
		}
	}
	if s.isMemory() {
		s.mu.Lock()
		v.UpdatedAt = now()
		s.suppliers[key] = v
		s.mu.Unlock()
	} else if e = s.db.Table(v.TableName()).Where("id = ?", key).Save(&v).Error; e != nil {
		return model.Supplier{}, e
	}
	return sanitizeSupplier(v), nil
}
func (s *Store) DeleteSupplier(key string) error {
	if !s.isMemory() {
		for _, table := range []string{"product.upstream_products", "instance.upstream_hosts", "order.upstream_orders", "order.unit_procurements"} {
			var count int64
			if e := s.db.Table(table).Where("supplier_id=?", key).Count(&count).Error; e != nil {
				return e
			}
			if count > 0 {
				return fmt.Errorf("供应商仍有商品、机器或历史交易关联，不能删除")
			}
		}
	}

	if s.isMemory() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.suppliers[key]; !ok {
			return fmt.Errorf("supplier not found")
		}
		delete(s.suppliers, key)
		return nil
	}
	r := s.db.Table((model.Supplier{}).TableName()).Where("id = ?", key).Delete(&model.Supplier{})
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected == 0 {
		return fmt.Errorf("supplier not found")
	}
	return nil
}
func (s *Store) ProviderFor(key string) (provider.UpstreamProvider, error) {
	return s.providerFor(key, false)
}

func (s *Store) catalogueProvider(key string) (provider.UpstreamProvider, error) {
	return s.providerFor(key, true)
}

func (s *Store) providerFor(key string, limited bool) (provider.UpstreamProvider, error) {
	v, e := s.GetSupplier(key)
	if e != nil {
		return nil, e
	}
	if e = s.CheckSupplierCapability(key, "maintenance"); e != nil {
		return nil, e
	}
	v.Token, e = s.decrypt(v.Token)
	if e != nil {
		return nil, e
	}
	v.Secret, e = s.decrypt(v.Secret)
	if e != nil {
		return nil, e
	}
	config := toProvider(v)
	config.LimitRequests = limited
	config.AuthorizeRequest = func(method, path string) error { return s.CheckSupplierCapability(key, "maintenance") }
	return provider.NewProvider(config)
}
func (s *Store) TestSupplier(key string) error {
	p, e := s.ProviderFor(key)
	if e != nil {
		return e
	}
	return p.Auth()
}
